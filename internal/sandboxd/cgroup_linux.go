package sandboxd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cgroupRoot is where cgroup v2 is mounted (sandboxd's init mounts it in the microVM).
var cgroupRoot = "/sys/fs/cgroup"

var cgroupSeq atomic.Int64

// harnessCgroup is a cgroup v2 leaf holding one harness and everything it
// starts. cgroup.kill (Linux 5.14) kills all of them at once, including
// helpers that left the harness's process group. A nil *harnessCgroup (no
// cgroup v2, not root, an old kernel) does nothing.
type harnessCgroup struct {
	dir string
	fd  int
}

func newHarnessCgroup(log *slog.Logger) *harnessCgroup {
	if os.Geteuid() != 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return nil // not cgroup v2
	}
	dir := filepath.Join(cgroupRoot, fmt.Sprintf("agenthold-harness-%d-%d", os.Getpid(), cgroupSeq.Add(1)))
	if err := os.Mkdir(dir, 0o755); err != nil {
		log.Warn("harness cgroup", "err", err)
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); err != nil {
		_ = os.Remove(dir)
		return nil // kernel before 5.14
	}
	fd, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(dir)
		return nil
	}
	return &harnessCgroup{dir: dir, fd: fd}
}

func (c *harnessCgroup) path() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// attach makes the process start inside the cgroup (clone3 CLONE_INTO_CGROUP).
func (c *harnessCgroup) attach(a *syscall.SysProcAttr) {
	if c != nil && c.fd >= 0 {
		a.UseCgroupFD, a.CgroupFD = true, c.fd
	}
}

// kill SIGKILLs every process in the cgroup.
func (c *harnessCgroup) kill() {
	if c == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0)
}

// remove kills what is left and deletes the cgroup. Safe to call twice.
func (c *harnessCgroup) remove() {
	if c == nil || c.dir == "" {
		return
	}
	c.kill()
	// The kernel empties the cgroup asynchronously.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, err := os.ReadFile(filepath.Join(c.dir, "cgroup.events"))
		if err != nil || strings.Contains(string(b), "populated 0") {
			break
		}
	}
	_ = os.Remove(c.dir)
	if c.fd >= 0 {
		_ = unix.Close(c.fd)
		c.fd = -1
	}
	c.dir = ""
}
