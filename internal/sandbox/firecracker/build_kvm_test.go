//go:build linux && kvm

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// TestBuildAndBootCustomLayerKVM builds a real custom image layer inside an
// ephemeral jailed Firecracker builder VM, seals and compresses it with zstd,
// installs it into a LayerStore, and boots a fresh agent microVM on that layer.
func TestBuildAndBootCustomLayerKVM(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER", "SANDBOXD"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER/SANDBOXD to run the custom image build KVM test")
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
		cmd := exec.Command(exe, "-test.run=^TestBuildAndBootCustomLayerKVM$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated build KVM test: %v\n%s", err, out)
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
	dir, err := os.MkdirTemp("/tmp", "ahbld-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var tokMu sync.RWMutex
	tokens := map[string]string{}
	setToken := func(_ context.Context, agentID, hash string) error {
		tokMu.Lock()
		defer tokMu.Unlock()
		if hash == "" {
			delete(tokens, agentID)
		} else {
			tokens[agentID] = hash
		}
		return nil
	}
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		tokMu.RLock()
		want := tokens[agent]
		tokMu.RUnlock()
		sum := sha256.Sum256([]byte(got))
		if want == "" || hex.EncodeToString(sum[:]) != want {
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

	pool := netip.MustParsePrefix("10.219.0.0/24")
	firewall := &egress.Firewall{Pool: pool, ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{port}}
	if err = firewall.Install(); err != nil {
		t.Fatal(err)
	}

	layerStore := node.NewLayerStore(dir)
	cfg := Config{
		DataDir:         dir,
		JailDir:         filepath.Join(dir, "j"),
		BaseImagePath:   os.Getenv("AGENTHOLD_KVM_BASE"),
		KernelPath:      os.Getenv("AGENTHOLD_KVM_KERNEL"),
		FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"),
		JailerPath:      os.Getenv("AGENTHOLD_KVM_JAILER"),
		SandboxdPath:    os.Getenv("AGENTHOLD_KVM_SANDBOXD"),
		ResolveLayer: func(_ context.Context, ref string) (LayerFile, error) {
			p, sub, err := layerStore.ResolveRef(ref)
			if err != nil {
				return LayerFile{}, err
			}
			return LayerFile{Path: p, Subdir: sub}, nil
		},
		NetworkPool:       pool,
		OverlaySizeBytes:  512 << 20,
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

	// Build a real custom layer via Driver.BuildLayer.
	setupScript := `
mkdir -p /opt/corp/bin
cat <<'EOF' >/opt/corp/bin/corp-cli
#!/bin/sh
echo "corp-cli-built-ok"
EOF
chmod 0755 /opt/corp/bin/corp-cli
echo 'export CORP_SKEL=enabled' >/etc/skel/.corprc
`
	runSh, setupSh, envDropIn := node.BuildScripts(nil, setupScript, map[string]string{
		"CORP_TOOL": "ready",
		"PATH":      "/opt/corp/bin:$PATH",
	})
	bootFiles := map[string][]byte{
		"/etc/agenthold/build-run.sh":              []byte(runSh),
		"/etc/agenthold/build-setup.sh":            []byte(setupSh),
		"/etc/agenthold/image.env.d/10-custom.env": []byte(envDropIn),
	}

	var buildLogs []string
	out, err := d.BuildLayer(ctx, BuildLayerOptions{
		BuildID:    "kvm_" + strconv.Itoa(os.Getpid()),
		BootFiles:  bootFiles,
		CPUs:       1,
		MemoryMB:   1024,
		DiskGB:     2,
		TunnelPort: port,
		SetToken:   setToken,
		WaitSession: func(ctx context.Context, agentID string) (*tunnel.Session, error) {
			return hub.Wait(ctx, agentID)
		},
		OnLog: func(line string) { buildLogs = append(buildLogs, line) },
	})
	if err != nil {
		t.Fatalf("BuildLayer: %v\nlogs:\n%s", err, strings.Join(buildLogs, "\n"))
	}
	defer out.Cleanup()

	if out.RawSize == 0 || out.ZstdSize == 0 || out.RawSize >= 256<<20 {
		t.Fatalf("unexpected layer sizes: raw=%d zstd=%d", out.RawSize, out.ZstdSize)
	}
	t.Logf("built custom layer: raw=%.2f MiB zstd=%.2f MiB", float64(out.RawSize)/(1<<20), float64(out.ZstdSize)/(1<<20))

	// Install from the compressed stream into LayerStore (same path as an edge node).
	ref := "image:img_kvm:1"
	zf, err := os.Open(out.ZstdPath)
	if err != nil {
		t.Fatal(err)
	}
	meta := node.LayerArtifact{
		Ref:        ref,
		SHA256:     out.RawSHA256,
		Size:       out.RawSize,
		ZstdSHA256: out.ZstdSHA256,
		ZstdSize:   out.ZstdSize,
		Subdir:     "upper",
	}
	if err := layerStore.InstallFromZstd(meta, zf); err != nil {
		_ = zf.Close()
		t.Fatalf("InstallFromZstd: %v", err)
	}
	_ = zf.Close()

	// Boot a fresh agent VM on the built custom layer.
	agentID := "ag_bld_" + strconv.Itoa(os.Getpid())
	agentTok := "kvm-custom-agent-token"
	sum := sha256.Sum256([]byte(agentTok))
	_ = setToken(ctx, agentID, hex.EncodeToString(sum[:]))

	id, err := d.Create(ctx, sandbox.Spec{
		AgentID:   agentID,
		Layers:    []string{ref},
		Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512},
	})
	if err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	t.Cleanup(func() {
		c, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		if err := d.Destroy(c, id, sandbox.DestroyOpts{}); err != nil {
			t.Error("cleanup:", err)
		}
	})
	if err := d.Start(ctx, id, sandbox.Boot{AgentID: agentID, Token: agentTok, TunnelPort: port}); err != nil {
		log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
		t.Fatalf("Start agent: %v\n%s", err, log)
	}
	sess, err := hub.Wait(ctx, agentID)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
		t.Fatalf("Wait agent: %v\n%s", err, log)
	}

	run := func(command string) string {
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
		b, err := io.ReadAll(io.LimitReader(p.Stdout, 1<<20))
		if err != nil {
			t.Fatalf("%s: %v stderr=%s", command, err, stderr.String())
		}
		<-p.Done()
		code, err := p.ExitCode()
		if err != nil || code != 0 {
			t.Fatalf("cmd %q failed (code=%d err=%v)\nstdout:\n%s\nstderr:\n%s", command, code, err, b, stderr.String())
		}
		return strings.TrimSpace(string(b))
	}

	if got := run("corp-cli"); got != "corp-cli-built-ok" {
		t.Fatalf("corp-cli = %q", got)
	}
	if got := run(`printf '%s|%s' "$CORP_TOOL" "$PATH"`); !strings.HasPrefix(got, "ready|/opt/corp/bin:") {
		t.Fatalf("env = %q", got)
	}
	if got := run("cat /home/agent/.corprc && stat -c '%U:%G' /home/agent/.corprc"); got != "export CORP_SKEL=enabled\nagent:agent" {
		t.Fatalf("skel = %q", got)
	}
	if got := run("test ! -e /etc/agenthold/build-run.sh && test ! -e /etc/agenthold/build-setup.sh && echo clean"); got != "clean" {
		t.Fatalf("builder scripts leaked into layer: %q", got)
	}
}
