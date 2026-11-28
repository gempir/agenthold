package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	fakeharness "github.com/gempir/agenthold/internal/harness/fake"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestOrgToolsRBACDeliveryAndEgressInjection(t *testing.T) {
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Auth.Admins = []string{"admin@example.com"}
		cfg.Auth.DefaultRole = config.RoleUser
	})
	admin := e.login("admin-sub", "Admin", "admin@example.com")
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	const orgSecret = "org-linear-super-secret-value-9876"

	// Regular user cannot create org tools (403).
	expect(t, "user create org tool forbidden", alice.do(http.MethodPost, "/org/tools", map[string]any{
		"slug": "nope", "name": "Nope", "kind": "mcp_http",
		"config": map[string]any{"url": "https://nope.example/mcp"},
	}, nil), http.StatusForbidden)

	// Admin creates an org_shared mcp_http tool.
	code, createBody := admin.rawJSON(http.MethodPost, "/org/tools", map[string]any{
		"slug":        "internal-docs",
		"name":        "Internal Docs",
		"description": "Company docs MCP",
		"kind":        "mcp_http",
		"authMode":    "org_shared",
		"config": map[string]any{
			"url":     "https://docs.company.example/mcp",
			"headers": map[string]string{"Authorization": "Bearer {{org_secret}}"},
		},
		"secret":           orgSecret,
		"secretHosts":      []string{"docs.company.example"},
		"enabledByDefault": true,
		"allowedRoles":     []string{"admin", "user"},
	})
	expect(t, "admin create org tool", code, http.StatusCreated)
	if strings.Contains(createBody, orgSecret) {
		t.Fatal("create response leaked org secret")
	}
	var docsTool gen.OrgTool
	if err := json.Unmarshal([]byte(createBody), &docsTool); err != nil {
		t.Fatal(err)
	}
	if !docsTool.HasSecret || !docsTool.EnabledByDefault || !docsTool.UserEnabled {
		t.Fatalf("unexpected docsTool: %+v", docsTool)
	}
	// Secret stored encrypted, never plaintext.
	dbRow, err := e.store.GetOrgTool(t.Context(), docsTool.Id)
	if err != nil || bytes.Contains(dbRow.EncryptedSecret, []byte(orgSecret)) {
		t.Fatalf("org tool secret stored insecurely: err=%v", err)
	}

	// Admin creates an admin-only org tool.
	var adminOnlyTool gen.OrgTool
	expect(t, "admin create admin-only tool", admin.do(http.MethodPost, "/org/tools", map[string]any{
		"slug":         "admin-ops",
		"name":         "Admin Ops",
		"kind":         "mcp_stdio",
		"authMode":     "none",
		"config":       map[string]any{"command": "npx", "args": []string{"-y", "ops-mcp"}},
		"allowedRoles": []string{"admin"},
	}, &adminOnlyTool), http.StatusCreated)

	// Alice sees internal-docs (never the secret), does NOT see admin-ops.
	code, listBody := alice.rawJSON(http.MethodGet, "/org/tools", nil)
	expect(t, "alice list org tools", code, http.StatusOK)
	if strings.Contains(listBody, orgSecret) {
		t.Fatal("GET /org/tools leaked org secret")
	}
	var aliceTools []gen.OrgTool
	_ = json.Unmarshal([]byte(listBody), &aliceTools)
	if len(aliceTools) != 1 || aliceTools[0].Slug != "internal-docs" || !aliceTools[0].HasSecret {
		t.Fatalf("alice org tools: %+v", aliceTools)
	}
	// Alice cannot toggle admin-only tool (404) or update/delete org tools (403).
	expect(t, "alice toggle admin tool", alice.do(http.MethodPut, "/me/org-tools/"+adminOnlyTool.Id, map[string]any{"enabled": true}, nil), http.StatusNotFound)
	expect(t, "alice update org tool", alice.do(http.MethodPut, "/org/tools/"+docsTool.Id, map[string]any{
		"slug": "internal-docs", "name": "Hacked", "kind": "mcp_http", "config": map[string]any{"url": "https://evil.example"},
	}, nil), http.StatusForbidden)
	expect(t, "alice delete org tool", alice.do(http.MethodDelete, "/org/tools/"+docsTool.Id, nil, nil), http.StatusForbidden)

	// Alice's agent automatically receives internal-docs with placeholder (never plaintext).
	ag := e.fakeAgent(alice, "")
	got := newSessionText(t, e, alice, ag.Id, "tools")
	if !strings.Contains(got, "http:internal-docs:https://docs.company.example/mcp:Authorization=Bearer agenthold-org-placeholder-"+docsTool.Id) {
		t.Fatalf("org MCP server not auto-delivered with placeholder: %s", got)
	}
	if strings.Contains(got, orgSecret) {
		t.Fatal("org secret entered sandbox")
	}
	if strings.Contains(got, "admin-ops") {
		t.Fatal("admin-only org tool delivered to user's agent")
	}

	// Egress proxy injects the real org secret ONLY for docs.company.example.
	_ = e.store.SetSandboxSubnet(t.Context(), db.SetSandboxSubnetParams{Subnet: "10.213.1.0/28", AgentID: ag.Id})
	e.directory.Invalidate()
	pa, err := e.directory.AgentForSource(t.Context(), netip.MustParseAddr("10.213.1.5"))
	if err != nil || pa == nil {
		t.Fatalf("directory lookup: %+v %v", pa, err)
	}
	ph := "agenthold-org-placeholder-" + docsTool.Id
	okReq := injectInto(pa, "https://docs.company.example/mcp", "Authorization", "Bearer "+ph)
	if okReq.Header.Get("Authorization") != "Bearer "+orgSecret {
		t.Fatalf("egress proxy did not inject org secret: %q", okReq.Header.Get("Authorization"))
	}
	evilReq := injectInto(pa, "https://evil.example/mcp", "Authorization", "Bearer "+ph)
	if evilReq.Header.Get("Authorization") != "Bearer "+ph {
		t.Fatalf("egress proxy leaked org secret to disallowed host: %q", evilReq.Header.Get("Authorization"))
	}

	// Alice disables internal-docs for herself -> omitted from next session & egress.
	var toggled gen.OrgTool
	expect(t, "alice disable org tool", alice.do(http.MethodPut, "/me/org-tools/"+docsTool.Id, map[string]any{"enabled": false}, &toggled), http.StatusOK)
	if toggled.UserEnabled {
		t.Fatalf("expected userEnabled=false: %+v", toggled)
	}
	got = newSessionText(t, e, alice, ag.Id, "tools")
	if strings.Contains(got, "internal-docs") {
		t.Fatalf("disabled org tool still delivered: %s", got)
	}
	if a := e.injections(ag.Id); len(a.Injections) != 0 {
		t.Fatalf("disabled org tool still injected: %+v", a.Injections)
	}

	// Admin updates and deletes org tool.
	var updated gen.OrgTool
	expect(t, "admin update tool", admin.do(http.MethodPut, "/org/tools/"+docsTool.Id, map[string]any{
		"slug": "internal-docs", "name": "Internal Docs v2", "kind": "mcp_http", "authMode": "org_shared",
		"config": map[string]any{"url": "https://docs.company.example/mcp", "headers": map[string]string{"Authorization": "Bearer {{org_secret}}"}},
	}, &updated), http.StatusOK)
	if updated.Name != "Internal Docs v2" || !updated.HasSecret {
		t.Fatalf("expected secret preserved on update: %+v", updated)
	}
	expect(t, "admin delete tool", admin.do(http.MethodDelete, "/org/tools/"+docsTool.Id, nil, nil), http.StatusNoContent)
}

