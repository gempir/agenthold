package tools

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestProviderInjectsExecutorMCPServer(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "u1", Email: "a@example.com", Name: "Alice", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag1", OwnerID: "u1", Name: "bot", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, IdleMinutes: 15, PermissionPolicy: "auto", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	available := true
	p := Provider{
		Store:             st,
		PlatformCommand:   "/usr/local/bin/sandboxd",
		PlatformArgs:      []string{"mcp"},
		ExecutorAvailable: func() bool { return available },
	}
	// Disabled by default -> executor server omitted.
	servers, err := p.MCPServers(ctx, "ag1", "sess1", acp.McpCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if hasExecutorServer(servers) {
		t.Fatalf("expected executor server to be omitted by default")
	}

	// Enable Executor on the agent -> injected.
	if err := st.SetAgentExecutor(ctx, db.SetAgentExecutorParams{ExecutorEnabled: 1, ExecutorApprovalMode: "model", UpdatedAt: 2, ID: "ag1"}); err != nil {
		t.Fatal(err)
	}
	servers, err = p.MCPServers(ctx, "ag1", "sess1", acp.McpCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	var gotPlatform, gotExecutor *acp.McpServerStdio
	for _, s := range servers {
		if s.Stdio != nil && s.Stdio.Name == PlatformServerName {
			gotPlatform = s.Stdio
		}
		if s.Stdio != nil && s.Stdio.Name == ExecutorServerName {
			gotExecutor = s.Stdio
		}
	}
	if gotPlatform == nil || gotExecutor == nil {
		t.Fatalf("expected both platform and executor MCP servers, got %+v", servers)
	}
	if gotExecutor.Command != "/usr/local/bin/sandboxd" || len(gotExecutor.Args) != 1 || gotExecutor.Args[0] != "executor-mcp" {
		t.Fatalf("unexpected executor server command/args: %+v", gotExecutor)
	}

	// Disable Executor on the agent -> omitted.
	if err := st.SetAgentExecutor(ctx, db.SetAgentExecutorParams{ExecutorEnabled: 0, ExecutorApprovalMode: "model", UpdatedAt: 3, ID: "ag1"}); err != nil {
		t.Fatal(err)
	}
	servers, err = p.MCPServers(ctx, "ag1", "sess1", acp.McpCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if hasExecutorServer(servers) {
		t.Fatalf("expected executor server to be omitted when disabled on agent")
	}

	// Re-enable on agent, mark controller unavailable -> omitted.
	if err := st.SetAgentExecutor(ctx, db.SetAgentExecutorParams{ExecutorEnabled: 1, ExecutorApprovalMode: "model", UpdatedAt: 4, ID: "ag1"}); err != nil {
		t.Fatal(err)
	}
	available = false
	servers, err = p.MCPServers(ctx, "ag1", "sess1", acp.McpCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if hasExecutorServer(servers) {
		t.Fatalf("expected executor server to be omitted when controller unavailable")
	}
}
