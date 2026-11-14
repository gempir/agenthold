// Package codex is the adapter for OpenAI Codex through its ACP adapter
// (@agentclientprotocol/codex-acp).
package codex

import "github.com/gempir/agenthold/internal/harness"

// Version installed in the base image.
const Version = "2.6.0"

type adapter struct{}

func init() { harness.Register(adapter{}) }

func (adapter) ID() string                      { return "codex" }
func (adapter) Name() string                    { return "Codex" }
func (adapter) Command(harness.Config) []string { return []string{"codex-acp"} }

func (adapter) Env(harness.Config) map[string]string {
	return map[string]string{"NO_BROWSER": "1"}
}

func (adapter) SecretPresets() []string   { return []string{"openai"} }
func (adapter) CredentialPaths() []string { return []string{".codex/auth.json"} }

func (adapter) InstallHint() string {
	return "Install Node.js 20+ and run: npm install -g @agentclientprotocol/codex-acp@" + Version
}

// AutoAuthenticate implements harness.AutoAuthenticator: with an OpenAI API
// key in the environment (an attached secret's placeholder), the "api-key"
// method signs in without user interaction.
func (adapter) AutoAuthenticate() []string { return []string{"api-key"} }
