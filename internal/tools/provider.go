package tools

import (
	"context"
	"log/slog"

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
	// Executor optionally syncs enabled executor_app org tools into the agent
	// owner's Executor instance on demand when a session starts.
	Executor ExecutorSyncer
	Log      *slog.Logger
}

// PlatformServerName is the platform MCP server's name.
const PlatformServerName = "agenthold"

// ExecutorServerName is the built-in Executor MCP server's name.
const ExecutorServerName = "executor"

// MCPServers returns the agent's MCP servers with secret placeholders, enabled
// organization tools, the platform server (agent-to-agent tools), and the
// built-in Executor MCP server when enabled for this agent.
func (p Provider) MCPServers(ctx context.Context, agentID, sessionID string, caps acp.McpCapabilities) ([]acp.McpServer, error) {
	agent, err := p.Store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out, err := ACPServers(ctx, p.Store, agent, caps)
	if err != nil {
		return nil, err
	}
	// Merge enabled organization tools for the agent's owner.
	if owner, err := p.Store.GetUser(ctx, agent.OwnerID); err == nil {
		if orgTools, err := EffectiveOrgToolsForUser(ctx, p.Store, owner); err == nil {
			existingNames := map[string]bool{}
			for _, s := range out {
				if n := mcpServerName(s); n != "" {
					existingNames[n] = true
				}
			}
			for _, s := range OrgToolACPServers(orgTools, caps) {
				if n := mcpServerName(s); n != "" && !existingNames[n] {
					existingNames[n] = true
					out = append(out, s)
				}
			}
			if agent.ExecutorEnabled != 0 && p.Executor != nil && p.Executor.Available() {
				if err := SyncExecutorOrgApps(ctx, p.Executor, owner.ID, orgTools); err != nil && p.Log != nil {
					p.Log.Warn("sync executor org apps failed", "user", owner.ID, "agent", agentID, "err", err)
				}
			}
		}
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
		execAvail := (p.ExecutorAvailable != nil && p.ExecutorAvailable()) || (p.Executor != nil && p.Executor.Available())
		if agent.ExecutorEnabled != 0 && execAvail && !hasExecutorServer(out) {
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

func mcpServerName(s acp.McpServer) string {
	switch {
	case s.Stdio != nil:
		return s.Stdio.Name
	case s.Http != nil:
		return s.Http.Name
	case s.Sse != nil:
		return s.Sse.Name
	default:
		return ""
	}
}

func hasExecutorServer(servers []acp.McpServer) bool {
	for _, s := range servers {
		if mcpServerName(s) == ExecutorServerName {
			return true
		}
	}
	return false
}

// Prepare installs the agent's user skills and enabled organization skills where
// its harness reads them.
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
	orgSkills, err := p.Store.ListDefaultOrgSkills(ctx)
	if err != nil {
		return err
	}
	return InstallSkills(ctx, ts, ad.SkillsDir, MergeSkills(skills, orgSkills))
}
