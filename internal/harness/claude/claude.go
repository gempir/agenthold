// Package claude is the adapter for Claude Code, driven through the ACP
// adapter for the Claude Agent SDK (@agentclientprotocol/claude-agent-acp).
package claude

import "github.com/gempir/agenthold/internal/harness"

func init() {
	harness.Register(&harness.Adapter{
		ID:      "claude-code",
		Name:    "Claude Code",
		Package: "@agentclientprotocol/claude-agent-acp",
		Command: []string{"claude-agent-acp"},
		Env: func(c harness.Config) map[string]string {
			env := map[string]string{
				// The image pins the version; the platform controls upgrades.
				"DISABLE_AUTOUPDATER": "1",
				// Logins run in the web terminal, which can show the URL and
				// accept a pasted code but cannot open a browser.
				"NO_BROWSER": "1",
			}
			if c.Model != "" {
				env["ANTHROPIC_MODEL"] = c.Model
			}
			return env
		},
		SecretPresets: []string{"anthropic"},
		// Claude Code keeps subscription logins here (Linux).
		CredentialPaths: []string{".claude/.credentials.json"},
		SkillsDir:       ".claude/skills",
		// Aliases ANTHROPIC_MODEL and the adapter's model option both resolve
		// to the newest model of the family the account can use.
		FallbackModels: []harness.Model{
			{ID: "default", Name: "Default", Description: "The recommended model for the account", Group: "Claude"},
			{ID: "opus", Name: "Opus", Description: "Newest Opus", Group: "Claude"},
			{ID: "sonnet", Name: "Sonnet", Description: "Newest Sonnet", Group: "Claude"},
			{ID: "haiku", Name: "Haiku", Description: "Newest Haiku", Group: "Claude"},
		},
		Login: logins{},
	})
}
