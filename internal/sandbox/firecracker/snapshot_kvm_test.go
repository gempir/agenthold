//go:build linux && kvm

package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

const counterPy = `import http.server, itertools
c = itertools.count(1)
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(s):
        b = str(next(c)).encode()
        s.send_response(200); s.send_header("Content-Length", str(len(b))); s.end_headers(); s.wfile.write(b)
    def log_message(*a): pass
http.server.HTTPServer(("127.0.0.1", 18091), H).serve_forever()
`

// wakeLog records the driver's resume outcomes, so that a resume falling back
// to a cold boot fails the test instead of only making a wake slower.
type wakeLog struct {
	mu     sync.Mutex
	events []string // "resumed" or "cold: <reason>"
}

// take returns the outcomes recorded since the last call.
func (w *wakeLog) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.events
	w.events = nil
	return e
}

type wakeHandler struct {
	slog.Handler
	log *wakeLog
}

func (h wakeHandler) Handle(ctx context.Context, r slog.Record) error {
	event := ""
	switch r.Message {
	case "resumed from snapshot":
		event = "resumed"
	case "cannot resume from snapshot; cold booting":
		event = "cold:"
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "err" {
				event += " " + a.Value.String()
			}
			return true
		})
	}
	if event != "" {
		h.log.mu.Lock()
		h.log.events = append(h.log.events, event)
		h.log.mu.Unlock()
	}
	return h.Handler.Handle(ctx, r)
}

func (h wakeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return wakeHandler{h.Handler.WithAttrs(as), h.log}
}
func (h wakeHandler) WithGroup(name string) slog.Handler {
	return wakeHandler{h.Handler.WithGroup(name), h.log}
}

