// Package fake registers the scripted test harness (cmd: internal/harness/fakeagent).
package fake

import "github.com/gempir/agenthold/internal/harness"

// Command is where the fake agent binary lives; tests point it at a local build.
var Command = "/opt/agenthold/fakeagent"

type adapter struct{}

func init() { harness.Register(adapter{}) }

func (adapter) ID() string                           { return "fake" }
func (adapter) Name() string                         { return "Scripted test agent" }
func (adapter) Command(harness.Config) []string      { return []string{Command} }
func (adapter) Env(harness.Config) map[string]string { return nil }
func (adapter) SecretPresets() []string              { return nil }
func (adapter) CredentialPaths() []string            { return []string{".fakeagent/credentials"} }
func (adapter) InstallHint() string                  { return "Shipped with agenthold for development." }
