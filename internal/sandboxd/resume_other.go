//go:build !linux

package sandboxd

import (
	"context"
	"syscall"
)

// serveResume is Linux only: resuming from a snapshot is a microVM feature.
func (d *Daemon) serveResume(context.Context, uint32) {}

// syncAll flushes every filesystem to its disk.
func syncAll() { _ = syscall.Sync() }