// TestSnapshotResumeKVM sleeps a 1 GiB machine with a memory snapshot and
// wakes it: a service with in-memory state (a counter) answers with its
// state, the same process, a file in /dev/shm survives and the guest clock is
// the host's. Then it breaks the snapshot in three ways (recorded Firecracker
// version, vmstate content, missing memory file) and checks each wake is a
// clean cold boot. AGENTHOLD_KVM_SNAPSHOT_BALLOON=0 runs it without the
// balloon (for measurements). Same artifacts and isolation as
// TestJailedLifecycle.
func TestSnapshotResumeKVM(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER", "SANDBOXD"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER/SANDBOXD to run the snapshot VM test")
		}
	}
	if os.Geteuid() != 0 {
		if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
			t.Fatal("required microVM test needs root")
		}
		t.Skip("microVM test requires root")
	}
	if os.Getenv("AGENTHOLD_KVM_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestSnapshotResumeKVM$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated snapshot VM test: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err = netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "ahsnap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var tokenMu sync.Mutex
	token := "kvm-snap-token-0"
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		tokenMu.Lock()
		defer tokenMu.Unlock()
		other := strings.HasSuffix(agent, "_b") && got == "kvm-snap-other"
		if !strings.HasPrefix(agent, "ag_kvmsnap_") || got != token && !other {
			return errors.New("unexpected guest identity")
		}
		return nil
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", hub)
	listener, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	balloon := os.Getenv("AGENTHOLD_KVM_SNAPSHOT_BALLOON") != "0"
	memMiB := int64(1024)
	if v := os.Getenv("AGENTHOLD_KVM_SNAPSHOT_MIB"); v != "" {
		if memMiB, err = strconv.ParseInt(v, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	outcomes := &wakeLog{}
	cfg := Config{
		DataDir: dir, JailDir: filepath.Join(dir, "j"), BaseImagePath: os.Getenv("AGENTHOLD_KVM_BASE"),
		KernelPath: os.Getenv("AGENTHOLD_KVM_KERNEL"), FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"),
		JailerPath: os.Getenv("AGENTHOLD_KVM_JAILER"), SandboxdPath: os.Getenv("AGENTHOLD_KVM_SANDBOXD"), NetworkPool: netip.MustParsePrefix("10.216.0.0/24"),
		OverlaySizeBytes: 256 << 20, HomeSizeBytes: 256 << 20, StopTimeout: 10 * time.Second,
		Snapshots: true, Balloon: balloon, SnapshotReserveBytes: 1 << 30,
		Log: slog.New(wakeHandler{slog.NewTextHandler(os.Stderr, nil), outcomes}),
	}
	firewall := &egress.Firewall{Pool: cfg.NetworkPool, ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{port}}
	if err = firewall.Install(); err != nil {
		t.Fatal(err)
	}
	cfg.RegisterNetwork = firewall.Register
	cfg.UnregisterNetwork = firewall.Unregister
	session := func(id sandbox.ID) (*tunnel.Session, error) {
		s, ok := hub.Session(string(id))
		if !ok {
			return nil, tunnel.ErrNotConnected
		}
		return s, nil
	}
	cfg.Shutdown = func(ctx context.Context, id sandbox.ID) error {
		s, err := session(id)
		if err != nil {
			return err
		}
		return s.Shutdown(ctx)
	}
	var slowSuspend atomic.Bool
	cfg.Suspend = func(ctx context.Context, id sandbox.ID) error {
		if slowSuspend.Swap(false) {
			time.Sleep(4 * time.Second) // a slow guest flush
		}
		s, err := session(id)
		if err != nil {
			return err
		}
		return s.Suspend(ctx)
	}
	cfg.WaitConnected = func(ctx context.Context, id sandbox.ID) error { return hub.WaitConnected(ctx, string(id)) }
	d, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := sandbox.Spec{AgentID: "ag_kvmsnap_" + strconv.Itoa(os.Getpid()), Resources: sandbox.Resources{CPUs: 1, MemoryMB: memMiB}}
	id, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(c, id, sandbox.DestroyOpts{}); err != nil {
			t.Error("cleanup:", err)
		}
	})
	wakes := 0
	// start boots or resumes with a new token each time, as the manager does.
	start := func() (*tunnel.Session, time.Duration) {
		t.Helper()
		wakes++
		tokenMu.Lock()
		token = fmt.Sprintf("kvm-snap-token-%d", wakes)
		tok := token
		tokenMu.Unlock()
		began := time.Now()
		if err := d.Start(ctx, id, sandbox.Boot{AgentID: spec.AgentID, Token: tok, TunnelPort: port}); err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("start: %v\n%s", err, log)
		}
		s, err := hub.Wait(ctx, spec.AgentID)
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("wait tunnel: %v\n%s", err, log)
		}
		return s, time.Since(began)
	}
	sleep := func() time.Duration {
		t.Helper()
		began := time.Now()
		if err := d.Stop(ctx, id); err != nil {
			t.Fatal("stop:", err)
		}
		took := time.Since(began)
		hub.Disconnect(spec.AgentID) // the manager drops the session after a stop
		return took
	}
	execIn := func(s *tunnel.Session, command string) string {
		t.Helper()
		r, err := s.Exec(ctx, []string{"/bin/sh", "-lc", command}, "", nil, 60, 0)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("%s: %v %+v", command, err, r)
		}
		return strings.TrimSpace(r.Output)
	}
	count := func(s *tunnel.Session) int {
		t.Helper()
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return s.Dial(ctx, 18091)
		}}}
		deadline := time.Now().Add(60 * time.Second)
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://counter/", nil)
			res, err := client.Do(req)
			if err == nil {
				body, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				n, err := strconv.Atoi(strings.TrimSpace(string(body)))
				if err == nil {
					return n
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("counter unreachable: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	servicePid := func(s *tunnel.Session) int {
		t.Helper()
		sts, err := s.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceStatus})
		if err != nil || len(sts) != 1 || sts[0].State != tunnel.ServiceRunning {
			t.Fatalf("service status: %+v %v", sts, err)
		}
		return sts[0].Pid
	}
	clockSkew := func(s *tunnel.Session) time.Duration {
		t.Helper()
		before := time.Now()
		out := execIn(s, "date +%s.%N")
		after := time.Now()
		f, err := strconv.ParseFloat(out, 64)
		if err != nil {
			t.Fatalf("guest date %q", out)
		}
		guest := time.Unix(0, int64(f*1e9))
		mid := before.Add(after.Sub(before) / 2)
		return guest.Sub(mid)
	}
	snapFile := func(name string) string { return filepath.Join(d.snapDir(id), name) }
	// wokeBy checks how the last start woke: "resumed", "cold boot" (no
	// snapshot to resume) or a fallback whose reason contains a given text.
	wokeBy := func(what, want string) {
		t.Helper()
		got := outcomes.take()
		switch {
		case want == "cold boot" && len(got) == 0:
		case want == "resumed" && len(got) == 1 && got[0] == "resumed":
		case want != "resumed" && want != "cold boot" && len(got) == 1 && strings.HasPrefix(got[0], "cold: ") && strings.Contains(got[0], want):
		default:
			t.Fatalf("%s: woke by %q, want %q", what, got, want)
		}
	}

	s, coldBoot := start()
	wokeBy("first start", "cold boot")
	execIn(s, "cat > ~/counter.py <<'EOF'\n"+counterPy+"EOF\necho kept > /dev/shm/marker")
	specs := []tunnel.ServiceSpec{{Name: "counter", Command: "exec python3 -u ~/counter.py", Enabled: true}}
	if _, err := s.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: specs}); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		if got := count(s); got != want {
			t.Fatalf("counter = %d, want %d", got, want)
		}
	}
	pid := servicePid(s)
	// Dirty memory the guest then frees (Linux does not zero freed pages):
	// what the balloon is for.
	execIn(s, `python3 -c "b = b'\x01' * (400 << 20)"`)

	// Sleep with the memory kept, and wake. The sleep is slowed down: another
	// machine boots meanwhile, which the sleep must not hold up.
	other := sandbox.Spec{AgentID: spec.AgentID + "_b", Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512}}
	otherID, err := d.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Destroy(c, otherID, sandbox.DestroyOpts{})
	})
	slowSuspend.Store(true)
	otherDone := make(chan time.Time, 1)
	go func() {
		time.Sleep(500 * time.Millisecond) // the sleep has begun
		if st, err := d.Status(ctx, id); err != nil || st.State != sandbox.StateRunning {
			t.Errorf("status during the sleep: %+v %v", st, err)
		}
		if err := d.Start(ctx, otherID, sandbox.Boot{AgentID: other.AgentID, Token: "kvm-snap-other", TunnelPort: port}); err != nil {
			t.Errorf("start of another machine during a sleep: %v", err)
		}
		otherDone <- time.Now()
	}()
	took := sleep()
	sleptAt := time.Now()
	if at := <-otherDone; !at.Before(sleptAt) {
		t.Fatalf("another machine's start waited for the sleep (done %s after it)", at.Sub(sleptAt))
	}
	if err := d.Destroy(ctx, otherID, sandbox.DestroyOpts{}); err != nil { // a shutdown, no snapshot
		t.Fatal(err)
	}
	took -= 4 * time.Second
	if !d.HasSnapshot(id) {
		t.Fatal("no snapshot after sleep")
	}
	var info snapshotInfo
	data, _ := os.ReadFile(snapFile(snapshotManifest))
	_ = json.Unmarshal(data, &info)
	t.Logf("MEASURE balloon=%v cold-boot=%s sleep=%s memory-file=%dMiB allocated=%dMiB balloon-reached=%dMiB vmstate=%dKiB",
		balloon, coldBoot.Round(time.Millisecond), took.Round(time.Millisecond), info.MemoryBytes>>20, allocated(snapFile("memory"))>>20, info.BalloonMiB, info.VMStateBytes>>10)
	time.Sleep(3 * time.Second) // the guest clock must not lose this
	s, wake := start()
	firstByte := time.Now()
	wokeBy("wake", "resumed")
	if got := count(s); got != 4 {
		t.Fatalf("counter after resume = %d, want 4 (state lost)", got)
	}
	// What a preview request to a sleeping machine waits for, minus the
	// controller's routing: wake, tunnel, dial, the service's answer.
	t.Logf("MEASURE balloon=%v wake-to-first-byte=%s", balloon, (wake + time.Since(firstByte)).Round(time.Millisecond))
	if got := servicePid(s); got != pid {
		t.Fatalf("service pid after resume = %d, want %d", got, pid)
	}
	if got := execIn(s, "cat /dev/shm/marker"); got != "kept" {
		t.Fatalf("/dev/shm after resume: %q", got)
	}
	if skew := clockSkew(s); skew < -time.Second || skew > time.Second {
		t.Fatalf("guest clock is %s off after resume", skew)
	}
	if d.HasSnapshot(id) {
		t.Fatal("a resumed machine still has a resumable snapshot")
	}
	t.Logf("MEASURE balloon=%v wake=%s (Start until tunnel)", balloon, wake.Round(time.Millisecond))

	// A second sleep and wake of the resumed VM (its memory file backs it
	// while it runs; the new snapshot replaces it).
	sleep()
	s, wake2 := start()
	wokeBy("second wake", "resumed")
	if got := count(s); got != 5 {
		t.Fatalf("counter after second resume = %d, want 5", got)
	}
	t.Logf("MEASURE balloon=%v second-wake=%s", balloon, wake2.Round(time.Millisecond))

	// Fallbacks: each wake below must be a cold boot from the disks.
	coldCheck := func(what string, s *tunnel.Session) {
		t.Helper()
		if got := count(s); got != 1 {
			t.Fatalf("%s: counter = %d, want a fresh process (1)", what, got)
		}
		if got := execIn(s, "cat /dev/shm/marker 2>/dev/null || echo none"); got != "none" {
			t.Fatalf("%s: /dev/shm survived: %q", what, got)
		}
		if got := execIn(s, "test -f ~/counter.py && echo disk-kept"); got != "disk-kept" {
			t.Fatalf("%s: home lost: %q", what, got)
		}
		if _, err := os.Stat(d.snapDir(id)); err == nil {
			if entries, _ := os.ReadDir(d.snapDir(id)); len(entries) > 0 {
				t.Fatalf("%s: snapshot files left after a cold boot: %v", what, entries)
			}
		}
		execIn(s, "echo kept > /dev/shm/marker")
	}
	// Each case breaks the snapshot on purpose; the driver logs the fallback
	// as a warning (the corrupt vmstate as Firecracker's "bitcode error").
	breakSnapshot := []struct {
		what   string
		reason string // in the driver's fallback warning
		break_ func()
	}{
		{"recorded Firecracker version changed", "the Firecracker binary changed", func() {
			var m map[string]any
			data, _ := os.ReadFile(snapFile(snapshotManifest))
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			m["firecracker"] = "0000"
			data, _ = json.Marshal(m)
			if err := os.WriteFile(snapFile(snapshotManifest), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"vmstate corrupt", "PUT /snapshot/load", func() {
			f, err := os.OpenFile(snapFile("vmstate"), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := f.Stat()
			junk := make([]byte, 4096)
			for i := range junk {
				junk[i] = byte(i * 7)
			}
			_, err = f.WriteAt(junk, st.Size()/3)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"host kernel changed", "host kernel changed since the snapshot", func() {
			var m map[string]any
			data, _ := os.ReadFile(snapFile(snapshotManifest))
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			m["hostKernel"] = "6.8.0-1-older"
			data, _ = json.Marshal(m)
			if err := os.WriteFile(snapFile(snapshotManifest), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"memory file missing", "snapshot file memory", func() {
			if err := os.Remove(snapFile("memory")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range breakSnapshot {
		sleep()
		if !d.HasSnapshot(id) {
			t.Fatalf("%s: no snapshot", c.what)
		}
		c.break_()
		s, wake := start()
		wokeBy(c.what, c.reason)
		coldCheck(c.what, s)
		t.Logf("fallback %q: cold boot in %s", c.what, wake.Round(time.Millisecond))
	}

	// The controller gave up waiting for the tunnel of a start (a slow cold
	// boot) and revoked its token: the guest keeps running and redialing with
	// it. The retry's Start finds the VM running and must hand it the new
	// token (here the processes survive: no reboot).
	hub.Disconnect(spec.AgentID)
	tokenMu.Lock()
	token = "kvm-snap-token-revoked"
	tokenMu.Unlock()
	time.Sleep(time.Second) // the guest redials and is refused
	if _, ok := hub.Session(spec.AgentID); ok {
		t.Fatal("the guest reconnected with a revoked token")
	}
	s, retry := start()
	wokeBy("start of a running machine", "cold boot")
	if got := execIn(s, "cat /dev/shm/marker 2>/dev/null || echo none"); got != "kept" {
		t.Fatalf("start of a running machine rebooted it: /dev/shm marker %q", got)
	}
	t.Logf("start of a running machine with a revoked token: connected in %s", retry.Round(time.Millisecond))
}
