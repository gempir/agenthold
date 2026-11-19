//go:build linux

package firecracker

// Memory snapshots: sleep keeps the guest's processes.
//
// Sleep (Stop with Config.Snapshots): sandboxd flushes the guest's
// filesystems (tunnel op "suspend"), the balloon is inflated to the guest's
// free memory, sandboxd syncs once more, the VM is paused, Firecracker
// writes a full snapshot (vmstate and memory file) into <dir>/snapout (the
// jail's /snap), and the VMM is killed. The host then moves the files into
// <dir>/snapshot and writes snapshot.json last, which makes the snapshot
// valid: it records what a resume must find unchanged (Firecracker, kernel,
// sandboxd, host CPU and kernel, VM shape, base image, disk sizes and mtimes).
//
// Wake (Start): a valid, compatible snapshot is consumed (snapshot.json is
// removed before anything runs, so it is never resumed twice and never
// outlives a change to the disks), a new jailed VMM loads it with resume,
// the balloon is deflated and sandboxd gets the boot configuration of this
// wake and the host's clock over vsock (sandboxd.ResumeMessage); it steps
// its clock and redials the tunnel with the new token. If the guest does not
// reconnect within ResumeConnectTimeout the VM is killed and cold booted.
//
// Fallback: anything unexpected (no or incompatible snapshot, load or resume
// error, no answer on vsock, no reconnect) discards the snapshot and cold
// boots from the disks, which are then as a power cut left them (after a
// sync). The disks of a machine with a valid snapshot are never written:
// a cold boot, Rebuild and Destroy discard the snapshot first, and Clone
// only reads them.
//
// While a resumed VM runs, its memory file stays in <dir>/snapshot: it is
// Firecracker's MAP_PRIVATE backing of guest memory. It is removed when that
// VMM exits, or replaced by the next snapshot.
//
// Trust: <dir>/snapout is writable by the VMM user (Firecracker creates the
// snapshot files there), so the host never opens, binds or renames anything
// there by path as root. It opens each file it expects without following
// symlinks and without blocking, checks it is a regular file of the VMM user
// with one link, and works on that descriptor only (punch, fsync, chown to
// root, renameat into the store). <dir>/snapshot is the store: root's, 0700,
// never mounted writable into a jail; resume binds its files read-only. The
// VMM user can read them through its group.
//
// The slow part of a sleep runs without the driver lock: the machine is
// marked suspending, and every other operation on it waits for the mark to
// go (Status answers "running" without touching the VM).

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
)

const (
	snapshotDirName  = "snapshot"
	snapOutDirName   = "snapout"
	snapshotManifest = "snapshot.json"
	snapshotFormat   = 1
	// jailSnapDir is where <dir>/snapout is bind-mounted inside the jail;
	// jailVMState and jailMemory are the store's files, bound read-only for
	// a resume.
	jailSnapDir = "snap"
	jailVMState = "snap-vmstate"
	jailMemory  = "snap-memory"
	// vsockPath is the vsock device's Unix socket inside the jail. It is part
	// of the snapshot: it must not change.
	vsockPath = "/v.sock"
	guestCID  = 3
	// balloonStatsSeconds is the balloon statistics interval (they can only
	// be enabled before boot); inflation is watched through them.
	balloonStatsSeconds = 1
	// balloonHeadroomMiB is guest memory left out of the balloon on top of
	// what the guest uses.
	balloonHeadroomMiB = 64
	// balloonPoll is how often inflation progress is read; balloonStall is
	// how long it may stand still before the guest is taken to give no more.
	balloonPoll  = 100 * time.Millisecond
	balloonStall = 2 * time.Second
)

// snapshotInfo is snapshot.json.
type snapshotInfo struct {
	Format        int                  `json:"format"`
	Created       time.Time            `json:"created"`
	Firecracker   string               `json:"firecracker"`
	Kernel        string               `json:"kernel"`
	Sandboxd      string               `json:"sandboxd"`
	ResumeVersion int                  `json:"resumeVersion"`
	HostCPU       string               `json:"hostCpu"`
	HostKernel    string               `json:"hostKernel"`
	BaseImagePath string               `json:"baseImagePath"`
	VCPUs         int                  `json:"vcpus"`
	MemoryMiB     int64                `json:"memoryMib"`
	Balloon       bool                 `json:"balloon"`
	BalloonMiB    int64                `json:"balloonMib,omitempty"`
	VMStateBytes  int64                `json:"vmstateBytes"`
	MemoryBytes   int64                `json:"memoryBytes"`
	Disks         map[string]diskStamp `json:"disks"`
}

