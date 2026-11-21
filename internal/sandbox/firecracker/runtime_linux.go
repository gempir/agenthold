//go:build linux

package firecracker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
)

func ensureDisk(path string, size int64, uid, gid uint32) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		st, e := os.Stat(path)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || st.Size() != size {
			return fmt.Errorf("existing disk %s is not a regular file of the requested size (%d)", path, size)
		}
		return os.Chown(path, int(uid), int(gid))
	}
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err = f.Truncate(size); err == nil {
		err = f.Chown(int(uid), int(gid))
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return err
	}
	ok = true
	return nil
}

func bindFile(src, dst string, readOnly bool) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	_ = f.Close()
	if err = unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s: %w", filepath.Base(src), err)
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if readOnly {
		flags |= unix.MS_RDONLY
	}
	if err = unix.Mount("", dst, "", flags, ""); err != nil {
		return fmt.Errorf("protect bind %s: %w", filepath.Base(src), err)
	}
	return nil
}

func (d *Driver) prepare(ctx context.Context, id sandbox.ID, m *manifest, boot sandbox.Boot) error {
	layers := d.activeLayers(m)
	if len(layers) > sandboxd.MaxGuestLayers {
		return fmt.Errorf("too many layers (%d, max %d)", len(layers), sandboxd.MaxGuestLayers)
	}
	var guestLayers []sandboxd.GuestLayer
	if len(layers) > 0 {
		guestLayers = make([]sandboxd.GuestLayer, len(layers))
		for i, l := range layers {
			guestLayers[i] = sandboxd.GuestLayer{
				Device: fmt.Sprintf("/dev/vd%c", 'e'+i),
				Subdir: l.Subdir,
			}
		}
	}
	data, err := sandboxd.EncodeGuestBoot(sandboxd.GuestBoot{
		AgentID: boot.AgentID, Token: boot.Token, TunnelPort: boot.TunnelPort,
		Env: m.Spec.Env, Files: boot.Files, ResetIdentity: m.Fresh, Layers: guestLayers,
	})
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	config := filepath.Join(d.dir(id), "config.img")
	if err = atomicWrite(config, data, 0o400); err != nil {
		return err
	}
	if err = os.Chown(config, int(m.UID), int(m.GID)); err != nil {
		return err
	}
	root := d.jailRoot(id)
	if err = os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	type bindEntry struct {
		source, name string
		ro           bool
	}
	binds := []bindEntry{
		{d.cfg.KernelPath, "kernel", true},
		{d.initrd, "initrd", true},
		{m.BaseImagePath, "base.ext4", true},
		{filepath.Join(d.dir(id), "overlay.ext4"), "overlay.ext4", false},
		{filepath.Join(d.dir(id), "home.ext4"), "home.ext4", false},
		{config, "config.img", true},
	}
	for i, l := range layers {
		binds = append(binds, bindEntry{l.Path, fmt.Sprintf("layer_%d.ext4", i), true})
	}
	for _, f := range binds {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = bindFile(f.source, filepath.Join(root, f.name), f.ro); err != nil {
			return err
		}
	}
	return d.bindSnapDir(id, m)
}

// prepareResume binds what a snapshot load re-opens, at the paths the
// snapshot recorded: the drives (the config drive the VM booted with is kept
// in the snapshot directory) and the snapshot directory.
func (d *Driver) prepareResume(ctx context.Context, id sandbox.ID, m *manifest) error {
	root := d.jailRoot(id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	type bindEntry struct {
		source, name string
		ro           bool
	}
	binds := []bindEntry{
		{m.BaseImagePath, "base.ext4", true},
		{filepath.Join(d.dir(id), "overlay.ext4"), "overlay.ext4", false},
		{filepath.Join(d.dir(id), "home.ext4"), "home.ext4", false},
		// The store's files (root's, see snapshot_linux.go), read-only.
		{filepath.Join(d.snapDir(id), "config.img"), "config.img", true},
		{filepath.Join(d.snapDir(id), "vmstate"), jailVMState, true},
		{filepath.Join(d.snapDir(id), "memory"), jailMemory, true},
	}
	for i, l := range d.activeLayers(m) {
		binds = append(binds, bindEntry{l.Path, fmt.Sprintf("layer_%d.ext4", i), true})
	}
	for _, f := range binds {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := bindFile(f.source, filepath.Join(root, f.name), f.ro); err != nil {
			return err
		}
	}
	return d.bindSnapDir(id, m)
}

// bindSnapDir makes a new, empty <dir>/snapout the jail's /snap, where
// Firecracker writes snapshots as the VMM user. Whatever an earlier VMM left
// there is removed unread.
func (d *Driver) bindSnapDir(id sandbox.ID, m *manifest) error {
	src := d.snapOut(id)
	if err := os.RemoveAll(src); err != nil {
		return err
	}
	if err := os.Mkdir(src, 0o700); err != nil {
		return err
	}
	if err := os.Chown(src, int(m.UID), int(m.GID)); err != nil {
		return err
	}
	dst := filepath.Join(d.jailRoot(id), jailSnapDir)
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind snapshot directory: %w", err)
	}
	if err := unix.Mount("", dst, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("protect snapshot directory: %w", err)
	}
	return nil
}

