//go:build linux && kvm

package firecracker

import (
	"context"
	"encoding/json"
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
	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

// TestAppServicesKVM boots an agent VM and an app VM, deploys a small Python
// web app from one to the other the way deploy_app does (tar through the
// host, release directory, symlink swap, service), fetches it through the
// tunnel's dial op, then checks that the service comes back by itself from
// the boot configuration after a stop and start, and that a killed service
// process is restarted. Same artifacts and isolation as TestJailedLifecycle.
func TestAppServicesKVM(t *testing.T) {
	for _, key := range []string{"BASE", "KERNEL", "FIRECRACKER", "JAILER"} {
		if os.Getenv("AGENTHOLD_KVM_"+key) == "" {
			if os.Getenv("AGENTHOLD_REQUIRE_KVM") == "1" {
				t.Fatal("required jailed VM artifact environment is missing: AGENTHOLD_KVM_" + key)
			}
			t.Skip("set AGENTHOLD_KVM_BASE/KERNEL/FIRECRACKER/JAILER to run the app services VM test")
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
		cmd := exec.Command(exe, "-test.run=^TestAppServicesKVM$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_KVM_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated app services VM test: %v\n%s", err, out)
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
	dir, err := os.MkdirTemp("/tmp", "ahapp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{
		DataDir: dir, JailDir: filepath.Join(dir, "j"), BaseImagePath: os.Getenv("AGENTHOLD_KVM_BASE"),
		KernelPath: os.Getenv("AGENTHOLD_KVM_KERNEL"), FirecrackerPath: os.Getenv("AGENTHOLD_KVM_FIRECRACKER"),
		JailerPath: os.Getenv("AGENTHOLD_KVM_JAILER"), NetworkPool: netip.MustParsePrefix("10.215.0.0/24"),
		OverlaySizeBytes: 256 << 20, HomeSizeBytes: 256 << 20, StopTimeout: 10 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	const token = "kvm-apps-token"
	hub := tunnel.NewHub(func(_ context.Context, agent, got string) error {
		if !strings.HasPrefix(agent, "ag_kvmapp_") || got != token {
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
	pid := strconv.Itoa(os.Getpid())
	machine := func(name string) (sandbox.ID, sandbox.Boot) {
		t.Helper()
		spec := sandbox.Spec{AgentID: "ag_kvmapp_" + name + "_" + pid, Resources: sandbox.Resources{CPUs: 1, MemoryMB: 512}}
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
		return id, sandbox.Boot{AgentID: spec.AgentID, Token: token, TunnelPort: port}
	}
	start := func(id sandbox.ID, boot sandbox.Boot) *tunnel.Session {
		t.Helper()
		if err := d.Start(ctx, id, boot); err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("start: %v\n%s", err, log)
		}
		s, err := hub.Wait(ctx, boot.AgentID)
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(d.dir(id), "serial.log"))
			t.Fatalf("wait tunnel: %v\n%s", err, log)
		}
		return s
	}
	execIn := func(s *tunnel.Session, command string) tunnel.ExecReply {
		t.Helper()
		r, err := s.Exec(ctx, []string{"/bin/sh", "-lc", command}, "", nil, 60, 0)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		return r
	}

	agentID, agentBoot := machine("agent")
	appID, appBoot := machine("app")
	agent := start(agentID, agentBoot)
	app := start(appID, appBoot)

	// The agent writes a tiny app.
	if r := execIn(agent, "mkdir -p ~/site/.git && echo kvm-app-v1 > ~/site/index.html && echo ref > ~/site/.git/HEAD && whoami"); r.ExitCode != 0 || strings.TrimSpace(r.Output) != "agent" {
		t.Fatalf("write site: %+v", r)
	}
	// Deploy: tar out of the agent, through the host, into a release of the app.
	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(agent.Tar(ctx, "site", []string{".git"}, 0, pw)) }()
	if err := app.Untar(ctx, "app/releases/1", 0, pr); err != nil {
		t.Fatal("untar:", err)
	}
	if err := app.Symlink(ctx, "app/current", "releases/1"); err != nil {
		t.Fatal(err)
	}
	const appPort = 18090
	specs := []tunnel.ServiceSpec{{
		Name: "app", Command: "exec python3 -u -m http.server $PORT --bind 127.0.0.1",
		Workdir: "app/current", Env: map[string]string{"PORT": strconv.Itoa(appPort)}, Enabled: true,
	}}
	if _, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceApply, Specs: specs}); err != nil {
		t.Fatal(err)
	}
	fetch := func(what string) {
		t.Helper()
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return app.Dial(ctx, appPort)
		}}}
		deadline := time.Now().Add(90 * time.Second)
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://app/index.html", nil)
			res, err := client.Do(req)
			if err == nil {
				body, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				if strings.TrimSpace(string(body)) == "kvm-app-v1" {
					return
				}
				err = errors.New("unexpected body " + string(body))
			}
			if time.Now().After(deadline) {
				var logs strings.Builder
				_ = app.ServiceLogs(ctx, "app", 50, false, &logs, 0)
				t.Fatalf("%s: app unreachable through dial: %v\nservice log:\n%s", what, err, logs.String())
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	fetch("after deploy")
	if r := execIn(app, "test -e ~/app/current/.git && echo leaked || echo clean"); strings.TrimSpace(r.Output) != "clean" {
		t.Fatalf(".git deployed: %q", r.Output)
	}
	status := func() tunnel.ServiceState {
		t.Helper()
		sts, err := app.Services(ctx, tunnel.ServiceRequest{Action: tunnel.ServiceStatus})
		if err != nil || len(sts) != 1 {
			t.Fatalf("status: %+v %v", sts, err)
		}
		return sts[0]
	}
	st := status()
	if st.State != tunnel.ServiceRunning || st.Pid == 0 {
		t.Fatalf("service = %+v", st)
	}
	// It runs as the agent account in its own cgroup.
	if r := execIn(app, "cat /proc/"+strconv.Itoa(st.Pid)+"/cgroup; stat -c %U /proc/"+strconv.Itoa(st.Pid)); !strings.Contains(r.Output, "/agenthold-svc-app") || !strings.Contains(r.Output, "agent") {
		t.Fatalf("service process: %q", r.Output)
	}

	// Stop and start the app VM with the services in its boot files (as the
	// controller delivers them): the service comes back without being asked.
	if err := d.Stop(ctx, appID); err != nil {
		t.Fatal(err)
	}
	waitGone := time.Now().Add(30 * time.Second)
	for {
		if _, ok := hub.Session(appBoot.AgentID); !ok || time.Now().After(waitGone) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := json.Marshal(specs)
	appBoot.Files = map[string][]byte{sandboxd.ServicesFile: b}
	app = start(appID, appBoot)
	fetch("after a cold boot")

	// A killed service process is restarted by the supervisor.
	first := status()
	if r := execIn(app, "kill -9 "+strconv.Itoa(first.Pid)+" && echo killed"); strings.TrimSpace(r.Output) != "killed" {
		t.Fatalf("kill: %+v", r)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		st = status()
		if st.State == tunnel.ServiceRunning && st.Pid != first.Pid && st.Restarts > first.Restarts {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not restarted: %+v (was %+v)", st, first)
		}
		time.Sleep(200 * time.Millisecond)
	}
	fetch("after the process was killed")
	t.Log("deploy across VMs, dial, boot-config services and supervisor restart passed")
}
