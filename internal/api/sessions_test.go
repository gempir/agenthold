package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/sandbox"
)

// fakeAgent creates an agent that runs the scripted harness.
func (e *env) fakeAgent(c *client, policy string) gen.Agent {
	e.t.Helper()
	var ag gen.Agent
	in := gen.AgentInput{Name: "chat", Harness: ptr("fake")}
	if policy != "" {
		p := gen.AgentInputPermissionPolicy(policy)
		in.PermissionPolicy = &p
	}
	expect(e.t, "create agent", c.do("POST", "/agents", in, &ag), 201)
	e.sandboxes.Wait()
	return ag
}

func (e *env) home(agentID string) string {
	sb, _ := e.driver.Get(sandbox.ID("fake-" + agentID))
	return sb.HomeDir
}

type stream struct {
	t    *testing.T
	ws   *websocket.Conn
	last int64
}

func (e *env) stream(c *client, sessionID string, after int64) *stream {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/sessions/" + sessionID + "/stream?after=" + itoa(after)
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		e.t.Fatal(err)
	}
	ws.SetReadLimit(1 << 22)
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	return &stream{t: e.t, ws: ws, last: after}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// until reads events until one matches pred, returning everything read.
func (s *stream) until(pred func(gen.SessionEvent) bool) []gen.SessionEvent {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), 15*time.Second)
	defer cancel()
	var got []gen.SessionEvent
	for {
		_, data, err := s.ws.Read(ctx)
		if err != nil {
			s.t.Fatalf("stream read: %v (got %d events)", err, len(got))
		}
		var probe struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
		}
		_ = json.Unmarshal(data, &probe)
		if probe.Seq == 0 {
			continue // status message
		}
		var ev gen.SessionEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			s.t.Fatal(err)
		}
		if ev.Seq != s.last+1 {
			s.t.Fatalf("seq %d after %d: gap or duplicate", ev.Seq, s.last)
		}
		s.last = ev.Seq
		got = append(got, ev)
		if pred(ev) {
			return got
		}
	}
}

func isType(t string) func(gen.SessionEvent) bool {
	return func(e gen.SessionEvent) bool { return string(e.Type) == t }
}

func text(evs []gen.SessionEvent) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == "update" && e.Data["sessionUpdate"] == "agent_message_chunk" {
			if c, ok := e.Data["content"].(map[string]any); ok {
				b.WriteString(c["text"].(string))
			}
		}
	}
	return b.String()
}

func TestChatEditsFilesAndResumesFromCursor(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	var sess gen.Session
	expect(t, "create session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	st := e.stream(alice, sess.Id, 0)

	expect(t, "prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "write notes.txt hello from the agent"}, nil), 202)
	evs := st.until(isType("turn_end"))
	if evs[0].Type != "prompt" || evs[0].Data["text"] != "write notes.txt hello from the agent" {
		t.Fatalf("first event %+v", evs[0])
	}
	var sawDiff, autoApproved bool
	for _, ev := range evs {
		if ev.Type == "update" && ev.Data["sessionUpdate"] == "tool_call" {
			content, _ := ev.Data["content"].([]any)
			for _, c := range content {
				if m, _ := c.(map[string]any); m["type"] == "diff" && m["newText"] == "hello from the agent\n" {
					sawDiff = true
				}
			}
		}
		if ev.Type == "permission_resolved" && ev.Data["by"] == "auto" && ev.Data["optionId"] == "allow" {
			autoApproved = true
		}
	}
	if !sawDiff || !autoApproved {
		t.Fatalf("diff=%v autoApproved=%v events=%v", sawDiff, autoApproved, evs)
	}
	b, err := os.ReadFile(filepath.Join(e.home(ag.Id), "notes.txt"))
	if err != nil || string(b) != "hello from the agent\n" {
		t.Fatalf("file in sandbox: %q %v", b, err)
	}

	// Reload mid-turn: start a slow turn, read a little, disconnect, reconnect from the cursor.
	expect(t, "slow prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 10"}, nil), 202)
	expect(t, "busy", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "again"}, nil), 409)
	first := st.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 2") })
	_ = st.ws.CloseNow()
	time.Sleep(500 * time.Millisecond) // the turn continues while nobody watches
	st2 := e.stream(alice, sess.Id, st.last)
	rest := st2.until(isType("turn_end"))
	all := text(append(first, rest...))
	for i := 1; i <= 10; i++ {
		if !strings.Contains(all, "chunk "+itoa(int64(i))+" ") {
			t.Fatalf("missing chunk %d in %q", i, all)
		}
	}
	if rest[len(rest)-1].Data["stopReason"] != "end_turn" {
		t.Fatalf("stop reason %v", rest[len(rest)-1].Data)
	}

	// Cancel.
	expect(t, "slow prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 50"}, nil), 202)
	st2.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })
	expect(t, "cancel", alice.do("POST", "/sessions/"+sess.Id+"/cancel", nil, nil), 202)
	end := st2.until(isType("turn_end"))
	if end[len(end)-1].Data["stopReason"] != "cancelled" {
		t.Fatalf("after cancel: %v", end[len(end)-1].Data)
	}

	// The REST log has everything.
	var log []gen.SessionEvent
	alice.do("GET", "/sessions/"+sess.Id+"/events?limit=5000", nil, &log)
	if int64(len(log)) != st2.last {
		t.Fatalf("log has %d events, stream saw %d", len(log), st2.last)
	}
	var list []gen.Session
	alice.do("GET", "/agents/"+ag.Id+"/sessions", nil, &list)
	if len(list) != 1 || list[0].Title != "write notes.txt hello from the agent" || list[0].Running {
		t.Fatalf("sessions %+v", list)
	}
}

