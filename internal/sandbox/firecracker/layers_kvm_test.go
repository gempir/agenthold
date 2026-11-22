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

// TestLayeredRootfsKVM verifies stacked read-only ext4 layers (custom layer
// with upper/ subdir over a default layer over base), overlayfs whiteouts
// across lowerdirs, /etc/agenthold/image.env.d/*.env with $PATH merging,
// /etc/skel seeding on fresh home disks, and Rebuild preserving layers+home.
func TestLayeredRootfsKVM(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER", "SANDBOXD"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER/SANDBOXD to run the layered rootfs KVM test")
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
		cmd := exec.Command(exe, "-test.run=^TestLayeredRootfsKVM$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated layered KVM test: %v\n%s", err, out)
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
	dir, err := os.MkdirTemp("/tmp", "ahlay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Build two synthetic read-only ext4 layers:
	// 1. Default layer (no subdir): simulates a platform layer (e.g. harnesses).
	defaultTree := filepath.Join(dir, "default-tree")
	mustWriteFile(t, filepath.Join(defaultTree, "opt/default-layer/tool.txt"), "default-ok\n", 0o644)
	mustWriteFile(t, filepath.Join(defaultTree, "etc/layer-conflict"), "from-default\n", 0o644)
	mustWriteFile(t, filepath.Join(defaultTree, "etc/delete-me"), "from-default\n", 0o644)
	mustWriteFile(t, filepath.Join(defaultTree, "etc/agenthold/image.env.d/05-default.env"),
		"DEFAULT_VAR=ok\nPATH=/opt/default-layer/bin:$PATH\n", 0o644)
	defaultImg := filepath.Join(dir, "default-layer.ext4")
	buildExt4Layer(t, defaultTree, defaultImg)

	// 2. Custom layer (with upper/ subdir): simulates a sealed builder overlay disk.
	customTree := filepath.Join(dir, "custom-tree")
	mustWriteFile(t, filepath.Join(customTree, "upper/usr/local/bin/custom-tool"),
		"#!/bin/sh\necho custom-tool-ok\n", 0o755)
	mustWriteFile(t, filepath.Join(customTree, "upper/etc/layer-conflict"), "from-custom\n", 0o644)
	if err := os.MkdirAll(filepath.Join(customTree, "upper/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Overlayfs whiteout (char device 0/0) hides /etc/delete-me from the lower layer.
	if err := unix.Mknod(filepath.Join(customTree, "upper/etc/delete-me"), unix.S_IFCHR, 0); err != nil {
		t.Fatalf("mknod whiteout: %v", err)
	}
	mustWriteFile(t, filepath.Join(customTree, "upper/etc/agenthold/image.env.d/10-custom.env"),
		"CUSTOM_VAR=yes\nPATH=/opt/custom/bin:${PATH}\n", 0o644)
	mustWriteFile(t, filepath.Join(customTree, "upper/etc/skel/.customrc"), "export CUSTOM_SKEL=1\n", 0o644)
	customImg := filepath.Join(dir, "custom-layer.ext4")
	buildExt4Layer(t, customTree, customImg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	token := "kvm-layered-rootfs-token"
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		if !strings.HasPrefix(agent, "ag_lay_") || got != token {
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

	pool := netip.MustParsePrefix("10.218.0.0/24")
	firewall := &egress.Firewall{Pool: pool, ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{port}}
	if err = firewall.Install(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DataDir:         dir,
		JailDir:         filepath.Join(dir, "j"),
		BaseImagePath:   os.Getenv("AGENTHOLD_KVM_BASE"),
		KernelPath:      os.Getenv("AGENTHOLD_KVM_KERNEL"),
		FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"),
		JailerPath:      os.Getenv("AGENTHOLD_KVM_JAILER"),
		SandboxdPath:    os.Getenv("AGENTHOLD_KVM_SANDBOXD"),
		DefaultLayers:   []LayerFile{{Path: defaultImg}},
		ResolveLayer: func(_ context.Context, ref string) (LayerFile, error) {
			if ref == "corp:v1" {
				return LayerFile{Path: customImg, Subdir: "upper"}, nil
			}
			return LayerFile{}, errors.New("unknown layer " + ref)
		},
		NetworkPool:       pool,
		OverlaySizeBytes:  256 << 20,
		HomeSizeBytes:     256 << 20,
		StopTimeout:       8 * time.Second,
		RegisterNetwork:   firewall.Register,
		UnregisterNetwork: firewall.Unregister,
		Shutdown: func(ctx context.Context, id sandbox.ID) error {
			s, ok := hub.Session(string(id))
			if !ok {
				return tunnel.ErrNotConnected
			}
			return s.Shutdown(ctx)
		},
		WaitConnected: func(ctx context.Context, id sandbox.ID) error { return hub.WaitConnected(ctx, string(id)) },
	}
	d, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := sandbox.Spec{
		AgentID:   "ag_lay_" + strconv.Itoa(os.Getpid()),
		Layers:    []string{"corp:v1"},
		Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512},
	}
	id, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		if err := d.Destroy(c, id, sandbox.DestroyOpts{}); err != nil {
			t.Error("cleanup:", err)
		}
	})
	boot := sandbox.Boot{AgentID: spec.AgentID, Token: token, TunnelPort: port}

	start := func() *tunnel.Session {
		t.Helper()
		if err := d.Start(ctx, id, boot); err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("start: %v\n%s", err, log)
		}
		sess, err := hub.Wait(ctx, spec.AgentID)
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
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
		<-p.Done()
		if err != nil {
			t.Fatalf("%s: %v stderr=%s", command, err, stderr.String())
		}
		code, err := p.ExitCode()
		if err != nil || code != 0 {
			t.Fatalf("cmd %q failed (code=%d err=%v)\nstdout:\n%s\nstderr:\n%s", command, code, err, out, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}

	// Boot 1: verify stacked layers, precedence, whiteout, image.env.d + PATH, and /etc/skel.
	sess := start()
	if got := run(sess, "custom-tool"); got != "custom-tool-ok" {
		t.Fatalf("custom-tool = %q", got)
	}
	if got := run(sess, "cat /opt/default-layer/tool.txt"); got != "default-ok" {
		t.Fatalf("default layer file = %q", got)
	}
	if got := run(sess, "cat /etc/layer-conflict"); got != "from-custom" {
		t.Fatalf("layer conflict = %q, want from-custom", got)
	}
	if got := run(sess, "test ! -e /etc/delete-me && echo gone"); got != "gone" {
		t.Fatalf("whiteout failed: %q", got)
	}
	if got := run(sess, `printf '%s|%s|%s' "$DEFAULT_VAR" "$CUSTOM_VAR" "$PATH"`); !strings.HasPrefix(got, "ok|yes|/opt/custom/bin:/opt/default-layer/bin:") || !strings.Contains(got, ":/usr/local/bin:") {
		t.Fatalf("env = %q", got)
	}
	if got := run(sess, "cat /home/agent/.customrc && stat -c '%U:%G' /home/agent/.customrc"); got != "export CUSTOM_SKEL=1\nagent:agent" {
		t.Fatalf("skel seed = %q", got)
	}
	// Write both overlay and home files, then stop + reboot.
	run(sess, "sudo sh -c 'echo overlay-state >/etc/agent-overlay-file' && echo home-state >/home/agent/user-data")
	if err := d.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}

	// Boot 2: both overlay and home writes survive reboot.
	sess = start()
	if got := run(sess, "cat /etc/agent-overlay-file /home/agent/user-data"); got != "overlay-state\nhome-state" {
		t.Fatalf("after reboot = %q", got)
	}
	if err := d.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}

	// Rebuild (Reset system): overlay file gone; layers and home survive.
	if err := d.Rebuild(ctx, id, spec); err != nil {
		t.Fatal(err)
	}
	sess = start()
	if got := run(sess, "test ! -e /etc/agent-overlay-file && custom-tool && cat /opt/default-layer/tool.txt /home/agent/user-data /home/agent/.customrc"); got != "custom-tool-ok\ndefault-ok\nhome-state\nexport CUSTOM_SKEL=1" {
		t.Fatalf("after rebuild = %q", got)
	}
}

func mustWriteFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func buildExt4Layer(t *testing.T, srcDir, dstImg string) {
	t.Helper()
	f, err := os.Create(dstImg)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16 << 20); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("mkfs.ext4", "-q", "-F", "-O", "^has_journal", "-m", "0", "-d", srcDir, dstImg).CombinedOutput()
	if err != nil {
		t.Fatalf("mkfs.ext4 %s: %v\n%s", dstImg, err, out)
	}
	if err := os.Chmod(dstImg, 0o644); err != nil {
		t.Fatal(err)
	}
}
