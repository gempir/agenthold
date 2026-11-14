package api_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
)

// tokenClient talks to the API with a bearer token only (like a script).
func (e *env) tokenClient(token string) *client {
	c := &client{t: e.t, base: e.srv.URL, http: &http.Client{Transport: bearer{token: token}}}
	return c
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestTokenScriptCreatesAndPromptsAgent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	var tok gen.CreatedAPIToken
	expect(t, "create token", alice.do("POST", "/me/tokens", map[string]any{"name": "ci"}, &tok), 201)
	if !strings.HasPrefix(tok.Token, "ahp_") || !strings.HasPrefix(tok.Token, tok.Prefix) {
		t.Fatalf("token %+v", tok)
	}

	script := e.tokenClient(tok.Token)
	var me gen.Me
	expect(t, "me via token", script.do("GET", "/me", nil, &me), 200)
	var ag gen.Agent
	expect(t, "create agent via token", script.do("POST", "/workspaces/"+me.PersonalWorkspaceId+"/agents", gen.AgentInput{Name: "scripted", Harness: ptr("fake")}, &ag), 201)
	e.sandboxes.Wait()
	var sess gen.Session
	expect(t, "session via token", script.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	expect(t, "prompt via token", script.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hello from a script"}, nil), 202)
	e.sessions.Wait()
	var evs []gen.SessionEvent
	script.do("GET", "/sessions/"+sess.Id+"/events", nil, &evs)
	if !strings.Contains(text(evs), "echo: hello from a script") {
		t.Fatalf("events %v", evs)
	}

	// The stream works with a token too.
	st := e.stream(script, sess.Id, 0)
	st.until(isType("turn_end"))

	// Tokens are as strong as their user, not more.
	bob := e.login("bob", "Bob", "b@example.com")
	var bobTok gen.CreatedAPIToken
	bob.do("POST", "/me/tokens", map[string]any{"name": "x"}, &bobTok)
	expect(t, "other user's token", e.tokenClient(bobTok.Token).do("GET", "/agents/"+ag.Id, nil, nil), 404)

	// Invalid, revoked and expired tokens are refused.
	expect(t, "bad token", e.tokenClient("ahp_nope").do("GET", "/me", nil, nil), 401)
	var list []gen.APIToken
	alice.do("GET", "/me/tokens", nil, &list)
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("tokens %+v", list)
	}
	expect(t, "revoke", alice.do("DELETE", "/me/tokens/"+tok.Id, nil, nil), 204)
	expect(t, "revoked token", script.do("GET", "/me", nil, nil), 401)
}

func TestScheduleWakesSleepingAgent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	expect(t, "bad cron", alice.do("POST", "/agents/"+ag.Id+"/schedules", gen.ScheduleInput{Name: "x", Cron: "nope", Prompt: "x"}, nil), 400)
	expect(t, "bad tz", alice.do("POST", "/agents/"+ag.Id+"/schedules", gen.ScheduleInput{Name: "x", Cron: "@daily", Timezone: ptr("Mars/Base"), Prompt: "x"}, nil), 400)
	var sc gen.Schedule
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/schedules", gen.ScheduleInput{Name: "nightly", Cron: "0 3 * * *", Timezone: ptr("Europe/Berlin"), Prompt: "nightly report please"}, &sc), 201)
	if sc.SessionId == "" || !sc.NextRunAt.After(e.clock.Now()) {
		t.Fatalf("schedule %+v", sc)
	}

	alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil)
	e.sandboxes.Wait()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("state %s", s)
	}

	e.scheduler.Tick(t.Context()) // not due yet
	e.sessions.Wait()
	var evs []gen.SessionEvent
	alice.do("GET", "/sessions/"+sc.SessionId+"/events", nil, &evs)
	if len(evs) != 0 {
		t.Fatal("ran before it was due")
	}

	e.clock.Advance(sc.NextRunAt.Sub(e.clock.Now()) + time.Second)
	e.scheduler.Tick(t.Context())
	e.sessions.Wait()
	alice.do("GET", "/sessions/"+sc.SessionId+"/events", nil, &evs)
	if !strings.Contains(text(evs), "echo: nightly report please") || evs[0].Data["user"].(map[string]any)["name"] != "Alice (schedule: nightly)" {
		t.Fatalf("schedule run events %v", evs)
	}
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("schedule did not wake the agent: %s", s)
	}
	var list []gen.Schedule
	alice.do("GET", "/agents/"+ag.Id+"/schedules", nil, &list)
	if list[0].LastRunAt == nil || list[0].LastError != "" || !list[0].NextRunAt.After(e.clock.Now()) {
		t.Fatalf("after run %+v", list[0])
	}
	expect(t, "run now", alice.do("POST", "/schedules/"+sc.Id+"/run", nil, nil), 202)
	e.sessions.Wait()
}

func sign(key, body []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWebhookWakesSleepingAgent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	var hook gen.CreatedWebhook
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/webhooks", gen.WebhookInput{
		Name: "issues", PromptTemplate: "A new issue arrived:\n{{payload}}", SigningSecret: ptr("s3cret"),
	}, &hook), 201)
	if !strings.Contains(hook.Url, "/api/v1/hooks/") || !hook.Signed || hook.SignatureHeader != "X-Hub-Signature-256" {
		t.Fatalf("hook %+v", hook)
	}

	alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil)
	e.sandboxes.Wait()

	post := func(body []byte, header map[string]string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, hook.Url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	body := []byte(`{"title":"Crash on start"}`)
	expect(t, "unsigned", post(body, nil), 401)
	expect(t, "wrong signature", post(body, map[string]string{"X-Hub-Signature-256": sign([]byte("other"), body)}), 401)
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("rejected delivery woke the agent: %s", s)
	}
	// A sender's own Authorization header does not interfere.
	expect(t, "signed", post(body, map[string]string{"X-Hub-Signature-256": sign([]byte("s3cret"), body), "Authorization": "Bearer whatever"}), 202)
	e.sessions.Wait()

	var evs []gen.SessionEvent
	alice.do("GET", "/sessions/"+hook.SessionId+"/events", nil, &evs)
	prompt := evs[0].Data["text"].(string)
	if !strings.HasPrefix(prompt, "A new issue arrived:\n--- BEGIN UNTRUSTED WEBHOOK PAYLOAD (application/json)") || !strings.Contains(prompt, `{"title":"Crash on start"}`) {
		t.Fatalf("prompt %q", prompt)
	}
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("webhook did not wake the agent: %s", s)
	}

	unknown := strings.Replace(hook.Url, "/hooks/", "/hooks/x", 1)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, unknown, nil)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	expect(t, "unknown hook", res.StatusCode, 404)

	// Too large.
	big := bytes.Repeat([]byte("a"), 300*1024)
	expect(t, "too large", post(big, map[string]string{"X-Hub-Signature-256": sign([]byte("s3cret"), big)}), 413)

	// The API reference is published.
	r := alice.raw("GET", "/openapi.yaml", nil)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "openapi: 3.0.3") {
		t.Fatal("openapi.yaml not served")
	}
}
