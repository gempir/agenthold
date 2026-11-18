//go:build linux && kvm

package firecracker

import (
	"context"
	"errors"
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
	"github.com/gempir/agenthold/internal/tunnel"
)

// TestJailedClone clones a stopped microVM and boots clone and source side by
// side. Same environment as TestJailedLifecycle (root, KVM, extracted pinned
// artifacts), in a disposable network namespace:
// AGENTHOLD_KVM_BASE=... AGENTHOLD_KVM_KERNEL=... AGENTHOLD_KVM_FIRECRACKER=... AGENTHOLD_KVM_JAILER=... AGENTHOLD_KVM_SANDBOXD=...
// sudo -E go test -tags=kvm ./internal/sandbox/firecracker -run TestJailedClone -v
func TestJailedClone(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER", "SANDBOXD"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER/SANDBOXD to run the jailed VM test")
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
		cmd := exec.Command(exe, "-test.run=^TestJailedClone$", "-test.v")
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
	dir, err := os.MkdirTemp("/tmp", "ahclone-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{DataDir: dir, JailDir: filepath.Join(dir, "j"), BaseImagePath: os.Getenv("AGENTHOLD_KVM_BASE"), KernelPath: os.Getenv("AGENTHOLD_KVM_KERNEL"), FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"), JailerPath: os.Getenv("AGENTHOLD_KVM_JAILER"), SandboxdPath: os.Getenv("AGENTHOLD_KVM_SANDBOXD"), NetworkPool: netip.MustParsePrefix("10.215.0.0/24"), OverlaySizeBytes: 256 << 20, HomeSizeBytes: 256 << 20, StopTimeout: 8 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	const token = "kvm-clone-config-disk-only-token"
	prefix := "ag_kvmc_" + strconv.Itoa(os.Getpid())
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		if !strings.HasPrefix(agent, prefix) || got != token {
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
	spec := func(id string) sandbox.Spec {
		return sandbox.Spec{AgentID: id, Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512}}
	}
	destroy := func(id sandbox.ID) {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.Destroy(c, id, sandbox.DestroyOpts{}); err != nil {
			t.Error("cleanup:", err)
		}
	}
	start := func(id sandbox.ID) *tunnel.Session {
		t.Helper()
		if err := d.Start(ctx, id, sandbox.Boot{AgentID: string(id), Token: token, TunnelPort: port}); err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("start %s: %v\n%s", id, err, log)
		}
		sess, err := hub.Wait(ctx, string(id))
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("wait tunnel %s: %v\n%s", id, err, log)
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

	srcID, err := d.Create(ctx, spec(prefix+"_src"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { destroy(srcID) })
	src := start(srcID)
	run(src, "echo home-from-source > ~/keep.txt; sudo sh -c 'echo system-from-source > /etc/kvm-system; echo source-engine > /var/lib/docker/engine-id; echo 0123456789abcdef0123456789abcdef > /etc/machine-id'; sync")
	// The delivered sandboxd stamps every system with its machine's id, on
	// any base image.
	if got := run(src, "cat /etc/agenthold/instance"); got != string(srcID) {
		t.Fatalf("source stamp: %q", got)
	}
	// A system that was never stamped (machines that booted only before the
	// stamp existed): the clone's own boot instruction must reset it.
	run(src, "sudo rm /etc/agenthold/instance; sync")

	// A running source is refused, and a refusal leaves nothing behind.
	if _, err = d.Clone(ctx, srcID, spec(prefix+"_dst")); !errors.Is(err, sandbox.ErrRunning) {
		t.Fatalf("clone of a running VM: %v", err)
	}
	if _, err = os.Stat(d.dir(sandbox.ID(prefix + "_dst"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused clone left files: %v", err)
	}
	if err = d.Stop(ctx, srcID); err != nil {
		t.Fatal(err)
	}

	dstID, err := d.Clone(ctx, srcID, spec(prefix+"_dst"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { destroy(dstID) })
	for _, name := range diskNames {
		s, c := filepath.Join(d.dir(srcID), name), filepath.Join(d.dir(dstID), name)
		ss, err := os.Stat(s)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := os.Stat(c)
		if err != nil || cs.Size() != ss.Size() {
			t.Fatalf("%s: %v", name, err)
		}
		// The clone is as sparse as its source: no 256 MiB of zeros.
		if got, want := allocatedBytes(t, c), allocatedBytes(t, s); got > want+(4<<20) {
			t.Fatalf("%s allocates %d bytes, source %d", name, got, want)
		}
	}

	// Both machines at the same time.
	src, dst := start(srcID), start(dstID)
	const files = "cat ~/keep.txt; cat /etc/kvm-system"
	for name, sess := range map[string]*tunnel.Session{"source": src, "clone": dst} {
		if got := run(sess, files); got != "home-from-source\nsystem-from-source" {
			t.Fatalf("%s sees %q", name, got)
		}
	}
	run(dst, "echo only-in-clone > ~/clone.txt; sudo sh -c 'echo clone > /etc/kvm-clone'")
	run(src, "echo only-in-source > ~/source.txt; sudo sh -c 'echo source > /etc/kvm-source'")
	if got := run(src, "ls ~/clone.txt /etc/kvm-clone 2>&1 | grep -c 'No such file'"); got != "2" {
		t.Fatalf("source sees the clone's writes: %q", got)
	}
	if got := run(dst, "ls ~/source.txt /etc/kvm-source 2>&1 | grep -c 'No such file'"); got != "2" {
		t.Fatalf("clone sees the source's later writes: %q", got)
	}

	// Their own network addresses, hardware addresses and VMM users.
	const nic = "ip -4 -o addr show eth0 | awk '{print $4}'; cat /sys/class/net/eth0/address"
	srcNet, dstNet := run(src, nic), run(dst, nic)
	sm, err := d.load(srcID)
	if err != nil {
		t.Fatal(err)
	}
	dm, err := d.load(dstID)
	if err != nil {
		t.Fatal(err)
	}
	if srcNet == dstNet || !strings.HasPrefix(srcNet, sm.Subnet.Addr().Next().Next().String()) || !strings.HasPrefix(dstNet, dm.Subnet.Addr().Next().Next().String()) {
		t.Fatalf("addresses: source %q (%s), clone %q (%s)", srcNet, sm.Subnet, dstNet, dm.Subnet)
	}
	if sm.UID == dm.UID {
		t.Fatal("clone shares its VMM user with the source")
	}

	// The clone is its own machine: machine identity is cleared, the source keeps its own.
	if got := run(dst, "cat /etc/agenthold/instance; wc -c < /etc/machine-id; ls /var/lib/docker/engine-id 2>&1 | grep -c 'No such file'"); got != string(dstID)+"\n0\n1" {
		t.Fatalf("clone identity: %q", got)
	}
	// The source was unstamped and had no instruction: it only stamps itself.
	if got := run(src, "cat /etc/agenthold/instance; cat /etc/machine-id; cat /var/lib/docker/engine-id"); got != string(srcID)+"\n0123456789abcdef0123456789abcdef\nsource-engine" {
		t.Fatalf("source identity: %q", got)
	}

	// The tunnel's dial op (preview URLs) works in a clone as in any machine.
	run(dst, "echo dial-works > /tmp/index.html")
	// The server runs as a harness process of its own for as long as the dial is needed.
	srvCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	server, err := dst.StartHarness(srvCtx, []string{"bash", "-lc", "cd /tmp && exec python3 -m http.server 18081 --bind 127.0.0.1"}, nil, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var conn net.Conn
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if conn, err = dst.Dial(ctx, 18081); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial into the clone: %v", err)
		}
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = io.WriteString(conn, "GET /index.html HTTP/1.0\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(conn)
	_ = conn.Close()
	stopServer()
	_ = server.Close()
	if !strings.Contains(string(reply), "dial-works") {
		t.Fatalf("dialed reply: %q", reply)
	}

	// Stop both; the source must be cloneable again after having run.
	if err = d.Stop(ctx, dstID); err != nil {
		t.Fatal(err)
	}
	if err = d.Stop(ctx, srcID); err != nil {
		t.Fatal(err)
	}
	again, err := d.Clone(ctx, dstID, spec(prefix+"_grandchild"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { destroy(again) })
	gc := start(again)
	if got := run(gc, "cat ~/keep.txt ~/clone.txt"); got != "home-from-source\nonly-in-clone" {
		t.Fatalf("clone of a clone: %q", got)
	}
	if got := run(gc, "cat /etc/agenthold/instance"); got != string(again) {
		t.Fatalf("grandchild stamp: %q", got)
	}
	t.Log("clone of a stopped microVM: sparse disks, both VMs side by side, independent writes, own addresses, own identity, clone of a clone")
}
