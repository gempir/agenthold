//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
)

func TestPunchZeros(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory")
	data := make([]byte, 8<<20+1000) // not a whole number of pages
	for _, off := range []int{0, 4096*3 + 17, 5 << 20, len(data) - 1} {
		data[off] = 0xa5
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := punchZeros(f); err != nil {
		if strings.Contains(err.Error(), "operation not supported") {
			t.Skip("filesystem cannot punch holes")
		}
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content changed: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	if st.Blocks*512 > 64<<10 {
		t.Fatalf("%d bytes still allocated", st.Blocks*512)
	}
}

// A machine with a snapshot resumes only what it can: every recorded
// property that changed makes the snapshot unusable, and finish keeps a valid
// snapshot but drops half-written or consumed ones.
func TestSnapshotCompatibility(t *testing.T) {
	d := unitDriver(t)
	d.host = hostIdentity{firecracker: "fc", kernel: "k", cpu: "cpu", hostKernel: "6.8"}
	d.initrd = "/x/initrd/abc.cpio"
	spec := sandbox.Spec{AgentID: "ag_snap", Resources: sandbox.Resources{CPUs: 1, MemoryMB: 256}}
	id, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	m, err := d.load(id)
	if err != nil {
		t.Fatal(err)
	}
	m.Resumable = true
	vmm := *m
	vmm.UID = uint32(os.Getuid()) // the files below are "the VMM's"
	unprivilegedChown(t)
	dir := d.snapDir(id)
	out := d.snapOut(id)
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmstate.new", "memory.new"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d.dir(id), "config.img"), []byte("cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := snapshotInfo{
		Format: snapshotFormat, Firecracker: "fc", Kernel: "k", HostCPU: "cpu", HostKernel: "6.8", ResumeVersion: sandboxd.ResumeVersion,
		Sandboxd: "abc.cpio", BaseImagePath: m.BaseImagePath, VCPUs: 1, MemoryMiB: 256,
	}
	if err := d.finalizeSnapshot(id, &vmm, &info); err != nil {
		t.Fatal(err)
	}
	s, err := d.readSnapshot(id)
	if err != nil || s == nil {
		t.Fatalf("snapshot %v %v", s, err)
	}
	if err := d.compatible(id, m, s); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*snapshotInfo, *manifest){
		"firecracker": func(s *snapshotInfo, _ *manifest) { s.Firecracker = "other" },
		"kernel":      func(s *snapshotInfo, _ *manifest) { s.Kernel = "other" },
		"cpu":         func(s *snapshotInfo, _ *manifest) { s.HostCPU = "other" },
		"host kernel": func(s *snapshotInfo, _ *manifest) { s.HostKernel = "5.10" },
		"protocol":    func(s *snapshotInfo, _ *manifest) { s.ResumeVersion++ },
		"sandboxd":    func(s *snapshotInfo, _ *manifest) { s.Sandboxd = "def.cpio" },
		"base":        func(_ *snapshotInfo, m *manifest) { m.BaseImagePath = "/other.ext4" },
		"layers":      func(_ *snapshotInfo, m *manifest) { m.Layers = []LayerFile{{Path: "/layer.ext4", Subdir: "upper"}} },
		"size":        func(_ *snapshotInfo, m *manifest) { m.Spec.Resources.MemoryMB = 512 },
		"devices":     func(_ *snapshotInfo, m *manifest) { m.Resumable = false },
		"disk": func(s *snapshotInfo, _ *manifest) {
			st := s.Disks["home.ext4"]
			st.ModNano++
			s.Disks["home.ext4"] = st
		},
		"memory size": func(s *snapshotInfo, _ *manifest) { s.MemoryBytes++ },
	} {
		var sc snapshotInfo
		data, _ := json.Marshal(s)
		_ = json.Unmarshal(data, &sc)
		mc := *m
		change(&sc, &mc)
		if err := d.compatible(id, &mc, &sc); err == nil {
			t.Errorf("%s change was accepted", name)
		}
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("store %v %v", st, err)
	}
	for _, name := range []string{"vmstate", "memory", "config.img", snapshotManifest} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("store lacks %s: %v", name, err)
		}
	}
	// finish keeps a valid snapshot, minus whatever the VMM left behind.
	if err := os.WriteFile(filepath.Join(out, "memory.new"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.finish(context.Background(), id, m); err != nil {
		t.Fatal(err)
	}
	if !d.HasSnapshot(id) {
		t.Fatal("finish dropped a valid snapshot")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("outbox kept")
	}
	// Without its manifest (consumed by a resume) nothing is kept.
	if err := os.Remove(filepath.Join(dir, snapshotManifest)); err != nil {
		t.Fatal(err)
	}
	if err := d.finish(context.Background(), id, m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("consumed snapshot files kept")
	}
	// Rebuild discards a snapshot.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, snapshotManifest), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Rebuild(context.Background(), id, spec); err != nil {
		t.Fatal(err)
	}
	if d.HasSnapshot(id) {
		t.Fatal("rebuild kept the snapshot")
	}
}

