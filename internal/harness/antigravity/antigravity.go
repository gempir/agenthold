// Package antigravity is the adapter for Google's Antigravity CLI (agy),
// which speaks ACP natively ("agy acp"). It is a native binary, not an npm
// package: images/base pins it by version and sha256 per architecture.
//
// Measured with agy 1.5.14 (no login, a dummy key against a local endpoint):
//
//   - It advertises the login methods oauth-personal, oauth-business,
//     gemini-api-key and agent-platform, plus terminal-login ("agy acp
//     --login": open a URL anywhere, paste the code back) to clients with
//     terminal auth. session/new without a login fails with "Authentication
//     required".
//   - gemini-api-key reads GEMINI_API_KEY and sends it as x-goog-api-key to
//     the Gemini API. Over ACP, authenticate is enough: unlike the TUI it
//     needs no "modelProvider" in settings.json (the choice is kept in
//     ~/.gemini/antigravity-cli/cache/acp-auth-state.json). Without the
//     variable, authenticate fails and nothing is remembered.
//   - Google sign-ins go to the OS keyring (Secret Service over D-Bus).
//     The guest has no session bus, so agy falls back to a file,
//     ~/.gemini/antigravity-cli/antigravity-oauth-token.
//   - Models are a "model" config option of base ids (gemini-4-neon) with a
//     per-model "thought_level" option (low…xhigh); the list depends on the
//     login. Third-party models (Claude) are only offered to the ACP clients
//     Google names (JetBrains, Zed, Xcode, Android Studio), so not here.
package antigravity

import "github.com/gempir/agenthold/internal/harness"

// Binary is the versions.env prefix of the pinned agy binary.
const Binary = "AGY"

func init() {
	harness.Register(&harness.Adapter{
		ID:     "antigravity",
		Name:   "Antigravity",
		Binary: Binary,
		// --disable-telemetry: no usage data to Google from this server
		// (Business logins never send it). Logs stay in
		// ~/.gemini/antigravity-cli/log.
		// --dangerously-skip-permissions: the microVM is the safety boundary.
		Command: []string{"agy", "acp", "--disable-telemetry", "--dangerously-skip-permissions"},
		Env: func(harness.Config) map[string]string {
			// The image pins the version; the platform controls upgrades.
			// SSH_CONNECTION + AGY_ACP_FORCE_FILE_STORAGE follow T3 Code so agy 1.5
			// stores Google sign-ins in ~/.gemini/antigravity-cli/antigravity-oauth-token
			// instead of trying an OS keyring.
			return map[string]string{
				"AGY_CLI_DISABLE_AUTO_UPDATE": "true",
				"AGY_ACP_FORCE_FILE_STORAGE":  "1",
				"SSH_CONNECTION":              "127.0.0.1 0 127.0.0.1 0",
				"PYTHONUNBUFFERED":            "1",
			}
		},
		SecretPresets: []string{"gemini"},
		// The file agy keeps a Google sign-in in when there is no keyring (as
		// in the guest), and its file for plugin and MCP server sign-ins.
		CredentialPaths: []string{
			".gemini/antigravity-cli/antigravity-oauth-token",
			".config/antigravity/plugin-secrets.json",
		},
		// User-level skills of the CLI and the Antigravity app.
		SkillsDir: ".gemini/config/skills",
		// With a Gemini API key in the environment (an attached secret's or
		// the account login's placeholder), this signs in without the user.
		// For Google account sign-ins, agy uses the stored oauth-personal token.
		AutoAuthenticate:    []string{"gemini-api-key", "agent-platform"},
		AuthenticateEagerly: true,
		// The model is a session config option; sessions start on agy's own
		// default and the turn switches them.
		FallbackModels: []harness.Model{
			{ID: "gemini-4-neon", Name: "Gemini 4 Neon", Group: "Gemini"},
			{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon", Group: "Gemini"},
			{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen", Group: "Gemini"},
			{ID: "gemini-4-argon", Name: "Gemini 4 Argon", Group: "Gemini"},
			{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Group: "Gemini"},
		},
		Login: logins{},
	})
}
