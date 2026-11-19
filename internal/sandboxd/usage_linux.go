//go:build linux

package sandboxd

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/tunnel"
)

// addUsage fills the machine-wide usage fields of st.
func addUsage(st *tunnel.Stats, home string) {
	if f, err := os.Open("/proc/stat"); err == nil {
		st.CPUBusyTicks, st.CPUTotalTicks, _ = parseCPUStat(f)
		_ = f.Close()
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		if total, avail, ok := parseMeminfo(f); ok && avail <= total {
			st.MemTotalBytes, st.MemUsedBytes = total, total-avail
		}
		_ = f.Close()
	}
	st.HomeTotalBytes, st.HomeUsedBytes = fsUsage(home)
	st.RootTotalBytes, st.RootUsedBytes = fsUsage("/")
}

func fsUsage(path string) (total, used uint64) {
	var fs unix.Statfs_t
	if unix.Statfs(path, &fs) != nil || fs.Bsize <= 0 {
		return 0, 0
	}
	unit := uint64(fs.Bsize)
	total = fs.Blocks * unit
	free := fs.Bfree * unit
	if free > total {
		return 0, 0
	}
	return total, total - free
}
