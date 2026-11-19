//go:build !linux

package sandboxd

import "github.com/gempir/agenthold/internal/tunnel"

// addUsage reports nothing: live usage needs procfs.
func addUsage(*tunnel.Stats, string) {}
