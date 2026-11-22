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

// TestCollapseGuestHugePagesKVM boots a real Firecracker VM while THP is
// temporarily disabled (forcing 4 KiB fallback), re-enables THP, and verifies
// collapseGuestHugePages collapses the VM's guest RAM into 2 MiB huge pages
// live without restarting the VM.
func TestCollapseGuestHugePagesKVM(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER", "SANDBOXD"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER/SANDBOXD to run the hugepage KVM test")
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
		cmd := exec.Command(exe, "-test.run=^TestCollapseGuestHugePagesKVM$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated hugepage KVM test: %v\n%s", err, out)
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

	thpPath := filepath.Join("/", thpEnabled)
	origBytes, err := os.ReadFile(thpPath)
	if err != nil {
		t.Skipf("transparent huge pages unavailable: %v", err)
	}
	origMode := selectedMode(string(origBytes))
	restoreTHP := func() { _ = os.WriteFile(thpPath, []byte(origMode), 0) }
	t.Cleanup(restoreTHP)
	if err := os.WriteFile(thpPath, []byte("never"), 0); err != nil {
		t.Skipf("cannot toggle THP for test: %v", err)
	}

	dir, err := os.MkdirTemp("/tmp", "ahhuge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{
		DataDir: dir, JailDir: filepath.Join(dir, "j"),
		BaseImagePath: os.Getenv("AGENTHOLD_KVM_BASE"), KernelPath: os.Getenv("AGENTHOLD_KVM_KERNEL"),
		FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"), JailerPath: os.Getenv("AGENTHOLD_KVM_JAILER"),
		SandboxdPath:     os.Getenv("AGENTHOLD_KVM_SANDBOXD"),
		NetworkPool:      netip.MustParsePrefix("10.215.0.0/24"),
		OverlaySizeBytes: 256 << 20, HomeSizeBytes: 256 << 20, StopTimeout: 8 * time.Second,
		KeepHugePages: true, // test drives collapseGuestHugePages directly
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	token := "kvm-hugepage-token"
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		if !strings.HasPrefix(agent, "ag_huge_") || got != token {
			return errors.New("unexpected guest identity")
		}
		return nil
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("/tunnel", hub)
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	fw := &egress.Firewall{Pool: cfg.NetworkPool, ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{port}}
	if err = fw.Install(); err != nil {
		t.Fatal(err)
	}
	cfg.RegisterNetwork = fw.Register
	cfg.UnregisterNetwork = fw.Unregister
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
	spec := sandbox.Spec{AgentID: "ag_huge_" + strconv.Itoa(os.Getpid()), Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512}}
	id, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = d.Destroy(c, id, sandbox.DestroyOpts{})
	})
	if err := d.Start(ctx, id, sandbox.Boot{AgentID: spec.AgentID, Token: token, TunnelPort: port}); err != nil {
		t.Fatal(err)
	}
	sess, err := hub.Wait(ctx, spec.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	// Touch ~64 MiB inside the guest while THP is disabled.
	p, err := sess.StartHarness(ctx, []string{"python3", "-c", "b = bytearray(64*1024*1024); b[::4096] = b'x' * (len(b)//4096); print(len(b))"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(p.Stdout)
	<-p.Done()
	_ = p.Close()

	m, err := d.load(id)
	if err != nil {
		t.Fatal(err)
	}
	rssBefore, hugeBefore, err := readSmapsRollupKB(m.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before collapse: rss=%d kB huge=%d kB", rssBefore, hugeBefore)
	if hugeBefore != 0 || rssBefore < 48*1024 {
		t.Fatalf("expected 4 KiB pages before collapse: rss=%d huge=%d", rssBefore, hugeBefore)
	}

	// Re-enable THP and collapse guest RAM live.
	if err := os.WriteFile(thpPath, []byte("always"), 0); err != nil {
		t.Fatal(err)
	}
	collapsed, unsupported, err := collapseGuestHugePages(m.Process.PID, m.Process.StartTime, 512)
	if err != nil {
		t.Fatalf("collapseGuestHugePages: %v", err)
	}
	if unsupported {
		t.Skip("host kernel does not support process_madvise(MADV_COLLAPSE)")
	}
	rssAfter, hugeAfter, err := readSmapsRollupKB(m.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after collapse (%d chunks): rss=%d kB huge=%d kB", collapsed, rssAfter, hugeAfter)
	if collapsed == 0 || hugeAfter < rssAfter/2 {
		t.Fatalf("expected majority of guest RAM collapsed to huge pages: collapsed=%d rss=%d huge=%d", collapsed, rssAfter, hugeAfter)
	}
}