func TestAskPolicyAndAccess(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "ask")
	var sess gen.Session
	alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess)
	expect(t, "outsider", bob.do("GET", "/sessions/"+sess.Id, nil, nil), 404)
	expect(t, "outsider prompt", bob.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "x"}, nil), 404)

	st := e.stream(alice, sess.Id, 0)
	alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "write secret.txt nope"}, nil)
	evs := st.until(isType("permission"))
	req := evs[len(evs)-1].Data["requestId"].(string)
	expect(t, "wrong id", alice.do("POST", "/sessions/"+sess.Id+"/permissions/perm_nope", map[string]string{"optionId": "allow"}, nil), 404)
	expect(t, "reject", alice.do("POST", "/sessions/"+sess.Id+"/permissions/"+req, map[string]string{"optionId": "reject"}, nil), 204)
	rest := st.until(isType("turn_end"))
	if !strings.Contains(text(rest), "did not change") {
		t.Fatalf("after reject: %q", text(rest))
	}
	if _, err := os.Stat(filepath.Join(e.home(ag.Id), "secret.txt")); err == nil {
		t.Fatal("file written despite rejection")
	}
}

func TestSessionSurvivesSleep(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess)
	st := e.stream(alice, sess.Id, 0)
	alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "remember the word banana"}, nil)
	st.until(isType("turn_end"))

	expect(t, "stop", alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	if got := alice.agent(ag.Id); got.State != gen.SandboxStateSleeping {
		t.Fatalf("state %s", got.State)
	}

	// The prompt wakes the agent; the harness restarts and loads the session,
	// and the replayed history is not appended to the log again.
	expect(t, "prompt after sleep", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "what was the word"}, nil), 202)
	evs := st.until(isType("turn_end"))
	for _, ev := range evs {
		if ev.Type == "update" && ev.Data["sessionUpdate"] == "user_message_chunk" {
			t.Fatalf("replayed history leaked into the log: %v", ev.Data)
		}
		if ev.Type == "notice" && strings.Contains(ev.Data["message"].(string), "fresh context") {
			t.Fatalf("context was reset although the harness supports session/load")
		}
	}
	if !strings.Contains(text(evs), "echo: what was the word") {
		t.Fatalf("answer %q", text(evs))
	}
}

func TestHarnessLoginThroughTerminal(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	var h gen.HarnessState
	expect(t, "harness", alice.do("GET", "/agents/"+ag.Id+"/harness?connect=true", nil, &h), 200)
	if !h.Connected || h.AgentName != "fakeagent" || !h.LoadSession || len(h.AuthMethods) != 2 || h.AuthMethods[0].Id != "fake-login" || h.AuthMethods[0].Type != "terminal" || h.AuthMethods[1].Type != "agent" {
		t.Fatalf("harness %+v", h)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/agents/" + ag.Id + "/terminal?auth=fake-login"
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: alice.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	var out strings.Builder
	sent := false
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (%q)", err, out.String())
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
			if strings.Contains(out.String(), "FAKE-1234") && !sent {
				sent = true
				_ = ws.Write(ctx, websocket.MessageBinary, []byte("\n"))
			}
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		if m["type"] == "exit" {
			break
		}
		if m["type"] == "error" {
			t.Fatalf("terminal error %v", m)
		}
	}
	if !strings.Contains(out.String(), "Logged in.") {
		t.Fatalf("output %q", out.String())
	}
	if b, err := os.ReadFile(filepath.Join(e.home(ag.Id), ".fakeagent", "credentials")); err != nil || !strings.Contains(string(b), "fake-subscription-token") {
		t.Fatalf("credentials: %v", err)
	}
	if got := alice.agent(ag.Id); !got.CredentialsInSandbox {
		t.Fatal("credentialsInSandbox not set")
	}

	// Credentials live on the agent's disk and survive a sleep.
	alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil)
	e.sandboxes.Wait()
	alice.do("POST", "/agents/"+ag.Id+"/start", nil, nil)
	e.sandboxes.Wait()
	if _, err := os.Stat(filepath.Join(e.home(ag.Id), ".fakeagent", "credentials")); err != nil {
		t.Fatal("credentials lost across sleep")
	}

	// Only agent admins can run logins.
	bob := e.login("bob", "Bob", "b@example.com")
	r := bob.raw("GET", "/agents/"+ag.Id+"/terminal?auth=fake-login", nil)
	r.Body.Close()
	expect(t, "outsider login", r.StatusCode, 404)
}

func TestHarnessList(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	var hs []gen.HarnessType
	alice.do("GET", "/harnesses", nil, &hs)
	ids := []string{}
	for _, h := range hs {
		ids = append(ids, h.Id)
	}
	if strings.Join(ids, ",") != "antigravity,claude-code,codex,fake,opencode" {
		t.Fatalf("harnesses %v", ids)
	}
	var me gen.Me
	alice.do("GET", "/me", nil, &me)
	expect(t, "unknown harness", alice.do("POST", "/agents", gen.AgentInput{Name: "x", Harness: ptr("nope")}, nil), 400)
}
