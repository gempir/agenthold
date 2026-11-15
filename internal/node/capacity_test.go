//go:build linux || darwin

package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gempir/agenthold/internal/sandbox/fake"
)

func TestCapacity(t *testing.T) {
	n := NewLocal(fake.New(t.TempDir()), nil)
	if _, err := n.Capacity(context.Background()); err == nil {
		t.Fatal("unconfigured data directory accepted")
	}
	dir := t.TempDir()
	n.SetDataDir(dir)
	got, err := n.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUs != runtime.NumCPU() || got.MemoryBytes == 0 || got.DiskBytes == 0 {
		t.Fatalf("unexpected capacity: %+v", got)
	}
	n.SetDataDir(filepath.Join(dir, "missing"))
	if _, err := n.Capacity(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.Capacity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}
