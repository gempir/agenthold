package sandboxd_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

func fastServices(t *testing.T) {
	t.Helper()
	t.Cleanup(sandboxd.SetServiceTiming(50*time.Millisecond, 400*time.Millisecond, time.Hour, time.Second))
}

func status(t *testing.T, s *tunnel.Session) map[string]tunnel.ServiceState {
	t.Helper()
	sts, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStatus})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]tunnel.ServiceState{}
	for _, st := range sts {
		out[st.Name] = st
	}
	return out
}

// waitFor polls cond until it holds (5 s).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func TestServicesStartFromBootConfig(t *testing.T) {
	fastServices(t)
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{
			{Name: "web", Command: "echo hello from $GREETING; exec sleep 300", Env: map[string]string{"GREETING": "env"}, Enabled: true},
			{Name: "off", Command: "sleep 300", Enabled: false},
		}
	})
	var st map[string]tunnel.ServiceState
	waitFor(t, "web running", func() bool { st = status(t, s); return st["web"].State == tunnel.ServiceRunning })
	if !alive(st["web"].Pid) || st["off"].State != tunnel.ServiceStopped || st["web"].StartedAt == 0 {
		t.Fatalf("status = %+v", st)
	}
	waitFor(t, "log line", func() bool {
		b, _ := os.ReadFile(filepath.Join(home, sandboxd.ServiceLogDir, "web", "log"))
		return bytes.Contains(b, []byte("hello from env"))
	})
	var buf bytes.Buffer
	if err := s.ServiceLogs(t.Context(), "web", 1, false, &buf, 0); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "hello from env" {
		t.Fatalf("last line = %q", buf.String())
	}
}

func TestServiceRestartsWithBackoff(t *testing.T) {
	fastServices(t)
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{{Name: "crash", Command: "echo run >> runs; exit 3", Enabled: true}}
	})
	waitFor(t, "several restarts", func() bool { return status(t, s)["crash"].Restarts >= 3 })
	st := status(t, s)["crash"]
	if st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("status = %+v", st)
	}
	if st.State != tunnel.ServiceBackoff && st.State != tunnel.ServiceRunning {
		t.Fatalf("state = %s", st.State)
	}
	b, _ := os.ReadFile(filepath.Join(home, "runs"))
	if n := strings.Count(string(b), "run"); n < 3 {
		t.Fatalf("ran %d times", n)
	}
	var logs bytes.Buffer
	_ = s.ServiceLogs(t.Context(), "crash", 50, false, &logs, 0)
	if !strings.Contains(logs.String(), "exited (code 3); restarting in") {
		t.Fatalf("logs do not explain the restarts:\n%s", logs.String())
	}
}

func TestServiceStopKillsChildren(t *testing.T) {
	fastServices(t)
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{{Name: "tree", Command: "sleep 300 & echo $! > child.pid; wait", Enabled: true}}
	})
	pidFile := filepath.Join(home, "child.pid")
	var child int
	waitFor(t, "child pid", func() bool {
		b, err := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	if !alive(child) {
		t.Fatal("child not running")
	}
	sts, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStop, Name: "tree"})
	if err != nil || len(sts) != 1 || sts[0].State != tunnel.ServiceStopped {
		t.Fatalf("stop = %+v, %v", sts, err)
	}
	waitFor(t, "child gone", func() bool { return !alive(child) })

	// Start brings it back.
	_ = os.Remove(pidFile)
	if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStart, Name: "tree"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool { return status(t, s)["tree"].State == tunnel.ServiceRunning })
}

func TestServiceKilledProcessIsRestarted(t *testing.T) {
	fastServices(t)
	s, _ := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{{Name: "web", Command: "exec sleep 300", Enabled: true}}
	})
	var first tunnel.ServiceState
	waitFor(t, "running", func() bool { first = status(t, s)["web"]; return first.State == tunnel.ServiceRunning })
	if err := syscall.Kill(first.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var again tunnel.ServiceState
	waitFor(t, "restarted", func() bool {
		again = status(t, s)["web"]
		return again.State == tunnel.ServiceRunning && again.Pid != first.Pid
	})
	if again.Restarts != 1 || again.Signal != "killed" {
		t.Fatalf("status = %+v", again)
	}
}

