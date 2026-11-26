package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeriveHexKeyDeterministicAndIsolated(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	k1a := deriveHexKey(master, "agenthold-executor-encryption-v1:", "usr_1")
	k1b := deriveHexKey(master, "agenthold-executor-encryption-v1:", "usr_1")
	k2 := deriveHexKey(master, "agenthold-executor-encryption-v1:", "usr_2")
	api1 := deriveHexKey(master, "agenthold-executor-apikey-v1:", "usr_1")

	if len(k1a) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(k1a))
	}
	if k1a != k1b {
		t.Fatal("derived key must be deterministic")
	}
	if k1a == k2 {
		t.Fatal("different users must get different encryption keys")
	}
	if k1a == api1 {
		t.Fatal("encryption key and api key must differ")
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("AGENTHOLD_FAKE_EXECUTOR") != "1" {
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
		_, _ = os.Stdout.WriteString("executor v2.0.0-fake\n")
		os.Exit(0)
	}
	apiKey := os.Getenv("EXECUTOR_API_KEY")
	var mu sync.Mutex
	sessions := map[string]bool{}
	var sessionCounter atomic.Int64
	pairedCookie := "executor_session_fake"

	mux := http.NewServeMux()
	requireAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer "+apiKey {
			return true
		}
		if c, err := r.Cookie(pairedCookie); err == nil && c.Value == "valid-session" {
			return true
		}
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return false
	}
	mux.HandleFunc("/dashboard/api/overview", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"apps": [
				{
					"id": "app_1",
					"slug": "vercel",
					"name": "Vercel",
					"activeVersion": "dpl_1",
					"requirements": {
						"accounts": {
							"vercel": {
								"scope": "user",
								"provider": "prv_vercel",
								"cardinality": "one",
								"definition": {
									"name": "Vercel",
									"auth": {
										"apiKey": {
											"type": "secrets",
											"label": "API Token",
											"guide": "Create a token in Vercel.",
											"fields": {
												"properties": {
													"token": {"type": "string", "title": "Token", "description": "Bearer token"}
												},
												"required": ["token"]
											}
										}
									}
								}
							}
						}
					}
				}
			],
			"links": [{"app": "app_1", "account": "acc_1", "requirement": "vercel"}],
			"appLinks": [],
			"accounts": [
				{
					"id": "acc_1",
					"provider": "prv_vercel",
					"providerName": "Vercel",
					"method": "apiKey",
					"label": "Work",
					"description": "Prod",
					"signIn": {"state": "saved"},
					"health": {"info": {"email": "dev@example.com"}}
				}
			]
		}`))
	})
	mux.HandleFunc("/dashboard/api/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apps":[{"id":"github","package":"@executor.sh/github","name":"GitHub","description":"GitHub API","category":"developer","auth":[{"method":"oauth","kind":"oauth2"}]}]}`))
	})
	mux.HandleFunc("/auth/pair", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"http://` + r.Host + `/#pair=deadbeef"}`))
	})
	mux.HandleFunc("/auth/exchange", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     pairedCookie,
			Value:    "valid-session",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authenticated":true}`))
	})
	mux.HandleFunc("/dashboard-check", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "leaked authorization header", http.StatusBadRequest)
			return
		}
		if _, err := r.Cookie("agenthold_session"); err == nil {
			http.Error(w, "leaked agenthold cookie", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("paired-ok host=" + r.Host))
	})
	mux.HandleFunc("/test/clear-sessions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		clear(sessions)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sid := r.Header.Get("Mcp-Session-Id")
		switch req.Method {
		case "initialize":
			newSID := "sid-" + string(rune('0'+sessionCounter.Add(1)))
			mu.Lock()
			sessions[newSID] = true
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", newSID)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"Executor","version":"fake"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			ok := sessions[sid]
			mu.Unlock()
			if !ok {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + string(req.ID) + ",\"result\":{\"tools\":[{\"name\":\"search\",\"description\":\"http://" + r.Host + "/account-connect/c1\"}]}}\n\n"))
		case "reset-session":
			mu.Lock()
			sessions = map[string]bool{}
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"mode":"` + r.URL.Query().Get("elicitation_mode") + `"}}`))
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	_, _ = os.Stdout.WriteString(`{"url":"http://` + ln.Addr().String() + `"}` + "\n")
	// Wait until stdin closes, mirroring `executor serve --stdio`.
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = srv.Close()
	os.Exit(0)
}

func fakeExecutorBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake-executor")
	content := "#!/bin/sh\nexport AGENTHOLD_FAKE_EXECUTOR=1\nexec \"" + exe + "\" -test.run=^TestHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestManagerLifecycleOverviewMCPAndProxy(t *testing.T) {
	bin := fakeExecutorBinary(t)
	mgr := New(Config{
		Binary:          bin,
		DataDir:         t.TempDir(),
		MasterKey:       bytes.Repeat([]byte{0x11}, 32),
		DashboardOrigin: "https://executor.example.com",
		IdleTimeout:     0,
		StartupTimeout:  10 * time.Second,
	})
	defer mgr.Close()

	if !mgr.Available() || !mgr.OAuthReady() {
		t.Fatalf("expected available & oauth ready")
	}
	if v := mgr.Version(); v != "2.0.0-fake" {
		t.Fatalf("version=%q want 2.0.0-fake", v)
	}
	if st := mgr.Status("usr_1"); st.Running {
		t.Fatal("should not be running before first request")
	}

	// Concurrent Ensure calls coalesce to one instance.
	var wg sync.WaitGroup
	insts := make([]*Instance, 4)
	errs := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			insts[idx], errs[idx] = mgr.Ensure(t.Context(), "usr_1")
		}(i)
	}
	wg.Wait()
	for i := range 4 {
		if errs[i] != nil {
			t.Fatalf("ensure[%d]: %v", i, errs[i])
		}
		if insts[i] != insts[0] {
			t.Fatalf("expected coalesced instance")
		}
	}

	// Overview parsing.
	ov, err := mgr.Overview(t.Context(), "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Apps) != 1 || ov.Apps[0].Slug != "vercel" || len(ov.Apps[0].Requirements) != 1 {
		t.Fatalf("unexpected apps: %+v", ov.Apps)
	}
	req0 := ov.Apps[0].Requirements[0]
	if len(req0.LinkedAccountIDs) != 1 || req0.LinkedAccountIDs[0] != "acc_1" {
		t.Fatalf("unexpected linked accounts: %+v", req0)
	}
	if len(ov.Accounts) != 1 || ov.Accounts[0].Identity != "dev@example.com" || ov.Accounts[0].Status != "ready" {
		t.Fatalf("unexpected accounts: %+v", ov.Accounts)
	}

	// Catalog parsing.
	cat, err := mgr.Catalog(t.Context(), "usr_1")
	if err != nil || len(cat) != 1 || cat[0].ID != "github" {
		t.Fatalf("catalog: %+v err=%v", cat, err)
	}

	// MCP bridge over in-memory pipe (tests JSON, SSE, URL rewriting, 404 plain-text session recovery, and live revocation).
	var revoked atomic.Bool
	clientConn, serverConn := net.Pipe()
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		mgr.BridgeMCP(t.Context(), BridgeOptions{
			UserID:       "usr_1",
			AgentID:      "ag_1",
			ApprovalMode: ApprovalBrowser,
			Authorize: func(context.Context) (string, error) {
				if revoked.Load() {
					return "", errors.New("disabled by owner")
				}
				return ApprovalBrowser, nil
			},
		}, serverConn)
	}()

	reader := bufio.NewReader(clientConn)
	roundTrip := func(msg string) string {
		t.Helper()
		if _, err := clientConn.Write([]byte(msg + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	initRes := roundTrip(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if !strings.Contains(initRes, `"Executor"`) {
		t.Fatalf("unexpected initRes: %s", initRes)
	}
	listRes := roundTrip(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if !strings.Contains(listRes, "https://executor.example.com/account-connect/c1") {
		t.Fatalf("expected rewritten dashboard origin in SSE response, got: %s", listRes)
	}
	// Evict all MCP sessions on the server -> next request hits plain-text HTTP 404 and must transparently re-initialize.
	if err := mgr.doJSON(t.Context(), "usr_1", http.MethodPost, "/test/clear-sessions", nil, nil); err != nil {
		t.Fatalf("clear sessions: %v", err)
	}
	modeRes := roundTrip(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search"}}`)
	if !strings.Contains(modeRes, `"mode":"browser"`) {
		t.Fatalf("expected 404 session recovery with approval mode browser, got: %s", modeRes)
	}
	// Live revocation: next request must fail with revoked error and close the bridge.
	revoked.Store(true)
	revokedRes := roundTrip(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search"}}`)
	if !strings.Contains(revokedRes, "revoked") {
		t.Fatalf("expected revocation error, got: %s", revokedRes)
	}
	_ = clientConn.Close()
	<-bridgeDone

	// Dashboard proxy auto-pairing, stale client cookie stripping, and Authorization stripping.
	rr := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodGet, "https://executor.example.com/dashboard-check", nil)
	httpReq.Header.Set("Authorization", "Bearer ah_secret_token")
	httpReq.AddCookie(&http.Cookie{Name: "agenthold_session", Value: "secret-agenthold-cookie"})
	httpReq.AddCookie(&http.Cookie{Name: "executor_session_fake", Value: "stale-other-user-cookie"})
	mgr.ProxyRequest(rr, httpReq, "usr_1", "gate-sess-1", []string{"agenthold_session"})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "paired-ok") {
		t.Fatalf("proxy status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestPrivateURLValidationAndEgressGuard(t *testing.T) {
	for _, blocked := range []string{
		"http://127.0.0.1:8080/mcp",
		"http://localhost:3000/mcp",
		"http://10.0.0.5/mcp",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/mcp",
	} {
		if err := ValidatePublicURL(t.Context(), blocked); !errors.Is(err, ErrPrivateURL) {
			t.Fatalf("ValidatePublicURL(%q) = %v, want ErrPrivateURL", blocked, err)
		}
	}

	// Verify egressGuard blocks loopback fetches with 403 Forbidden.
	privateTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret-internal"))
	}))
	defer privateTarget.Close()

	guard, err := startEgressGuard(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	proxyURL, err := url.Parse(guard.url)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	resp, err := client.Get(privateTarget.URL)
	if err != nil {
		t.Fatalf("egress guard request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 from egress guard for loopback target, got %d", resp.StatusCode)
	}
}