func resources(m *manifest) (cpus float64, memory int64, vcpus int) {
	cpus = m.Spec.Resources.CPUs
	if cpus == 0 {
		cpus = 1
	}
	memory = m.Spec.Resources.MemoryMB
	if memory == 0 {
		memory = 512
	}
	return cpus, memory, int(math.Ceil(cpus))
}

func (d *Driver) jailerArgs(id sandbox.ID, m *manifest) []string {
	cpus, memory, _ := resources(m)
	return []string{"--id", d.jailID(id), "--exec-file", d.cfg.FirecrackerPath, "--uid", strconv.FormatUint(uint64(m.UID), 10), "--gid", strconv.FormatUint(uint64(m.GID), 10), "--chroot-base-dir", d.cfg.JailDir, "--cgroup-version", "2", "--parent-cgroup", "agenthold", "--cgroup", fmt.Sprintf("cpu.max=%d 100000", int64(math.Ceil(cpus*100000))), "--cgroup", fmt.Sprintf("memory.max=%d", (memory+d.cfg.MemoryOverheadMB)<<20), "--", "--api-sock", "/api.sock"}
}

func (d *Driver) launch(ctx context.Context, id sandbox.ID, m *manifest) error {
	a, err := d.startVMM(ctx, id, m)
	if err != nil {
		return err
	}
	defer a.close()
	bootCtx, cancel := context.WithTimeout(ctx, d.cfg.StartTimeout)
	defer cancel()
	_, mem, vcpus := resources(m)
	if err = a.put(bootCtx, "/machine-config", map[string]any{"vcpu_count": vcpus, "mem_size_mib": mem}); err != nil {
		return err
	}
	// sandboxd arrives as the initramfs's /init and mounts the base (vda) itself.
	args := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off quiet ip=%s::%s:255.255.255.252::eth0:off", m.Subnet.Addr().Next().Next(), m.Subnet.Addr().Next())
	if err = a.put(bootCtx, "/boot-source", map[string]string{"kernel_image_path": "/kernel", "initrd_path": "/initrd", "boot_args": args}); err != nil {
		return err
	}
	// Insertion order is part of sandboxd's ABI: vda base, vdb system,
	// vdc home, vdd config, then optional read-only layer drives (vde, vdf, ...).
	// Do not iterate a map or sort drive IDs.
	for i, name := range []string{"base", "overlay", "home", "config"} {
		path := "/" + name + ".ext4"
		if name == "config" {
			path = "/config.img"
		}
		if err = a.put(bootCtx, "/drives/"+name, map[string]any{"drive_id": name, "path_on_host": path, "is_root_device": false, "is_read_only": i == 0 || i == 3}); err != nil {
			return err
		}
	}
	for i := range d.activeLayers(m) {
		name := fmt.Sprintf("layer_%d", i)
		if err = a.put(bootCtx, "/drives/"+name, map[string]any{"drive_id": name, "path_on_host": "/" + name + ".ext4", "is_root_device": false, "is_read_only": true}); err != nil {
			return err
		}
	}
	b := m.Subnet.Addr().Next().Next().As4()
	mac := fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
	if err = a.put(bootCtx, "/network-interfaces/eth0", map[string]string{"iface_id": "eth0", "host_dev_name": TapName(id), "guest_mac": mac}); err != nil {
		return err
	}
	// The host tells a resumed sandboxd about its wake over vsock; every VM
	// gets the device, so any running machine can be snapshotted.
	if err = a.put(bootCtx, "/vsock", map[string]any{"guest_cid": guestCID, "uds_path": vsockPath}); err != nil {
		return err
	}
	m.Resumable = true
	m.Balloon = d.cfg.Balloon
	if m.Balloon {
		// Free page reporting: the guest hands memory it frees back to the
		// host (Firecracker drops it, so it reads as zero) about 2 s later,
		// in 2 MiB blocks. A sleep's inflation then mostly takes pages the
		// host no longer backs, which is cheap, and the zero pages are
		// punched out of the memory file.
		if err = a.put(bootCtx, "/balloon", map[string]any{"amount_mib": 0, "deflate_on_oom": true, "stats_polling_interval_s": balloonStatsSeconds, "free_page_reporting": true}); err != nil {
			return err
		}
	}
	if err = a.action(bootCtx, "InstanceStart"); err != nil {
		return err
	}
	go d.watchConsole(id, m.Process)
	m.State = sandbox.StateRunning
	m.Fresh = false // the guest has the instruction; it resets and stamps itself
	return d.save(id, m)
}