func TestServiceApplyDiffs(t *testing.T) {
	fastServices(t)
	s, _ := setupWith(t, nil)
	apply := func(specs ...tunnel.ServiceSpec) map[string]tunnel.ServiceState {
		t.Helper()
		if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: specs}); err != nil {
			t.Fatal(err)
		}
		var st map[string]tunnel.ServiceState
		waitFor(t, "all enabled running", func() bool {
			st = status(t, s)
			for _, sp := range specs {
				if sp.Enabled && st[sp.Name].State != tunnel.ServiceRunning {
					return false
				}
			}
			return true
		})
		return st
	}
	a := tunnel.ServiceSpec{Name: "a", Command: "exec sleep 300", Enabled: true}
	b := tunnel.ServiceSpec{Name: "b", Command: "exec sleep 300", Enabled: true}
	st1 := apply(a, b)
	b.Command = "exec sleep 301"
	c := tunnel.ServiceSpec{Name: "c", Command: "exec sleep 300", Enabled: true}
	st2 := apply(a, b, c)
	if st2["a"].Pid != st1["a"].Pid {
		t.Fatal("an unchanged service was restarted")
	}
	if st2["b"].Pid == st1["b"].Pid {
		t.Fatal("a changed service kept running its old command")
	}
	st3 := apply(a)
	if len(st3) != 1 || st3["a"].Pid != st1["a"].Pid {
		t.Fatalf("after removing b and c: %+v", st3)
	}
	waitFor(t, "removed services ended", func() bool { return !alive(st2["b"].Pid) && !alive(st2["c"].Pid) })
	// Disabling stops it but keeps it listed.
	a.Enabled = false
	st4 := apply(a)
	if st4["a"].State != tunnel.ServiceStopped {
		t.Fatalf("disabled: %+v", st4)
	}
	if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: []tunnel.ServiceSpec{{Name: "Bad Name", Command: "x"}}}); err == nil {
		t.Fatal("an invalid name was accepted")
	}
}

