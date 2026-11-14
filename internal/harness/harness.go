// Package harness describes the agent harnesses (Claude Code, ...) that run
// inside sandboxes and speak ACP on stdio. Everything harness-specific lives
// in an adapter package; the rest of the server only uses this interface.
package harness

import (
	"slices"
	"sort"
	"sync"
)

// Config is what an adapter may use from the agent's settings.
type Config struct {
	Model string
}

// Adapter is one harness.
type Adapter interface {
	// ID is stored with each agent ("claude-code").
	ID() string
	// Name is shown in the UI.
	Name() string
	// Command starts the harness in ACP mode inside the sandbox.
	Command(c Config) []string
	// Env is added to the harness's environment (never secret values).
	Env(c Config) map[string]string
	// SecretPresets names the secret presets this harness can use (API keys).
	SecretPresets() []string
	// CredentialPaths are files where the harness keeps subscription logins,
	// relative to the agent's home.
	CredentialPaths() []string
	// InstallHint explains how to add the harness to a custom image.
	InstallHint() string
}

var (
	mu       sync.RWMutex
	adapters = map[string]Adapter{}
)

// Register adds an adapter. Adapter packages call it from init.
func Register(a Adapter) {
	mu.Lock()
	defer mu.Unlock()
	adapters[a.ID()] = a
}

// Get returns an adapter by id.
func Get(id string) (Adapter, bool) {
	mu.RLock()
	defer mu.RUnlock()
	a, ok := adapters[id]
	return a, ok
}

// List returns all adapters, sorted by id. Hidden adapters (tests) are left
// out unless includeHidden is set.
func List(includeHidden bool) []Adapter {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		if h, ok := a.(interface{ Hidden() bool }); ok && h.Hidden() && !includeHidden {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// IsCredentialPath reports whether path (relative to home or absolute under
// home) holds a harness login of any adapter.
func IsCredentialPath(home, path string) bool {
	mu.RLock()
	defer mu.RUnlock()
	for _, a := range adapters {
		for _, p := range a.CredentialPaths() {
			if path == p || path == home+"/"+p || slices.Contains([]string{"~/" + p}, path) {
				return true
			}
		}
	}
	return false
}
