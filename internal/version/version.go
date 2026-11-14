// Package version holds build information injected at link time.
package version

// Set with -ldflags "-X github.com/gempir/agenthold/internal/version.Version=... -X ...Commit=...".
var (
	Version = "dev"
	Commit  = "unknown"
)
