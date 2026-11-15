//go:build docker

package docker

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// buildSandboxd cross-compiles sandboxd for the Docker host's Linux.
func buildSandboxd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "sandboxd"), "github.com/gempir/agenthold/cmd/sandboxd")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sandboxd: %v\n%s", err, out)
	}
	return dir
}

// TestTunnelAcceptance is the M3 acceptance check: sandboxd, injected into
// stock images, dials the server; a shell and file access work through it.
func TestTunnelAcceptance(t *testing.T) {
	for _, image := range []string{"debian:bookworm-slim", "debian:trixie"} {
		t.Run(image, func(t *testing.T) { testTunnel(t, image) })
	}
}

func testTunnel(t *testing.T, image string) {
	bin := buildSandboxd(t)
	ctx := t.Context()

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", CreatedAt: 1, UpdatedAt: 1})

	// The tunnel listener runs on the host; sandboxes reach it via host.docker.internal.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctxTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d, err := New(ctxTimeout, Config{
		NamePrefix: "agenthold-test", NetworkPool: mustPrefix("10.215.0.0/16"), SubnetBits: 28,
		SandboxdSource: bin, TunnelHost: "host.docker.internal", ExternalNetwork: true,
	})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	var m *sandbox.Manager
	hub := tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return m.AuthenticateTunnel(ctx, agentID, token)
	}, nil)
	srv := &http.Server{Handler: hub, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	m = sandbox.NewManagerWithOptions(st, d, sandbox.BaseSpec, sandbox.Options{Tunnel: hub, TunnelPort: port, ConnectTimeout: 30 * time.Second}, nil)
	defer m.Close()

	agentID := strings.ToLower(id.New("ag"))
	var provision func()
	if err := st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, OwnerID: "usr_1", Name: "a", Image: image,
			Harness: "claude-code", Cpus: 1, MemoryMb: 256, IdleMinutes: 15, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			return err
		}
		provision, err = m.Provision(ctx, q, agentID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	provision()
	m.Wait()
	t.Cleanup(func() {
		_ = d.Destroy(context.Background(), sandbox.ID(d.containerName(agentID)), sandbox.DestroyOpts{})
	})
	if sb, _ := st.GetSandbox(ctx, agentID); sb.State != "running" {
		t.Fatalf("state %s: %s", sb.State, sb.Error)
	}
	s, ok := hub.Session(agentID)
	if !ok {
		t.Fatal("no session")
	}

	// Shell.
	term, err := s.PTY(ctx, tunnel.Open{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = term.Write([]byte("echo marker-$((6*7)); . /etc/os-release; echo \"os=$ID\"; whoami; pwd; exit 0\n"))
	var out bytes.Buffer
	code, err := term.ReadOutput(&out)
	if err != nil {
		t.Fatalf("pty: %v (output %q)", err, out.String())
	}
	for _, want := range []string{"marker-42", "os=debian", "root", "/home/agent"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("shell output %q lacks %q", out.String(), want)
		}
	}
	if code != 0 {
		t.Errorf("exit code %d", code)
	}

	// Files survive a sleep.
	if err := s.WriteFile(ctx, "notes/hello.txt", 0o644, strings.NewReader("hi from the test")); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.Start(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	s, ok = hub.Session(agentID)
	if !ok {
		sb, _ := st.GetSandbox(ctx, agentID)
		t.Fatalf("no session after restart: %s %s", sb.State, sb.Error)
	}
	var file bytes.Buffer
	if err := s.ReadFile(ctx, "notes/hello.txt", nil, &file); err != nil || file.String() != "hi from the test" {
		t.Fatalf("read after restart: %q %v", file.String(), err)
	}
	ls, err := s.List(ctx, "/home/agent")
	if err != nil || len(ls.Entries) == 0 {
		t.Fatalf("list: %+v %v", ls, err)
	}

	// The old boot token is no longer accepted.
	if err := m.AuthenticateTunnel(ctx, agentID, "stale"); err == nil {
		t.Fatal("stale token accepted")
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
