//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
)

// CheckHost validates host requirements before downloading release artifacts.
func CheckHost(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: run the node as root", ErrUnsupported)
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("%w: open /dev/kvm: %w", ErrUnsupported, err)
	}
	_ = f.Close()
	controllers, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return fmt.Errorf("firecracker requires cgroup v2: %w", err)
	}
	available := strings.Fields(string(controllers))
	for _, controller := range []string{"cpu", "memory"} {
		if !slices.Contains(available, controller) {
			return fmt.Errorf("firecracker requires the cgroup v2 %s controller", controller)
		}
	}
	return nil
}

// KVMPresent reports whether this host exposes /dev/kvm, which decides
// whether sandbox.driver "auto" runs a local node.
func KVMPresent() bool {
	fi, err := os.Stat("/dev/kvm")
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