func TestOrgSkillsAutoDeliveryAndOverride(t *testing.T) {
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Auth.Admins = []string{"admin@example.com"}
		cfg.Auth.DefaultRole = config.RoleUser
	})
	admin := e.login("admin-sub", "Admin", "admin@example.com")
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	// User cannot write org skills (403).
	expect(t, "user create org skill forbidden", alice.do(http.MethodPost, "/org/skills", map[string]any{
		"name": "deploy-guide", "description": "Deploy", "content": "Run deploy.",
	}, nil), http.StatusForbidden)

	// Admin creates enabled and disabled org skills.
	var enabledSkill, disabledSkill gen.OrgSkill
	expect(t, "admin create enabled org skill", admin.do(http.MethodPost, "/org/skills", map[string]any{
		"name": "deploy-guide", "description": "Org deploy guide", "content": "# Org Deploy\nUse CI.", "enabledByDefault": true,
	}, &enabledSkill), http.StatusCreated)
	expect(t, "admin create disabled org skill", admin.do(http.MethodPost, "/org/skills", map[string]any{
		"name": "draft-skill", "description": "Draft", "content": "WIP", "enabledByDefault": false,
	}, &disabledSkill), http.StatusCreated)

	// Any authenticated user can read org skills.
	var orgSkills []gen.OrgSkill
	expect(t, "alice list org skills", alice.do(http.MethodGet, "/org/skills", nil, &orgSkills), http.StatusOK)
	if len(orgSkills) != 2 {
		t.Fatalf("expected 2 org skills, got %+v", orgSkills)
	}

	ag := e.fakeAgent(alice, "")
	// Agent-created skill in sandbox must remain untouched.
	own := filepath.Join(e.home(ag.Id), ".fakeagent/skills/agent-own/SKILL.md")
	_ = os.MkdirAll(filepath.Dir(own), 0o755)
	_ = os.WriteFile(own, []byte("agent-written"), 0o644)

	got := newSessionText(t, e, alice, ag.Id, "tools")
	if !strings.Contains(got, "deploy-guide(") || strings.Contains(got, "draft-skill(") || !strings.Contains(got, "agent-own") {
		t.Fatalf("unexpected skills delivered: %s", got)
	}
	md, err := os.ReadFile(filepath.Join(e.home(ag.Id), ".fakeagent/skills/deploy-guide/SKILL.md"))
	if err != nil || !strings.Contains(string(md), "# Org Deploy") {
		t.Fatalf("org SKILL.md: %q %v", md, err)
	}

	// Admin deletes org skill -> removed on next session, agent-own stays.
	expect(t, "admin delete org skill", admin.do(http.MethodDelete, "/org/skills/"+enabledSkill.Id, nil, nil), http.StatusNoContent)
	got = newSessionText(t, e, alice, ag.Id, "tools")
	if strings.Contains(got, "deploy-guide(") || !strings.Contains(got, "agent-own") {
		t.Fatalf("expected removed org skill to be cleaned up while keeping agent-own: %s", got)
	}
}

