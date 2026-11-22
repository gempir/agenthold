// Package harness describes the agent harnesses (Claude Code, Codex,
// OpenCode, Antigravity) that run inside sandboxes and speak ACP on stdio. Everything
// harness-specific is one Adapter value in an adapter package; the rest of
// the server only reads these descriptors.
package harness

import (
	"maps"
	"sort"
	"sync"
)

// Config is what an adapter may use from the agent's settings.
type Config struct {
	// Model is the agent's default model ('' = the harness's own default).
	// The harness gets it when it starts; a running harness is switched
	// through ACP session config options instead.
	Model string
}

// Model is one entry of an adapter's fallback model list.
type Model struct {
	ID          string
	Name        string
	Description string
	// Group is the heading in the picker (a provider, a family).
	Group string
}

// Protocol identifies the wire protocol a harness speaks on stdio.
type Protocol string

const (
	// ProtocolACP is the Agent Client Protocol (default when empty).
	ProtocolACP Protocol = "acp"
	// ProtocolCodex is OpenAI Codex's native JSON-RPC app-server protocol.
	ProtocolCodex Protocol = "codex"
	// ProtocolClaude is Claude Code's native stream-json control protocol.
	ProtocolClaude Protocol = "claude"
)

// Adapter describes one harness. Adapter packages register one from init.
type Adapter struct {
	// ID is stored with each agent ("claude-code").
	ID string
	// Name is shown in the UI.
	Name string
	// Protocol selects the wire protocol on stdio (empty defaults to ProtocolACP).
	Protocol Protocol
	// Source identifies where the adapter came from ("builtin", "registry", "local").
	Source string
	// Description, Version and IconURL are optional metadata shown for ACP Store
	// and custom harnesses.
	Description string
	Version     string
	IconURL     string
	// Package is the npm package of images/harnesses/package.json that installs
	// Command[0] into the harnesses layer. Empty for harnesses not in the image
	// (the test fake) and for native binaries (Binary).
	Package string
	// Binary is set instead of Package for a harness the image installs as a
	// pinned native binary: the prefix of its variables in
	// images/harnesses/versions.env (<Binary>_VERSION and <Binary>_SHA256_ARM64/X64).
	// scripts/guest/build-harnesses.sh links it into /usr/local/bin as Command[0].
	Binary string
	// Command starts the harness in ACP or native mode inside the sandbox.
	Command []string
	// Env returns what is added to the harness's environment (never secret
	// values). Nil means nothing.
	Env func(Config) map[string]string
	// SecretPresets names the secret presets this harness can use (API keys).
	SecretPresets []string
	// CredentialPaths are files where the harness keeps subscription logins,
	// relative to the agent's home.
	CredentialPaths []string
	// SkillsDir is where the harness reads Agent Skills
	// (<dir>/<name>/SKILL.md), relative to the agent's home. Empty: none.
	SkillsDir string
	// AutoAuthenticate lists login methods the client may call on its own
	// when the harness asks for authentication (methods backed by
	// environment variables, such as API keys provided as secrets).
	AutoAuthenticate []string
	// AuthenticateEagerly calls AutoAuthenticate before opening a session when
	// the corresponding auth method is advertised (Antigravity picks its
	// credential source from authenticate).
	AuthenticateEagerly bool
	// TerminalLogins maps login methods to the command that runs them in a
	// terminal, for harnesses that advertise a method without saying so.
	TerminalLogins map[string][]string
	// FallbackModels is shown by the model picker until the harness has
	// reported its own list (session config options), for example before
	// the agent ever ran. Keep it small: what the harness accepts whatever
	// the account (aliases, not dated ids).
	FallbackModels []Model
	// CustomModelsList holds user-configured extra model IDs (ACP Store / local
	// ACP harnesses) that are merged into the picker catalog.
	CustomModelsList []string
	// ProbeModels: the client may describe the models' options by switching a
	// throwaway session through them (see session.Manager.ProbeModels). Set it
	// only for a harness where that has no side effects: a session that is
	// not stored, starts no hooks or MCP servers and costs nothing. None of
	// the harnesses in the image qualifies (a session/new persists in OpenCode
	// and Codex, Claude Code runs SessionStart hooks and its session/options
	// cannot choose a model), so they show provisional levels instead.
	ProbeModels bool
	// CustomModels: the harness accepts model ids it does not list (OpenCode's
	// "provider/model" for providers configured in its own config).
	CustomModels bool
	// Hidden keeps the adapter out of listings (the test fake).
	Hidden bool
	// Login is set when the harness's provider login can live on the user's
	// account (see AccountLoginer).
	Login AccountLoginer
}

// EffectiveProtocol returns the adapter's wire protocol, defaulting to ProtocolACP.
func (a *Adapter) EffectiveProtocol() Protocol {
	if a.Protocol == "" {
		return ProtocolACP
	}
	return a.Protocol
}

// Environment returns the harness's environment for c (a fresh map).
func (a *Adapter) Environment(c Config) map[string]string {
	env := map[string]string{}
	if a.Env != nil {
		maps.Copy(env, a.Env(c))
	}
	return env
}

var (
	mu       sync.RWMutex
	adapters = map[string]*Adapter{}
)

// Register adds an adapter. Adapter packages call it from init.
func Register(a *Adapter) {
	mu.Lock()
	defer mu.Unlock()
	adapters[a.ID] = a
}

// Get returns an adapter by id.
func Get(id string) (*Adapter, bool) {
	mu.RLock()
	defer mu.RUnlock()
	a, ok := adapters[id]
	return a, ok
}

// List returns all adapters, sorted by id. Hidden adapters (tests) are left
// out unless includeHidden is set.
func List(includeHidden bool) []*Adapter {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]*Adapter, 0, len(adapters))
	for _, a := range adapters {
		if a.Hidden && !includeHidden {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IsCredentialPath reports whether path (relative to home, "~/"-prefixed or
// absolute under home) holds a harness login of any adapter.
func IsCredentialPath(home, path string) bool {
	mu.RLock()
	defer mu.RUnlock()
	for _, a := range adapters {
		for _, p := range a.CredentialPaths {
			if path == p || path == home+"/"+p || path == "~/"+p {
				return true
			}
		}
	}
	return false
}
