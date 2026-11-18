//go:build !linux

package sandboxd

import "context"

// serveResume is Linux only: resuming from a snapshot is a microVM feature.
func (d *Daemon) serveResume(context.Context, uint32) {}