type diskStamp struct {
	Size    int64 `json:"size"`
	ModNano int64 `json:"modNano"`
}

// slow lets requests of this instance take up to t (snapshots write the
// whole guest memory).
func (a *api) slow(t time.Duration) *api {
	a.client.Timeout = t
	return a
}

func (a *api) patch(ctx context.Context, path string, b any) error {
	return a.request(ctx, http.MethodPatch, path, b, nil)
}

// hostIdentity is what a snapshot depends on besides its own files.
type hostIdentity struct {
	firecracker, kernel, cpu, hostKernel string
}

func (d *Driver) snapDir(id sandbox.ID) string { return filepath.Join(d.dir(id), snapshotDirName) }
func (d *Driver) snapOut(id sandbox.ID) string { return filepath.Join(d.dir(id), snapOutDirName) }

// beginSuspend marks id as being snapshotted until endSuspend. Callers hold
// the driver lock for both.
func (d *Driver) beginSuspend(id sandbox.ID) {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	if d.suspending == nil {
		d.suspending = map[sandbox.ID]chan struct{}{}
	}
	d.suspending[id] = make(chan struct{})
}

func (d *Driver) endSuspend(id sandbox.ID) {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	if ch, ok := d.suspending[id]; ok {
		delete(d.suspending, id)
		close(ch)
	}
}

func (d *Driver) suspendingNow(id sandbox.ID) chan struct{} {
	d.cloningMu.Lock()
	defer d.cloningMu.Unlock()
	return d.suspending[id]
}

