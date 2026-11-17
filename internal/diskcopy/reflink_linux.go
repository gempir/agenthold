//go:build linux

package diskcopy

import (
	"os"

	"golang.org/x/sys/unix"
)

// reflink makes dst share in's blocks (FICLONE). It fails on filesystems
// without reflinks (ext4, tmpfs) and across filesystems.
func reflink(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
