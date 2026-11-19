package sandboxd

import (
	"strings"
	"testing"
)

func TestParseCPUStat(t *testing.T) {
	in := "cpu  100 10 50 800 20 5 5 10 7 3\ncpu0 1 1 1 1 1 1 1 1 1 1\n"
	busy, total, ok := parseCPUStat(strings.NewReader(in))
	if !ok {
		t.Fatal("not parsed")
	}
	// total = 100+10+50+800+20+5+5+10 (guest and guest_nice are inside user/nice).
	if total != 1000 || busy != 180 {
		t.Fatalf("busy=%d total=%d", busy, total)
	}
	if _, _, ok := parseCPUStat(strings.NewReader("intr 1 2 3\n")); ok {
		t.Fatal("line without cpu accepted")
	}
	if _, _, ok := parseCPUStat(strings.NewReader("cpu  a b c d e\n")); ok {
		t.Fatal("garbage accepted")
	}
	// Old kernels report fewer columns.
	busy, total, ok = parseCPUStat(strings.NewReader("cpu 10 0 10 70 10\n"))
	if !ok || total != 100 || busy != 20 {
		t.Fatalf("short line: %d %d %v", busy, total, ok)
	}
}

func TestParseMeminfo(t *testing.T) {
	total, avail, ok := parseMeminfo(strings.NewReader("MemTotal:  2048 kB\nMemFree: 1 kB\nMemAvailable: 512 kB\n"))
	if !ok || total != 2048*1024 || avail != 512*1024 {
		t.Fatalf("%d %d %v", total, avail, ok)
	}
	if _, _, ok := parseMeminfo(strings.NewReader("MemTotal: 2048 kB\n")); ok {
		t.Fatal("missing MemAvailable accepted")
	}
}
