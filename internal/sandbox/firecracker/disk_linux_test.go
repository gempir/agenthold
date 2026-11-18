//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

// The disks are sparse: the node reports the blocks they occupy, not their size.
func TestAllocatedDiskBytes(t *testing.T) {
	d := unitDriver(t)
	got, err := d.AllocatedDiskBytes(t.Context())
	if err != nil || got != 0 {
		t.Fatalf("empty node: %d %v", got, err)
	}
	dir := filepath.Join(d.cfg.DataDir, "agents", "sb1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const apparent = 1 << 30
	for _, name := range diskNames {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(apparent); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(make([]byte, 1<<20), 0); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Other files in the directory are not disks.
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = d.AllocatedDiskBytes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got < 2<<20 || got >= apparent {
		t.Fatalf("allocated = %d for two 1 GiB sparse disks holding 1 MiB each", got)
	}
}
