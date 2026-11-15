package sandboxd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths sandboxd uses as PID 1. Everything under InitRun is on a tmpfs.
const (
	InitRun  = "/run/agenthold"
	initBase = InitRun + "/base" // bind of the read-only base (lower layer)
	initRoot = InitRun + "/root" // the assembled root before pivot_root
	// InitDisk is where the overlay disk is mounted, before and after the
	// switch to the new root. It holds upper/ and work/ of the root overlay
	// and the directories bind-mounted for container runtimes.
	InitDisk = InitRun + "/disk"
	// InitBootFile is the boot file the daemon child reads (tmpfs, 0600).
	InitBootFile = InitRun + "/boot.json"
	// ImageEnvFile is the image's environment, see images/base/image.env.
	ImageEnvFile = "/etc/agenthold/image.env"
	// AgentHome is where the home disk is mounted.
	AgentHome = "/home/agent"
)

// NativeDirs are bind-mounted from the overlay disk's own ext4 instead of
// living in the overlay: overlayfs cannot be the upper layer of another
// overlay mount (Docker's overlay2, containerd's overlayfs snapshotter,
// podman). They survive sleep and are reset with the overlay disk.
var NativeDirs = []string{"/var/lib/docker", "/var/lib/containerd", "/var/lib/containers"}

// MountFlag is a mount option independent of the host OS.
type MountFlag uint

// Mount flags.
const (
	FlagReadOnly MountFlag = 1 << iota
	FlagNoSuid
	FlagNoDev
	FlagNoExec
	FlagNoAtime
	FlagBind
	FlagMove
	FlagRemount
	// FlagOptional ignores failures (for example devtmpfs the kernel already mounted).
	FlagOptional
)

// Step is one action of the init mount plan: a mkdir (FSType, Source and
// Flags empty) or a mount.
type Step struct {
	Source, Target, FSType, Data string
	Flags                        MountFlag
	// Mode is the mode for creating Target (0 means 0755).
	Mode os.FileMode
}

// IsMkdir reports whether the step only creates a directory.
func (s Step) IsMkdir() bool { return s.Source == "" && s.FSType == "" && s.Flags == 0 }

func (s Step) String() string {
	if s.IsMkdir() {
		return "mkdir " + s.Target
	}
	var opts []string
	for _, f := range []struct {
		f MountFlag
		s string
	}{{FlagReadOnly, "ro"}, {FlagNoSuid, "nosuid"}, {FlagNoDev, "nodev"}, {FlagNoExec, "noexec"}, {FlagNoAtime, "noatime"}, {FlagBind, "bind"}, {FlagMove, "move"}, {FlagRemount, "remount"}, {FlagOptional, "optional"}} {
		if s.Flags&f.f != 0 {
			opts = append(opts, f.s)
		}
	}
	if s.Data != "" {
		opts = append(opts, s.Data)
	}
	return fmt.Sprintf("mount %s %s %s %s", s.FSType, s.Source, s.Target, strings.Join(opts, ","))
}

// EarlyMounts are mounted on the base image right after the kernel starts
// sandboxd (the base's own /proc, /sys, /dev and /run are mount points).
func EarlyMounts() []Step {
	return []Step{
		{Source: "proc", Target: "/proc", FSType: "proc", Flags: FlagNoSuid | FlagNoDev | FlagNoExec},
		{Source: "sysfs", Target: "/sys", FSType: "sysfs", Flags: FlagNoSuid | FlagNoDev | FlagNoExec},
		// CONFIG_DEVTMPFS_MOUNT usually did this already.
		{Source: "devtmpfs", Target: "/dev", FSType: "devtmpfs", Flags: FlagNoSuid | FlagOptional, Data: "mode=0755"},
		{Source: "tmpfs", Target: "/run", FSType: "tmpfs", Flags: FlagNoSuid | FlagNoDev, Data: "mode=0755,size=20%"},
		{Target: InitRun, Mode: 0o700},
	}
}

