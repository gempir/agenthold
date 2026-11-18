//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	dir := d.snapDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmstate.new", "memory.new"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d.dir(id), "config.img"), []byte("cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := snapshotInfo{Format: snapshotFormat, Firecracker: "fc", Kernel: "k", HostCPU: "cpu", HostKernel: "6.8", ResumeVersion: sandboxd.ResumeVersion,
		BaseImagePath: m.BaseImagePath, VCPUs: 1, MemoryMiB: 256}
	if err := d.finalizeSnapshot(id, &info); err != nil {
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
		"base":        func(_ *snapshotInfo, m *manifest) { m.BaseImagePath = "/other.ext4" },
		"size":        func(_ *snapshotInfo, m *manifest) { m.Spec.Resources.MemoryMB = 512 },
		"devices":     func(_ *snapshotInfo, m *manifest) { m.Resumable = false },
		"disk":        func(s *snapshotInfo, _ *manifest) { st := s.Disks["home.ext4"]; st.ModNano++; s.Disks["home.ext4"] = st },
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
	// finish keeps a valid snapshot, minus stray partial files.
	if err := os.WriteFile(filepath.Join(dir, "memory.new"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.finish(context.Background(), id, m); err != nil {
		t.Fatal(err)
	}
	if !d.HasSnapshot(id) {
		t.Fatal("finish dropped a valid snapshot")
	}
	if _, err := os.Stat(filepath.Join(dir, "memory.new")); !os.IsNotExist(err) {
		t.Fatal("partial file kept")
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
