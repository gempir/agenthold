//go:build darwin

package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// parseVMStat estimates reclaimable memory as free, inactive and speculative
// pages. macOS does not expose the Linux MemAvailable metric.
func parseVMStat(r io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	if !scanner.Scan() {
		return 0, errors.New("missing vm_stat header")
	}
	var size uint64
	words := strings.Fields(scanner.Text())
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "of" {
			size, _ = strconv.ParseUint(words[i+1], 10, 64)
			break
		}
	}
	if size == 0 {
		return 0, errors.New("invalid vm_stat page size")
	}
	pages := map[string]uint64{}
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name != "Pages free" && name != "Pages inactive" && name != "Pages speculative" {
			continue
		}
		value := strings.TrimSuffix(strings.TrimSpace(parts[1]), ".")
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", name, err)
		}
		pages[name] = v
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	var total uint64
	for _, name := range []string{"Pages free", "Pages inactive", "Pages speculative"} {
		v, ok := pages[name]
		if !ok {
			return 0, fmt.Errorf("%s missing from vm_stat", name)
		}
		if total > math.MaxUint64-v {
			return 0, errors.New("vm_stat page count overflows")
		}
		total += v
	}
	if total > math.MaxUint64/size {
		return 0, errors.New("vm_stat memory overflows")
	}
	return total * size, nil
}

func hostCapacity(ctx context.Context, dir string) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return Capacity{}, fmt.Errorf("stat node data directory %q: %w", dir, err)
	}
	if fs.Bsize == 0 || fs.Bavail > math.MaxUint64/uint64(fs.Bsize) {
		return Capacity{}, errors.New("invalid filesystem capacity")
	}
	output, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		if ctx.Err() != nil {
			return Capacity{}, ctx.Err()
		}
		return Capacity{}, fmt.Errorf("run vm_stat: %w", err)
	}
	memory, err := parseVMStat(strings.NewReader(string(output)))
	if err != nil {
		return Capacity{}, fmt.Errorf("read available memory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	return Capacity{CPUs: runtime.NumCPU(), MemoryBytes: memory, DiskBytes: fs.Bavail * uint64(fs.Bsize)}, nil
}