func TestLiveExecutorIntegration(t *testing.T) {
	bin := os.Getenv("AGENTHOLD_TEST_EXECUTOR_BIN")
	if bin == "" {
		t.Skip("set AGENTHOLD_TEST_EXECUTOR_BIN to run live Executor integration test")
	}
	mgr := New(Config{
		Binary:         bin,
		DataDir:        t.TempDir(),
		MasterKey:      bytes.Repeat([]byte{0x77}, 32),
		StartupTimeout: 30 * time.Second,
	})
	defer mgr.Close()

	ov, err := mgr.Overview(t.Context(), "usr_live")
	if err != nil {
		t.Fatalf("live overview: %v", err)
	}
	if len(ov.Apps) == 0 || !ov.Apps[0].Builtin {
		t.Fatalf("expected built-in executor app, got %+v", ov.Apps)
	}

	// Verify live MCP bridge initialize + tools/list against real Executor binary.
	c, s := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.BridgeMCP(t.Context(), BridgeOptions{
			UserID:       "usr_live",
			AgentID:      "ag_live",
			ApprovalMode: ApprovalModel,
		}, s)
	}()
	r := bufio.NewReader(c)
	_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agenthold-test","version":"1"}}}` + "\n"))
	initLine, err := r.ReadString('\n')
	if err != nil || !strings.Contains(initLine, `"Executor"`) {
		t.Fatalf("live mcp init: %q err=%v", initLine, err)
	}
	_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"))
	listLine, err := r.ReadString('\n')
	if err != nil || !strings.Contains(listLine, `"execute"`) || !strings.Contains(listLine, `"search"`) {
		t.Fatalf("live mcp tools/list: %q err=%v", listLine, err)
	}
	_ = c.Close()
	<-done
}