func TestServiceSavesDesiredState(t *testing.T) {
	fastServices(t)
	file := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(file, []byte(`[{"name":"web","command":"exec sleep 300","enabled":true}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := setupWith(t, func(c *sandboxd.Config) { c.ServicesFile = file })
	waitFor(t, "boot service from file", func() bool { return status(t, s)["web"].State == tunnel.ServiceRunning })
	if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStop, Name: "web"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	specs, err := sandboxd.ParseServices(b)
	if err != nil || len(specs) != 1 || specs[0].Enabled {
		t.Fatalf("saved %s (%v)", b, err)
	}
}

func TestServiceLogRotationAndFollow(t *testing.T) {
	fastServices(t)
	t.Cleanup(sandboxd.SetServiceLogMax(4096))
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{{Name: "chatty", Command: "i=0; while true; do i=$((i+1)); echo line $i; sleep 0.002; done", Enabled: true}}
	})
	dir := filepath.Join(home, sandboxd.ServiceLogDir, "chatty")
	waitFor(t, "rotation", func() bool {
		_, err := os.Stat(filepath.Join(dir, "log.1"))
		return err == nil
	})
	for _, f := range []string{"log", "log.1"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Size() > 4096 {
			t.Fatalf("%s: %v %v", f, fi.Size(), err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	w := &lineCounter{}
	done := make(chan error, 1)
	go func() { done <- s.ServiceLogs(ctx, "chatty", 2, true, w, 0) }()
	waitFor(t, "followed lines", func() bool { return w.lines() > 20 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type lineCounter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lineCounter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lineCounter) lines() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Count(l.buf.Bytes(), []byte{'\n'})
}

func TestServiceMissingWorkdirFails(t *testing.T) {
	fastServices(t)
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = []tunnel.ServiceSpec{{Name: "app", Command: "exec sleep 300", Workdir: "app/current", Enabled: true}}
	})
	waitFor(t, "failed", func() bool { return status(t, s)["app"].State == tunnel.ServiceFailed })
	// Once the directory exists, the next retry starts it.
	if err := os.MkdirAll(filepath.Join(home, "app", "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { return status(t, s)["app"].State == tunnel.ServiceRunning })
}

func TestTarUntarRoundTrip(t *testing.T) {
	s, home := setup(t)
	src := filepath.Join(home, "src")
	for path, body := range map[string]string{"index.html": "<h1>hi</h1>", "lib/app.py": "print(1)", ".git/HEAD": "ref", "node_modules/x/i.js": "1"} {
		p := filepath.Join(src, path)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(filepath.Join(src, "lib/app.py"), 0o755)
	if err := os.Symlink("lib/app.py", filepath.Join(src, "run")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := s.Tar(t.Context(), "src", []string{".git"}, 0, &archive); err != nil {
		t.Fatal(err)
	}
	if err := s.Untar(t.Context(), "dst/1", 0, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(home, "dst/1")
	if b, _ := os.ReadFile(filepath.Join(dst, "index.html")); string(b) != "<h1>hi</h1>" {
		t.Fatalf("index.html = %q", b)
	}
	if fi, err := os.Stat(filepath.Join(dst, "lib/app.py")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode not kept: %v %v", fi, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "run")); err != nil || l != "lib/app.py" {
		t.Fatalf("symlink = %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Fatal(".git was not excluded")
	}
	if _, err := os.Stat(filepath.Join(dst, "node_modules/x/i.js")); err != nil {
		t.Fatal("node_modules must be kept by default")
	}
	// Into a directory that is not empty: refused.
	if err := s.Untar(t.Context(), "dst/1", 0, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("extracted over existing files")
	}
	// Over the limit: refused.
	var small bytes.Buffer
	if err := s.Tar(t.Context(), "src", nil, 4, &small); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("limit: %v", err)
	}
	// Symlink swap.
	if err := s.Symlink(t.Context(), "current", "dst/1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Symlink(t.Context(), "current", "dst/2"); err != nil {
		t.Fatal(err)
	}
	if l, _ := os.Readlink(filepath.Join(home, "current")); l != "dst/2" {
		t.Fatalf("current -> %q", l)
	}
}

func TestExtractTarRefusesEscapes(t *testing.T) {
	cases := map[string][]tar.Header{
		"dotdot":   {{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644}},
		"absolute": {{Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644}},
		"through symlink": {
			{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"},
			{Name: "link/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		},
		"hardlink": {{Name: "h", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}},
		"device":   {{Name: "d", Typeflag: tar.TypeChar}},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, h := range hdrs {
				if err := tw.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
			}
			_ = tw.Close()
			dir := filepath.Join(t.TempDir(), "x")
			if err := sandboxd.ExtractTar(&buf, dir, 0, func(*os.Root, string) {}); err == nil {
				t.Fatal("extracted")
			}
		})
	}
	// setuid is dropped.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "s", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	dir := filepath.Join(t.TempDir(), "x")
	if err := sandboxd.ExtractTar(&buf, dir, 0, func(*os.Root, string) {}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "s")); fi.Mode()&os.ModeSetuid != 0 {
		t.Fatal("setuid kept")
	}
}

func TestExec(t *testing.T) {
	s, home := setup(t)
	r, err := s.Exec(t.Context(), []string{"/bin/sh", "-c", "echo out; echo err >&2; pwd; exit 7"}, "", nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(home)
	if r.ExitCode != 7 || !strings.Contains(r.Output, "out\nerr\n") || !strings.Contains(r.Output, real) && !strings.Contains(r.Output, home) {
		t.Fatalf("reply = %+v", r)
	}
	r, err = s.Exec(t.Context(), []string{"/bin/sh", "-c", "sleep 5"}, "", nil, 1, 0)
	if err != nil || !r.TimedOut {
		t.Fatalf("timeout: %+v %v", r, err)
	}
	r, err = s.Exec(t.Context(), []string{"/bin/sh", "-c", "yes | head -c 100000"}, "", nil, 10, 1000)
	if err != nil || !r.Truncated || len(r.Output) != 1000 || r.ExitCode != 0 {
		t.Fatalf("truncated: %d %+v %v", len(r.Output), r.Truncated, err)
	}
}

// Concurrent changes must not deadlock the supervisor (apply takes the
// supervisor's lock, then a service's; launch must not take them the other
// way round).
func TestServiceConcurrentApplies(t *testing.T) {
	fastServices(t)
	s, _ := setupWith(t, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 15 {
					spec := tunnel.ServiceSpec{Name: "x", Command: "exec sleep 30" + strconv.Itoa((g+i)%3), Enabled: (g+i)%4 != 0}
					other := tunnel.ServiceSpec{Name: "y", Command: "exec sleep 300", Enabled: i%2 == 0}
					_, _ = s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: []tunnel.ServiceSpec{spec, other}})
					_, _ = s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceRestart, Name: "y"})
					_, _ = s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStatus})
				}
			})
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent applies deadlocked the supervisor")
	}
	if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceStatus}); err != nil {
		t.Fatal(err)
	}
}

// sandboxd writes as root: symlinks the agent places in its home must not
// redirect untar, the symlink swap or service logs outside the home.
func TestRootWritesStayInHome(t *testing.T) {
	fastServices(t)
	outside := t.TempDir()
	s, home := setupWith(t, func(c *sandboxd.Config) {
		c.Services = nil
	})
	if err := os.Symlink(outside, filepath.Join(home, "app")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	_ = tw.WriteHeader(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if err := s.Untar(t.Context(), "app/releases/1", 0, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("untar followed a symlink out of the home")
	}
	if err := s.Untar(t.Context(), "/tmp/elsewhere", 0, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("untar outside the home")
	}
	if err := s.Symlink(t.Context(), "app/current", "releases/1"); err == nil {
		t.Fatal("symlink swap followed a symlink out of the home")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the home: %v", entries)
	}

	// A log path that is a symlink out of the home is not written.
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(home, sandboxd.ServiceLogDir, "web")
	_ = os.MkdirAll(logDir, 0o755)
	if err := os.Symlink(victim, filepath.Join(logDir, "log")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Services(t.Context(), tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: []tunnel.ServiceSpec{{Name: "web", Command: "echo leak; exec sleep 300", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	waitFor(t, "output in the in-memory tail", func() bool {
		logs.Reset()
		_ = s.ServiceLogs(t.Context(), "web", 5, false, &logs, 0)
		return strings.Contains(logs.String(), "leak")
	})
	if b, _ := os.ReadFile(victim); len(b) != 0 {
		t.Fatalf("the log followed a symlink out of the home: %q", b)
	}
}

func TestExecOutputFitsAFrame(t *testing.T) {
	s, _ := setup(t)
	// Control characters escape to six bytes each in JSON.
	r, err := s.Exec(t.Context(), []string{"/bin/sh", "-c", "head -c 2000000 /dev/zero"}, "", nil, 10, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || len(r.Output) >= tunnel.MaxFrame/6 {
		t.Fatalf("output %d bytes, truncated %v", len(r.Output), r.Truncated)
	}
}
