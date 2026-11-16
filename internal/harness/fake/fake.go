// Package fake registers the scripted test harness (cmd: internal/harness/fakeagent).
package fake

import "github.com/gempir/agenthold/internal/harness"

var adapter = &harness.Adapter{
	ID:              "fake",
	Name:            "Scripted test agent",
	Command:         []string{"/opt/agenthold/fakeagent"},
	CredentialPaths: []string{".fakeagent/credentials"},
	SkillsDir:       ".fakeagent/skills",
	// Like Codex's "api-key": the client may call it on its own.
	AutoAuthenticate: []string{"fake-key"},
	// It is not in the image: keep it out of production listings.
	Hidden: true,
	Login:  logins{},
}

func init() { harness.Register(adapter) }

// SetCommand points the adapter at a local build of the fake agent. Call it
// before any harness starts (TestMain, server startup).
func SetCommand(path string) { adapter.Command = []string{path} }
