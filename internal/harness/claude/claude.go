// Package claude is the adapter for Claude Code, driven through the ACP
// adapter for the Claude Agent SDK (@agentclientprotocol/claude-agent-acp).
package claude

import "github.com/gempir/agenthold/internal/harness"

// Version of the ACP adapter installed in the base image.
const Version = "0.107.0"

type adapter struct{}

func init() { harness.Register(adapter{}) }

func (adapter) ID() string   { return "claude-code" }
func (adapter) Name() string { return "Claude Code" }

func (adapter) Command(harness.Config) []string { return []string{"claude-agent-acp"} }

func (adapter) Env(c harness.Config) map[string]string {
	env := map[string]string{
		// The image pins the version; the platform controls upgrades.
		"DISABLE_AUTOUPDATER": "1",
		// Logins run in the web terminal, which can show the URL and accept a
		// pasted code but cannot open a browser.
		"NO_BROWSER": "1",
	}
	if c.Model != "" {
		env["ANTHROPIC_MODEL"] = c.Model
	}
	return env
}

func (adapter) SecretPresets() []string { return []string{"anthropic"} }

// Claude Code keeps subscription logins here (Linux).
func (adapter) CredentialPaths() []string { return []string{".claude/.credentials.json"} }

func (adapter) InstallHint() string {
	return "Install Node.js 20+ and run: npm install -g @agentclientprotocol/claude-agent-acp@" + Version
}

// SkillsDir implements harness.SkillsDirer.
func (adapter) SkillsDir() string { return ".claude/skills" }
