//go:build linux

package firecracker

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func fakeHost(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := hostRoot
	hostRoot = root
	t.Cleanup(func() { hostRoot = old })
}

func TestEnableHugePages(t *testing.T) {
	fakeHost(t, map[string]string{
		thpEnabled:            "always [madvise] never\n",
		thpDefrag:             "always defer defer+madvise [madvise] never\n",
		thpScanSleepMillisecs: "10000\n",
		thpAllocSleepMillis:   "60000\n",
	})
	prev, err := EnableHugePages()
	if err != nil || prev != "madvise" {
		t.Fatalf("EnableHugePages = %q, %v", prev, err)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpEnabled)); string(got) != "always" {
		t.Fatalf("enabled = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpDefrag)); string(got) != "defer+madvise" {
		t.Fatalf("defrag = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpScanSleepMillisecs)); string(got) != "1000" {
		t.Fatalf("scan_sleep_millisecs = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpAllocSleepMillis)); string(got) != "5000" {
		t.Fatalf("alloc_sleep_millisecs = %q", got)
	}
	// Already tuned: defrag=always and smaller sleep intervals are left alone.
	fakeHost(t, map[string]string{
		thpEnabled:            "[always] madvise never\n",
		thpDefrag:             "[always] defer defer+madvise madvise never\n",
		thpScanSleepMillisecs: "500\n",
		thpAllocSleepMillis:   "2000\n",
	})
	if prev, err = EnableHugePages(); err != nil || prev != "always" {
		t.Fatalf("already always: %q, %v", prev, err)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpDefrag)); string(got) != "[always] defer defer+madvise madvise never\n" {
		t.Fatalf("defrag overwritten: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(hostRoot, thpScanSleepMillisecs)); string(got) != "500\n" {
		t.Fatalf("scan_sleep overwritten: %q", got)
	}
}

func TestReadSmapsRollupAndGuestRanges(t *testing.T) {
	fakeHost(t, map[string]string{
		"proc/42/smaps_rollup": "00400000-ffff0000 ---p 00000000 00:00 0 [rollup]\n" +
			"Rss:              742328 kB\n" +
			"Pss:              742312 kB\n" +
			"AnonHugePages:    600064 kB\n",
		"proc/42/maps": "00400000-00800000 r-xp 00000000 08:01 123 /firecracker\n" +
			"7f0000100000-7f0040100000 rw-p 00000000 00:00 0 \n" + // 1 GiB, unaligned by 1 MiB -> inward aligned
			"7f0050000000-7f0050400000 rw-p 00000000 00:00 0 \n" + // 4 MiB, below minBytes
			"7f0060000000-7f00a0000000 rw-s 00000000 00:01 456 /memfile\n", // file-backed, ignored
	})
	rss, huge, err := readSmapsRollupKB(42)
	if err != nil || rss != 742328 || huge != 600064 {
		t.Fatalf("readSmapsRollupKB = (%d, %d, %v)", rss, huge, err)
	}
	ranges, err := findGuestRAMRanges(42, 512<<20)
	if err != nil || len(ranges) != 1 {
		t.Fatalf("findGuestRAMRanges = %+v, %v", ranges, err)
	}
	if ranges[0].start != 0x7f0000200000 || ranges[0].end != 0x7f0040000000 {
		t.Fatalf("range = %#x-%#x", ranges[0].start, ranges[0].end)
	}
}

func TestCandidateCollapseChunks(t *testing.T) {
	dir := t.TempDir()
	const pageSize = 4096
	const pagesPerHuge = hugePageSize / pageSize // 512
	// Three 2 MiB chunks starting at address 0:
	// chunk 0: untouched (not present) -> skipped
	// chunk 1: partially present (4 KiB pages) -> candidate
	// chunk 2: fully present with KPF_THP on PFN 10 -> skipped
	pagemapData := make([]byte, 3*pagesPerHuge*8)
	// Chunk 1: page 0 present with PFN 5 (not THP).
	binary.LittleEndian.PutUint64(pagemapData[(pagesPerHuge)*8:], pagemapPresent|5)
	// Chunk 2: all 512 pages present starting at PFN 10 (marked THP).
	for i := range pagesPerHuge {
		binary.LittleEndian.PutUint64(pagemapData[(2*pagesPerHuge+i)*8:], pagemapPresent|uint64(10+i))
	}
	pmPath := filepath.Join(dir, "pagemap")
	if err := os.WriteFile(pmPath, pagemapData, 0o600); err != nil {
		t.Fatal(err)
	}
	kpfData := make([]byte, 600*8)
	binary.LittleEndian.PutUint64(kpfData[10*8:], kpfTHP)
	kpfPath := filepath.Join(dir, "kpageflags")
	if err := os.WriteFile(kpfPath, kpfData, 0o600); err != nil {
		t.Fatal(err)
	}
	pm, err := os.Open(pmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pm.Close()
	kpf, err := os.Open(kpfPath)
	if err != nil {
		t.Fatal(err)
	}
	defer kpf.Close()

	chunks, err := candidateCollapseChunks(pm, kpf, addrRange{start: 0, end: 3 * hugePageSize}, pageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].start != hugePageSize || chunks[0].end != 2*hugePageSize {
		t.Fatalf("chunks = %+v, want only chunk 1", chunks)
	}
}

func TestNestedVirtualization(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"apple vz", map[string]string{"sys/class/dmi/id/sys_vendor": "Apple Inc.\n", "sys/class/dmi/id/product_name": "Apple Virtualization Generic Platform\n"}, "Apple Inc. Apple Virtualization Generic Platform"},
		{"x86 guest", map[string]string{"proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu vme hypervisor lm\n", "sys/class/dmi/id/sys_vendor": "Hetzner\n"}, "Hetzner"},
		{"bare metal", map[string]string{"proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu vme lm\n", "sys/class/dmi/id/sys_vendor": "Supermicro\n"}, ""},
		{"nothing known", map[string]string{}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeHost(t, c.files)
			if got := NestedVirtualization(); got != c.want {
				t.Fatalf("NestedVirtualization = %q, want %q", got, c.want)
			}
		})
	}
}

func TestVMMLine(t *testing.T) {
	for line, want := range map[string]bool{
		`2026-11-21T11:21:05.773632641 [ah17dfe79d968eb8ecb4533f7d:main] Running Firecracker v1.18.0`:                     true,
		`2026-11-21T11:21:05.792373373 [ah17dfe79d968eb8ecb4533f7d:fc_vcpu 0] Received a VcpuEvent::Resume message`:       true,
		`2026-11-21T10:09:58.398876519 [ah17dfe79d968eb8ecb4533f7d:fc_api] The API server received a Get request on "/".`: true,
		`[    0.000000] PCI: Unknown option ` + "`off'":                                                                   false,
		`time=2026-11-21T11:21:05.570Z level=INFO msg="init started" component=sandboxd-init uptime=0.12s`:                false,
		`{"time":"2026-11-21T11:21:05.833385666Z","level":"INFO","msg":"tunnel connected"}`:                               false,
	} {
		if got := vmmLine(line); got != want {
			t.Errorf("vmmLine(%q) = %v, want %v", line, got, want)
		}
	}
}
