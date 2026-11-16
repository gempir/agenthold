package diskcopy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const diskSize = 64 << 20

// writeSparse makes a 64 MiB file with a little data in a few places, one run
// of explicit zeros inside a data extent, and holes everywhere else.
func writeSparse(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = f.Truncate(diskSize); err != nil {
		t.Fatal(err)
	}
	pattern := func(seed byte, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i%251)
		}
		return b
	}
	for _, w := range []struct {
		off  int64
		data []byte
	}{
		{0, pattern(1, 8192)},
		{3 << 20, pattern(2, 3<<20)},                   // spans several chunks
		{20<<20 + 100, append(make([]byte, 20000), 7)}, // data after explicit zeros, unaligned
		{diskSize - 4096, pattern(3, 4096)},            // the very last block
	} {
		if _, err = f.WriteAt(w.data, w.off); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
}

func allocated(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Blocks * 512
}

func equal(t *testing.T, a, b string) {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x, y) {
		t.Fatal("copy differs from the source")
	}
}

func TestSparseCopyKeepsContentAndHoles(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.ext4"), filepath.Join(dir, "dst.ext4")
	writeSparse(t, src)
	m, err := Copy(context.Background(), src, dst, Options{NoReflink: true})
	if err != nil {
		t.Fatal(err)
	}
	if m != Sparse {
		t.Fatalf("method %s", m)
	}
	equal(t, src, dst)
	st, _ := os.Stat(dst)
	if st.Size() != diskSize || st.Mode().Perm() != 0o600 {
		t.Fatalf("size %d mode %v", st.Size(), st.Mode())
	}
	// About 3 MiB of data in a 64 MiB file: the copy must not be allocated in
	// full, and not more than the source (APFS allocates in large units).
	if got, source := allocated(t, dst), allocated(t, src); got > source+(1<<20) || got > diskSize/2 {
		t.Fatalf("copy allocates %d bytes (source %d): holes were not kept", got, source)
	}
}

func TestZeroBlocksInsideDataStayHoles(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	// A fully written file of zeros has no holes in the source, but the copy
	// should not allocate it.
	if err := os.WriteFile(src, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), src, dst, Options{NoReflink: true}); err != nil {
		t.Fatal(err)
	}
	equal(t, src, dst)
	if got := allocated(t, dst); got > 1<<20 {
		t.Fatalf("zero data was allocated: %d bytes", got)
	}
}

func TestDefaultCopyFallsBackWithoutReflink(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	writeSparse(t, src)
	m, err := Copy(context.Background(), src, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Whether this filesystem has reflinks depends on the machine; the result
	// must be right either way.
	if m != Reflink && m != Sparse {
		t.Fatalf("method %q", m)
	}
	equal(t, src, dst)
}

func TestCancelMidCopyLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	writeSparse(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	chunks := 0
	chunkHook = func() {
		if chunks++; chunks == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { chunkHook = nil })
	if _, err := Copy(ctx, src, dst, Options{NoReflink: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a cancelled copy left %s behind (%v)", dst, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestExistingDestinationIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	writeSparse(t, src)
	if err := os.WriteFile(dst, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(context.Background(), src, dst, Options{NoReflink: true}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "precious" {
		t.Fatal("existing destination was clobbered or removed")
	}
}

func TestNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Copy(context.Background(), dir, filepath.Join(dir, "x"), Options{}); err == nil {
		t.Fatal("copied a directory")
	}
}