// RootMounts assembles the new root under initRoot once both disks hold a
// filesystem: overlay disk, base bind, the root overlay, home, the native
// directories, a fresh /run, and finally moves the early mounts into it.
func RootMounts(l Layout) []Step {
	root := func(p string) string { return filepath.Join(initRoot, p) }
	steps := []Step{
		{Target: InitDisk},
		{Source: l.Overlay, Target: InitDisk, FSType: "ext4", Flags: FlagNoAtime},
		{Target: InitDisk + "/upper"},
		{Target: InitDisk + "/work"},
		{Target: initBase},
		{Source: "/", Target: initBase, Flags: FlagBind},
		{Source: "/", Target: initBase, Flags: FlagBind | FlagRemount | FlagReadOnly},
		{Target: initRoot},
		{
			Source: "overlay", Target: initRoot, FSType: "overlay",
			Data: "lowerdir=" + initBase + ",upperdir=" + InitDisk + "/upper,workdir=" + InitDisk + "/work",
		},
		{Target: root(AgentHome)},
		{Source: l.Home, Target: root(AgentHome), FSType: "ext4", Flags: FlagNoAtime},
	}
	for _, d := range NativeDirs {
		disk := InitDisk + "/native/" + strings.ReplaceAll(strings.TrimPrefix(d, "/"), "/", "-")
		steps = append(steps,
			Step{Target: disk},
			Step{Target: root(d)},
			Step{Source: disk, Target: root(d), Flags: FlagBind},
		)
	}
	steps = append(steps,
		Step{Source: "tmpfs", Target: root("/run"), FSType: "tmpfs", Flags: FlagNoSuid | FlagNoDev, Data: "mode=0755,size=20%"},
		Step{Target: root(InitRun), Mode: 0o700},
		Step{Target: root(InitDisk)},
		Step{Source: InitDisk, Target: root(InitDisk), Flags: FlagMove},
		Step{Source: "/dev", Target: root("/dev"), Flags: FlagMove},
		Step{Source: "/proc", Target: root("/proc"), Flags: FlagMove},
		Step{Source: "/sys", Target: root("/sys"), Flags: FlagMove},
	)
	return steps
}

// NewRoot is the directory RootMounts assembles and pivot_root switches to.
func NewRoot() string { return initRoot }

// LateMounts complete the new root after pivot_root.
func LateMounts() []Step {
	return []Step{
		{Target: "/dev/pts"},
		{Source: "devpts", Target: "/dev/pts", FSType: "devpts", Flags: FlagNoSuid | FlagNoExec, Data: "newinstance,ptmxmode=0666,mode=0620,gid=5"},
		{Target: "/dev/shm"},
		{Source: "tmpfs", Target: "/dev/shm", FSType: "tmpfs", Flags: FlagNoSuid | FlagNoDev, Data: "mode=1777"},
		{Target: "/dev/mqueue"},
		{Source: "mqueue", Target: "/dev/mqueue", FSType: "mqueue", Flags: FlagNoSuid | FlagNoDev | FlagNoExec | FlagOptional},
		{Source: "cgroup2", Target: "/sys/fs/cgroup", FSType: "cgroup2", Flags: FlagNoSuid | FlagNoDev | FlagNoExec, Data: "nsdelegate"},
		{Source: "tmpfs", Target: "/tmp", FSType: "tmpfs", Flags: FlagNoSuid | FlagNoDev, Data: "mode=1777,size=50%"},
	}
}

// DevLinks are the /dev symlinks devtmpfs does not create.
var DevLinks = map[string]string{
	"/dev/fd":     "/proc/self/fd",
	"/dev/stdin":  "/proc/self/fd/0",
	"/dev/stdout": "/proc/self/fd/1",
	"/dev/stderr": "/proc/self/fd/2",
	"/dev/core":   "/proc/kcore",
}

// HostsFile is /etc/hosts for hostname.
func HostsFile(hostname string) string {
	return "# managed by agenthold (rewritten at every boot)\n" +
		"127.0.0.1\tlocalhost\n" +
		"127.0.1.1\t" + hostname + "\n" +
		"::1\tlocalhost ip6-localhost ip6-loopback\n"
}
