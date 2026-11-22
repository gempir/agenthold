package session

import (
	"context"

	acp "github.com/coder/acp-go-sdk"
)

// protoClient is the wire-protocol driver owned by a harnessConn.
// Implementations speak ACP (Antigravity, OpenCode, ACP Store, local ACP, fake),
// native Codex App Server JSON-RPC (`codex app-server`), or native Claude Code
// `stream-json` control protocol (`claude -p`).
type protoClient interface {
	Initialize(ctx context.Context) (acp.InitializeResponse, []ConfigOption, map[string][]ConfigOption, error)
	Authenticate(ctx context.Context, methodID string) error
	NewSession(ctx context.Context, cwd string, mcp []acp.McpServer) (string, []ConfigOption, error)
	ResumeSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error)
	LoadSession(ctx context.Context, sessionID, cwd string, mcp []acp.McpServer) ([]ConfigOption, error)
	CloseSession(ctx context.Context, sessionID string) error
	SetConfigOption(ctx context.Context, sessionID string, opt ConfigOption, value string) ([]ConfigOption, error)
	InjectHistory(ctx context.Context, sessionID, threadID string, sel HandoffSelection) (bool, error)
	Prompt(ctx context.Context, sessionID, text string) (acp.PromptResponse, error)
	Cancel(ctx context.Context, sessionID string) error
	Done() <-chan struct{}
	Close()
}

// SlashCommand is a slash command reported by a harness (ACP
// available_commands_update or Claude initialize commands).
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"inputHint,omitempty"`
}

// ContextUsage tracks the latest context window occupancy and cost reported
// for a session.
type ContextUsage struct {
	UsedTokens int64   `json:"usedTokens"`
	MaxTokens  int64   `json:"maxTokens,omitempty"`
	CostUSD    float64 `json:"costUsd,omitempty"`
}

// HarnessResolver resolves built-in and user-configured ACP Store / local ACP
// harnesses, installing Registry agents on demand inside the sandbox.
type HarnessResolver interface {
	AdapterFor(ctx context.Context, harnessID string) (any, bool, error)
}