// lockIdle takes the driver lock once id is not being snapshotted.
func (d *Driver) lockIdle(ctx context.Context, id sandbox.ID) (func(), error) {
	for {
		unlock, err := d.lock(ctx)
		if err != nil {
			return nil, err
		}
		ch := d.suspendingNow(id)
		if ch == nil {
			return unlock, nil
		}
		unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readHostIdentity(cfg Config) (hostIdentity, error) {
	var id hostIdentity
	var err error
	if id.firecracker, err = fileSHA256(cfg.FirecrackerPath); err != nil {
		return id, err
	}
	if id.kernel, err = fileSHA256(cfg.KernelPath); err != nil {
		return id, err
	}
	if id.cpu, err = hostCPU(); err != nil {
		return id, err
	}
	var u unix.Utsname
	if err = unix.Uname(&u); err != nil {
		return id, err
	}
	id.hostKernel = unix.ByteSliceToString(u.Release[:])
	return id, nil
}

// hostCPU identifies the CPU model: snapshots do not move across models
// (Firecracker docs, snapshot-support.md). It hashes the model lines of the
// first processor in /proc/cpuinfo (not frequencies or BogoMIPS) and, on
// arm64, cpu0's MIDR.
func hostCPU() (string, error) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	keep := map[string]bool{
		"vendor_id": true, "cpu family": true, "model": true, "model name": true, "stepping": true, "flags": true,
		"CPU implementer": true, "CPU architecture": true, "CPU variant": true, "CPU part": true, "CPU revision": true, "Features": true,
	}
	h := sha256.New()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		k = strings.TrimSpace(k)
		if !ok || !keep[k] || seen[k] {
			continue
		}
		seen[k] = true
		fmt.Fprintf(h, "%s=%s\n", k, strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if midr, err := os.ReadFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1"); err == nil {
		fmt.Fprintf(h, "midr=%s\n", strings.TrimSpace(string(midr)))
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

func (d *Driver) diskStamps(id sandbox.ID) (map[string]diskStamp, error) {
	out := map[string]diskStamp{}
	for _, name := range diskNames {
		st, err := os.Stat(filepath.Join(d.dir(id), name))
		if err != nil {
			return nil, err
		}
		out[name] = diskStamp{Size: st.Size(), ModNano: st.ModTime().UnixNano()}
	}
	return out, nil
}

// readSnapshot returns the machine's valid snapshot, nil if it has none.
func (d *Driver) readSnapshot(id sandbox.ID) (*snapshotInfo, error) {
	var st unix.Stat_t
	if err := unix.Lstat(d.snapDir(id), &st); errors.Is(err, unix.ENOENT) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&0o077 != 0 {
		return nil, errors.New("snapshot store is not a private directory of the driver")
	}
	data, err := os.ReadFile(filepath.Join(d.snapDir(id), snapshotManifest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s snapshotInfo
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("snapshot manifest: %w", err)
	}
	return &s, nil
}

// HasSnapshot reports whether a sleeping machine will resume with its memory.
func (d *Driver) HasSnapshot(id sandbox.ID) bool {
	s, err := d.readSnapshot(id)
	return err == nil && s != nil
}

// compatible explains why s cannot be resumed for m on this host (nil: it can).
func (d *Driver) compatible(id sandbox.ID, m *manifest, s *snapshotInfo) error {
	_, mem, vcpus := resources(m)
	switch {
	case s.Format != snapshotFormat:
		return fmt.Errorf("snapshot format %d", s.Format)
	case s.Firecracker != d.host.firecracker:
		return errors.New("the Firecracker binary changed since the snapshot")
	case s.Kernel != d.host.kernel:
		return errors.New("guest kernel changed since the snapshot")
	case s.HostCPU != d.host.cpu:
		return errors.New("host CPU model changed since the snapshot")
	case s.HostKernel != d.host.hostKernel:
		return fmt.Errorf("host kernel changed since the snapshot (%s, now %s)", s.HostKernel, d.host.hostKernel)
	case s.Sandboxd != filepath.Base(d.initrd):
		// The guest would keep running the old sandboxd, and a machine that
		// only ever sleeps and wakes would never get a new one.
		return errors.New("sandboxd changed since the snapshot")
	case s.ResumeVersion != sandboxd.ResumeVersion:
		return fmt.Errorf("resume protocol %d, want %d", s.ResumeVersion, sandboxd.ResumeVersion)
	case s.BaseImagePath != m.BaseImagePath:
		return errors.New("base image changed since the snapshot")
	case s.VCPUs != vcpus || s.MemoryMiB != mem:
		return errors.New("machine size changed since the snapshot")
	case !m.Resumable:
		return errors.New("machine was not booted with the resume devices")
	}
	disks, err := d.diskStamps(id)
	if err != nil {
		return err
	}
	for name, want := range s.Disks {
		if disks[name] != want {
			return fmt.Errorf("disk %s changed since the snapshot", name)
		}
	}
	if len(s.Disks) != len(disks) {
		return errors.New("disk set changed since the snapshot")
	}
	for name, size := range map[string]int64{"vmstate": s.VMStateBytes, "memory": s.MemoryBytes, "config.img": -1} {
		st, err := os.Stat(filepath.Join(d.snapDir(id), name))
		if err != nil {
			return fmt.Errorf("snapshot file %s: %w", name, err)
		}
		if size >= 0 && st.Size() != size {
			return fmt.Errorf("snapshot file %s has %d bytes, want %d", name, st.Size(), size)
		}
	}
	return nil
}

// discardSnapshot removes the machine's snapshot. Its VMM must not run.
func (d *Driver) discardSnapshot(id sandbox.ID) error {
	dir := d.snapDir(id)
	// The manifest goes first: whatever remains afterwards is not resumable.
	if err := os.Remove(filepath.Join(dir, snapshotManifest)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDir(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return errors.Join(os.RemoveAll(dir), os.RemoveAll(dir+".pending"), os.RemoveAll(d.snapOut(id)))
}

// cleanSnapshotFiles removes what a stopped machine keeps of an invalid,
// consumed or half-written snapshot (finish calls it; the VMM has exited).
func (d *Driver) cleanSnapshotFiles(id sandbox.ID) error {
	// What the VMM wrote and the host did not take is never trusted.
	err := errors.Join(os.RemoveAll(d.snapOut(id)), os.RemoveAll(d.snapDir(id)+".pending"))
	if s, e := d.readSnapshot(id); e == nil && s != nil {
		return err
	}
	return errors.Join(err, os.RemoveAll(d.snapDir(id)))
}

// SnapshotBytes is the disk space all snapshot files of this node take,
// including the memory files backing resumed VMs.
func (d *Driver) SnapshotBytes() (int64, error) {
	entries, err := os.ReadDir(filepath.Join(d.cfg.DataDir, "agents"))
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var files []os.DirEntry
		for _, sub := range []string{snapshotDirName, snapOutDirName} {
			fs, err := os.ReadDir(filepath.Join(d.cfg.DataDir, "agents", e.Name(), sub))
			if err == nil {
				files = append(files, fs...)
			}
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				continue
			}
			if st, ok := info.Sys().(*unix.Stat_t); ok {
				total += st.Blocks * 512
			} else {
				total += info.Size()
			}
		}
	}
	return total, nil
}

// snapshotRoom fails when a snapshot of memMiB would break the node's cap or
// its free-space reserve. It is deliberately conservative: it asks for the
// full memory size although the stored file ends up sparse (Firecracker writes
// it fully allocated before the zero pages are punched, so that much must be
// free for a moment), and the node's total counts the memory files backing
// resumed VMs, including the one this snapshot will replace.
func (d *Driver) snapshotRoom(memMiB int64) error {
	need := memMiB << 20
	if d.cfg.SnapshotMaxBytes > 0 {
		used, err := d.SnapshotBytes()
		if err != nil {
			return err
		}
		if used+need > d.cfg.SnapshotMaxBytes {
			return fmt.Errorf("snapshots take %d MiB of the node's %d MiB", used>>20, d.cfg.SnapshotMaxBytes>>20)
		}
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(d.cfg.DataDir, &fs); err != nil {
		return err
	}
	if free := int64(fs.Bavail) * fs.Bsize; free-need < d.cfg.SnapshotReserveBytes {
		return fmt.Errorf("%d MiB free on the data filesystem", free>>20)
	}
	return nil
}

// canSnapshot reports whether Stop should try a snapshot of m.
func (d *Driver) canSnapshot(m *manifest) bool {
	return d.cfg.Snapshots && m.Resumable && m.State == sandbox.StateRunning
}

// suspend snapshots a running VM and stops its VMM. On error the VM is left
// running (resumed, if it was paused) or stopped; the caller then shuts it
// down the cold way. It runs without the driver lock, with id marked
// suspending; the caller calls finish afterwards, under the lock.
func (d *Driver) suspend(ctx context.Context, id sandbox.ID, m *manifest) (err error) {
	started := time.Now()
	_, mem, vcpus := resources(m)
	if err = d.snapshotRoom(mem); err != nil {
		return err
	}
	state, err := d.guestState(ctx, id)
	if err != nil {
		return err
	}
	if state != "Running" {
		return fmt.Errorf("VM is %s", state)
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = d.cfg.Suspend(sctx, id)
	cancel()
	if err != nil {
		return fmt.Errorf("sandboxd suspend: %w", err)
	}
	tSynced := time.Now()
	a := newAPI(d.socket(id))
	defer a.close()
	var balloon, balloonTarget int64
	balloonStop := "off"
	tInflated := tSynced
	if m.Balloon {
		balloon, balloonTarget, balloonStop = d.inflate(ctx, a, mem)
		tInflated = time.Now()
		// The guest ran on while the balloon grew: flush again right before
		// the pause.
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = d.cfg.Suspend(sctx, id)
		cancel()
		if err != nil {
			d.deflate(ctx, a, balloon)
			return fmt.Errorf("sandboxd suspend: %w", err)
		}
	}
	tPaused := time.Now()
	if err = a.patch(ctx, "/vm", map[string]string{"state": "Paused"}); err != nil {
		d.deflate(ctx, a, balloon)
		return err
	}
	dir := d.snapDir(id)
	defer func() {
		if err != nil {
			// The VM still runs from its disks: undo the pause and the balloon.
			// (Partial files in the outbox go at finish.)
			if alive, _ := d.running(m); alive {
				_ = a.patch(context.WithoutCancel(ctx), "/vm", map[string]string{"state": "Resumed"})
				d.deflate(context.WithoutCancel(ctx), a, balloon)
			}
		}
	}()
	snapCtx, cancelSnap := context.WithTimeout(ctx, d.cfg.SnapshotTimeout)
	defer cancelSnap()
	if err = newAPI(d.socket(id)).slow(d.cfg.SnapshotTimeout).put(snapCtx, "/snapshot/create", map[string]any{
		"snapshot_type": "Full", "snapshot_path": "/" + jailSnapDir + "/vmstate.new", "mem_file_path": "/" + jailSnapDir + "/memory.new",
	}); err != nil {
		return err
	}
	tWritten := time.Now()
	// The snapshot and the disks (Firecracker fsyncs them) are complete; the
	// paused VM must not run again.
	if err = d.kill(m); err != nil {
		return err
	}
	if err = d.waitExit(context.WithoutCancel(ctx), m); err != nil {
		return err
	}
	info := snapshotInfo{
		Format: snapshotFormat, Created: time.Now().UTC(), Firecracker: d.host.firecracker, Kernel: d.host.kernel,
		Sandboxd: filepath.Base(d.initrd), ResumeVersion: sandboxd.ResumeVersion, HostCPU: d.host.cpu, HostKernel: d.host.hostKernel,
		BaseImagePath: m.BaseImagePath, VCPUs: vcpus, MemoryMiB: mem, Balloon: m.Balloon, BalloonMiB: balloon,
	}
	// From here a failure must not resume the VM (it is gone): the caller's
	// cold stop finds it stopped and finish removes the partial files.
	if err = d.finalizeSnapshot(id, m, &info); err != nil {
		return err
	}
	d.cfg.Log.Info("snapshot taken", "sandbox", id, "memoryMib", mem, "balloonMib", balloon, "balloonTargetMib", balloonTarget, "balloonStop", balloonStop,
		"memoryFileBytes", info.MemoryBytes, "memoryFileAllocated", allocated(filepath.Join(dir, "memory")),
		"prepare", tPaused.Sub(started).Round(time.Millisecond), "sync", tSynced.Sub(started).Round(time.Millisecond),
		"inflate", tInflated.Sub(tSynced).Round(time.Millisecond), "resync", tPaused.Sub(tInflated).Round(time.Millisecond), "write", tWritten.Sub(tPaused).Round(time.Millisecond),
		"finalize", time.Since(tWritten).Round(time.Millisecond),
		"total", time.Since(started).Round(time.Millisecond))
	return nil
}

func allocated(path string) int64 {
	var st unix.Stat_t
	if unix.Stat(path, &st) != nil {
		return -1
	}
	return st.Blocks * 512
}

// punchZeros deallocates the all-zero pages of f. Firecracker writes a full
// snapshot's memory file completely (measured on v1.18: allocated = guest
// memory even with the balloon inflated), but pages the balloon took, and
// memory the guest never touched, are zero: as holes they take no disk and
// still read as zero when the file is mapped on resume.
func punchZeros(f *os.File) error {
	const page = 4096
	buf := make([]byte, 4<<20)
	var off, holeStart int64 = 0, -1
	punch := func(end int64) error {
		if holeStart < 0 {
			return nil
		}
		err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, holeStart, end-holeStart)
		holeStart = -1
		return err
	}
	for {
		n, err := f.ReadAt(buf, off)
		for i := 0; i+page <= n; i += page {
			if isZero(buf[i : i+page]) {
				if holeStart < 0 {
					holeStart = off + int64(i)
				}
			} else if e := punch(off + int64(i)); e != nil {
				return e
			}
		}
		off += int64(n)
		if errors.Is(err, io.EOF) {
			return punch(off - off%page)
		}
		if err != nil {
			return err
		}
	}
}

func isZero(b []byte) bool {
	for len(b) >= 8 {
		if b[0]|b[1]|b[2]|b[3]|b[4]|b[5]|b[6]|b[7] != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// finalizeSnapshot moves the written files from the outbox into the store,
// fsyncs the disks and writes the manifest last. The VMM has exited (it was
// killed and waited for), and no other process runs as its user, so nothing
// changes the outbox while this runs; the files are still checked through
// their descriptors (see the trust note above).
func (d *Driver) finalizeSnapshot(id sandbox.ID, m *manifest, info *snapshotInfo) error {
	store := d.snapDir(id)
	if err := os.RemoveAll(store + ".pending"); err != nil {
		return err
	}
	// A fresh store: a resumed VM's files (vmstate, memory backing a VMM that
	// has exited, config drive) are replaced, the config drive moved over.
	if err := os.Mkdir(store+".pending", 0o700); err != nil {
		return err
	}
	pending := store + ".pending"
	defer os.RemoveAll(pending)
	out, err := openDir(d.snapOut(id))
	if err != nil {
		return err
	}
	defer unix.Close(out)
	dst, err := openDir(pending)
	if err != nil {
		return err
	}
	defer unix.Close(dst)
	for _, name := range []string{"vmstate", "memory"} {
		size, err := takeFile(out, name+".new", dst, name, m.UID, m.GID, name == "memory")
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", name, err)
		}
		if name == "vmstate" {
			info.VMStateBytes = size
		} else {
			info.MemoryBytes = size
		}
	}
	// The config drive is part of the VM: a resume attaches the same file.
	// A cold-booted VM's is in the machine's directory (root's); a resumed
	// VM's is in the store.
	config := filepath.Join(d.dir(id), "config.img")
	if _, err := os.Lstat(config); errors.Is(err, os.ErrNotExist) {
		config = filepath.Join(store, "config.img")
	}
	if err := moveRootFile(config, filepath.Join(pending, "config.img"), m.GID, 0o440); err != nil {
		return fmt.Errorf("snapshot config drive: %w", err)
	}
	// Firecracker flushed its writes; make them durable on the host before
	// the manifest says the disks belong to this snapshot.
	for _, name := range diskNames {
		f, err := os.Open(filepath.Join(d.dir(id), name))
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("sync %s: %w", name, err)
		}
	}
	disks, err := d.diskStamps(id)
	if err != nil {
		return err
	}
	info.Disks = disks
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(pending, snapshotManifest), data, 0o600); err != nil {
		return err
	}
	if err := os.RemoveAll(store); err != nil {
		return err
	}
	if err := os.Rename(pending, store); err != nil {
		return err
	}
	return syncDir(d.dir(id))
}

// fchownRoot gives a file to root (the driver), group gid. Unprivileged
// unit tests replace it.
var fchownRoot = func(fd int, gid uint32) error { return unix.Fchown(fd, 0, int(gid)) }

func openDir(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// takeFile moves name from the VMM-writable directory fromDir into the
// root-owned toDir as to: opened without following symlinks or blocking
// (a FIFO), it must be a regular file of uid with one link; it is punched
// (punch), fsynced, given to root (group gid, 0640: the VMM reads it on
// resume) and renamed, and the renamed entry must be the same file.
func takeFile(fromDir int, name string, toDir int, to string, uid, gid uint32, punch bool) (int64, error) {
	fd, err := unix.Openat(fromDir, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uid || st.Nlink != 1 {
		return 0, fmt.Errorf("%s is not a regular file of the VMM user", name)
	}
	if punch {
		if err := punchZeros(f); err != nil {
			return 0, err
		}
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	if err := fchownRoot(fd, gid); err != nil {
		return 0, err
	}
	if err := unix.Fchmod(fd, 0o640); err != nil {
		return 0, err
	}
	if err := unix.Renameat(fromDir, name, toDir, to); err != nil {
		return 0, err
	}
	var moved unix.Stat_t
	if err := unix.Fstatat(toDir, to, &moved, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, err
	}
	if moved.Dev != st.Dev || moved.Ino != st.Ino {
		return 0, fmt.Errorf("%s changed while it was moved", name)
	}
	return st.Size, unix.Fsync(toDir)
}

// moveRootFile renames a file between root-owned directories and makes it
// root's (group gid, mode).
func moveRootFile(from, to string, gid uint32, mode uint32) error {
	fd, err := unix.Open(from, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%s is not a regular file", from)
	}
	if err := fchownRoot(fd, gid); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return err
	}
	return os.Rename(from, to)
}

type balloonStats struct {
	ActualMiB       int64 `json:"actual_mib"`
	TargetMiB       int64 `json:"target_mib"`
	TotalMemory     int64 `json:"total_memory"`
	AvailableMemory int64 `json:"available_memory"`
}

// inflate grows the balloon to the guest's free memory (minus headroom) and
// waits until the guest stops giving pages. It returns the size reached, the
// target and why it stopped waiting (for the log). Errors only cost snapshot
// size: they are logged.
func (d *Driver) inflate(ctx context.Context, a *api, memMiB int64) (reached, target int64, stop string) {
	var st balloonStats
	if err := a.request(ctx, "GET", "/balloon/statistics", nil, &st); err != nil || st.TotalMemory == 0 {
		d.cfg.Log.Warn("balloon statistics unavailable; snapshot without inflating", "err", err)
		return 0, 0, "no statistics"
	}
	// The balloon's own pages count as used (deflate_on_oom keeps them in
	// the guest's total): after a resume it can still be deflating.
	usedMiB := (st.TotalMemory-st.AvailableMemory)>>20 - st.ActualMiB
	target = memMiB - usedMiB - balloonHeadroomMiB
	if target <= 0 {
		return 0, target, "no room"
	}
	if err := a.patch(ctx, "/balloon", map[string]int64{"amount_mib": target}); err != nil {
		d.cfg.Log.Warn("balloon inflate", "err", err)
		return 0, target, "error"
	}
	deadline := time.Now().Add(15 * time.Second)
	last, lastChange := int64(-1), time.Now()
	stop = "deadline"
	for time.Now().Before(deadline) {
		if err := delay(ctx, balloonPoll); err != nil {
			stop = "cancelled"
			break
		}
		if err := a.request(ctx, "GET", "/balloon/statistics", nil, &st); err != nil {
			stop = "error"
			break
		}
		if st.ActualMiB >= target {
			stop = "target"
			break
		}
		if st.ActualMiB != last {
			last, lastChange = st.ActualMiB, time.Now()
		} else if time.Since(lastChange) > balloonStall {
			stop = "stalled" // the guest gives no more
			break
		}
	}
	return max(st.ActualMiB, 0), target, stop
}

func (d *Driver) deflate(ctx context.Context, a *api, balloon int64) {
	if balloon <= 0 {
		return
	}
	if err := a.patch(ctx, "/balloon", map[string]int64{"amount_mib": 0}); err != nil {
		d.cfg.Log.Warn("balloon deflate", "err", err)
	}
}

// unpause resumes a VM found paused with the driver lock held: only suspend
// pauses, under the lock, so a node died while it snapshotted the VM. The VM
// runs on from its disks; its half-written snapshot goes at its next stop.
//
// The guest then gets a resume message: the host clock (it lagged while the
// VM was paused) and boot's token if it has one. Without (Status), sandboxd
// keeps its token, which the node still accepts, and redials at once.
func (d *Driver) unpause(ctx context.Context, id sandbox.ID, m *manifest, boot sandbox.Boot) {
	d.cfg.Log.Warn("found a paused VM (interrupted sleep); resuming it", "sandbox", id)
	a := newAPI(d.socket(id))
	defer a.close()
	if err := a.patch(ctx, "/vm", map[string]string{"state": "Resumed"}); err != nil {
		d.cfg.Log.Warn("resume paused VM", "sandbox", id, "err", err)
		return
	}
	if m.Balloon {
		d.deflate(ctx, a, 1)
	}
	if !m.Resumable {
		return
	}
	if boot.AgentID == "" {
		boot = sandbox.Boot{AgentID: m.Spec.AgentID}
	}
	if _, err := d.sendResume(ctx, id, boot); err != nil {
		d.cfg.Log.Warn("resume message to a VM that was left paused", "sandbox", id, "err", err)
	}
}

// resume tries to wake m from its snapshot. It returns false without error
// when there is none. On error everything it started is torn down and the
// snapshot is gone: the caller cold boots. The driver lock is held.
func (d *Driver) resume(ctx context.Context, id sandbox.ID, m *manifest, boot sandbox.Boot) (resumed bool, err error) {
	s, err := d.readSnapshot(id)
	if err != nil || s == nil {
		return false, err
	}
	if err = d.compatible(id, m, s); err != nil {
		return false, err
	}
	// Consumed before anything runs: from here the disks will change.
	if err = os.Remove(filepath.Join(d.snapDir(id), snapshotManifest)); err != nil {
		return false, err
	}
	if err = syncDir(d.snapDir(id)); err != nil {
		return false, err
	}
	started := time.Now()
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			killErr := d.kill(m)
			if killErr == nil {
				killErr = d.waitExit(cleanupCtx, m)
			}
			if killErr == nil {
				killErr = d.finish(cleanupCtx, id, m)
			}
			err = errors.Join(err, killErr)
		}
	}()
	if err = d.prepareResume(ctx, id, m); err != nil {
		return false, err
	}
	if err = d.ensureTap(ctx, id, m); err != nil {
		return false, err
	}
	if err = d.cfg.RegisterNetwork(ctx, TapName(id), m.Subnet.Addr().Next().Next()); err != nil {
		return false, err
	}
	a, err := d.startVMM(ctx, id, m)
	if err != nil {
		return false, err
	}
	defer a.close()
	tStarted := time.Now()
	loadCtx, cancel := context.WithTimeout(ctx, d.cfg.SnapshotTimeout)
	defer cancel()
	if err = newAPI(d.socket(id)).slow(d.cfg.SnapshotTimeout).put(loadCtx, "/snapshot/load", map[string]any{
		"snapshot_path": "/" + jailVMState,
		"mem_backend":   map[string]string{"backend_type": "File", "backend_path": "/" + jailMemory},
		"resume_vm":     true,
	}); err != nil {
		return false, err
	}
	tLoaded := time.Now()
	m.State = sandbox.StateRunning
	if err = d.save(id, m); err != nil {
		return false, err
	}
	if s.Balloon && s.BalloonMiB > 0 {
		if err = a.patch(ctx, "/balloon", map[string]int64{"amount_mib": 0}); err != nil {
			return false, fmt.Errorf("balloon deflate: %w", err)
		}
	}
	reply, err := d.sendResume(ctx, id, boot)
	if err != nil {
		return false, fmt.Errorf("resume message: %w", err)
	}
	d.cfg.Log.Info("resumed from snapshot", "sandbox", id, "start", tStarted.Sub(started).Round(time.Millisecond),
		"load", tLoaded.Sub(tStarted).Round(time.Millisecond), "total", time.Since(started).Round(time.Millisecond),
		"clockStep", (time.Duration(reply.ClockStepMillis) * time.Millisecond).String())
	return true, nil
}

// sendResume hands sandboxd this wake's configuration and the host clock
// through Firecracker's vsock (CONNECT <port>, see Firecracker docs/vsock.md).
func (d *Driver) sendResume(ctx context.Context, id sandbox.ID, boot sandbox.Boot) (sandboxd.ResumeReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var last error
	for {
		reply, err := d.trySendResume(ctx, id, boot)
		if err == nil {
			if !reply.OK {
				return reply, fmt.Errorf("sandboxd refused: %s", reply.Error)
			}
			return reply, nil
		}
		last = err
		if delay(ctx, 50*time.Millisecond) != nil {
			return reply, last
		}
	}
}

func (d *Driver) trySendResume(ctx context.Context, id sandbox.ID, boot sandbox.Boot) (sandboxd.ResumeReply, error) {
	var reply sandboxd.ResumeReply
	conn, err := dialSocket(ctx, filepath.Join(d.jailRoot(id), strings.TrimPrefix(vsockPath, "/")))
	if err != nil {
		return reply, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err = fmt.Fprintf(conn, "CONNECT %d\n", sandboxd.ResumePort); err != nil {
		return reply, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return reply, fmt.Errorf("vsock connect: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		return reply, fmt.Errorf("vsock connect: %q", strings.TrimSpace(line))
	}
	msg := sandboxd.ResumeMessage{Version: sandboxd.ResumeVersion, AgentID: boot.AgentID, Token: boot.Token, Files: boot.Files, TimeUnixNano: time.Now().UnixNano()}
	if err = json.NewEncoder(conn).Encode(msg); err != nil {
		return reply, err
	}
	if err = json.NewDecoder(r).Decode(&reply); err != nil {
		return reply, fmt.Errorf("resume reply: %w", err)
	}
	return reply, nil
}
