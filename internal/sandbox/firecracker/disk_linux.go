//go:build linux

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/gempir/agenthold/internal/sandbox"
)

var _ sandbox.DiskAccounter = (*Driver)(nil)

// AllocatedDiskBytes implements sandbox.DiskAccounter: the blocks the
// writable disks of every sandbox occupy. The files are sparse, so this is
// what they really cost, not their size. It stats the files without taking the
// driver lock.
func (d *Driver) AllocatedDiskBytes(ctx context.Context) (uint64, error) {
	dirs, err := os.ReadDir(filepath.Join(d.cfg.DataDir, "agents"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var total uint64
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for _, name := range diskNames {
			var st syscall.Stat_t
			if syscall.Stat(filepath.Join(d.cfg.DataDir, "agents", dir.Name(), name), &st) == nil && st.Blocks > 0 {
				total += uint64(st.Blocks) * 512
			}
		}
	}
	return total, nil
}
