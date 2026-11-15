//go:build linux || darwin

package runtimeassets

import (
	"os"
	"syscall"
)

func ownedByCurrentUser(fi os.FileInfo) bool {
	s, ok := fi.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Geteuid())
}
