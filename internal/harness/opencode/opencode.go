// Package opencode is the adapter for OpenCode, which speaks ACP natively
// ("opencode acp").
package opencode

import (
	"encoding/json"

	"github.com/gempir/agenthold/internal/harness"
)

func init() {
	harness.Register(&harness.Adapter{
		ID:      "opencode",
		Name:    "OpenCode",
		Package: "opencode-ai",
		Command: []string{"opencode", "acp"},
		Env: func(c harness.Config) map[string]string {
			// The microVM is the safety boundary: allow all tool calls without
			// prompting.
			cfg := map[string]any{
				"autoupdate": false,
				"permission": "allow",
			}
			if c.Model != "" {
				cfg["model"] = c.Model // "provider/model", e.g. "anthropic/claude-sonnet-5-5"
			}
			b, _ := json.Marshal(cfg)
			return map[string]string{"OPENCODE_CONFIG_CONTENT": string(b)}
		},
		// API keys OpenCode picks up from the environment.
		SecretPresets:   []string{"anthropic", "openai", "openrouter"},
		CredentialPaths: []string{".local/share/opencode/auth.json"},
		SkillsDir:       ".config/opencode/skills",
		// Its models depend on the providers configured in the sandbox (API
		// keys, logins, config): there is no list that holds for every agent,
		// but any "provider/model" id can be set.
		CustomModels: true,
		// OpenCode advertises its login without the command to run.
		TerminalLogins: map[string][]string{"opencode-login": {"opencode", "auth", "login"}},
	})
}
