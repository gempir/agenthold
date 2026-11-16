//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gempir/agenthold/internal/sandbox"
)

func cloneDriver(t *testing.T) *Driver {
	t.Helper()
	d := unitDriver(t)
	d.chown = func(string, int, int) error { return nil } // not root
	return d
}

func writeAt(t *testing.T, path string, off int64, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteAt(data, off); err != nil {
		t.Fatal(err)
	}
}

func readAt(t *testing.T, path string, off int64, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err = f.ReadAt(b, off); err != nil {
		t.Fatal(err)
	}
	return b
}

func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Blocks * 512
}

func TestCloneCopiesDisksAndGetsItsOwnIdentity(t *testing.T) {
	d := cloneDriver(t)
	ctx := context.Background()
	small := sandbox.Spec{AgentID: "ag_src", Resources: sandbox.Resources{DiskGB: 1}}
	if _, err := d.Create(ctx, small); err != nil {
		t.Fatal(err)
	}
	src := d.dir("ag_src")
	writeAt(t, filepath.Join(src, "home.ext4"), 1<<20, []byte("home data"))
	writeAt(t, filepath.Join(src, "overlay.ext4"), 5<<20, []byte("system data"))
	srcManifest, err := d.load("ag_src")
	if err != nil {
		t.Fatal(err)
	}
	srcManifest.BaseImagePath = "/old/base.ext4" // built from an older base: the clone must keep it
	if err = d.save("ag_src", srcManifest); err != nil {
		t.Fatal(err)
	}

	spec := sandbox.Spec{AgentID: "ag_dst", Resources: sandbox.Resources{CPUs: 2, MemoryMB: 2048}, Env: map[string]string{"A": "b"}}
	id, err := d.Clone(ctx, "ag_src", spec)
	if err != nil || id != "ag_dst" {
		t.Fatalf("clone: %q %v", id, err)
	}
	dst := d.dir("ag_dst")
	if got := readAt(t, filepath.Join(dst, "home.ext4"), 1<<20, 9); string(got) != "home data" {
		t.Fatalf("home = %q", got)
	}
	if got := readAt(t, filepath.Join(dst, "overlay.ext4"), 5<<20, 11); string(got) != "system data" {
		t.Fatalf("overlay = %q", got)
	}
	for _, name := range diskNames {
		s, c := filepath.Join(src, name), filepath.Join(dst, name)
		ss, _ := os.Stat(s)
		cs, err := os.Stat(c)
		if err != nil || cs.Size() != ss.Size() {
			t.Fatalf("%s: %v size %d, want %d", name, err, cs.Size(), ss.Size())
		}
		// The disks are sparse files: holes are not materialized.
		if a := allocatedBytes(t, c); a > 8<<20 {
			t.Fatalf("%s allocates %d bytes", name, a)
		}
	}
	// Independent from here on.
	writeAt(t, filepath.Join(dst, "home.ext4"), 1<<20, []byte("changed!!"))
	if got := readAt(t, filepath.Join(src, "home.ext4"), 1<<20, 9); string(got) != "home data" {
		t.Fatalf("a write in the clone reached the source: %q", got)
	}
	// No temporary files are left, and the clone is a created sandbox.
	if entries, _ := os.ReadDir(dst); len(entries) != 3 { // home, overlay, state.json
		t.Fatalf("clone dir: %v", entries)
	}
	m, err := d.load("ag_dst")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != sandbox.StateCreated || m.BaseImagePath != "/old/base.ext4" || m.Spec.AgentID != "ag_dst" || m.Spec.Resources.MemoryMB != 2048 || m.Spec.Env["A"] != "b" {
		t.Fatalf("manifest = %+v", m)
	}
	// Its own network allocation and VMM user, like Create.
	if m.Subnet == srcManifest.Subnet || m.UID == srcManifest.UID || m.GID == srcManifest.GID {
		t.Fatalf("clone shares network or user with its source: %+v vs %+v", m, srcManifest)
	}
	st, err := d.Status(ctx, "ag_dst")
	if err != nil || st.State != sandbox.StateCreated || st.GatewayIP == "" {
		t.Fatalf("status %+v %v", st, err)
	}
	// Cloning again onto the same id returns the existing sandbox untouched.
	if again, err := d.Clone(ctx, "ag_src", spec); err != nil || again != "ag_dst" {
		t.Fatalf("second clone: %q %v", again, err)
	}
	if got := readAt(t, filepath.Join(dst, "home.ext4"), 1<<20, 9); string(got) != "changed!!" {
		t.Fatalf("second clone overwrote the first: %q", got)
	}
}

