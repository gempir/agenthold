package sandboxd_test

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/gempir/agenthold/internal/sandboxd"
)

type cpioEntry struct {
	name           string
	mode           uint64
	rmajor, rminor uint64
	data           []byte
}

func parseNewc(t *testing.T, b []byte) []cpioEntry {
	t.Helper()
	var out []cpioEntry
	off := 0
	align := func() { off = (off + 3) &^ 3 }
	for {
		if off+110 > len(b) || string(b[off:off+6]) != "070701" {
			t.Fatalf("bad header at %d", off)
		}
		field := func(i int) uint64 {
			v, err := strconv.ParseUint(string(b[off+6+8*i:off+14+8*i]), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		e := cpioEntry{mode: field(1), rmajor: field(9), rminor: field(10)}
		size, namesize := int(field(6)), int(field(11))
		off += 110
		e.name = string(b[off : off+namesize-1])
		if b[off+namesize-1] != 0 {
			t.Fatalf("name %q not NUL-terminated", e.name)
		}
		off += namesize
		align()
		e.data = b[off : off+size]
		off += size
		align()
		if e.name == "TRAILER!!!" {
			if off != len(b) {
				t.Fatalf("%d trailing bytes", len(b)-off)
			}
			return out
		}
		out = append(out, e)
	}
}

func TestWriteInitramfs(t *testing.T) {
	bin := []byte("\x7fELF pretend sandboxd binary, odd length.")
	var a, b bytes.Buffer
	if err := sandboxd.WriteInitramfs(&a, bytes.NewReader(bin), int64(len(bin))); err != nil {
		t.Fatal(err)
	}
	if err := sandboxd.WriteInitramfs(&b, bytes.NewReader(bin), int64(len(bin))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("initramfs is not deterministic")
	}
	got := map[string]cpioEntry{}
	for _, e := range parseNewc(t, a.Bytes()) {
		got[e.name] = e
	}
	init, ok := got["init"]
	if !ok || init.mode != 0o100755 || !bytes.Equal(init.data, bin) {
		t.Fatalf("init entry %+v", init)
	}
	if c := got["dev/console"]; c.mode != 0o020600 || c.rmajor != 5 || c.rminor != 1 {
		t.Fatalf("console entry %+v", c)
	}
	for _, d := range []string{"dev", "proc", "sys", "run"} {
		if got[d].mode != 0o040755 {
			t.Fatalf("directory %s: %+v", d, got[d])
		}
	}
	if err := sandboxd.WriteInitramfs(&bytes.Buffer{}, bytes.NewReader(bin), int64(len(bin))+1); err == nil {
		t.Fatal("short binary accepted")
	}
}
