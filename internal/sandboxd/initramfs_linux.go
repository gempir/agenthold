//go:build linux

package sandboxd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// initramfsBase is where the base image is mounted inside the initramfs
// before it becomes the root.
const initramfsBase = "/base"

// fromInitramfs reports whether / is the kernel's initramfs (rootfs is a
// ramfs or tmpfs; the base image is ext4).
func fromInitramfs() bool {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return false
	}
	return st.Type == unix.RAMFS_MAGIC || st.Type == unix.TMPFS_MAGIC
}

// switchToBase runs in the initramfs: it keeps a handle on the delivered
// binary, mounts the read-only base image and makes it the root (the
// switch_root dance: move the mount onto / and chroot into it). From then on
// boot continues exactly as if the kernel had mounted the base itself.
func (in *initState) switchToBase() error {
	self, err := os.Open(InitramfsInit)
	if err != nil {
		return fmt.Errorf("delivered sandboxd: %w", err)
	}
	unix.CloseOnExec(int(self.Fd()))
	in.delivered = self
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("initramfs: mount /proc: %w", err)
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	_ = unix.Unmount("/proc", unix.MNT_DETACH)
	if err != nil {
		return err
	}
	dev := BaseDevice(ParseCmdline(string(cmdline)))
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"); err != nil && !errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("initramfs: mount /dev: %w", err)
	}
	if err := os.MkdirAll(initramfsBase, 0o755); err != nil {
		return err
	}
	// Built-in virtio-blk probes before init runs; wait briefly anyway.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = unix.Mount(dev, initramfsBase, "ext4", unix.MS_RDONLY, "")
		if err == nil || time.Now().After(deadline) || !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENXIO) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("mount base image %s: %w", dev, err)
	}
	_ = unix.Unmount("/dev", unix.MNT_DETACH)
	if err := unix.Chdir(initramfsBase); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move base image to /: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot into base image: %w", err)
	}
	return unix.Chdir("/")
}

// bindDelivered makes the delivered binary visible at SandboxdPath in the
// assembled root, read-only, over whatever the base image has there (an older
// sandboxd in bases built before G1, an empty placeholder since). The daemon
// child is started from there too, so every sandboxd process in the guest is
// the host's version.
func (in *initState) bindDelivered() error {
	if err := ensureMountTarget(SandboxdPath); err != nil {
		return fmt.Errorf("prepare %s: %w", SandboxdPath, err)
	}
	src := fmt.Sprintf("/proc/self/fd/%d", in.delivered.Fd())
	if err := unix.Mount(src, SandboxdPath, "", unix.MS_BIND, ""); err != nil {
		// Not expected; fall back to a copy on the root's tmpfs /run.
		in.log.Warn("bind delivered sandboxd, copying it instead", "err", err)
		copyPath := "/run/agenthold-sandboxd"
		if err := copyFile(in.delivered, copyPath); err != nil {
			return fmt.Errorf("copy delivered sandboxd: %w", err)
		}
		if err := unix.Mount(copyPath, SandboxdPath, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind delivered sandboxd: %w", err)
		}
	}
	if err := unix.Mount("", SandboxdPath, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("protect delivered sandboxd: %w", err)
	}
	in.self = SandboxdPath
	return nil
}

// ensureMountTarget creates an empty file at p if nothing is there (the
// overlay keeps it; a base image normally provides it).
func ensureMountTarget(p string) error {
	if _, err := os.Lstat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	return f.Close()
}

func copyFile(src *os.File, dst string) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
