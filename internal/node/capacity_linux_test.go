//go:build linux

package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemAvailable(t *testing.T) {
	tests := []struct {
		name, input string
		want        uint64
		invalid     bool
	}{
		{"normal", "MemTotal: 999 kB\nMemAvailable: 12345 kB\n", 12345 * 1024, false},
		{"zero", "MemAvailable: 0 kB\n", 0, false},
		{"missing", "MemFree: 999 kB\n", 0, true},
		{"bad number", "MemAvailable: nope kB\n", 0, true},
		{"bad unit", "MemAvailable: 42 MB\n", 0, true},
		{"overflow", "MemAvailable: 18014398509481984 kB\n", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMemAvailable(strings.NewReader(tc.input))
			if (err != nil) != tc.invalid || (!tc.invalid && got != tc.want) {
				t.Fatalf("got %d, %v; want %d invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestMemAvailableFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	calls := 0
	fallback := func() (uint64, error) { calls++; return 321, nil }
	got, err := readLinuxAvailableMemory(path, fallback)
	if err != nil || got != 321 || calls != 1 {
		t.Fatalf("missing file: %d %v calls %d", got, err, calls)
	}
	if err := os.WriteFile(path, []byte("MemAvailable: bad kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readLinuxAvailableMemory(path, fallback)
	if err != nil || got != 321 || calls != 2 {
		t.Fatalf("invalid value: %d %v calls %d", got, err, calls)
	}
	if err := os.WriteFile(path, []byte("MemAvailable: 5 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readLinuxAvailableMemory(path, fallback)
	if err != nil || got != 5120 || calls != 2 {
		t.Fatalf("valid value: %d %v calls %d", got, err, calls)
	}
	sentinel := errors.New("sysinfo unavailable")
	_, err = readLinuxAvailableMemory(filepath.Join(dir, "missing"), func() (uint64, error) { return 0, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
