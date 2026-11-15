//go:build linux

package firecracker

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
)

func unitDriver(t *testing.T) *Driver {
	t.Helper()
	cfg := testConfig(t.TempDir())
	cfg.JailDir = "/tmp/ah-unit-jails"
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(cfg.DataDir, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Driver{cfg: cfg, disk: func(path string, size int64, _, _ uint32) error {
		return ensureDisk(path, size, uint32(os.Getuid()), uint32(os.Getgid()))
	}}
}

func TestAllocation(t *testing.T) {
	pool := netip.MustParsePrefix("172.29.1.0/29")
	a, i, err := allocate(pool, nil)
	if err != nil || a.String() != "172.29.1.0/30" || i != 0 {
		t.Fatalf("%s %d %v", a, i, err)
	}
	b, i, err := allocate(pool, []netip.Prefix{a})
	if err != nil || b.String() != "172.29.1.4/30" || i != 1 {
		t.Fatalf("%s %d %v", b, i, err)
	}
	if _, _, err = allocate(pool, []netip.Prefix{a, b}); err == nil {
		t.Fatal("exhausted allocation succeeded")
	}
	if _, _, err = allocate(pool, []netip.Prefix{pool}); err == nil {
		t.Fatal("overlap succeeded")
	}
}

func TestConcurrentCreateAndRestart(t *testing.T) {
	d := unitDriver(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			id, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_same"})
			if err != nil {
				t.Error(err)
			} else if id != "ag_same" {
				t.Error(id)
			}
		})
	}
	wg.Wait()
	id, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_other"})
	if err != nil {
		t.Fatal(err)
	}
	// New driver object reads persisted manifests rather than an in-memory map.
	reopened := &Driver{cfg: d.cfg}
	a, err := reopened.Status(ctx, "ag_same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := reopened.Status(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != sandbox.StateCreated || a.Subnet.Overlaps(b.Subnet) || a.GatewayIP == b.GatewayIP {
		t.Fatalf("bad persisted status %+v %+v", a, b)
	}
	m, err := d.load("ag_same")
	if err != nil {
		t.Fatal(err)
	}
	n, err := d.load(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.UID == n.UID || m.GID == n.GID {
		t.Fatal("VM users are shared")
	}
	for _, name := range []string{"overlay.ext4", "home.ext4"} {
		st, err := os.Stat(filepath.Join(d.dir(id), name))
		if err != nil || st.Size() == 0 {
			t.Fatalf("disk %s %v", name, err)
		}
	}
	missing, err := d.Status(ctx, "../x")
	if err != nil || missing.State != sandbox.StateMissing {
		t.Fatalf("unsafe lookup: %+v %v", missing, err)
	}
	if err = d.Start(ctx, "unknown", sandbox.Boot{}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestLockCancellationAcrossInstances(t *testing.T) {
	d := unitDriver(t)
	unlock, err := d.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other := &Driver{cfg: d.cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = other.lock(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestJailerEnforcesResourcesAndNoBootSecret(t *testing.T) {
	d := unitDriver(t)
	m := &manifest{Spec: sandbox.Spec{Resources: sandbox.Resources{CPUs: 0.25, MemoryMB: 256}}, UID: 12345, GID: 12346}
	args := strings.Join(d.jailerArgs("ag_test", m), " ")
	for _, want := range []string{"--uid 12345", "--gid 12346", "--cgroup-version 2", "--cgroup cpu.max=25000 100000", "--cgroup memory.max=402653184", "--api-sock /api.sock"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s: %s", want, args)
		}
	}
	if strings.Contains(args, "token") || strings.Contains(args, "--daemonize") {
		t.Fatal(args)
	}
	_, mem, vcpus := resources(m)
	if mem != 256 || vcpus != 1 {
		t.Fatal(mem, vcpus)
	}
}

func TestSparseDiskNeverTruncatesExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disk.ext4")
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if err := ensureDisk(p, 64<<20, uid, gid); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte("user-data"), 8<<20)
	_ = f.Close()
	if err = ensureDisk(p, 128<<20, uid, gid); err == nil {
		t.Fatal("implicitly resized existing disk")
	}
	data := make([]byte, 9)
	f, err = os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.ReadAt(data, 8<<20)
	_ = f.Close()
	if err != nil || string(data) != "user-data" {
		t.Fatalf("disk data lost: %q %v", data, err)
	}
}

func TestPIDIdentityDoesNotMatchReusedProcess(t *testing.T) {
	d := unitDriver(t)
	start, state, err := procIdentity(os.Getpid())
	if err != nil || state == "Z" || start == "" {
		t.Fatal(start, state, err)
	}
	m := &manifest{Process: process{PID: os.Getpid(), StartTime: "not-the-same-start-time"}}
	alive, err := d.running(m)
	if err != nil || alive {
		t.Fatal(alive, err)
	}
	if err = d.kill(m); err != nil {
		t.Fatal(err)
	} // must not signal this test process
}

func TestFailedCreateRollsBackOnlyFreshDisks(t *testing.T) {
	d := unitDriver(t)
	d.disk = func(path string, size int64, _, _ uint32) error {
		if strings.HasSuffix(path, "overlay.ext4") {
			return errors.New("injected disk failure")
		}
		return ensureDisk(path, size, uint32(os.Getuid()), uint32(os.Getgid()))
	}
	spec := sandbox.Spec{AgentID: "ag_rollback"}
	if _, err := d.Create(context.Background(), spec); err == nil {
		t.Fatal("injected failure ignored")
	}
	if _, err := os.Stat(d.dir("ag_rollback")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh files leaked: %v", err)
	}
	// A prior KeepHome disk belongs to the user, even without a manifest.
	dir := d.dir("ag_kept")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home.ext4")
	if err := ensureDisk(home, d.cfg.HomeSizeBytes, uint32(os.Getuid()), uint32(os.Getgid())); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(home, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte("keep"), 0)
	_ = f.Close()
	spec.AgentID = "ag_kept"
	if _, err = d.Create(context.Background(), spec); err == nil {
		t.Fatal("injected failure ignored")
	}
	data := make([]byte, 4)
	f, err = os.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.ReadAt(data, 0)
	_ = f.Close()
	if err != nil || string(data) != "keep" {
		t.Fatalf("kept home modified: %q %v", data, err)
	}
}
