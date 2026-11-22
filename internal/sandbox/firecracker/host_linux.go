//go:build linux

package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hostRoot is where the host's /proc and /sys are read from (tests).
var hostRoot = "/"

const (
	thpEnabled            = "sys/kernel/mm/transparent_hugepage/enabled"
	thpDefrag             = "sys/kernel/mm/transparent_hugepage/defrag"
	thpScanSleepMillisecs = "sys/kernel/mm/transparent_hugepage/khugepaged/scan_sleep_millisecs"
	thpAllocSleepMillis   = "sys/kernel/mm/transparent_hugepage/khugepaged/alloc_sleep_millisecs"
)

// EnableHugePages switches the host's transparent huge pages to "always" and
// returns the mode it found. Firecracker does not madvise guest memory, so in
// the usual "madvise" mode guest RAM is faulted in 4 KiB at a time. Under
// nested virtualization every such fault costs tens of microseconds: on the
// demo host a 4 GiB guest needed 25 s to reach init and its harness minutes
// to answer, against 0.13 s with huge pages.
//
// It also switches defrag from "madvise"/"never" to "defer+madvise" (waking
// kcompactd asynchronously on fallback without stalling faults synchronously)
// and shortens khugepaged's sleep intervals best-effort so fragmented hosts
// recover 2 MiB pages quickly.
func EnableHugePages() (previous string, err error) {
	path := filepath.Join(hostRoot, thpEnabled)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("transparent huge pages: %w", err)
	}
	previous = selectedMode(string(data))
	if previous != "always" {
		if err = os.WriteFile(path, []byte("always"), 0); err != nil {
			return previous, fmt.Errorf("transparent huge pages: set always (now %s): %w", previous, err)
		}
	}
	// Best-effort defrag and khugepaged tuning: under Ubuntu's default
	// defrag=madvise, non-madvised Firecracker VMAs never wake kcompactd when
	// page cache fragments order-9 blocks, causing silent 4 KiB fallbacks.
	defragPath := filepath.Join(hostRoot, thpDefrag)
	if b, derr := os.ReadFile(defragPath); derr == nil {
		if cur := selectedMode(string(b)); cur == "madvise" || cur == "never" {
			_ = os.WriteFile(defragPath, []byte("defer+madvise"), 0)
		}
	}
	capSysfsInt(filepath.Join(hostRoot, thpScanSleepMillisecs), 1000)
	capSysfsInt(filepath.Join(hostRoot, thpAllocSleepMillis), 5000)
	return previous, nil
}

func capSysfsInt(path string, maxVal int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cur int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &cur); err != nil || cur <= maxVal {
		return
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", maxVal)), 0)
}

// selectedMode returns the bracketed choice of a sysfs mode list such as
// "always [madvise] never".
func selectedMode(s string) string {
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]") {
			return strings.Trim(f, "[]")
		}
	}
	return strings.TrimSpace(s)
}

// hypervisorVendors are DMI vendor and product names of virtual machines.
var hypervisorVendors = []string{"QEMU", "KVM", "Apple Virtualization", "VMware", "VirtualBox", "innotek", "Parallels", "Xen", "Microsoft Corporation Virtual Machine", "Google Compute Engine", "Cloud Hypervisor", "Firecracker", "bhyve", "OpenStack"}

// NestedVirtualization names the hypervisor this host itself runs under, or
// returns "" for bare metal (or when it cannot tell). Guests of a nested host
// pay a full exit to the outer hypervisor for every memory fault.
func NestedVirtualization() string {
	if cpuinfo, err := os.ReadFile(filepath.Join(hostRoot, "proc/cpuinfo")); err == nil {
		for _, line := range strings.Split(string(cpuinfo), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "flags" && hasField(v, "hypervisor") {
				return dmiName()
			}
		}
	}
	name := dmiName()
	for _, v := range hypervisorVendors {
		if strings.Contains(name, v) {
			return name
		}
	}
	return ""
}

func dmiName() string {
	var parts []string
	for _, f := range []string{"sys_vendor", "product_name"} {
		if b, err := os.ReadFile(filepath.Join(hostRoot, "sys/class/dmi/id", f)); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, s)
			}
		}
	}
	if len(parts) == 0 {
		return "a hypervisor"
	}
	return strings.Join(parts, " ")
}

func hasField(s, want string) bool {
	for _, f := range strings.Fields(s) {
		if f == want {
			return true
		}
	}
	return false
}
