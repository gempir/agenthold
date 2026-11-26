package tools

import (
	"context"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Provider implements session.Tools from the database.
type Provider struct {
	Store *store.Store
	// PlatformCommand runs the platform MCP server inside the sandbox
	// ("<sandboxd> mcp"). Empty leaves the platform tools out.
	PlatformCommand string
	// PlatformArgs are its arguments. Default ["mcp"].
	PlatformArgs []string
	// ExecutorAvailable reports whether the controller's built-in Executor runtime
	// is enabled and available. When true and an agent has executor_enabled != 0,
	// the built-in "executor" MCP server is exposed to its sessions automatically.
	ExecutorAvailable func() bool
}

// PlatformServerName is the platform MCP server's name.
const PlatformServerName = "agenthold"

// ExecutorServerName is the built-in Executor MCP server's name.
const ExecutorServerName = "executor"

// MCPServers returns the agent's MCP servers with secret placeholders, the
// platform server (agent-to-agent tools), and the built-in Executor MCP server
// when enabled for this agent.
func (p Provider) MCPServers(ctx context.Context, agentID, sessionID string, caps acp.McpCapabilities) ([]acp.McpServer, error) {
	agent, err := p.Store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out, err := ACPServers(ctx, p.Store, agent, caps)
	if err != nil {
		return nil, err
	}
	if p.PlatformCommand != "" {
		args := p.PlatformArgs
		if args == nil {
			args = []string{"mcp"}
		}
		out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{
			Name: PlatformServerName, Command: p.PlatformCommand, Args: args,
			Env: []acp.EnvVariable{{Name: "AGENTHOLD_SESSION", Value: sessionID}},
		}})
		if agent.ExecutorEnabled != 0 && p.ExecutorAvailable != nil && p.ExecutorAvailable() && !hasExecutorServer(out) {
			execArgs := make([]string, len(args))
			copy(execArgs, args)
			if len(execArgs) > 0 && execArgs[len(execArgs)-1] == "mcp" {
				execArgs[len(execArgs)-1] = "executor-mcp"
			} else {
				execArgs = []string{"executor-mcp"}
			}
			out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{
				Name: ExecutorServerName, Command: p.PlatformCommand, Args: execArgs,
				Env: []acp.EnvVariable{{Name: "AGENTHOLD_SESSION", Value: sessionID}},
			}})
		}
	}
	return out, nil
}

func hasExecutorServer(servers []acp.McpServer) bool {
	for _, s := range servers {
		if s.Stdio != nil && s.Stdio.Name == ExecutorServerName {
			return true
		}
		if s.Http != nil && s.Http.Name == ExecutorServerName {
			return true
		}
		if s.Sse != nil && s.Sse.Name == ExecutorServerName {
			return true
		}
	}
	return false
}

// Prepare installs the agent's skills where its harness reads them.
func (p Provider) Prepare(ctx context.Context, agentID string, ts *tunnel.Session) error {
	agent, err := p.Store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	ad, ok := harness.Get(agent.Harness)
	if !ok {
		return nil
	}
	if ad.SkillsDir == "" {
		return nil
	}
	skills, err := p.Store.ListAgentSkills(ctx, agentID)
	if err != nil {
		return err
	}
	return InstallSkills(ctx, ts, ad.SkillsDir, skills)
}
