//go:build linux

package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// parseMemAvailable reads only the kernel's MemAvailable estimate, in bytes.
func parseMemAvailable(r io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "MemAvailable:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("invalid MemAvailable entry")
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kb > math.MaxUint64/1024 {
			return 0, errors.New("invalid MemAvailable value")
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("MemAvailable not found")
}

// parseMemTotal reads only MemTotal, in bytes.
func parseMemTotal(r io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "MemTotal:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, errors.New("invalid MemTotal entry")
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kb > math.MaxUint64/1024 {
			return 0, errors.New("invalid MemTotal value")
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("MemTotal not found")
}

// linuxTotalMemory is best effort: the overview shows no memory bar without it.
func linuxTotalMemory() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	total, err := parseMemTotal(f)
	if err != nil {
		return 0
	}
	return total
}

func linuxAvailableMemory() (uint64, error) {
	return readLinuxAvailableMemory("/proc/meminfo", linuxSysinfoMemory)
}

func readLinuxAvailableMemory(path string, fallback func() (uint64, error)) (uint64, error) {
	f, err := os.Open(path)
	if err == nil {
		var memory uint64
		memory, err = parseMemAvailable(f)
		closeErr := f.Close()
		if err == nil && closeErr == nil {
			return memory, nil
		}
		if err == nil {
			err = closeErr
		}
	}
	memory, sysErr := fallback()
	if sysErr != nil {
		return 0, fmt.Errorf("read available memory: %w", errors.Join(err, sysErr))
	}
	return memory, nil
}

func linuxSysinfoMemory() (uint64, error) {
	// Older kernels may lack MemAvailable. Free RAM plus buffers is a
	// conservative fallback; unlike MemAvailable it does not include page cache.
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0, err
	}
	if info.Unit == 0 || info.Freeram > math.MaxUint64-info.Bufferram {
		return 0, errors.New("invalid sysinfo memory values")
	}
	free := info.Freeram + info.Bufferram
	if free > math.MaxUint64/uint64(info.Unit) {
		return 0, errors.New("sysinfo available memory overflows")
	}
	return free * uint64(info.Unit), nil
}

func hostCapacity(ctx context.Context, dir string) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return Capacity{}, fmt.Errorf("stat node data directory %q: %w", dir, err)
	}
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	unit := fs.Frsize
	if unit <= 0 {
		unit = fs.Bsize
	}
	if unit <= 0 || fs.Bavail > math.MaxUint64/uint64(unit) {
		return Capacity{}, errors.New("invalid filesystem capacity")
	}
	memory, err := linuxAvailableMemory()
	if err != nil {
		return Capacity{}, err
	}
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	return Capacity{CPUs: runtime.NumCPU(), MemoryBytes: memory, DiskBytes: fs.Bavail * uint64(unit), MemoryTotalBytes: linuxTotalMemory()}, nil
}
