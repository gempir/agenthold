package sandboxd

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// parseCPUStat reads the aggregate "cpu" line of /proc/stat: busy is every
// state but idle and iowait (steal included, a stolen tick is a tick the
// guest did not get), total the sum.
func parseCPUStat(r io.Reader) (busy, total uint64, ok bool) {
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [10]uint64
		for i := 1; i < len(f) && i <= len(v); i++ {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return 0, 0, false
			}
			v[i-1] = n
			total += n
		}
		// user nice system idle iowait irq softirq steal guest guest_nice;
		// guest time is already part of user and nice.
		total -= v[8] + v[9]
		busy = total - v[3] - v[4]
		return busy, total, true
	}
	return 0, 0, false
}

// parseMeminfo returns MemTotal and MemAvailable in bytes.
func parseMeminfo(r io.Reader) (total, available uint64, ok bool) {
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	var haveTotal, haveAvail bool
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[2] != "kB" {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || n > 1<<40 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, haveTotal = n*1024, true
		case "MemAvailable:":
			available, haveAvail = n*1024, true
		}
	}
	return total, available, haveTotal && haveAvail
}
