// Package opencode is the adapter for OpenCode, which speaks ACP natively
// ("opencode acp").
package opencode

import (
	"encoding/json"

	"github.com/gempir/agenthold/internal/harness"
)

// Version installed in the base image.
const Version = "1.19.5"

type adapter struct{}

func init() { harness.Register(adapter{}) }

func (adapter) ID() string   { return "opencode" }
func (adapter) Name() string { return "OpenCode" }

func (adapter) Command(harness.Config) []string { return []string{"opencode", "acp"} }

func (adapter) Env(c harness.Config) map[string]string {
	cfg := map[string]any{"autoupdate": false}
	if c.Model != "" {
		cfg["model"] = c.Model // "provider/model", e.g. "anthropic/claude-sonnet-5-5"
	}
	b, _ := json.Marshal(cfg)
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(b)}
}

// API keys OpenCode picks up from the environment.
func (adapter) SecretPresets() []string { return []string{"anthropic", "openai", "openrouter"} }

func (adapter) CredentialPaths() []string { return []string{".local/share/opencode/auth.json"} }

func (adapter) InstallHint() string {
	return "Install Node.js 20+ and run: npm install -g opencode-ai@" + Version
}

// TerminalLogin implements harness.TerminalLoginer: OpenCode advertises its
// login without the command to run.
func (adapter) TerminalLogin(methodID string) ([]string, bool) {
	if methodID == "opencode-login" {
		return []string{"opencode", "auth", "login"}, true
	}
	return nil, false
}

// SkillsDir implements harness.SkillsDirer.
func (adapter) SkillsDir() string { return ".config/opencode/skills" }
