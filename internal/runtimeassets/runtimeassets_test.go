package runtimeassets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/compress/zstd"
)

func privateDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func digest(b []byte) artifact {
	h := sha256.Sum256(b)
	return artifact{SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
}

func fixture(t *testing.T) (fixtureManifest, fstest.MapFS, []byte, []byte) {
	t.Helper()
	m := fixtureManifest{manifest: manifest{Format: 2, Version: "v1.2.3", Arch: runtime.GOARCH, Files: map[string]artifact{}}}
	files := fstest.MapFS{}
	for _, name := range bundledNames {
		data := []byte("verified " + name)
		files[name] = &fstest.MapFile{Data: data}
		m.Files[name] = digest(data)
	}
	raw := bytes.Repeat([]byte("base disk fixture\n"), 100)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(raw, nil)
	_ = encoder.Close()
	m.Base = fixtureBase{baseArtifact: baseArtifact{artifact: digest(raw), Compressed: digest(compressed)}, URL: "https://example.invalid/base.ext4.zst"}
	setManifest(t, files, m)
	return m, files, raw, compressed
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && (strings.HasPrefix(d.Name(), ".agenthold-") || strings.HasSuffix(d.Name(), ".part")) {
			t.Errorf("temporary file remains: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDownloadCacheRepairAndOffline(t *testing.T) {
	m, files, raw, compressed := fixture(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(compressed)
	}))
	defer srv.Close()
	m.Base.URL = srv.URL + "/pinned.ext4.zst"
	setManifest(t, files, m)
	dir := privateDir(t)
	paths, err := prepareT(t.Context(), dir, files, srv.Client(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !paths.Bundled || paths.Version != m.Version || !filepath.IsAbs(paths.BaseImage) || requests.Load() != 1 {
		t.Fatalf("paths=%+v requests=%d", paths, requests.Load())
	}
	if paths.BaseImage != filepath.Join(dir, "base", m.Base.SHA256+".ext4") {
		// macOS t.TempDir can contain trusted parent symlinks (for example /var).
		canonical, err := filepath.EvalSymlinks(dir)
		if err != nil || paths.BaseImage != filepath.Join(canonical, "base", m.Base.SHA256+".ext4") {
			t.Fatalf("base path=%q", paths.BaseImage)
		}
	}
	got, err := os.ReadFile(paths.BaseImage)
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatalf("base bytes: %v", err)
	}
	for _, path := range []string{paths.Firecracker, paths.Jailer, paths.Kernel, paths.Sandboxd, paths.BaseImage} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm()&0o444 != 0o444 {
			t.Fatalf("not world readable: %s %v", path, err)
		}
	}
	if err := os.WriteFile(paths.Firecracker, []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.Close() // A verified pre-placed base is sufficient for a fully offline startup.
	if _, err := prepareT(t.Context(), dir, files, srv.Client(), Options{}); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(paths.Firecracker)
	if err != nil || string(got) != "verified firecracker" || requests.Load() != 1 {
		t.Fatalf("cache repair: %q err=%v requests=%d", got, err, requests.Load())
	}
	assertNoTemps(t, dir)
}

func TestPreparePreplacedBase(t *testing.T) {
	m, files, raw, _ := fixture(t)
	dir := privateDir(t)
	if err := os.Mkdir(filepath.Join(dir, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base", m.Base.SHA256+".ext4"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareT(t.Context(), dir, files, &http.Client{Timeout: time.Millisecond}, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCorruption(t *testing.T) {
	for _, kind := range []string{"compressed", "raw", "embedded", "preplaced", "zstd"} {
		t.Run(kind, func(t *testing.T) {
			m, files, raw, compressed := fixture(t)
			dir := privateDir(t)
			switch kind {
			case "compressed":
				compressed[0] ^= 0xFF
			case "raw":
				m.Base.artifact = digest(bytes.Repeat([]byte("x"), len(raw)))
			case "embedded":
				files["firecracker"].Data = []byte("wrong firecracker")
			case "preplaced":
				if err := os.Mkdir(filepath.Join(dir, "base"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "base", m.Base.SHA256+".ext4"), []byte("corrupt"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "zstd":
				compressed = []byte("not a zstd stream")
				m.Base.Compressed = digest(compressed)
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(compressed)
			}))
			defer srv.Close()
			m.Base.URL = srv.URL
			setManifest(t, files, m)
			if _, err := prepareT(t.Context(), dir, files, srv.Client(), Options{}); err == nil {
				t.Fatal("corrupt artifact accepted")
			}
			if kind == "preplaced" && requests.Load() != 0 {
				t.Fatal("corrupt pre-placed base must be reported without downloading over it")
			}
			if kind != "preplaced" {
				if _, err := os.Stat(filepath.Join(dir, "base", m.Base.SHA256+".ext4")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("unverified base was published: %v", err)
				}
			}
			assertNoTemps(t, dir)
		})
	}
}

func TestPrepareCancellation(t *testing.T) {
	m, files, _, _ := fixture(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0})
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	m.Base.URL = srv.URL
	setManifest(t, files, m)
	dir := privateDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := prepareT(ctx, dir, files, srv.Client(), Options{}); done <- err }()
	select {
	case <-started:
		canonical, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		part := partPath(filepath.Join(canonical, "base"), m.Base.SHA256)
		deadline := time.Now().Add(5 * time.Second)
		for fi, err := os.Stat(part); err != nil || fi.Size() == 0; fi, err = os.Stat(part) {
			if time.Now().After(deadline) {
				t.Fatal("download wrote nothing")
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop download")
	}
	// The partial download is the resume point, so it stays; nothing else does.
	if _, err := os.Stat(filepath.Join(dir, "base", m.Base.SHA256+".ext4")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("base published after cancellation: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(partPath(filepath.Join(canonical, "base"), m.Base.SHA256)); err != nil {
		t.Fatalf("partial download not kept for resuming: %v", err)
	}
}

func TestManifestAndUnsafePaths(t *testing.T) {
	for _, kind := range []string{"version", "arch", "sha", "size", "filelist", "symlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			m, files, _, _ := fixture(t)
			dir := privateDir(t)
			switch kind {
			case "version":
				m.Version = "../../escape"
			case "arch":
				m.Arch = "wrong"
			case "sha":
				m.Base.SHA256 = "not a checksum"
			case "size":
				m.Base.Compressed.Size = 2_000_000_001
			case "filelist":
				m.Files["../escape"] = digest([]byte("x"))
			case "symlink":
				if err := os.Symlink(privateDir(t), filepath.Join(dir, "runtime")); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(dir, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			setManifest(t, files, m)
			if _, err := prepareT(t.Context(), dir, files, &http.Client{Timeout: time.Millisecond}, Options{}); err == nil {
				t.Fatal("unsafe manifest or directory accepted")
			}
		})
	}
}

func TestUnbundledHasNoSideEffects(t *testing.T) {
	if bundledFiles() != nil {
		t.Skip("release build")
	}
	dir := filepath.Join(privateDir(t), "missing")
	paths, err := Prepare(t.Context(), dir, Options{})
	if err != nil || paths.Bundled || paths != (Paths{}) {
		t.Fatalf("unbundled: %+v %v", paths, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unbundled startup touched data directory: %v", err)
	}
}

func TestPinBaseSurvivesSourceRebuild(t *testing.T) {
	dir := privateDir(t)
	source := filepath.Join(privateDir(t), "mutable.ext4")
	if err := os.WriteFile(source, []byte("original image"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := PinBase(t.Context(), dir, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replacement image"), 0o644); err != nil {
		t.Fatal(err)
	}
	next, err := PinBase(t.Context(), dir, source)
	if err != nil || old == next {
		t.Fatalf("new pin=%s old=%s err=%v", next, old, err)
	}
	got, err := os.ReadFile(old)
	if err != nil || string(got) != "original image" {
		t.Fatalf("old image changed: %q %v", got, err)
	}
	fi, err := os.Stat(next)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := PinBase(t.Context(), dir, next)
	if err != nil || canonical != next {
		t.Fatalf("canonical pin=%s err=%v", canonical, err)
	}
	after, err := os.Stat(canonical)
	if err != nil || !os.SameFile(fi, after) {
		t.Fatal("canonical image was unnecessarily replaced")
	}
	assertNoTemps(t, dir)
}
