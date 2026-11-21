//go:build linux

package sandboxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// EnvInitChild marks the daemon process started by sandboxd's init mode.
const EnvInitChild = "AGENTHOLD_INIT_CHILD"

// Shutdown timing: how long processes get after SIGTERM before SIGKILL.
const (
	shutdownGrace = 3 * time.Second
	killWait      = 2 * time.Second
)

type initState struct {
	log  *slog.Logger
	self string

	layout Layout
	// delivered is the sandboxd the host delivered in the initramfs (nil
	// when the kernel started sandboxd from the base image directly).
	delivered  *os.File
	env        []string
	childPid   int
	restarts   int
	restartAt  time.Time
	stopping   bool
	poweroffMu sync.Mutex
}

// RunInit runs sandboxd as the microVM's init (PID 1). It never returns: it
// assembles the root filesystem, starts the daemon as a child process, reaps
// orphans, and powers the VM off on SIGTERM, SIGINT (Ctrl+Alt+Del), SIGPWR or
// a shutdown request through the tunnel. A setup error is logged to the
// console and powers off as well, so the host always sees the VMM exit.
func RunInit(self string) {
	in := &initState{
		log:  slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "sandboxd-init"),
		self: self,
	}
	if err := in.setup(); err != nil {
		in.log.Error("boot failed, powering off", "err", err)
		in.poweroff()
	}
	in.loop()
}

func uptime() string {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "?"
	}
	f, _, _ := strings.Cut(string(b), " ")
	return f + "s"
}

func (in *initState) setup() error {
	unix.Umask(0o022)
	_ = os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	// Ctrl+Alt+Del (Firecracker's SendCtrlAltDel on x86) becomes SIGINT to us.
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_CAD_OFF)

	if fromInitramfs() {
		if err := in.switchToBase(); err != nil {
			return err
		}
	}
	if err := runSteps(EarlyMounts()); err != nil {
		return err
	}
	in.log.Info("init started", "uptime", uptime())
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	args := ParseCmdline(string(cmdline))
	in.layout = LayoutFromCmdline(args)
	ipc, err := ParseIP(args["ip"])
	if err != nil {
		return err
	}

	boot, err := readGuestBoot(in.layout.Config)
	if err != nil {
		return err
	}
	baseDev := BaseDevice(args)
	for _, layer := range boot.Layers {
		switch layer.Device {
		case baseDev, in.layout.Overlay, in.layout.Home, in.layout.Config:
			return fmt.Errorf("layer device %s collides with a core guest device", layer.Device)
		}
	}

	uid, gid, err := lookupAgent("/etc/passwd")
	if err != nil {
		return err
	}
	if _, err := in.prepareDisk(in.layout.Overlay, "agenthold-overlay", -1, -1); err != nil {
		return err
	}
	homeFormatted, err := in.prepareDisk(in.layout.Home, "agenthold-home", uid, gid)
	if err != nil {
		return err
	}

	if err := runSteps(RootMounts(in.layout, boot.Layers...)); err != nil {
		return err
	}
	if err := pivot(NewRoot()); err != nil {
		return err
	}
	if err := runSteps(LateMounts()); err != nil {
		return err
	}
	for link, target := range DevLinks {
		_ = os.Symlink(target, link)
	}
	if in.delivered != nil {
		if err := in.bindDelivered(); err != nil {
			return err
		}
	}
	enableControllers()

	in.growIfNeeded(in.layout.Overlay, InitDisk)
	in.growIfNeeded(in.layout.Home, AgentHome)
	// The agent's uid as the (possibly modified) system on the overlay sees it.
	if u, g, err := lookupAgent("/etc/passwd"); err == nil {
		uid, gid = u, g
	}
	if homeFormatted {
		_ = os.Remove(filepath.Join(AgentHome, "lost+found"))
		if err := SeedHomeFromSkel(SkelDir, AgentHome, uid, gid); err != nil {
			return fmt.Errorf("seed %s from %s: %w", AgentHome, SkelDir, err)
		}
	}
	if err := os.Chown(AgentHome, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", AgentHome, err)
	}

	// A disk copied from another agent's sandbox must not be that machine.
	if reset, err := EnsureIdentity("/", boot.AgentID, boot.ResetIdentity); err != nil {
		in.log.Warn("machine identity", "err", err)
	} else if reset {
		in.log.Info("system disk was cloned: machine identity reset")
		if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, "ssh-keygen", "-A").CombinedOutput(); err != nil {
				in.log.Warn("regenerate ssh host keys", "err", err, "output", string(out))
			}
		}
	}

	hostname := boot.Hostname
	if hostname == "" {
		hostname = "agent"
	}
	if err := unix.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("sethostname: %w", err)
	}
	_ = writeFileAtomic("/etc/hostname", []byte(hostname+"\n"), 0o644)
	_ = writeFileAtomic("/etc/hosts", []byte(HostsFile(hostname)), 0o644)

	if err := initNetwork(ipc); err != nil {
		return err
	}

	for _, p := range slices.Sorted(maps.Keys(boot.Files)) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(p, boot.Files[p], 0o644); err != nil {
			return fmt.Errorf("boot file %s: %w", p, err)
		}
	}

	daemonBoot, bootEnv, err := boot.Resolve(ipc.Gateway)
	if err != nil {
		return err
	}
	data, err := json.Marshal(daemonBoot)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(InitBootFile, data, 0o600); err != nil {
		return err
	}
	in.env = childEnv(bootEnv, in.log)
	for _, kv := range in.env {
		if pathVal, ok := strings.CutPrefix(kv, "PATH="); ok {
			_ = os.MkdirAll("/etc/profile.d", 0o755)
			_ = writeFileAtomic("/etc/profile.d/agenthold-path.sh", []byte(ProfilePathScript(pathVal)), 0o644)
			break
		}
	}
	in.log.Info("system ready", "uptime", uptime(), "agent", boot.AgentID, "tunnel", daemonBoot.TunnelURL,
		"overlay", in.layout.Overlay, "home", in.layout.Home)
	return nil
}

