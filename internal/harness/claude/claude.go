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
				// Agenthold does not surface Claude Code's follow-up prompt
				// suggestions, so disable them to avoid extra end-of-turn work.
				"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false",
				// Claude Code retries a 429 or a 5xx ten times with exponential
				// backoff (about three minutes, measured with 0.107) and says
				// nothing over ACP meanwhile. Three retries take about 4s: a
				// blip is ridden out, and an exhausted rate limit fails while
				// the user still waits. The trade-off: an outage or rate limit
				// that lasts longer now ends the turn instead of stalling it,
				// and the user (or the schedule) sends the message again.
				// Subscription usage limits are not retried either way, and 529
				// overloaded errors have their own budget (about 2s, three
				// attempts; CLAUDE_CODE_OVERLOADED_RETRY_*) that this leaves alone.
				"CLAUDE_CODE_MAX_RETRIES": "3",
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