func TestOrgProviderLoginFallbackAndPersonalOverride(t *testing.T) {
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Auth.Admins = []string{"admin@example.com"}
		cfg.Auth.DefaultRole = config.RoleUser
	})
	admin := e.login("admin-sub", "Admin", "admin@example.com")
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	const orgKey = "fake-org-shared-key-9999"
	const personalKey = "fake-alice-personal-key-1111"

	// Non-admin cannot configure org providers.
	expect(t, "user upsert org provider forbidden", alice.do(http.MethodPut, "/org/providers/fake", map[string]any{
		"kind": "api_key", "credential": orgKey,
	}, nil), http.StatusForbidden)

	// Admin configures org provider login for "fake" and "claude-code" (gateway).
	code, body := admin.rawJSON(http.MethodPut, "/org/providers/fake", map[string]any{
		"kind": "api_key", "label": "Shared Fake Key", "credential": orgKey, "enabled": true,
	})
	expect(t, "admin put org fake provider", code, http.StatusOK)
	if strings.Contains(body, orgKey) {
		t.Fatal("PUT /org/providers leaked credential")
	}
	code, listBody := alice.rawJSON(http.MethodGet, "/org/providers", nil)
	expect(t, "alice list org providers", code, http.StatusOK)
	if strings.Contains(listBody, orgKey) {
		t.Fatal("GET /org/providers leaked credential")
	}

	// New user Alice has NO personal login -> falls back to Org Provider Login!
	ag := e.fakeAgent(alice, "")
	if ag.Login.Source != gen.AgentLoginSourceOrg || !ag.Login.Connected {
		t.Fatalf("expected org fallback login, got %+v", ag.Login)
	}
	answer := e.ask(alice, ag.Id, "whoami")
	ph := strings.TrimSuffix(strings.TrimPrefix(answer, "token="), " account=")
	if !strings.HasPrefix(ph, "fake-agenthold-placeholder-") || strings.Contains(answer, orgKey) {
		t.Fatalf("harness saw %q", answer)
	}
	req := injectInto(e.injections(ag.Id), "https://"+fakeharness.APIHost+"/v1/x", "Authorization", "Bearer "+ph)
	if req.Header.Get("Authorization") != "Bearer "+orgKey {
		t.Fatalf("expected org key injected, got %q", req.Header.Get("Authorization"))
	}

	// Alice connects her own personal login -> overrides org fallback!
	expect(t, "alice connects personal", alice.connect("fake", "token", personalKey), http.StatusOK)
	if got := alice.agent(ag.Id).Login; got.Source != gen.AgentLoginSourceAccount || !got.Connected {
		t.Fatalf("expected personal login override, got %+v", got)
	}
	a := e.injections(ag.Id)
	if len(a.Injections) != 1 || a.Injections[0].Value != personalKey {
		t.Fatalf("expected personal key injected, got %+v", a.Injections)
	}

	// Alice disconnects personal login -> falls back to org login again!
	expect(t, "alice disconnects personal", alice.do(http.MethodDelete, "/me/provider-logins/fake", nil, nil), http.StatusNoContent)
	if got := alice.agent(ag.Id).Login; got.Source != gen.AgentLoginSourceOrg || !got.Connected {
		t.Fatalf("expected org fallback after personal disconnect, got %+v", got)
	}

	// Test gateway org provider login for claude-code.
	expect(t, "admin put claude gateway", admin.do(http.MethodPut, "/org/providers/claude-code", map[string]any{
		"kind": "gateway", "credential": "gw-secret-token-4242", "baseUrl": "https://gateway.corp.example/anthropic", "enabled": true,
	}, nil), http.StatusOK)
	var claudeAg gen.Agent
	expect(t, "switch harness to claude-code", alice.do(http.MethodPatch, "/agents/"+ag.Id, map[string]any{"harness": "claude-code"}, &claudeAg), http.StatusOK)
	if claudeAg.Login.Source != gen.AgentLoginSourceOrg || !claudeAg.Login.Connected {
		t.Fatalf("expected org gateway login for claude-code: %+v", claudeAg.Login)
	}
	ca := e.injections(ag.Id)
	if len(ca.Injections) == 0 {
		t.Fatal("expected gateway injection")
	}
	gwReq := injectInto(ca, "https://gateway.corp.example/anthropic/v1/messages", "X-Api-Key", ca.Injections[0].Placeholder)
	if gwReq.Header.Get("X-Api-Key") != "gw-secret-token-4242" {
		t.Fatalf("gateway injection failed: %q", gwReq.Header.Get("X-Api-Key"))
	}

	// Admin deletes org provider login.
	expect(t, "admin delete org fake provider", admin.do(http.MethodDelete, "/org/providers/fake", nil, nil), http.StatusNoContent)
}