// childEnv is the daemon's environment: defaults, then the image's
// image.env and image.env.d/*.env, then the host's per-boot variables.
func childEnv(boot map[string]string, log *slog.Logger) []string {
	return BuildChildEnv(ImageEnvFile, ImageEnvDir, boot, log)
}

func readGuestBoot(dev string) (GuestBoot, error) {
	f, err := os.Open(dev)
	if err != nil {
		return GuestBoot{}, fmt.Errorf("boot configuration: %w", err)
	}
	defer f.Close()
	b, err := DecodeGuestBoot(f)
	if err != nil {
		return b, fmt.Errorf("boot configuration on %s: %w", dev, err)
	}
	return b, nil
}

func lookupAgent(passwd string) (int, int, error) {
	b, err := os.ReadFile(passwd)
	if err != nil {
		return 0, 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 && f[0] == "agent" {
			uid, err1 := strconv.Atoi(f[2])
			gid, err2 := strconv.Atoi(f[3])
			if err1 == nil && err2 == nil {
				return uid, gid, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("no user agent in %s", passwd)
}

// prepareDisk formats a blank disk (all zeros at the start, as a freshly
// truncated sparse file is) and leaves an ext4 disk alone. Anything else is an
// error: sandboxd never formats over data.
func (in *initState) prepareDisk(dev, label string, uid, gid int) (bool, error) {
	f, err := os.Open(dev)
	if err != nil {
		return false, err
	}
	kind, _, err := ClassifyDisk(f)
	_ = f.Close()
	if err != nil {
		return false, fmt.Errorf("%s: %w", dev, err)
	}
	switch kind {
	case DiskExt4:
		return false, nil
	case DiskOther:
		return false, fmt.Errorf("%s is neither blank nor ext4; refusing to format it", dev)
	}
	t := time.Now()
	ext := "assume_storage_prezeroed=1,nodiscard"
	if uid >= 0 {
		ext += fmt.Sprintf(",root_owner=%d:%d", uid, gid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-L", label, "-m", "0", "-E", ext, dev).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("mkfs.ext4 %s: %w: %s", dev, err, bytes.TrimSpace(out))
	}
	in.log.Info("formatted blank disk", "dev", dev, "label", label, "took", time.Since(t).Round(time.Millisecond))
	return true, nil
}

// growIfNeeded grows a mounted ext4 to its device after the host enlarged the
// backing file.
func (in *initState) growIfNeeded(dev, mountpoint string) {
	f, err := os.Open(dev)
	if err != nil {
		return
	}
	devSize, err := unix.IoctlGetUint64(int(f.Fd()), unix.BLKGETSIZE64)
	_, sb, cerr := ClassifyDisk(f)
	_ = f.Close()
	if err != nil || cerr != nil || sb.BlockSize == 0 {
		return
	}
	if int64(devSize)-sb.Size() < 16<<20 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "resize2fs", dev).CombinedOutput()
	if err != nil {
		in.log.Warn("resize2fs", "dev", dev, "err", err, "output", string(bytes.TrimSpace(out)))
		return
	}
	in.log.Info("grew filesystem", "dev", dev, "mountpoint", mountpoint, "from", sb.Size(), "to", devSize)
}

func mountFlags(f MountFlag) uintptr {
	var m uintptr
	for _, x := range []struct {
		f MountFlag
		m uintptr
	}{
		{FlagReadOnly, unix.MS_RDONLY},
		{FlagNoSuid, unix.MS_NOSUID},
		{FlagNoDev, unix.MS_NODEV},
		{FlagNoExec, unix.MS_NOEXEC},
		{FlagNoAtime, unix.MS_NOATIME},
		{FlagBind, unix.MS_BIND},
		{FlagMove, unix.MS_MOVE},
		{FlagRemount, unix.MS_REMOUNT},
	} {
		if f&x.f != 0 {
			m |= x.m
		}
	}
	return m
}

func runSteps(steps []Step) error {
	for _, s := range steps {
		if s.IsMkdir() {
			mode := s.Mode
			if mode == 0 {
				mode = 0o755
			}
			if err := os.MkdirAll(s.Target, mode); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
			continue
		}
		err := unix.Mount(s.Source, s.Target, s.FSType, mountFlags(s.Flags), s.Data)
		if err != nil && s.Flags&FlagOptional == 0 {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

// pivot makes newRoot the root and detaches the old one (the base stays
// referenced as the overlay's lower layer).
func pivot(newRoot string) error {
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	return unix.Chdir("/")
}

// enableControllers delegates every cgroup v2 controller to child cgroups
// (Docker, systemd-run --user and friends expect that of the root).
func enableControllers() {
	b, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return
	}
	var ctl []string
	for c := range strings.FieldsSeq(string(b)) {
		ctl = append(ctl, "+"+c)
	}
	_ = os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte(strings.Join(ctl, " ")), 0)
}

func initNetwork(ipc IPConfig) error {
	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(lo)
	}
	dev := ipc.Device
	if dev == "" {
		dev = "eth0"
	}
	link, err := netlink.LinkByName(dev)
	if err != nil {
		return fmt.Errorf("network device %s: %w", dev, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up %s: %w", dev, err)
	}
	// The kernel's ip= normally configured the address already.
	mask := ipc.Netmask
	if mask == nil {
		mask = net.CIDRMask(30, 32)
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ipc.Client, Mask: mask}}
	if err := netlink.AddrReplace(link, addr); err != nil {
		return fmt.Errorf("address on %s: %w", dev, err)
	}
	if err := NetSetup(ipc.Gateway.String()); err != nil {
		return err
	}
	if ns := ipc.Nameserver(); !ns.Equal(ipc.Gateway) {
		return writeFileAtomic("/etc/resolv.conf", fmt.Appendf(nil, "# managed by agenthold\nnameserver %s\noptions ndots:0\n", ns), 0o644)
	}
	return nil
}

// loop starts the daemon and supervises it until shutdown.
func (in *initState) loop() {
	sigs := make(chan os.Signal, 32)
	signal.Notify(sigs, unix.SIGCHLD, unix.SIGTERM, unix.SIGINT, unix.SIGPWR, unix.SIGHUP)
	in.startChild()
	tick := time.NewTicker(time.Second)
	for {
		select {
		case s := <-sigs:
			if s == unix.SIGCHLD {
				in.reap()
				continue
			}
			if s == unix.SIGHUP {
				continue
			}
			in.shutdown("signal " + s.String())
		case <-tick.C:
			in.reap()
			if in.childPid == 0 && !in.restartAt.IsZero() && time.Now().After(in.restartAt) {
				in.startChild()
			}
		}
	}
}

func (in *initState) startChild() {
	in.restartAt = time.Time{}
	devnull, _ := os.Open(os.DevNull)
	p, err := os.StartProcess(in.self, []string{in.self, "--boot", InitBootFile}, &os.ProcAttr{
		Dir: "/", Env: in.env, Files: []*os.File{devnull, os.Stderr, os.Stderr},
	})
	if devnull != nil {
		_ = devnull.Close()
	}
	if err != nil {
		in.log.Error("start sandboxd", "err", err)
		in.scheduleRestart()
		return
	}
	in.childPid = p.Pid
	// The reaper collects its status with wait4(-1); drop the os.Process handle.
	_ = p.Release()
	in.log.Info("sandboxd daemon started", "pid", in.childPid, "uptime", uptime())
}

func (in *initState) scheduleRestart() {
	in.restarts++
	in.restartAt = time.Now().Add(min(time.Duration(in.restarts)*time.Second, 10*time.Second))
}

// reap collects every exited child (the daemon and reparented orphans).
func (in *initState) reap() (remaining bool) {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil { // ECHILD: no children at all
			return false
		}
		if pid == 0 {
			return true
		}
		if pid == in.childPid {
			in.childPid = 0
			if !in.stopping {
				in.log.Warn("sandboxd daemon exited, restarting", "status", ws.ExitStatus(), "signal", ws.Signal())
				in.scheduleRestart()
			}
		}
	}
}

func (in *initState) shutdown(reason string) {
	if in.stopping {
		return
	}
	in.stopping = true
	in.log.Info("shutting down", "reason", reason, "uptime", uptime())
	_ = unix.Kill(-1, unix.SIGTERM)
	deadline := time.Now().Add(shutdownGrace)
	for in.reap() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = unix.Kill(-1, unix.SIGKILL)
	deadline = time.Now().Add(killWait)
	for in.reap() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	in.poweroff()
}

// poweroff syncs, unmounts what it can, makes the writable filesystems read
// only and turns the VM off (which ends the Firecracker process).
func (in *initState) poweroff() {
	in.poweroffMu.Lock()
	unix.Sync()
	if mounts, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		lines := strings.Split(strings.TrimSpace(string(mounts)), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			f := strings.Fields(lines[i])
			if len(f) < 5 {
				continue
			}
			mp := unescapeMount(f[4])
			switch mp {
			case "/", "/proc", "/sys", "/dev", "/run", InitRun, InitDisk:
				continue
			}
			_ = unix.Unmount(mp, 0)
		}
	}
	if err := unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		in.log.Warn("remount / read-only", "err", err)
	}
	if err := unix.Mount("", InitDisk, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		in.log.Warn("remount overlay disk read-only", "err", err)
	}
	unix.Sync()
	in.log.Info("power off", "uptime", uptime())
	cmd := unix.LINUX_REBOOT_CMD_POWER_OFF
	if runtime.GOARCH == "amd64" {
		// Firecracker on x86 ends the VM on the i8042 reset that reboot=k
		// triggers; it has no ACPI power-off.
		cmd = unix.LINUX_REBOOT_CMD_RESTART
	}
	for {
		_ = unix.Reboot(cmd)
		time.Sleep(time.Second)
	}
}

// unescapeMount undoes the octal escapes of /proc/self/mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// RequestShutdown asks init (PID 1) to power the VM off. It is how the
// daemon child acts on a shutdown request from the tunnel.
func RequestShutdown() error { return unix.Kill(1, unix.SIGTERM) }

// InInitChild reports whether this process is the daemon started by init.
func InInitChild() bool { return os.Getenv(EnvInitChild) == "1" && os.Getppid() == 1 }