// startVMM starts the jailed Firecracker process, records it and waits for
// its API (nothing configured yet).
func (d *Driver) startVMM(ctx context.Context, id sandbox.ID, m *manifest) (*api, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(d.dir(id), "serial.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(d.cfg.JailerPath, d.jailerArgs(id, m)...) //nolint:noctx // VM lifetime exceeds the request; Start explicitly kills and cleans up on cancellation.
	// The VMM is intentionally not tied to the request context: cancellation is
	// handled explicitly, and a successful boot survives controller restarts.
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("start jailer: %w", err)
	}
	_ = log.Close()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	start, _, err := procIdentity(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		<-done
		return nil, fmt.Errorf("jailer exited before process identity was recorded: %w", err)
	}
	m.Process = process{PID: cmd.Process.Pid, StartTime: start}
	// Persist before configuring/starting: a crash here is recoverable, including
	// an API-ready VMM whose guest has not started yet.
	if err = d.save(id, m); err != nil {
		return nil, err
	}
	bootCtx, cancel := context.WithTimeout(ctx, d.cfg.StartTimeout)
	defer cancel()
	a := newAPI(d.socket(id))
	for {
		var info struct {
			State string `json:"state"`
		}
		err = a.request(bootCtx, "GET", "/", nil, &info)
		if err == nil {
			return a, nil
		}
		alive, e := d.running(m)
		if e != nil {
			a.close()
			return nil, e
		}
		if !alive {
			a.close()
			return nil, errors.New("jailer/Firecracker exited before API was ready; see serial.log")
		}
		if err = delay(bootCtx, 20*time.Millisecond); err != nil {
			a.close()
			return nil, fmt.Errorf("wait for Firecracker API: %w", err)
		}
	}
}

// procIdentity parses /proc stat from the final ')' (comm may contain spaces
// and parentheses). Field 22 is the kernel start time, stable across exec.
func procIdentity(pid int) (start, state string, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return "", "", errors.New("invalid process stat")
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 20 {
		return "", "", errors.New("short process stat")
	}
	return f[19], f[0], nil
}