// unprivilegedChown lets the store's files stay the test user's when the
// test cannot give them to root.
func unprivilegedChown(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		return
	}
	old := fchownRoot
	fchownRoot = func(int, uint32) error { return nil }
	t.Cleanup(func() { fchownRoot = old })
}

// The outbox is the VMM user's: what it plants there is refused without
// being followed, read or blocked on.
func TestTakeFileRefusesPlantedFiles(t *testing.T) {
	unprivilegedChown(t)
	base := t.TempDir()
	out, store := filepath.Join(base, "out"), filepath.Join(base, "store")
	for _, p := range []string{out, store} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(base, "host-file")
	content := make([]byte, 64<<10) // zero pages: punching would deallocate them
	if err := os.WriteFile(victim, content, 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if err := os.Symlink(victim, filepath.Join(out, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(out, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(out, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(out, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "good"), []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	outFD, err := openDir(out)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(outFD)
	storeFD, err := openDir(store)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(storeFD)
	for _, name := range []string{"symlink", "fifo", "hardlink", "dir", "missing"} {
		done := make(chan error, 1)
		go func() { _, err := takeFile(outFD, name, storeFD, name, uid, gid, true); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s was taken", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked the host", name)
		}
		if _, err := os.Lstat(filepath.Join(store, name)); !os.IsNotExist(err) {
			t.Errorf("%s reached the store", name)
		}
	}
	// A file of another user is refused too.
	if _, err := takeFile(outFD, "good", storeFD, "good", uid+1, gid, false); err == nil {
		t.Error("a file of another user was taken")
	}
	var st unix.Stat_t
	if err := unix.Stat(victim, &st); err != nil || st.Blocks*512 < int64(len(content)) {
		t.Fatalf("the host file was punched: %d bytes allocated, %v", st.Blocks*512, err)
	}
	size, err := takeFile(outFD, "good", storeFD, "memory", uid, gid, true)
	if err != nil || size != 8 {
		t.Fatalf("regular file: %d %v", size, err)
	}
	if got, _ := os.ReadFile(filepath.Join(store, "memory")); string(got) != "snapshot" {
		t.Fatalf("moved file %q", got)
	}
}

// Sockets in a jail are dialed without following a symlink the VMM put there.
func TestDialSocketRefusesSymlinks(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ahsock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	real := filepath.Join(dir, "host.sock")
	ln, err := net.Listen("unix", real)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	c, err := dialSocket(context.Background(), real)
	if err != nil {
		t.Fatal("real socket:", err)
	}
	_ = c.Close()
	link := filepath.Join(dir, "api.sock")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if c, err := dialSocket(context.Background(), link); err == nil {
		_ = c.Close()
		t.Fatal("dialed through a symlink")
	}
	file := filepath.Join(dir, "file.sock")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dialSocket(context.Background(), file); err == nil {
		t.Fatal("dialed a regular file")
	}
}

// While a machine sleeps (snapshot written without the driver lock), the
// other machines' operations go on, its Status says running without
// touching it, and its own operations wait for the sleep to end.
func TestSuspendingMachineDoesNotHoldTheDriver(t *testing.T) {
	d := unitDriver(t)
	ctx := context.Background()
	for _, id := range []string{"ag_a", "ag_b"} {
		if _, err := d.Create(ctx, sandbox.Spec{AgentID: id}); err != nil {
			t.Fatal(err)
		}
	}
	unlock, err := d.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.beginSuspend("ag_a")
	unlock()
	quick := func(what string, fn func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fn() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited for another machine's sleep", what)
		}
	}
	quick("status of the sleeping machine", func() error {
		st, err := d.Status(ctx, "ag_a")
		if err == nil && st.State != sandbox.StateRunning {
			err = fmt.Errorf("state %s", st.State)
		}
		return err
	})
	quick("status of another machine", func() error { _, err := d.Status(ctx, "ag_b"); return err })
	quick("create", func() error { _, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_c"}); return err })
	quick("destroy of another machine", func() error { return d.Destroy(ctx, "ag_c", sandbox.DestroyOpts{}) })
	destroyed := make(chan error, 1)
	go func() { destroyed <- d.Destroy(ctx, "ag_a", sandbox.DestroyOpts{}) }()
	select {
	case err := <-destroyed:
		t.Fatalf("destroy of the sleeping machine did not wait: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	unlock, err = d.lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.endSuspend("ag_a")
	unlock()
	select {
	case err := <-destroyed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("destroy did not go on after the sleep")
	}
}
