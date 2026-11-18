//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteInitrdKeepsTheNewestTwo(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sandboxd")
	if err := os.WriteFile(bin, []byte("pretend sandboxd"), 0o755); err != nil {
		t.Fatal(err)
	}
	idir := filepath.Join(dir, "initrd")
	if err := os.MkdirAll(idir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"oldest.cpio", "older.cpio", "previous.cpio"} {
		p := filepath.Join(idir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(idir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := writeInitrd(dir, bin)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 || !strings.HasSuffix(path, ".cpio") {
		t.Fatalf("initrd %s: %v %v", path, fi, err)
	}
	var names []string
	entries, _ := os.ReadDir(idir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := map[string]bool{filepath.Base(path): true, "previous.cpio": true, "notes.txt": true}
	if len(names) != len(want) {
		t.Fatalf("initrd dir: %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("initrd dir: %v", names)
		}
	}
	// Same binary again: same archive, nothing else removed.
	again, err := writeInitrd(dir, bin)
	if err != nil || again != path {
		t.Fatalf("second write %s %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(idir, "previous.cpio")); err != nil {
		t.Fatal("previous archive removed by an unchanged start")
	}
}

func TestRuntimeFilesSandboxdNotWritableByOthers(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{}
	for _, f := range []struct {
		p    *string
		name string
		mode os.FileMode
	}{{&cfg.BaseImagePath, "base", 0o644}, {&cfg.KernelPath, "kernel", 0o644}, {&cfg.FirecrackerPath, "fc", 0o755}, {&cfg.JailerPath, "jailer", 0o755}, {&cfg.SandboxdPath, "sandboxd", 0o755}} {
		*f.p = filepath.Join(dir, f.name)
		if err := os.WriteFile(*f.p, []byte("x"), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(*f.p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRuntimeFiles(cfg); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o775, 0o757} {
		if err := os.Chmod(cfg.SandboxdPath, mode); err != nil {
			t.Fatal(err)
		}
		if err := checkRuntimeFiles(cfg); err == nil || !strings.Contains(err.Error(), "sandboxd") {
			t.Fatalf("mode %o: %v", mode, err)
		}
	}
	if err := os.Chmod(cfg.SandboxdPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.SandboxdPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.SandboxdPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkRuntimeFiles(cfg); err == nil {
		t.Fatal("a directory accepted as sandboxd")
	}
}