func (d *Driver) running(m *manifest) (bool, error) {
	p := m.Process
	if p.PID <= 0 || p.StartTime == "" {
		return false, nil
	}
	start, state, err := procIdentity(p.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return start == p.StartTime && state != "Z" && state != "X", nil
}

func (d *Driver) kill(m *manifest) error {
	if m.Process.PID == 0 {
		return nil
	}
	// Holding a pidfd closes the PID-reuse race between verification and signal.
	fd, err := unix.PidfdOpen(m.Process.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	alive, err := d.running(m)
	if err != nil || !alive {
		return err
	}
	err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (d *Driver) waitExit(ctx context.Context, m *manifest) error {
	for {
		alive, err := d.running(m)
		if err != nil || !alive {
			return err
		}
		if err = delay(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
}

func (d *Driver) finish(ctx context.Context, id sandbox.ID, m *manifest) error {
	alive, err := d.running(m)
	if err != nil {
		return err
	}
	if alive {
		return errors.New("refusing cleanup of a running microVM")
	}
	var errs []error
	if d.cfg.UnregisterNetwork != nil {
		errs = append(errs, d.cfg.UnregisterNetwork(ctx, TapName(id), m.Subnet.Addr().Next().Next()))
	}
	errs = append(errs, d.deleteTap(id))
	// Unmount before RemoveAll: removing through a live bind could delete data.
	clean := true
	unmounts := make([]string, 0, 9+sandboxd.MaxGuestLayers)
	for i := sandboxd.MaxGuestLayers - 1; i >= 0; i-- {
		unmounts = append(unmounts, fmt.Sprintf("layer_%d.ext4", i))
	}
	unmounts = append(unmounts, jailSnapDir, jailVMState, jailMemory, "config.img", "home.ext4", "overlay.ext4", "base.ext4", "initrd", "kernel")
	for _, name := range unmounts {
		// The jail is the VMM user's: never follow a symlink it put there.
		err = unix.Unmount(filepath.Join(d.jailRoot(id), name), unix.UMOUNT_NOFOLLOW)
		if err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, err)
			clean = false
		}
	}
	if clean {
		errs = append(errs, os.RemoveAll(filepath.Dir(d.jailRoot(id))))
	}
	if err = os.Remove(filepath.Join(d.dir(id), "config.img")); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	// Only a valid snapshot (and nothing half-written) outlives its VMM.
	if clean {
		errs = append(errs, d.cleanSnapshotFiles(id))
	}
	cg := filepath.Join(d.cfg.CgroupRoot, d.jailID(id))
	if err = os.Remove(cg); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove cgroup: %w", err))
	}
	m.Process = process{}
	if m.State != sandbox.StateCreated {
		m.State = sandbox.StateStopped
	}
	errs = append(errs, d.save(id, m))
	return errors.Join(errs...)
}

func (d *Driver) provisionDisk(path string, size int64, uid, gid uint32) error {
	if d.disk != nil {
		return d.disk(path, size, uid, gid)
	}
	return ensureDisk(path, size, uid, gid)
}

func (d *Driver) guestState(ctx context.Context, id sandbox.ID) (string, error) {
	a := newAPI(d.socket(id))
	defer a.close()
	var info struct {
		State string `json:"state"`
	}
	err := a.request(ctx, "GET", "/", nil, &info)
	return info.State, err
}

// recoverProcess closes the crash window between starting jailer and persisting
// its PID. Jailer puts its one process into a deterministic, per-agent cgroup
// before dropping privileges/exec. A restarted controller can recover that PID
// even if state.json still describes an unstarted VM. Never guess from a PID
// file alone: the cgroup is the kernel's live ownership record.
func (d *Driver) recoverProcess(id sandbox.ID, m *manifest) error {
	alive, err := d.running(m)
	if err != nil || alive {
		return err
	}
	path := filepath.Join(d.cfg.CgroupRoot, d.jailID(id), "cgroup.procs")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pids := strings.Fields(string(data))
	if len(pids) == 0 {
		return nil
	}
	if len(pids) != 1 {
		return fmt.Errorf("sandbox %s cgroup contains %d processes; refusing ambiguous adoption", id, len(pids))
	}
	pid, err := strconv.Atoi(pids[0])
	if err != nil || pid <= 0 {
		return fmt.Errorf("sandbox %s cgroup has an invalid PID", id)
	}
	start, state, err := procIdentity(pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "Z" || state == "X" {
		return nil
	}
	// The process can exit and its numeric PID can be reused after reading
	// cgroup.procs. Confirm current membership after capturing its start time
	// before adopting it; otherwise a later Stop could target another service.
	membership, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	want := "0::/agenthold/" + d.jailID(id)
	if !slices.Contains(strings.Split(strings.TrimSpace(string(membership)), "\n"), want) {
		return fmt.Errorf("sandbox %s recovery PID no longer belongs to its cgroup", id)
	}
	m.Process = process{PID: pid, StartTime: start}
	return d.save(id, m)
}

// consoleWatch bounds how long a boot's console is copied into the log.
const consoleWatch = 2 * time.Minute

// watchConsole copies a booting guest's console (kernel messages, sandboxd's
// init phases, its tunnel attempts) into the log at debug level, as "guest
// console" records of the machine: the activity view shows them while a wake
// is slow. It stops when sandboxd reports its tunnel, the VMM is replaced or
// consoleWatch has passed. Firecracker's own API lines are left out.
func (d *Driver) watchConsole(id sandbox.ID, p process) {
	f, err := os.Open(filepath.Join(d.dir(id), "serial.log"))
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	deadline := time.Now().Add(consoleWatch)
	var partial strings.Builder
	for time.Now().Before(deadline) {
		chunk, err := r.ReadString('\n')
		partial.WriteString(chunk)
		if err != nil {
			if m, lerr := d.load(id); lerr != nil || m.Process != p {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		line := strings.TrimSpace(partial.String())
		partial.Reset()
		if line == "" || vmmLine(line) {
			continue
		}
		if len(line) > 400 {
			line = line[:400] + "…"
		}
		d.cfg.Log.Debug("guest console", "sandbox", id, "line", line)
		if strings.Contains(line, "tunnel connected") {
			return
		}
	}
}

// vmmLine reports whether a console line is Firecracker's own log
// ("2026-11-21T11:21:05.77Z [<jail id>:<thread>] ..."), not the guest's.
func vmmLine(line string) bool {
	if len(line) < 20 || line[4] != '-' || line[10] != 'T' {
		return false
	}
	_, rest, ok := strings.Cut(line, " [")
	if !ok {
		return false
	}
	tag, _, ok := strings.Cut(rest, "]")
	return ok && strings.Contains(tag, ":")
}
