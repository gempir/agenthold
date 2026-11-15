package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/egress"
	fakeharness "github.com/gempir/agenthold/internal/harness/fake"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/store/db"
)

// ask prompts a new session of the agent and returns the agent's answer.
func (e *env) ask(c *client, agentID, prompt string) string {
	e.t.Helper()
	var sess gen.Session
	expect(e.t, "session", c.do("POST", "/agents/"+agentID+"/sessions", map[string]string{}, &sess), 201)
	st := e.stream(c, sess.Id, 0)
	expect(e.t, "prompt", c.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": prompt}, nil), 202)
	evs := st.until(isType("turn_end"))
	return text(evs)
}

func (c *client) rawJSON(method, path string, body any) (int, string) {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r := c.raw(method, path, bytes.NewReader(b))
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(out)
}

func (c *client) connect(provider, kind, credential string) int {
	c.t.Helper()
	code, _ := c.rawJSON("PUT", "/me/provider-logins/"+provider, gen.ProviderLoginInput{Kind: kind, Credential: credential})
	return code
}

// injections returns what the proxy would inject for the agent right now.
func (e *env) injections(agentID string) *egress.Agent {
	e.t.Helper()
	e.directory.Invalidate()
	a, err := e.directory.Agent(e.t.Context(), agentID)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func injectInto(a *egress.Agent, url, header, value string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, url, nil)
	req.Header.Set(header, value)
	for _, inj := range a.Injections {
		_, _ = inj.Apply(req)
	}
	return req
}

func TestProviderLoginConnectDeliverDisconnect(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	const secret = "fake-real-secret-value-1234"

	var list []gen.ProviderLoginInfo
	expect(t, "list", alice.do("GET", "/me/provider-logins", nil, &list), 200)
	providers := map[string]gen.ProviderLoginInfo{}
	for _, p := range list {
		providers[p.Provider] = p
	}
	if _, ok := providers["claude-code"]; !ok || providers["codex"].Login != nil || len(providers["codex"].Kinds) != 3 {
		t.Fatalf("providers %+v", list)
	}
	if _, ok := providers["opencode"]; ok {
		t.Fatal("opencode has no account logins")
	}

	expect(t, "bad kind", alice.connect("fake", "nope", secret), 400)
	expect(t, "invalid credential", alice.connect("fake", "token", "not-fake"), 400)
	expect(t, "unknown provider", alice.connect("opencode", "token", secret), 404)
	code, body := alice.rawJSON("PUT", "/me/provider-logins/fake", gen.ProviderLoginInput{Kind: "token", Credential: secret})
	expect(t, "connect", code, 200)
	var info gen.ProviderLoginInfo
	_ = json.Unmarshal([]byte(body), &info)
	if strings.Contains(body, secret) || info.Login == nil || info.Login.Label != "Token …1234" || info.Login.Status != "ok" {
		t.Fatalf("connect response %s", body)
	}
	// Never returned, never stored in clear.
	_, listBody := alice.rawJSON("GET", "/me/provider-logins", nil)
	if strings.Contains(listBody, secret) {
		t.Fatal("list returns the credential")
	}
	var me gen.Me
	alice.do("GET", "/me", nil, &me)
	row, err := e.store.GetProviderLogin(t.Context(), db.GetProviderLoginParams{UserID: me.User.Id, Provider: "fake"})
	if err != nil || bytes.Contains(row.Ciphertext, []byte(secret)) {
		t.Fatalf("stored %v", err)
	}

	// A new agent uses its creator's logins, with no sign-in of its own.
	ag := e.fakeAgent(alice, "")
	if l := ag.Login; l.Source != "account" || !l.Mine || !l.Connected || l.User == nil || l.User.Id != me.User.Id || *l.Kind != "token" {
		t.Fatalf("agent login %+v", ag.Login)
	}
	answer := e.ask(alice, ag.Id, "whoami")
	ph := strings.TrimSuffix(strings.TrimPrefix(answer, "token="), " account=")
	if !strings.HasPrefix(ph, "fake-agenthold-placeholder-") || strings.Contains(answer, secret) {
		t.Fatalf("harness saw %q", answer)
	}
	// The proxy swaps the placeholder for the value, in that header only.
	req := injectInto(e.injections(ag.Id), "https://"+fakeharness.APIHost+"/v1/x", "Authorization", "Bearer "+ph)
	if req.Header.Get("Authorization") != "Bearer "+secret {
		t.Fatalf("injected %q", req.Header.Get("Authorization"))
	}
	if r := injectInto(e.injections(ag.Id), "https://elsewhere.example/v1/x", "Authorization", "Bearer "+ph); r.Header.Get("Authorization") != "Bearer "+ph {
		t.Fatal("injected into another host")
	}
	var audit []gen.AuditEntry
	alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &audit)
	if !hasAction(audit, logins.ActionUse) {
		t.Fatalf("no use audit: %+v", audit)
	}
	wsAudit := mustAudit(t, e, me.User.Id)
	if !auditHas(wsAudit, logins.ActionConnect) {
		t.Fatal("no connect audit")
	}
	for _, a := range wsAudit {
		if strings.Contains(a.Details, secret) {
			t.Fatal("audit contains the credential")
		}
	}

	// The settings page lists the agents using the login.
	alice.do("GET", "/me/provider-logins", nil, &list)
	for _, p := range list {
		if p.Provider == "fake" && (len(p.Agents) != 1 || p.Agents[0].Id != ag.Id) {
			t.Fatalf("agents %+v", p.Agents)
		}
	}

	// Disconnect: the proxy stops injecting at once, the harness restarts without it.
	expect(t, "disconnect", alice.do("DELETE", "/me/provider-logins/fake", nil, nil), 204)
	expect(t, "disconnect again", alice.do("DELETE", "/me/provider-logins/fake", nil, nil), 404)
	if a := e.injections(ag.Id); len(a.Injections) != 0 {
		t.Fatalf("still injecting %+v", a.Injections)
	}
	if got := alice.agent(ag.Id).Login; got.Connected || got.Source != "account" {
		t.Fatalf("after disconnect %+v", got)
	}
	if answer := e.ask(alice, ag.Id, "whoami"); answer != "token= account=" {
		t.Fatalf("after disconnect the harness saw %q", answer)
	}
	if !auditHas(mustAudit(t, e, me.User.Id), logins.ActionDisconnect) {
		t.Fatal("no disconnect audit")
	}
}