func TestCloneRefusesWhatItCannotCopy(t *testing.T) {
	d := cloneDriver(t)
	ctx := context.Background()
	if _, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_src", Resources: sandbox.Resources{DiskGB: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Clone(ctx, "ag_missing", sandbox.Spec{AgentID: "ag_a"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "ag_src"}); err == nil {
		t.Fatal("cloned onto itself")
	}
	if _, err := d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "../escape"}); err == nil {
		t.Fatal("accepted an unsafe destination id")
	}

	// A running source: pretend our own process is its VMM.
	m, err := d.load("ag_src")
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := procIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	m.Process = process{PID: os.Getpid(), StartTime: start}
	if err = d.save("ag_src", m); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "ag_dst"}); !errors.Is(err, sandbox.ErrRunning) {
		t.Fatalf("clone of a running source: %v", err)
	}
	if _, err = os.Stat(d.dir("ag_dst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused clone left %s: %v", d.dir("ag_dst"), err)
	}
}

func TestFailedAndCancelledClonesLeaveNothing(t *testing.T) {
	d := cloneDriver(t)
	ctx := context.Background()
	if _, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_src", Resources: sandbox.Resources{DiskGB: 1}}); err != nil {
		t.Fatal(err)
	}
	gone := func(id string) {
		t.Helper()
		if _, err := os.Stat(d.dir(sandbox.ID(id))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind: %v", id, err)
		}
		if st, err := d.Status(ctx, sandbox.ID(id)); err != nil || st.State != sandbox.StateMissing {
			t.Fatalf("status of %s: %+v %v", id, st, err)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Clone(cancelled, "ag_src", sandbox.Spec{AgentID: "ag_cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clone: %v", err)
	}
	gone("ag_cancelled")

	// Fails after both disks were copied, at the last step before they become visible.
	d.chown = func(string, int, int) error { return errors.New("injected chown failure") }
	if _, err := d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "ag_failed"}); err == nil {
		t.Fatal("injected failure ignored")
	}
	gone("ag_failed")
	// And the source is still fine, and no network was taken.
	d.chown = func(string, int, int) error { return nil }
	if _, err := d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "ag_ok"}); err != nil {
		t.Fatal(err)
	}
	if m, err := d.load("ag_ok"); err != nil || m.Subnet.Addr().Next().Next().String() == "" {
		t.Fatalf("%+v %v", m, err)
	}
}

// A crash during the copy leaves temporary files in the destination
// directory, but no manifest, so no sandbox exists. The next clone starts clean.
func TestCloneAfterACrashedAttempt(t *testing.T) {
	d := cloneDriver(t)
	ctx := context.Background()
	if _, err := d.Create(ctx, sandbox.Spec{AgentID: "ag_src", Resources: sandbox.Resources{DiskGB: 1}}); err != nil {
		t.Fatal(err)
	}
	dst := d.dir("ag_dst")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home.ext4.cloning", "overlay.ext4", "stale"} {
		if err := os.WriteFile(filepath.Join(dst, name), bytes.Repeat([]byte{1}, 4096), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := d.Status(ctx, "ag_dst"); err != nil || st.State != sandbox.StateMissing {
		t.Fatalf("debris looks like a sandbox: %+v %v", st, err)
	}
	if _, err := d.Clone(ctx, "ag_src", sandbox.Spec{AgentID: "ag_dst"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("debris of the crashed attempt survived")
	}
	if got := readAt(t, filepath.Join(dst, "overlay.ext4"), 0, 4); !bytes.Equal(got, make([]byte, 4)) {
		t.Fatalf("overlay carries debris: %v", got)
	}
}

// While a clone reads the source's disks (without the driver lock), the
// source cannot be started, rebuilt or destroyed.
func TestSourceIsProtectedWhileItIsCloned(t *testing.T) {
	d := cloneDriver(t)
	ctx := context.Background()
	spec := sandbox.Spec{AgentID: "ag_src", Resources: sandbox.Resources{DiskGB: 1}}
	if _, err := d.Create(ctx, spec); err != nil {
		t.Fatal(err)
	}
	d.beginClone("ag_src")
	if err := d.Start(ctx, "ag_src", sandbox.Boot{AgentID: "ag_src"}); !errors.Is(err, errCloning) {
		t.Fatalf("start: %v", err)
	}
	if err := d.Rebuild(ctx, "ag_src", spec); !errors.Is(err, errCloning) {
		t.Fatalf("rebuild: %v", err)
	}
	if err := d.Destroy(ctx, "ag_src", sandbox.DestroyOpts{}); !errors.Is(err, errCloning) {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.dir("ag_src"), "home.ext4")); err != nil {
		t.Fatal(err)
	}
	d.endClone("ag_src")
	if err := d.Destroy(ctx, "ag_src", sandbox.DestroyOpts{}); err != nil {
		t.Fatalf("destroy after the clone: %v", err)
	}
}
