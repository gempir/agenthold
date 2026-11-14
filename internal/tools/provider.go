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
}

// PlatformServerName is the platform MCP server's name.
const PlatformServerName = "agenthold"

// MCPServers returns the agent's MCP servers with secret placeholders, and
// the platform server (agent-to-agent tools) for this session.
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
		out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{
			Name: PlatformServerName, Command: p.PlatformCommand, Args: []string{"mcp"},
			Env: []acp.EnvVariable{{Name: "AGENTHOLD_SESSION", Value: sessionID}},
		}})
	}
	return out, nil
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
	sd, ok := ad.(harness.SkillsDirer)
	if !ok {
		return nil
	}
	skills, err := p.Store.ListAgentSkills(ctx, agentID)
	if err != nil {
		return err
	}
	return InstallSkills(ctx, ts, sd.SkillsDir(), skills)
}