// mustAudit returns userID's account-level audit entries (not tied to an agent).
func mustAudit(t *testing.T, e *env, userID string) []db.AuditLog {
	rows, err := e.store.DB.QueryContext(t.Context(), `SELECT action, details FROM audit_log WHERE user_id = ? AND agent_id = '' ORDER BY id DESC`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.AuditLog
	for rows.Next() {
		var a db.AuditLog
		if err := rows.Scan(&a.Action, &a.Details); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func auditHas(rows []db.AuditLog, action string) bool {
	for _, r := range rows {
		if r.Action == action {
			return true
		}
	}
	return false
}

func hasAction(entries []gen.AuditEntry, action string) bool {
	for _, a := range entries {
		if a.Action == action {
			return true
		}
	}
	return false
}

func TestSwitchingHarnessUsesTheOtherLogin(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	expect(t, "claude", alice.connect("claude-code", "oauth-token", "sk-ant-oat01-real-claude-token-abcd"), 200)
	expect(t, "codex", alice.connect("codex", "api-key", "sk-proj-real-openai-key-wxyz"), 200)
	ag := e.fakeAgent(alice, "")
	for _, h := range []struct{ harness, kind, url, header, sent, want string }{
		{"claude-code", "oauth-token", "https://api.anthropic.com/v1/messages", "Authorization", "Bearer sk-ant-oat01-", "Bearer sk-ant-oat01-real-claude-token-abcd"},
		{"codex", "api-key", "https://api.openai.com/v1/responses", "Authorization", "Bearer sk-", "Bearer sk-proj-real-openai-key-wxyz"},
	} {
		var got gen.Agent
		expect(t, "switch", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr(h.harness)}, &got), 200)
		if !got.Login.Connected || *got.Login.Kind != h.kind || got.Login.Source != "account" {
			t.Fatalf("%s: %+v", h.harness, got.Login)
		}
		a := e.injections(ag.Id)
		if len(a.Injections) != 1 {
			t.Fatalf("%s injections %d", h.harness, len(a.Injections))
		}
		ph := a.Injections[0].Placeholder
		if !strings.HasPrefix(ph, strings.TrimPrefix(h.sent, "Bearer ")) || !strings.Contains(ph, "agenthold-placeholder-") {
			t.Fatalf("placeholder %q", ph)
		}
		if r := injectInto(a, h.url, h.header, "Bearer "+ph); r.Header.Get(h.header) != h.want {
			t.Fatalf("%s: %q", h.harness, r.Header.Get(h.header))
		}
	}

	// An API key secret attached to the agent for its harness wins.
	var sec gen.Secret
	expect(t, "secret", alice.do("POST", "/secrets", map[string]any{"name": "k", "value": "sk-other", "preset": "openai"}, &sec), 201)
	expect(t, "attach", alice.do("PUT", "/agents/"+ag.Id+"/secrets/"+sec.Id, map[string]any{"envVar": "OPENAI_API_KEY"}, nil), 200)
	if got := alice.agent(ag.Id).Login; got.Source != "secret" {
		t.Fatalf("with secret: %+v", got)
	}
	for _, inj := range e.injections(ag.Id).Injections {
		if strings.HasPrefix(inj.SecretID, "login:") {
			t.Fatal("account login injected although a secret overrides it")
		}
	}
}

// TestProviderLoginIsolation: one user's login is never used by another
// user's agents; sharing an agent lends the owner's login, never takes the
// shared user's.
func TestProviderLoginIsolation(t *testing.T) {
	tm := newTeam(t)
	e, ag := tm.e, tm.agent.Id
	tm.share("bob")
	expect(t, "bob connects", tm.bob.connect("fake", "token", "fake-bob-secret-0001"), 200)

	// Alice's agent uses Alice's logins; she has none, so nothing is injected.
	if l := tm.alice.agent(ag).Login; l.User == nil || l.User.Id != tm.ids["alice"] || l.Connected {
		t.Fatalf("login %+v", l)
	}
	if len(e.injections(ag).Injections) != 0 {
		t.Fatal("another user's login injected")
	}
	// A shared user cannot attach their login; there is no way to name someone else's.
	expect(t, "shared user binds", tm.bob.do("PUT", "/agents/"+ag+"/login", nil, nil), 403)
	expect(t, "stranger binds", tm.dave.do("PUT", "/agents/"+ag+"/login", nil, nil), 404)
	expect(t, "shared user unbinds", tm.bob.do("DELETE", "/agents/"+ag+"/login", nil, nil), 403)

	// The owner connects; the shared user sees whose login it is and uses it through the agent.
	expect(t, "alice connects", tm.alice.connect("fake", "token", "fake-alice-secret-0002"), 200)
	if l := tm.bob.agent(ag).Login; l.User.Id != tm.ids["alice"] || l.Mine || !l.Connected {
		t.Fatalf("bob sees %+v", l)
	}
	if a := e.injections(ag); len(a.Injections) != 1 || a.Injections[0].Value != "fake-alice-secret-0002" {
		t.Fatal("alice's login not injected")
	}
	// Nobody reads another user's logins.
	var bobList []gen.ProviderLoginInfo
	tm.bob.do("GET", "/me/provider-logins", nil, &bobList)
	for _, p := range bobList {
		if p.Provider == "fake" && (p.Login.Label != "Token …0001" || len(p.Agents) != 0) {
			t.Fatalf("bob's list %+v", p)
		}
	}

	// The owner stops using account logins, and uses them again.
	expect(t, "alice unbinds", tm.alice.do("DELETE", "/agents/"+ag+"/login", nil, nil), 200)
	if len(e.injections(ag).Injections) != 0 || tm.alice.agent(ag).Login.User != nil {
		t.Fatal("still bound")
	}
	var got gen.Agent
	expect(t, "alice binds", tm.alice.do("PUT", "/agents/"+ag+"/login", nil, &got), 200)
	if got.Login.User.Id != tm.ids["alice"] || !got.Login.Mine || !got.Login.Connected {
		t.Fatalf("alice %+v", got.Login)
	}
	var audit []gen.AuditEntry
	tm.alice.do("GET", "/agents/"+ag+"/audit", nil, &audit)
	if !hasAction(audit, logins.ActionBind) || !hasAction(audit, logins.ActionUnbind) {
		t.Fatal("bind not audited")
	}

	// Tokens cannot connect logins.
	var tok gen.CreatedAPIToken
	tm.alice.do("POST", "/me/tokens", map[string]any{"name": "ci"}, &tok)
	expect(t, "token connects", e.tokenClient(tok.Token).connect("fake", "token", "fake-x-secret-9999"), 403)
	expect(t, "token lists", e.tokenClient(tok.Token).do("GET", "/me/provider-logins", nil, nil), 200)
}

// TestRotatingLoginRefreshedCentrally: the server owns the refresh token;
// sandboxes get placeholders, and their own refresh requests are answered
// without ever reaching the provider more than needed.
func TestRotatingLoginRefreshedCentrally(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	current := "R1"
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		if r.Form.Get("refresh") != current {
			w.WriteHeader(http.StatusUnauthorized) // reused or revoked
			return
		}
		n := calls.Load()
		current = "R" + strconv.Itoa(int(n)+1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access": "A" + strconv.Itoa(int(n)+1), "refresh": current, "expires": time.Now().Add(2 * time.Hour).Unix()})
	}))
	defer tokens.Close()
	fakeharness.TokenURL = tokens.URL

	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	// A login the agent made inside its sandbox is kept, not destroyed.
	home := e.home(ag.Id)
	_ = os.MkdirAll(filepath.Join(home, ".fakeagent"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".fakeagent", "account.json"), []byte(`{"access":"own"}`), 0o600)

	login, _ := json.Marshal(map[string]any{"access": "A1", "refresh": "R1", "expires": time.Now().Add(10 * time.Minute).Unix()})
	expect(t, "connect", alice.connect("fake", "refreshing", string(login)), 200)

	answer := e.ask(alice, ag.Id, "whoami")
	if strings.Contains(answer, "A1") || strings.Contains(answer, "R1") || !strings.Contains(answer, "agenthold-placeholder-") {
		t.Fatalf("harness saw %q", answer)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".fakeagent", "account.json.agenthold-backup")); string(b) != `{"access":"own"}` {
		t.Fatalf("backup %q", b)
	}

	// Due for refresh: the server refreshes once, and the proxy injects the new token.
	e.logins.RefreshDue(t.Context())
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	a := e.injections(ag.Id)
	if len(a.Injections) != 1 || a.Injections[0].Value != "A2" || len(a.Responders) != 1 {
		t.Fatalf("after refresh %+v", a)
	}
	e.logins.RefreshDue(t.Context()) // not due anymore
	if calls.Load() != 1 {
		t.Fatal("refreshed although not due")
	}

	// Many sandboxes refreshing on their own at once: answered locally with
	// placeholders, at most one call to the provider (here none: just refreshed).
	var placeholderRefresh string
	{
		var f map[string]string
		_ = json.Unmarshal([]byte(strings.TrimPrefix(answer[strings.Index(answer, "account=")+len("account="):], "")), &f)
		placeholderRefresh = f["refresh"]
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "https://"+fakeharness.AuthHost+"/token", strings.NewReader("refresh="+placeholderRefresh))
			if !a.Responders[0].Handle(rec, req) || rec.Code != 200 || strings.Contains(rec.Body.String(), "A2") || strings.Contains(rec.Body.String(), "R2") {
				t.Errorf("intercept: %d %q", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times", calls.Load())
	}
	// Someone else's refresh token passes through untouched.
	other := httptest.NewRequest(http.MethodPost, "https://"+fakeharness.AuthHost+"/token", strings.NewReader("refresh=own"))
	if a.Responders[0].Handle(httptest.NewRecorder(), other) {
		t.Fatal("answered a request that was not ours")
	}
	if b, _ := io.ReadAll(other.Body); string(b) != "refresh=own" {
		t.Fatal("body not restored")
	}

	// The provider revokes the login: it is marked, and sandboxes are told to sign in again.
	mu.Lock()
	current = "something-else"
	mu.Unlock()
	if _, err := e.store.DB.ExecContext(t.Context(), "UPDATE provider_logins SET refreshed_at = 0"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Responders[0].Handle(rec, httptest.NewRequest(http.MethodPost, "https://"+fakeharness.AuthHost+"/token", strings.NewReader("refresh="+placeholderRefresh)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked: %d", rec.Code)
	}
	var list []gen.ProviderLoginInfo
	alice.do("GET", "/me/provider-logins", nil, &list)
	for _, p := range list {
		if p.Provider == "fake" && (p.Login == nil || p.Login.Status != "error") {
			t.Fatalf("status %+v", p.Login)
		}
	}
	if got := alice.agent(ag.Id).Login; got.Status == nil || *got.Status != "error" {
		t.Fatalf("agent login %+v", got)
	}

	// Disconnected: the placeholder file goes on the next start.
	expect(t, "disconnect", alice.do("DELETE", "/me/provider-logins/fake", nil, nil), 204)
	e.ask(alice, ag.Id, "whoami")
	if _, err := os.Stat(filepath.Join(home, ".fakeagent", "account.json")); !os.IsNotExist(err) {
		t.Fatalf("placeholder file left behind: %v", err)
	}
}

// Old-style agents (signed in inside their sandbox) keep their login.
func TestSandboxLoginKeepsWorking(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	expect(t, "stop using", alice.do("DELETE", "/agents/"+ag.Id+"/login", nil, nil), 200)
	expect(t, "connect", alice.connect("fake", "token", "fake-real-secret-value-5678"), 200)
	if got := alice.agent(ag.Id).Login; got.Source != "sandbox" || got.User != nil {
		t.Fatalf("login %+v", got)
	}
	if answer := e.ask(alice, ag.Id, "whoami"); answer != "token= account=" {
		t.Fatalf("harness saw %q", answer)
	}
	if len(e.injections(ag.Id).Injections) != 0 {
		t.Fatal("injected without a binding")
	}
	var got gen.Agent
	expect(t, "use mine", alice.do("PUT", "/agents/"+ag.Id+"/login", nil, &got), 200)
	if !got.Login.Connected || got.Login.Source != "account" {
		t.Fatalf("after use %+v", got.Login)
	}
	if answer := e.ask(alice, ag.Id, "whoami"); !strings.Contains(answer, "agenthold-placeholder-") {
		t.Fatalf("harness not restarted with the login: %q", answer)
	}
}
