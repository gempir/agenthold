package tools

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

type fakeExecutorSyncer struct {
	avail    bool
	overview executor.Overview
	imported []string
}

func (f *fakeExecutorSyncer) Available() bool { return f.avail }
func (f *fakeExecutorSyncer) Overview(context.Context, string) (executor.Overview, error) {
	return f.overview, nil
}

func (f *fakeExecutorSyncer) ImportCatalogApp(_ context.Context, _, entry, name string) (executor.App, error) {
	f.imported = append(f.imported, entry)
	return executor.App{ID: "app_" + entry, Slug: entry, Name: name}, nil
}

func TestOrgToolsDeliverySyncAndSecretInjection(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, err := secrets.NewBox(bytes.Repeat([]byte{0x42}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}

	if err := st.CreateUser(ctx, db.CreateUserParams{
		ID: "u1", Email: "u1@example.com", Name: "User", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserRole(ctx, db.SetUserRoleParams{Role: "user", ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag1", OwnerID: "u1", Name: "bot", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentExecutor(ctx, db.SetAgentExecutorParams{ExecutorEnabled: 1, ExecutorApprovalMode: "model", UpdatedAt: 2, ID: "ag1"}); err != nil {
		t.Fatal(err)
	}

	// Declarative YAML seed via SyncOrgConfig.
	enabled := true
	cfg := config.Org{
		Tools: []config.OrgToolConfig{
			{
				Slug:             "internal-docs",
				Name:             "Internal Docs",
				Kind:             OrgToolKindMCPHTTP,
				AuthMode:         OrgToolAuthOrgShared,
				URL:              "https://docs.internal.example/mcp",
				Headers:          map[string]string{"Authorization": "Bearer {{org_secret}}"},
				Secret:           "real-org-docs-token",
				SecretHosts:      []string{"docs.internal.example"},
				EnabledByDefault: &enabled,
				AllowedRoles:     []string{"admin", "user"},
			},
			{
				Slug:             "admin-only",
				Name:             "Admin Only",
				Kind:             OrgToolKindMCPStdio,
				AuthMode:         OrgToolAuthNone,
				Command:          "npx",
				Args:             []string{"-y", "admin-tool"},
				EnabledByDefault: &enabled,
				AllowedRoles:     []string{"admin"},
			},
			{
				Slug:              "linear",
				Name:              "Linear",
				Kind:              OrgToolKindExecutorApp,
				AuthMode:          OrgToolAuthUserOAuth,
				ExecutorCatalogID: "linear",
				EnabledByDefault:  &enabled,
			},
		},
		Skills: []config.OrgSkillConfig{
			{Name: "company-style", Description: "Company style guide", Content: "Always write tests."},
		},
	}
	if err := SyncOrgConfig(ctx, st, box, cfg); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-sync without secret preserves existing encrypted secret.
	cfg.Tools[0].Secret = ""
	if err := SyncOrgConfig(ctx, st, box, cfg); err != nil {
		t.Fatal(err)
	}

	execSync := &fakeExecutorSyncer{avail: true}
	p := Provider{
		Store:             st,
		PlatformCommand:   "/usr/local/bin/sandboxd",
		ExecutorAvailable: execSync.Available,
		Executor:          execSync,
	}
	servers, err := p.MCPServers(ctx, "ag1", "ses1", acp.McpCapabilities{Http: true})
	if err != nil {
		t.Fatal(err)
	}

	var docsHTTP *acp.McpServerHttpInline
	for _, s := range servers {
		if s.Http != nil && s.Http.Name == "internal-docs" {
			docsHTTP = s.Http
		}
		if s.Stdio != nil && s.Stdio.Name == "admin-only" {
			t.Fatal("admin-only org tool leaked to regular user")
		}
	}
	if docsHTTP == nil {
		t.Fatalf("expected internal-docs HTTP MCP server, got %+v", servers)
	}
	if len(docsHTTP.Headers) != 1 || !strings.HasPrefix(docsHTTP.Headers[0].Value, "Bearer agenthold-org-placeholder-") {
		t.Fatalf("expected placeholder header, got %+v", docsHTTP.Headers)
	}
	if strings.Contains(docsHTTP.Headers[0].Value, "real-org-docs-token") {
		t.Fatal("plaintext org secret leaked into MCP header")
	}
	// executor_app org tool synced on demand.
	if len(execSync.imported) != 1 || execSync.imported[0] != "linear" {
		t.Fatalf("expected linear catalog app to be synced on demand, got %+v", execSync.imported)
	}

	// Egress proxy injection swaps placeholder ONLY for allowed host.
	u, _ := st.GetUser(ctx, "u1")
	injs, err := OrgToolInjections(ctx, st, box, u)
	if err != nil || len(injs) != 1 {
		t.Fatalf("org tool injections: %+v %v", injs, err)
	}
	ph := injs[0].Placeholder
	okReq := httptest.NewRequest(http.MethodPost, "https://docs.internal.example/mcp", nil)
	okReq.Header.Set("Authorization", "Bearer "+ph)
	applied, err := injs[0].Apply(okReq)
	if err != nil || !applied || okReq.Header.Get("Authorization") != "Bearer real-org-docs-token" {
		t.Fatalf("expected injection on allowed host: applied=%v err=%v hdr=%q", applied, err, okReq.Header.Get("Authorization"))
	}
	evilReq := httptest.NewRequest(http.MethodPost, "https://evil.example/steal", nil)
	evilReq.Header.Set("Authorization", "Bearer "+ph)
	applied, err = injs[0].Apply(evilReq)
	if err != nil || applied || evilReq.Header.Get("Authorization") != "Bearer "+ph {
		t.Fatalf("secret must NEVER be injected for disallowed host: applied=%v hdr=%q", applied, evilReq.Header.Get("Authorization"))
	}

	// MergeSkills: user skill overrides org skill of same name.
	orgSkills, err := st.ListDefaultOrgSkills(ctx)
	if err != nil || len(orgSkills) != 1 {
		t.Fatalf("org skills: %+v %v", orgSkills, err)
	}
	merged := MergeSkills([]db.Skill{{Name: "company-style", Content: "User override"}, {Name: "extra", Content: "Mine"}}, orgSkills)
	if len(merged) != 2 || merged[0].Name != "company-style" || merged[0].Content != "User override" {
		t.Fatalf("unexpected merged skills: %+v", merged)
	}
}
