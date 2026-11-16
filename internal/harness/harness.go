// Package harness describes the agent harnesses (Claude Code, Codex,
// OpenCode) that run inside sandboxes and speak ACP on stdio. Everything
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
	Model string
}

// Adapter describes one harness. Adapter packages register one from init.
type Adapter struct {
	// ID is stored with each agent ("claude-code").
	ID string
	// Name is shown in the UI.
	Name string
	// Package is the npm package of images/base/package.json that installs
	// Command[0] into the base image. Empty for harnesses not in the image
	// (the test fake).
	Package string
	// Command starts the harness in ACP mode inside the sandbox.
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
	// TerminalLogins maps login methods to the command that runs them in a
	// terminal, for harnesses that advertise a method without saying so.
	TerminalLogins map[string][]string
	// Hidden keeps the adapter out of listings (the test fake).
	Hidden bool
	// Login is set when the harness's provider login can live on the user's
	// account (see AccountLoginer).
	Login AccountLoginer
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
