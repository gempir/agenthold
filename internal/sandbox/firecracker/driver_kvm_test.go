//go:build linux && kvm

package firecracker

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/tunnel"
)

// TestJailedLifecycle runs in a disposable network/mount namespace so it can
// install the production firewall without touching the developer's host rules.
// Use built N2 artifacts and extracted pinned Firecracker+jailer binaries:
// AGENTHOLD_KVM_BASE=/.../base.ext4 AGENTHOLD_KVM_KERNEL=/.../vmlinux
// AGENTHOLD_KVM_FIRECRACKER=/.../firecracker AGENTHOLD_KVM_JAILER=/.../jailer
// sudo -E go test -tags=kvm ./internal/sandbox/firecracker -run TestJailedLifecycle -v
func TestJailedLifecycle(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER to run the jailed VM test")
		}
	}
	if os.Geteuid() != 0 {
		if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
			t.Fatal("required jailed microVM test needs root")
		}
		t.Skip("jailed microVM test requires root")
	}
	if os.Getenv("AGENTHOLD_KVM_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestJailedLifecycle$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated jailed VM test: %v\n%s", err, out)
		} else {
			t.Log(string(out))
		}
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
	dir, err := os.MkdirTemp("/tmp", "ahfc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{DataDir: dir, JailDir: filepath.Join(dir, "j"), BaseImagePath: os.Getenv("AGENTHOLD_KVM_BASE"), KernelPath: os.Getenv("AGENTHOLD_KVM_KERNEL"), FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"), JailerPath: os.Getenv("AGENTHOLD_KVM_JAILER"), NetworkPool: netip.MustParsePrefix("10.214.0.0/24"), OverlaySizeBytes: 256 << 20, HomeSizeBytes: 256 << 20, StopTimeout: 8 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	token := "kvm-test-config-disk-only-token"
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		if !strings.HasPrefix(agent, "ag_kvm_") || got != token {
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
	firewall := &egress.Firewall{Pool: cfg.NetworkPool, ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{port}}
	if err = firewall.Install(); err != nil {
		t.Fatal(err)
	}
	cfg.RegisterNetwork = firewall.Register
	cfg.UnregisterNetwork = firewall.Unregister
	cfg.Shutdown = func(ctx context.Context, id sandbox.ID) error {
		s, ok := hub.Session(string(id))
		if !ok {
			return tunnel.ErrNotConnected
		}
		return s.Shutdown(ctx)
	}
	d, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := sandbox.Spec{AgentID: "ag_kvm_" + strconv.Itoa(os.Getpid()), Resources: sandbox.Resources{CPUs: 1.5, MemoryMB: 512}, Env: map[string]string{"KVM_GATEWAY": "{{server}}", "KVM_SECRET": "ph-kvm-placeholder"}}
	id, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := d.Destroy(c, id, sandbox.DestroyOpts{}); err != nil {
			t.Error("cleanup:", err)
		}
	})
	ca, upstreamAuth := startKVMProxy(t, dir, spec.AgentID)
	boot := sandbox.Boot{AgentID: spec.AgentID, Token: token, TunnelPort: port, Files: map[string][]byte{"/etc/agenthold/kvm-file": []byte("boot-config-file"), sandboxd.CAFile: ca}}

	start := func(driver *Driver) *tunnel.Session {
		t.Helper()
		if err := driver.Start(ctx, id, boot); err != nil {
			log, _ := os.ReadFile(filepath.Join(driver.dir(id), "serial.log"))
			t.Fatalf("start: %v\n%s", err, log)
		}
		sess, err := hub.Wait(ctx, spec.AgentID)
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(driver.dir(id), "serial.log"))
			t.Fatalf("wait tunnel: %v\n%s", err, log)
		}
		return sess
	}
	run := func(sess *tunnel.Session, command string) string {
		t.Helper()
		cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
		defer ccancel()
		var stderr strings.Builder
		p, err := sess.StartHarness(cctx, []string{"bash", "-lc", "exec </dev/null; " + command}, nil, func(line string) { stderr.WriteString(line + "\n") })
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		stop := context.AfterFunc(cctx, func() { _ = p.Close() })
		defer stop()
		out, err := io.ReadAll(io.LimitReader(p.Stdout, 1<<20))
		if err != nil {
			t.Fatalf("%s: %v stderr=%s", command, err, stderr.String())
		}
		<-p.Done()
		code, err := p.ExitCode()
		if err != nil || code != 0 {
			t.Fatalf("%s: code=%d err=%v stderr=%s stdout=%s", command, code, err, stderr.String(), out)
		}
		return strings.TrimSpace(string(out))
	}
	sess := start(d)
	// Helpers that left the harness's process group (setsid, double fork)
	// still go with the harness: it runs in a cgroup that is killed.
	escaped := run(sess, "setsid sleep 300 >/dev/null 2>&1 & echo $!")
	if got := run(sess, "for i in $(seq 1 50); do test -e /proc/"+escaped+" || { echo gone; exit 0; }; sleep 0.1; done; cat /proc/"+escaped+"/cgroup; echo alive"); got != "gone" {
		t.Fatalf("a setsid helper outlived its harness: %s", got)
	}
	if got := run(sess, "id -un; cat /etc/agenthold/kvm-file; echo; echo $KVM_GATEWAY; cat /proc/cmdline"); !strings.Contains(got, "agent\nboot-config-file\n10.214.0.1") || strings.Contains(got, token) {
		t.Fatalf("guest init contract: %s", got)
	}
	// The guest only knows the placeholder. Its verified TLS request crosses
	// the production transparent proxy; only the upstream receives the real key.
	if got := run(sess, `printf '%s\n' "$KVM_SECRET"; curl --noproxy '*' --cacert /etc/agenthold/ca.pem --resolve api.example.com:443:198.51.100.1 --max-time 20 -fsS https://api.example.com/secret -H "Authorization: Bearer $KVM_SECRET"`); got != "ph-kvm-placeholder\nupstream-ok" {
		t.Fatalf("guest secret/proxy response: %q", got)
	}
	select {
	case auth := <-upstreamAuth:
		if auth != "Bearer real-key-kept-on-host" {
			t.Fatalf("placeholder was not replaced upstream: %q", auth)
		}
	case <-ctx.Done():
		t.Fatal("upstream received no request")
	}
	config, err := os.ReadFile(filepath.Join(d.dir(id), "config.img"))
	if err != nil || bytes.Contains(config, []byte("real-key-kept-on-host")) {
		t.Fatal("real secret entered guest boot configuration", err)
	}
	if got := run(sess, "env; cat /proc/cmdline"); strings.Contains(got, "real-key-kept-on-host") {
		t.Fatal("real secret readable in guest environment or commandline")
	}
	// Verify actual jail, privilege drop, and cgroup limits rather than flags.
	m, err := d.load(id)
	if err != nil {
		t.Fatal(err)
	}
	proc := fmt.Sprintf("/proc/%d", m.Process.PID)
	status, err := os.ReadFile(filepath.Join(proc, "status"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d", m.UID, m.UID, m.UID, m.UID)) {
		t.Fatalf("VMM did not drop uid: %s", status)
	}
	rootInfo, err := os.Stat(filepath.Join(proc, "root"))
	if err != nil {
		t.Fatal(err)
	}
	jailInfo, err := os.Stat(d.jailRoot(id))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rootInfo, jailInfo) {
		t.Fatal("VMM process root is not the jail root inode")
	}
	if _, err = os.Stat(filepath.Join(proc, "root", "etc", "passwd")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host /etc/passwd is visible in jail: %v", err)
	}
	for file, want := range map[string]string{"cpu.max": "150000 100000", "memory.max": "671088640"} {
		b, err := os.ReadFile(filepath.Join("/sys/fs/cgroup/agenthold", d.jailID(id), file))
		if err != nil || strings.TrimSpace(string(b)) != want {
			t.Fatalf("cgroup %s=%q want %q: %v", file, b, want, err)
		}
	}
	if err = sess.WriteFile(ctx, "/home/agent/keep.txt", 0o600, strings.NewReader("home-survives\n")); err != nil {
		t.Fatal(err)
	}
	run(sess, "sudo sh -c 'echo system-survives > /etc/kvm-system'; test ! -r /dev/vdd; echo file-written")
	terminal, err := sess.PTY(ctx, tunnel.Open{Cmd: []string{"bash", "-lc", "printf terminal-works"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code, err := terminal.ReadOutput(&output)
	_ = terminal.Close()
	if err != nil || code != 0 || !strings.Contains(output.String(), "terminal-works") {
		t.Fatalf("terminal %q %d %v", output.String(), code, err)
	}
	// Simulate the controller dying immediately after jailer starts, before
	// it can persist a PID. The cgroup must recover the still-live process.
	m.Process = process{}
	if err = d.save(id, m); err != nil {
		t.Fatal(err)
	}
	// A reopened driver discovers and adopts an existing live jail.
	reopened, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Attach(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, err := reopened.Status(ctx, id)
	if err != nil || st.State != sandbox.StateRunning {
		t.Fatalf("recovered status %+v %v", st, err)
	}
	if err = reopened.Start(ctx, id, boot); err != nil {
		t.Fatal(err)
	} // idempotent
	// Production firewall blocks unrelated host listeners and the second VM.
	hostListener, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	blockedPort := hostListener.Addr().(*net.TCPAddr).Port
	if got := run(sess, fmt.Sprintf("if timeout 1 bash -c 'exec 3<>/dev/tcp/10.214.0.1/%d' 2>/dev/null; then echo reachable; else echo blocked; fi", blockedPort)); got != "blocked" {
		t.Fatal("guest reached unrelated host service:", got)
	}
	secondSpec := spec
	secondSpec.AgentID += "_peer"
	second, err := d.Create(ctx, secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = d.Destroy(c, second, sandbox.DestroyOpts{})
	})
	secondBoot := boot
	secondBoot.AgentID = secondSpec.AgentID
	if err = d.Start(ctx, second, secondBoot); err != nil {
		t.Fatal(err)
	}
	peer, err := hub.Wait(ctx, secondSpec.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	// The listener lives in a harness of its own: everything a harness
	// starts goes with it.
	peerServer, err := peer.StartHarness(ctx, []string{"bash", "-lc", "exec </dev/null; exec python3 -m http.server 18080 --bind 0.0.0.0 >/tmp/listener.log 2>&1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peerServer.Close()
	run(peer, "for i in $(seq 1 50); do curl --noproxy '*' --max-time 1 -fsS http://127.0.0.1:18080/ >/dev/null 2>&1 && exit 0; sleep 0.1; done; cat /tmp/listener.log; exit 1")
	if got := run(peer, "curl --noproxy '*' --max-time 2 -fsS -o /dev/null http://10.214.0.6:18080/ && echo reachable"); got != "reachable" {
		t.Fatal("the peer's listener is not reachable on its own address:", got)
	}
	if got := run(sess, "if curl --noproxy '*' --max-time 2 -fsS http://10.214.0.6:18080/ >/dev/null 2>&1; then echo reachable; else echo blocked; fi"); got != "blocked" {
		t.Fatal("guest reached peer:", got)
	}
	if err = d.Stop(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(d.dir(id), "config.img")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("boot token config remains after stop: %v", err)
	}
	sess = start(reopened)
	if got := run(sess, "cat ~/keep.txt; cat /etc/kvm-system"); got != "home-survives\nsystem-survives" {
		t.Fatalf("cold boot lost files: %q", got)
	}
	if err = reopened.Rebuild(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	sess = start(reopened)
	if got := run(sess, "cat ~/keep.txt; if test -e /etc/kvm-system; then echo system-present; else echo system-reset; fi"); got != "home-survives\nsystem-reset" {
		t.Fatalf("reset system: %q", got)
	}
	// Force-stop path is bounded even with a broken shutdown callback.
	reopened.cfg.Shutdown = func(ctx context.Context, _ sandbox.ID) error { <-ctx.Done(); return ctx.Err() }
	reopened.cfg.StopTimeout = 50 * time.Millisecond
	if err = reopened.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Stop(ctx, id); err != nil {
		t.Fatal("idempotent stop:", err)
	}
	// Partial boot failure must remove the jail, tap, config and cgroup.
	reopened.cfg.RegisterNetwork = func(ctx context.Context, tap string, guest netip.Addr) error {
		if err := firewall.Register(ctx, tap, guest); err != nil {
			return err
		}
		return errors.New("injected registration failure")
	}
	if err = reopened.Start(ctx, id, boot); err == nil {
		t.Fatal("injected registration failure was ignored")
	}
	if _, err = netlink.LinkByName(TapName(id)); err == nil {
		t.Fatal("tap leaked after failed start")
	}
	if _, err = os.Stat(d.jailRoot(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("jail leaked after failed start: %v", err)
	}
	// Cancellation after registration still revokes the just-installed rule
	// and removes all tap/mount/config resources.
	cancelled, cancelStart := context.WithCancel(ctx)
	reopened.cfg.RegisterNetwork = func(c context.Context, tap string, guest netip.Addr) error {
		err := firewall.Register(c, tap, guest)
		cancelStart()
		return err
	}
	err = reopened.Start(cancelled, id, boot)
	cancelStart()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start: %v", err)
	}
	if _, err = netlink.LinkByName(TapName(id)); err == nil {
		t.Fatal("tap leaked after cancelled start")
	}
	if _, err = os.Stat(filepath.Join(d.dir(id), "config.img")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config leaked after cancelled start: %v", err)
	}
	t.Log("jailed boot, tunnel/harness, terminal/files, TLS secret substitution, resource caps, adoption, host/peer isolation, sleep/wake, reset system, bounded kill and partial-failure cleanup passed")
}

// kvmAgents supplies a host-only secret for exactly the authenticated guest IP.
type kvmAgents struct{ agent *egress.Agent }

func (a kvmAgents) AgentForSource(_ context.Context, ip netip.Addr) (*egress.Agent, error) {
	if ip.Unmap() != netip.MustParseAddr("10.214.0.2") {
		return nil, nil
	}
	return a.agent, nil
}

func startKVMProxy(t *testing.T, dir, agentID string) ([]byte, <-chan string) {
	t.Helper()
	ca, err := egress.LoadOrCreateCA(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	upstreamCA, err := egress.LoadOrCreateCA(filepath.Join(dir, "upstream-ca"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := upstreamCA.Leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	auth := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "upstream-ok")
	}), ReadHeaderTimeout: time.Second}
	go func() {
		_ = srv.Serve(tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}}))
	}()
	t.Cleanup(func() { _ = srv.Close() })
	agent := &egress.Agent{ID: agentID, Injections: []secrets.Injection{{SecretID: "kvm-secret", Value: "real-key-kept-on-host", Placeholder: "ph-kvm-placeholder", Rule: secrets.Rule{Hosts: []string{"api.example.com"}, Header: "Authorization", Template: "Bearer {{.Value}}"}}}}
	proxy := &egress.Proxy{PeekTimeout: 5 * time.Second, Log: func(event egress.Event) { t.Logf("proxy event: %+v", event) }, CA: ca, Agents: kvmAgents{agent}, Names: egress.NewNameCache(), OriginalDst: egress.OriginalDst, UpstreamRoots: upstreamCA.Pool(), Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, Reachable: func(ip netip.Addr) bool { return ip == netip.MustParseAddr("127.0.0.1") }}
	listener, err := net.Listen("tcp4", ":15001")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = proxy.ServeTransparent(listener) }()
	t.Cleanup(func() { _ = listener.Close() })
	return ca.CertPEM(), auth
}
