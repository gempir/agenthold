// Package codex is the adapter for OpenAI Codex through its ACP adapter
// (@agentclientprotocol/codex-acp).
package codex

import "github.com/gempir/agenthold/internal/harness"

func init() {
	harness.Register(&harness.Adapter{
		ID:      "codex",
		Name:    "Codex",
		Package: "@agentclientprotocol/codex-acp",
		Command: []string{"codex-acp"},
		Env: func(harness.Config) map[string]string {
			return map[string]string{"NO_BROWSER": "1"}
		},
		SecretPresets:   []string{"openai"},
		CredentialPaths: []string{".codex/auth.json"},
		SkillsDir:       ".codex/skills",
		// No fallback models: Codex's list comes from its app server and
		// depends on the login (ChatGPT plan or API key), and a model can only
		// be chosen through the session's config options.
		// With an OpenAI API key in the environment (an attached secret's
		// placeholder), the "api-key" method signs in without user interaction.
		AutoAuthenticate: []string{"api-key"},
		Login:            logins{},
	})
}
