// Package claude is the adapter for Claude Code, driven directly through its
// native stream-json control protocol on stdio (matching T3 Code).
package claude

import "github.com/gempir/agenthold/internal/harness"

func init() {
	harness.Register(&harness.Adapter{
		ID:       "claude-code",
		Name:     "Claude Code",
		Protocol: harness.ProtocolClaude,
		Package:  "@anthropic-ai/claude-code",
		Command: []string{
			"claude", "-p",
			"--input-format", "stream-json",
			"--output-format", "stream-json",
			"--verbose",
			"--include-partial-messages",
			"--permission-prompt-tool", "stdio",
		},
		Env: func(c harness.Config) map[string]string {
			env := map[string]string{
				// The image pins the version; the platform controls upgrades.
				"DISABLE_AUTOUPDATER": "1",
				// Logins run in the web terminal, which can show the URL and
				// accept a pasted code but cannot open a browser.
				"NO_BROWSER": "1",
				// Agenthold does not surface Claude Code's follow-up prompt
				// suggestions, so disable them to avoid extra end-of-turn work.
				"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false",
				// Keep short retries for transient blips while failing promptly
				// on sustained rate limits.
				"CLAUDE_CODE_MAX_RETRIES": "3",
			}
			if c.Model != "" {
				env["ANTHROPIC_MODEL"] = c.Model
			}
			return env
		},
		SecretPresets:   []string{"anthropic"},
		CredentialPaths: []string{".claude/.credentials.json"},
		SkillsDir:       ".claude/skills",
		TerminalLogins: map[string][]string{
			"claude-login": {"claude", "auth", "login"},
		},
		FallbackModels: []harness.Model{
			{ID: "default", Name: "Default", Description: "The recommended model for the account", Group: "Claude"},
			{ID: "opus", Name: "Opus", Description: "Newest Opus", Group: "Claude"},
			{ID: "sonnet", Name: "Sonnet", Description: "Newest Sonnet", Group: "Claude"},
			{ID: "haiku", Name: "Haiku", Description: "Newest Haiku", Group: "Claude"},
		},
		Login: logins{},
	})
}
