package api_test

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/executor"
)

// TestExecutorHelperProcess acts as a lightweight `executor serve --stdio` server for API tests.
func TestExecutorHelperProcess(t *testing.T) {
	if os.Getenv("AGENTHOLD_API_FAKE_EXECUTOR") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) > 0 && args[0] == "--version" {
		_, _ = os.Stdout.WriteString("executor v2.0.0-apitest\n")
		os.Exit(0)
	}
	apiKey := os.Getenv("EXECUTOR_API_KEY")
	mux := http.NewServeMux()
	authOK := func(r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer "+apiKey {
			return true
		}
		if c, err := r.Cookie("executor_session_443"); err == nil && c.Value == "ok" {
			return true
		}
		return false
	}
	mux.HandleFunc("/dashboard/api/overview", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"apps": [{
				"id": "app_gh", "slug": "github", "name": "GitHub", "activeVersion": "dpl_1",
				"requirements": {"accounts": {"github": {
					"scope": "user", "provider": "prv_gh", "cardinality": "one",
					"definition": {"name": "GitHub", "auth": {
						"pat": {"type": "secrets", "label": "Personal Access Token", "guide": "Create a PAT.",
							"fields": {"properties": {"token": {"type": "string", "title": "Token"}}, "required": ["token"]}}
					}}
				}}}
			}],
			"links": [{"app": "app_gh", "account": "acc_gh", "requirement": "github"}],
			"appLinks": [],
			"accounts": [{
				"id": "acc_gh", "provider": "prv_gh", "providerName": "GitHub", "method": "pat",
				"label": "Personal", "description": "My GitHub", "signIn": {"state": "saved"},
				"health": {"info": {"login": "octocat"}}
			}]
		}`))
	})
	mux.HandleFunc("/dashboard/api/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apps":[{"id":"linear","package":"@executor.sh/linear","name":"Linear","description":"Issues","category":"productivity","auth":[{"method":"apiKey","kind":"secrets"}]}]}`))
	})
	mux.HandleFunc("/dashboard/api/catalog/import", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app_lin","slug":"linear","name":"Linear","activeVersion":"dpl_2"}`))
	})
	mux.HandleFunc("/account-connect/api/apps/app_gh/accounts/user/github/connections", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connection":"con_1","url":"http://` + r.Host + `/account-connect/con_1#token=tok123","expiresAt":"2030-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/account-connect/api/read", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"prv_gh","providerDefinition":{"name":"GitHub","auth":{"pat":{"type":"secrets","label":"PAT","fields":{"properties":{"token":{"type":"string"}},"required":["token"]}}}}}`))
	})
	mux.HandleFunc("/account-connect/api/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"acc_new","provider":"prv_gh","providerName":"GitHub","method":"pat","label":"Work","signIn":{"state":"saved"}}`))
	})
	mux.HandleFunc("/dashboard/api/accounts/acc_new", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"acc_new","provider":"prv_gh","providerName":"GitHub","method":"pat","label":"Work","description":"Work repos","signIn":{"state":"saved"}}`))
	})
	mux.HandleFunc("/dashboard/api/accounts/acc_gh/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","apps":[]}`))
	})
	mux.HandleFunc("/auth/pair", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"http://` + r.Host + `/#pair=p123"}`))
	})
	mux.HandleFunc("/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "executor_session_443", Value: "ok", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authenticated":true}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("executor-ui-ok"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	_, _ = os.Stdout.WriteString(`{"url":"http://` + ln.Addr().String() + `"}` + "\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = srv.Close()
	os.Exit(0)
}

func apiFakeExecutorBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake-api-executor")
	body := "#!/bin/sh\nexport AGENTHOLD_API_FAKE_EXECUTOR=1\nexec \"" + exe + "\" -test.run=^TestExecutorHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestExecutorAPIAndAgentSettings(t *testing.T) {
	mgr := executor.New(executor.Config{
		Binary:          apiFakeExecutorBin(t),
		DataDir:         t.TempDir(),
		MasterKey:       bytes.Repeat([]byte{0x22}, 32),
		DashboardOrigin: "https://executor-demo.agenthold.dev",
		StartupTimeout:  10 * time.Second,
	})
	defer mgr.Close()

	e := newEnv(t)
	e.api.Executor = mgr
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	// Status before start.
	var st gen.ExecutorStatus
	expect(t, "status", alice.do(http.MethodGet, "/me/executor", nil, &st), http.StatusOK)
	if !st.Available || !st.Enabled || st.Running || !st.OauthReady {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	// Overview starts the user's instance and returns apps + accounts.
	var ov gen.ExecutorOverview
	expect(t, "overview", alice.do(http.MethodGet, "/me/executor/overview", nil, &ov), http.StatusOK)
	if !ov.Status.Running || len(ov.Apps) != 1 || len(ov.Accounts) != 1 {
		t.Fatalf("unexpected overview: %+v", ov)
	}
	if ov.Accounts[0].Identity == nil || *ov.Accounts[0].Identity != "octocat" {
		t.Fatalf("unexpected account identity: %+v", ov.Accounts[0])
	}

	// Catalog + import.
	var cat gen.ExecutorCatalog
	expect(t, "catalog", alice.do(http.MethodGet, "/me/executor/catalog", nil, &cat), http.StatusOK)
	if len(cat.Apps) != 1 || cat.Apps[0].Id != "linear" {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
	var imported gen.ExecutorApp
	expect(t, "import", alice.do(http.MethodPost, "/me/executor/catalog/import", map[string]string{"entry": "linear", "name": "Linear"}, &imported), http.StatusCreated)
	if imported.Id != "app_lin" {
		t.Fatalf("unexpected imported app: %+v", imported)
	}

	// Custom app import is blocked with 403 when allow_private_fetch is false.
	expect(t, "block custom app when allow_private_fetch is false", alice.do(http.MethodPost, "/me/executor/apps/import", map[string]string{
		"name": "Internal", "url": "http://127.0.0.1:9999/mcp",
	}, nil), http.StatusForbidden)

	// Account connect + submit secrets (including description).
	var sess gen.ExecutorConnectionSession
	expect(t, "connect", alice.do(http.MethodPost, "/me/executor/apps/app_gh/accounts/user/github/connect", map[string]any{}, &sess), http.StatusCreated)
	if sess.ConnectionId != "con_1" || sess.Token != "tok123" || sess.BrowserUrl == nil || !strings.HasPrefix(*sess.BrowserUrl, "https://executor-demo.agenthold.dev/account-connect/con_1") {
		t.Fatalf("unexpected connect session: %+v", sess)
	}
	var acc gen.ExecutorAccount
	expect(t, "submit", alice.do(http.MethodPost, "/me/executor/connections/submit", map[string]any{
		"connectionId": sess.ConnectionId, "token": sess.Token, "method": "pat", "label": "Work", "description": "Work repos", "fields": map[string]string{"token": "ghp_secret"},
	}, &acc), http.StatusOK)
	if acc.Id != "acc_new" || acc.Description == nil || *acc.Description != "Work repos" {
		t.Fatalf("unexpected submitted account: %+v", acc)
	}

	// Health check.
	var health gen.ExecutorAccountHealth
	expect(t, "health", alice.do(http.MethodPost, "/me/executor/accounts/acc_gh/health", nil, &health), http.StatusOK)
	if health.Status != "ok" {
		t.Fatalf("unexpected health: %+v", health)
	}

	// Agent Executor settings: default enabled=true when Executor is available, mode=model;
	// explicit opt-out respected; patchable; preserved on clone; auto-disabled on share.
	var ag gen.Agent
	expect(t, "create agent", alice.do(http.MethodPost, "/agents", map[string]any{"name": "exec-agent", "harness": "fake"}, &ag), http.StatusCreated)
	e.sandboxes.Wait()
	if !ag.ExecutorEnabled || ag.ExecutorApprovalMode != gen.Model {
		t.Fatalf("unexpected default agent executor settings: enabled=%v mode=%v", ag.ExecutorEnabled, ag.ExecutorApprovalMode)
	}
	var optedOut gen.Agent
	expect(t, "create agent explicit opt-out", alice.do(http.MethodPost, "/agents", map[string]any{"name": "exec-optout", "harness": "fake", "executorEnabled": false}, &optedOut), http.StatusCreated)
	e.sandboxes.Wait()
	if optedOut.ExecutorEnabled {
		t.Fatalf("expected explicit executorEnabled=false opt-out to be respected")
	}
	var patched gen.Agent
	expect(t, "patch agent", alice.do(http.MethodPatch, "/agents/"+ag.Id, map[string]any{
		"executorEnabled": true, "executorApprovalMode": "browser",
	}, &patched), http.StatusOK)
	if !patched.ExecutorEnabled || patched.ExecutorApprovalMode != gen.Browser {
		t.Fatalf("unexpected patched agent executor settings: enabled=%v mode=%v", patched.ExecutorEnabled, patched.ExecutorApprovalMode)
	}
	expect(t, "stop agent before clone", alice.do(http.MethodPost, "/agents/"+ag.Id+"/stop", nil, nil), http.StatusAccepted)
	e.sandboxes.Wait()
	var cloned gen.Agent
	expect(t, "clone agent", alice.do(http.MethodPost, "/agents/"+ag.Id+"/clone", map[string]any{"name": "exec-clone"}, &cloned), http.StatusCreated)
	e.sandboxes.Wait()
	if !cloned.ExecutorEnabled || cloned.ExecutorApprovalMode != gen.Browser {
		t.Fatalf("clone did not preserve executor settings: enabled=%v mode=%v", cloned.ExecutorEnabled, cloned.ExecutorApprovalMode)
	}

	// Sharing an agent automatically turns off Executor so collaborators never silently gain owner credentials.
	_ = e.login("bob-sub", "Bob", "bob@example.com")
	expect(t, "share agent", alice.do(http.MethodPost, "/agents/"+ag.Id+"/shares", map[string]string{"email": "bob@example.com"}, nil), http.StatusOK)
	var afterShare gen.Agent
	expect(t, "get agent after share", alice.do(http.MethodGet, "/agents/"+ag.Id, nil, &afterShare), http.StatusOK)
	if afterShare.ExecutorEnabled {
		t.Fatal("sharing an agent must automatically disable Executor until the owner explicitly re-enables it")
	}
	// Updating only approval mode must not re-enable Executor.
	var modeOnly gen.Agent
	expect(t, "patch approval mode only", alice.do(http.MethodPatch, "/agents/"+ag.Id, map[string]any{
		"executorApprovalMode": "automatic",
	}, &modeOnly), http.StatusOK)
	if modeOnly.ExecutorEnabled || modeOnly.ExecutorApprovalMode != gen.Automatic {
		t.Fatalf("mode-only patch changed enabled state: enabled=%v mode=%v", modeOnly.ExecutorEnabled, modeOnly.ExecutorApprovalMode)
	}

	// Executor host auth handoff & reverse proxy test.
	hostHandler := e.api.ExecutorHostHandler(http.NotFoundHandler())
	req1 := httptest.NewRequest(http.MethodGet, "https://executor-demo.agenthold.dev/", nil)
	req1.Header.Set("Sec-Fetch-Mode", "navigate")
	rr1 := httptest.NewRecorder()
	hostHandler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusFound {
		t.Fatalf("expected redirect to authorize, got %d", rr1.Code)
	}
	loc, err := url.Parse(rr1.Header().Get("Location"))
	if err != nil || loc.Path != "/api/v1/executor/authorize" {
		t.Fatalf("unexpected authorize redirect: %q", rr1.Header().Get("Location"))
	}
	var stateCookie *http.Cookie
	for _, ck := range rr1.Result().Cookies() {
		if ck.Name == "__Host-ah_executor_state" {
			stateCookie = ck
		}
	}
	if stateCookie == nil {
		t.Fatal("missing executor state cookie")
	}

	// Complete authorize on main host using alice's session cookie jar (non-following client).
	noRedirect := &http.Client{
		Jar:           alice.http.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	authReq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+loc.RequestURI(), nil)
	authReq.Header.Set("Sec-Fetch-Mode", "navigate")
	authReq.Header.Set("Sec-Fetch-Dest", "document")
	authRes, err := noRedirect.Do(authReq)
	if err != nil {
		t.Fatal(err)
	}
	defer authRes.Body.Close()
	if authRes.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(authRes.Body)
		t.Fatalf("authorize status=%d body=%s", authRes.StatusCode, string(b))
	}
	cbURL, _ := url.Parse(authRes.Header.Get("Location"))

	// Exchange callback code on Executor host.
	req2 := httptest.NewRequest(http.MethodGet, cbURL.String(), nil)
	req2.AddCookie(stateCookie)
	rr2 := httptest.NewRecorder()
	hostHandler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	var sessCookie *http.Cookie
	for _, ck := range rr2.Result().Cookies() {
		if ck.Name == "__Host-ah_executor" {
			sessCookie = ck
		}
	}
	if sessCookie == nil {
		t.Fatal("missing executor host session cookie")
	}

	// Subsequent request reaches Executor dashboard auto-paired.
	req3 := httptest.NewRequest(http.MethodGet, "https://executor-demo.agenthold.dev/", nil)
	req3.AddCookie(sessCookie)
	rr3 := httptest.NewRecorder()
	hostHandler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK || rr3.Body.String() != "executor-ui-ok" {
		t.Fatalf("proxied dashboard status=%d body=%q", rr3.Code, rr3.Body.String())
	}

	// CSRF protection: cross-origin / same-site sibling POSTs to dashboard and logout are rejected with 403.
	reqCSRF := httptest.NewRequest(http.MethodPost, "https://executor-demo.agenthold.dev/dashboard/api/apps", strings.NewReader(`{}`))
	reqCSRF.AddCookie(sessCookie)
	reqCSRF.Header.Set("Origin", "https://evil-preview-demo.agenthold.dev")
	reqCSRF.Header.Set("Sec-Fetch-Site", "same-site")
	rrCSRF := httptest.NewRecorder()
	hostHandler.ServeHTTP(rrCSRF, reqCSRF)
	if rrCSRF.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for sibling-origin POST, got %d", rrCSRF.Code)
	}

	reqSameOrigin := httptest.NewRequest(http.MethodPost, "https://executor-demo.agenthold.dev/dashboard/api/apps", strings.NewReader(`{}`))
	reqSameOrigin.AddCookie(sessCookie)
	reqSameOrigin.Header.Set("Origin", "https://executor-demo.agenthold.dev")
	reqSameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	rrSameOrigin := httptest.NewRecorder()
	hostHandler.ServeHTTP(rrSameOrigin, reqSameOrigin)
	if rrSameOrigin.Code != http.StatusOK {
		t.Fatalf("expected 200 for same-origin POST, got %d", rrSameOrigin.Code)
	}

	// Main-site logout immediately revokes the Executor host session cookie.
	expect(t, "main logout", alice.do(http.MethodPost, "/auth/logout", nil, nil), http.StatusNoContent)
	reqAfterLogout := httptest.NewRequest(http.MethodGet, "https://executor-demo.agenthold.dev/", nil)
	reqAfterLogout.AddCookie(sessCookie)
	rrAfterLogout := httptest.NewRecorder()
	hostHandler.ServeHTTP(rrAfterLogout, reqAfterLogout)
	if rrAfterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after main logout, got %d", rrAfterLogout.Code)
	}
}