func TestOrgToolOneStepConnectAndPolicy(t *testing.T) {
	mgr := executor.New(executor.Config{
		Binary:          apiFakeExecutorBin(t),
		DataDir:         t.TempDir(),
		MasterKey:       bytes.Repeat([]byte{0x33}, 32),
		DashboardOrigin: "https://executor-demo.agenthold.dev",
		StartupTimeout:  10 * time.Second,
	})
	defer mgr.Close()

	allowCustom := false
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Auth.Admins = []string{"admin@example.com"}
		cfg.Auth.DefaultRole = config.RoleUser
		cfg.Org.AllowUserCustomMCP = &allowCustom
	})
	e.api.Executor = mgr
	e.platform.Executor = mgr

	admin := e.login("admin-sub", "Admin", "admin@example.com")
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	// allow_user_custom_mcp=false blocks non-admin custom MCP creation, allows admin.
	expect(t, "user custom mcp blocked", alice.do(http.MethodPost, "/mcp-servers", gen.MCPServerInput{
		Name: "custom", Transport: "http", Url: ptr("https://custom.example/mcp"),
	}, nil), http.StatusForbidden)
	expect(t, "admin custom mcp allowed", admin.do(http.MethodPost, "/mcp-servers", gen.MCPServerInput{
		Name: "custom", Transport: "http", Url: ptr("https://custom.example/mcp"),
	}, nil), http.StatusCreated)

	// Admin creates an executor_app org tool for github (matches fake executor overview).
	var ghTool gen.OrgTool
	expect(t, "admin create github org tool", admin.do(http.MethodPost, "/org/tools", map[string]any{
		"slug": "github", "name": "GitHub", "kind": "executor_app", "authMode": "user_oauth",
		"config": map[string]any{"executorCatalogId": "github"},
	}, &ghTool), http.StatusCreated)
	var linearTool gen.OrgTool
	expect(t, "admin create linear org tool", admin.do(http.MethodPost, "/org/tools", map[string]any{
		"slug": "linear", "name": "Linear", "kind": "executor_app", "authMode": "user_oauth",
		"config": map[string]any{"executorCatalogId": "linear"},
	}, &linearTool), http.StatusCreated)

	// One-step connect transparently starts Executor & imports Linear catalog app.
	var linConn gen.OrgToolConnectResponse
	expect(t, "connect linear org tool", alice.do(http.MethodPost, "/me/org-tools/"+linearTool.Id+"/connect", nil, &linConn), http.StatusOK)
	if linConn.App == nil || linConn.App.Id != "app_lin" {
		t.Fatalf("expected linear imported via one-step connect: %+v", linConn)
	}
	// Now Executor is running: GET /org/tools reports github as connected.
	var list []gen.OrgTool
	expect(t, "list org tools while executor running", alice.do(http.MethodGet, "/org/tools", nil, &list), http.StatusOK)
	foundGH := false
	for _, item := range list {
		if item.Slug == "github" {
			foundGH = true
			if !item.Connected {
				t.Fatalf("expected github org tool connected=true, got %+v", item)
			}
		}
	}
	if !foundGH {
		t.Fatalf("github org tool missing from list: %+v", list)
	}
}

