package sandboxd

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"github.com/gempir/agenthold/internal/tunnel"
)

// stats sums the CPU time of every process in the sandbox except sandboxd
// and supervised services. The server compares successive readings to tell
// idle sandboxes from busy ones (a long build keeps an agent awake; a
// service alone does not).
func stats() tunnel.Stats {
	var st tunnel.Stats
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return st
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || inServiceCgroup(e.Name()) {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// Fields after the command (which may contain spaces) start after ')'.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		// f[0] is state (field 3); utime and stime are fields 14 and 15.
		if len(f) < 13 {
			continue
		}
		ut, _ := strconv.ParseUint(f[11], 10, 64)
		stt, _ := strconv.ParseUint(f[12], 10, 64)
		st.CPUTicks += ut + stt
		st.Processes++
	}
	return st
}

// inServiceCgroup reports whether the process runs in a service's cgroup.
func inServiceCgroup(pid string) bool {
	b, err := os.ReadFile("/proc/" + pid + "/cgroup")
	return err == nil && bytes.Contains(b, []byte("/"+serviceCgroupPrefix))
}
