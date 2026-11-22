//go:build linux

package firecracker

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/sandbox"
)

const (
	hugePageSize = 2 << 20 // 2 MiB THP on arm64 (4 KiB base pages) and amd64
	// hugePageCollapseThresholdKB skips collapsing when a VMM's non-huge RSS
	// is within normal Firecracker overhead (~3-4 MiB).
	hugePageCollapseThresholdKB = 16 * 1024
	// kpfTHP is KPF_THP (bit 22) in Linux's /proc/kpageflags.
	kpfTHP uint64 = 1 << 22
	// pagemapPresent is bit 63 in /proc/<pid>/pagemap entries.
	pagemapPresent uint64 = 1 << 63
	// pagemapPFNMask extracts the 55-bit PFN from a present pagemap entry.
	pagemapPFNMask uint64 = (1 << 55) - 1

	hugePageCheckInterval = 15 * time.Second
)

var hugePageCheckDelays = []time.Duration{1 * time.Second, 3 * time.Second}

type addrRange struct {
	start uint64
	end   uint64
}

// remoteIovec mirrors struct iovec for process_madvise without storing a
// remote process address in a Go pointer type.
type remoteIovec struct {
	base uintptr
	len  uintptr
}

// watchHugePages monitors a running Firecracker VM and collapses any 4 KiB
// fallback pages in its guest RAM VMA back into 2 MiB huge pages using
// process_madvise(MADV_COLLAPSE). Under nested virtualization, even a partial
// 4 KiB fallback (common when host page cache fragments order-9 blocks) slows
// guest workloads like Claude Code by 40x-70x.
//
// It exits automatically once the VMM exits, is replaced, or the host kernel
// does not support MADV_COLLAPSE (< Linux 6.1).
func (d *Driver) watchHugePages(id sandbox.ID, p process, memMB int64, immediate bool) {
	if d.cfg.KeepHugePages || p.PID <= 0 || p.StartTime == "" {
		return
	}
	d.hugeMu.Lock()
	if d.hugeWatching == nil {
		d.hugeWatching = map[sandbox.ID]process{}
	}
	if d.hugeWatching[id] == p {
		d.hugeMu.Unlock()
		return
	}
	d.hugeWatching[id] = p
	d.hugeMu.Unlock()

	go func() {
		defer func() {
			d.hugeMu.Lock()
			if d.hugeWatching[id] == p {
				delete(d.hugeWatching, id)
			}
			d.hugeMu.Unlock()
		}()
		check := func() bool {
			d.hugeMu.Lock()
			current := d.hugeWatching[id] == p
			d.hugeMu.Unlock()
			if !current {
				return false
			}
			t0 := time.Now()
			collapsed, unsupported, err := collapseGuestHugePages(p.PID, p.StartTime, memMB)
			if unsupported || errors.Is(err, os.ErrNotExist) {
				return false
			}
			if collapsed > 0 {
				d.cfg.Log.Info("collapsed guest 4 KiB pages into 2 MiB huge pages",
					"sandbox", id, "collapsed_mb", collapsed*2, "took", time.Since(t0).Round(time.Millisecond))
			}
			return true
		}
		if immediate {
			if !check() {
				return
			}
		}
		for _, wait := range hugePageCheckDelays {
			time.Sleep(wait)
			if !check() {
				return
			}
		}
		t := time.NewTicker(hugePageCheckInterval)
		defer t.Stop()
		for range t.C {
			if !check() {
				return
			}
		}
	}()
}

// collapseGuestHugePages inspects pid's guest RAM VMA and collapses any 2 MiB
// chunks backed by 4 KiB pages into 2 MiB transparent huge pages.
func collapseGuestHugePages(pid int, expectedStart string, memMB int64) (collapsed int, unsupported bool, err error) {
	if !sameProcess(pid, expectedStart) {
		return 0, false, os.ErrNotExist
	}
	rssKB, hugeKB, err := readSmapsRollupKB(pid)
	if err != nil {
		return 0, false, err
	}
	if rssKB-hugeKB < hugePageCollapseThresholdKB {
		return 0, false, nil
	}
	minBytes := uint64(64 << 20)
	if memMB > 0 {
		minBytes = uint64(memMB) << 20
	}
	ranges, err := findGuestRAMRanges(pid, minBytes)
	if err != nil || len(ranges) == 0 {
		return 0, false, err
	}
	pageSize := uint64(os.Getpagesize())
	if pageSize == 0 || hugePageSize%pageSize != 0 {
		return 0, true, nil
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return 0, false, os.ErrNotExist
		}
		if errors.Is(err, unix.ENOSYS) {
			return 0, true, nil
		}
		return 0, false, err
	}
	defer unix.Close(pidfd)
	// Re-check process identity after opening pidfd to close any PID-reuse race.
	if !sameProcess(pid, expectedStart) {
		return 0, false, os.ErrNotExist
	}

	pagemap, err := os.Open(filepath.Join(hostRoot, "proc", strconv.Itoa(pid), "pagemap"))
	if err != nil {
		return 0, false, err
	}
	defer pagemap.Close()
	kpageflags, _ := os.Open(filepath.Join(hostRoot, "proc", "kpageflags"))
	if kpageflags != nil {
		defer kpageflags.Close()
	}

	for _, r := range ranges {
		chunks, cerr := candidateCollapseChunks(pagemap, kpageflags, r, pageSize)
		if cerr != nil {
			return collapsed, false, cerr
		}
		for _, c := range chunks {
			err := processMadviseCollapse(pidfd, c.start, c.end-c.start)
			if err == nil {
				collapsed++
				continue
			}
			if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EPERM) {
				return collapsed, true, nil
			}
			if errors.Is(err, unix.ESRCH) {
				return collapsed, false, os.ErrNotExist
			}
		}
	}
	return collapsed, false, nil
}