func TestRequestSecretAndAccountConnectTools(t *testing.T) {
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Auth.Admins = []string{"admin@example.com"}
		cfg.Auth.DefaultRole = config.RoleUser
	})
	admin := e.login("admin-sub", "Admin", "admin@example.com")
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	var linearTool gen.OrgTool
	expect(t, "admin create linear tool", admin.do(http.MethodPost, "/org/tools", map[string]any{
		"slug": "linear", "name": "Linear", "kind": "executor_app", "authMode": "user_oauth",
		"config": map[string]any{"executorCatalogId": "linear"},
	}, &linearTool), http.StatusCreated)

	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "create session", alice.do(http.MethodPost, "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), http.StatusCreated)

	// 1. Test request_secret: agent calls tool, user submits secret via API.
	const submittedSecret = "sk-live-never-in-chat-history-12345"
	toolDone := make(chan string, 1)
	toolErr := make(chan error, 1)
	go func() {
		res, err := e.platform.Call(t.Context(), ag.Id, sess.Id, "request_secret", json.RawMessage(`{
			"name": "stripe-key",
			"description": "Stripe API key for billing lookups",
			"hosts": ["api.stripe.com"],
			"env_var": "STRIPE_API_KEY",
			"header": "Authorization",
			"wait_seconds": 30
		}`))
		if err != nil {
			toolErr <- err
			return
		}
		toolDone <- res.(string)
	}()

	// Poll session events until secret_request appears.
	var reqID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var evs []gen.SessionEvent
		expect(t, "events", alice.do(http.MethodGet, "/sessions/"+sess.Id+"/events", nil, &evs), http.StatusOK)
		for _, ev := range evs {
			if ev.Type == gen.SessionEventTypeSecretRequest {
				if id, ok := ev.Data["requestId"].(string); ok {
					reqID = id
				}
			}
		}
		if reqID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reqID == "" {
		t.Fatal("timed out waiting for secret_request event")
	}

	// Submit the secret through the dedicated endpoint.
	code, respBody := alice.rawJSON(http.MethodPost, "/sessions/"+sess.Id+"/secret-requests/"+reqID, map[string]any{
		"value": submittedSecret,
	})
	expect(t, "submit secret request", code, http.StatusOK)
	if strings.Contains(respBody, submittedSecret) {
		t.Fatal("secret request submission response leaked secret value")
	}
	var submitResp gen.SecretRequestResponse
	_ = json.Unmarshal([]byte(respBody), &submitResp)
	if submitResp.Status != gen.Submitted || submitResp.Placeholder == nil || !strings.HasPrefix(*submitResp.Placeholder, "agenthold-placeholder-") {
		t.Fatalf("unexpected submit response: %+v", submitResp)
	}

	// Wait for platform tool call to wake up and return.
	select {
	case err := <-toolErr:
		t.Fatalf("request_secret tool failed: %v", err)
	case toolOut := <-toolDone:
		if strings.Contains(toolOut, submittedSecret) {
			t.Fatal("request_secret tool output leaked secret value")
		}
		if !strings.Contains(toolOut, `"status":"submitted"`) || !strings.Contains(toolOut, *submitResp.Placeholder) {
			t.Fatalf("unexpected tool output: %s", toolOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for request_secret tool to wake")
	}

	// Verify secret NEVER appears in session event history!
	code, evRaw := alice.rawJSON(http.MethodGet, "/sessions/"+sess.Id+"/events", nil)
	expect(t, "list events after secret submit", code, http.StatusOK)
	if strings.Contains(evRaw, submittedSecret) {
		t.Fatal("submitted secret leaked into session event history!")
	}
	if !strings.Contains(evRaw, `"type":"secret_request_resolved"`) {
		t.Fatalf("missing secret_request_resolved event: %s", evRaw)
	}

	// Verify egress proxy now injects the submitted secret for api.stripe.com!
	stripeReq := injectInto(e.injections(ag.Id), "https://api.stripe.com/v1/charges", "Authorization", "Bearer "+*submitResp.Placeholder)
	if stripeReq.Header.Get("Authorization") != "Bearer "+submittedSecret {
		t.Fatalf("expected submitted secret injected by egress proxy, got %q", stripeReq.Header.Get("Authorization"))
	}

	// 2. Test declining a secret request.
	resAny, err := e.platform.Call(t.Context(), ag.Id, sess.Id, "request_secret", json.RawMessage(`{
		"name": "optional-key", "description": "Optional", "hosts": ["opt.example.com"], "wait_seconds": 0
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var pendingOut struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	_ = json.Unmarshal([]byte(resAny.(string)), &pendingOut)
	if pendingOut.Status != "pending" || pendingOut.RequestID == "" {
		t.Fatalf("expected pending status: %+v", pendingOut)
	}
	var declineResp gen.SecretRequestResponse
	expect(t, "decline secret request", alice.do(http.MethodPost, "/sessions/"+sess.Id+"/secret-requests/"+pendingOut.RequestID, map[string]any{
		"decline": true,
	}, &declineResp), http.StatusOK)
	if declineResp.Status != gen.Declined {
		t.Fatalf("expected declined, got %+v", declineResp)
	}

	// 3. Test request_account_connect platform tool.
	acRes, err := e.platform.Call(t.Context(), ag.Id, sess.Id, "request_account_connect", json.RawMessage(`{
		"tool_slug": "linear",
		"reason": "Need to read Linear issues"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(acRes.(string), `"slug":"linear"`) || !strings.Contains(acRes.(string), "/api/v1/me/org-tools/"+linearTool.Id+"/connect") {
		t.Fatalf("unexpected account connect tool result: %v", acRes)
	}
	var evs []gen.SessionEvent
	expect(t, "events after account connect", alice.do(http.MethodGet, "/sessions/"+sess.Id+"/events", nil, &evs), http.StatusOK)
	foundAC := false
	for _, ev := range evs {
		if ev.Type == gen.SessionEventTypeAccountConnectRequest && ev.Data["slug"] == "linear" && ev.Data["reason"] == "Need to read Linear issues" {
			foundAC = true
		}
	}
	if !foundAC {
		t.Fatalf("account_connect_request event not found in session stream: %+v", evs)
	}
}
