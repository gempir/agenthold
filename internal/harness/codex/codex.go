// Package codex is the adapter for OpenAI Codex driven directly through its
// native JSON-RPC app-server protocol (`codex app-server`), matching T3 Code.
package codex

import "github.com/gempir/agenthold/internal/harness"

func init() {
	harness.Register(&harness.Adapter{
		ID:       "codex",
		Name:     "Codex",
		Protocol: harness.ProtocolCodex,
		Package:  "@openai/codex",
		// thread_unload_delay_secs=0 matches T3 Code: session lifecycle is owned
		// by the server rather than Codex's own unload timer. The microVM is the
		// safety boundary, so approvals and Codex's inner sandbox are disabled.
		Command: []string{
			"codex", "app-server",
			"-c", "thread_unload_delay_secs=0",
			"-c", `approval_policy="never"`,
			"-c", `sandbox_mode="danger-full-access"`,
		},
		Env: func(harness.Config) map[string]string {
			return map[string]string{"NO_BROWSER": "1"}
		},
		SecretPresets:   []string{"openai"},
		CredentialPaths: []string{".codex/auth.json"},
		SkillsDir:       ".codex/skills",
		// No fallback models: Codex's list comes from model/list on the app
		// server and depends on the login (ChatGPT plan or API key).
		TerminalLogins: map[string][]string{
			"chatgpt-device": {"codex", "login", "--device-auth"},
		},
		AutoAuthenticate: []string{"api-key"},
		Login:            logins{},
	})
}