func sameProcess(pid int, expectedStart string) bool {
	start, state, err := procIdentityAt(hostRoot, pid)
	return err == nil && start == expectedStart && state != "Z" && state != "X"
}

func procIdentityAt(root string, pid int) (start, state string, err error) {
	data, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", "", err
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return "", "", errors.New("invalid process stat")
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 20 {
		return "", "", errors.New("short process stat")
	}
	return f[19], f[0], nil
}

func readSmapsRollupKB(pid int) (rssKB, hugeKB int64, err error) {
	f, err := os.Open(filepath.Join(hostRoot, "proc", strconv.Itoa(pid), "smaps_rollup"))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Rss:"):
			rssKB = parseKBField(line)
		case strings.HasPrefix(line, "AnonHugePages:"):
			hugeKB = parseKBField(line)
		}
	}
	return rssKB, hugeKB, sc.Err()
}

func parseKBField(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[1], 10, 64)
	return v
}

// findGuestRAMRanges returns 2 MiB-aligned private anonymous writable VMAs of
// at least minBytes bytes (Firecracker's guest RAM mappings).
func findGuestRAMRanges(pid int, minBytes uint64) ([]addrRange, error) {
	f, err := os.Open(filepath.Join(hostRoot, "proc", strconv.Itoa(pid), "maps"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []addrRange
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Anonymous mapping lines have 5 fields (no pathname):
		// start-end perms offset dev inode
		if len(fields) != 5 || fields[1] != "rw-p" || fields[4] != "0" {
			continue
		}
		aStr, bStr, ok := strings.Cut(fields[0], "-")
		if !ok {
			continue
		}
		start, err1 := strconv.ParseUint(aStr, 16, 64)
		end, err2 := strconv.ParseUint(bStr, 16, 64)
		if err1 != nil || err2 != nil || end <= start || end-start < minBytes {
			continue
		}
		// Align inward to 2 MiB boundaries so MADV_COLLAPSE gets exact PMDs.
		alignedStart := (start + hugePageSize - 1) &^ (hugePageSize - 1)
		alignedEnd := end &^ (hugePageSize - 1)
		if alignedEnd > alignedStart {
			out = append(out, addrRange{start: alignedStart, end: alignedEnd})
		}
	}
	return out, sc.Err()
}

// candidateCollapseChunks scans r in 2 MiB chunks via pagemap (and kpageflags
// when available) and returns only chunks that have present pages not already
// backed by a 2 MiB THP.
func candidateCollapseChunks(pagemap, kpageflags *os.File, r addrRange, pageSize uint64) ([]addrRange, error) {
	pagesPerHuge := hugePageSize / pageSize
	buf := make([]byte, pagesPerHuge*8)
	var kpfBuf [8]byte
	var out []addrRange
	for addr := r.start; addr+hugePageSize <= r.end; addr += hugePageSize {
		off := int64((addr / pageSize) * 8)
		n, err := pagemap.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return out, err
		}
		if n < 8 {
			continue
		}
		var firstPresent uint64
		presentCount := 0
		for i := 0; i+8 <= n; i += 8 {
			entry := binary.LittleEndian.Uint64(buf[i : i+8])
			if entry&pagemapPresent != 0 {
				if presentCount == 0 {
					firstPresent = entry
				}
				presentCount++
			}
		}
		if presentCount == 0 {
			continue
		}
		// If all pages in the chunk are present and the first page already has
		// KPF_THP, the PMD is already a 2 MiB huge page: skip the syscall.
		if kpageflags != nil && uint64(presentCount) == pagesPerHuge {
			pfn := firstPresent & pagemapPFNMask
			if pfn != 0 {
				if kn, kerr := kpageflags.ReadAt(kpfBuf[:], int64(pfn*8)); kerr == nil && kn == 8 {
					flags := binary.LittleEndian.Uint64(kpfBuf[:])
					if flags&kpfTHP != 0 {
						continue
					}
				}
			}
		}
		out = append(out, addrRange{start: addr, end: addr + hugePageSize})
	}
	return out, nil
}

func processMadviseCollapse(pidfd int, addr, length uint64) error {
	iov := remoteIovec{base: uintptr(addr), len: uintptr(length)}
	_, _, errno := unix.Syscall6(
		unix.SYS_PROCESS_MADVISE,
		uintptr(pidfd),
		uintptr(unsafe.Pointer(&iov)),
		1,
		uintptr(unix.MADV_COLLAPSE),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
