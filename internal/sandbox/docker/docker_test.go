//go:build docker

// Integration tests against a real Docker daemon: go test -tags docker ./internal/sandbox/docker/
// They are skipped when Docker is not reachable.
package docker

import (
	"archive/tar"
	"context"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/sandbox"
)

const testImage = "debian:bookworm-slim"

func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := New(ctx, Config{
		NamePrefix:  "agenthold-test",
		NetworkPool: netip.MustParsePrefix("10.214.0.0/16"),
		SubnetBits:  28,
		PIDsLimit:   256,
	})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	return d
}

// readFile copies a file out of a container (running or stopped).
func readFile(t *testing.T, d *Driver, ctr, path string) string {
	t.Helper()
	res, err := d.cli.CopyFromContainer(t.Context(), ctr, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		t.Fatalf("copy %s: %v", path, err)
	}
	defer res.Content.Close()
	tr := tar.NewReader(res.Content)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(tr)
	return string(b)
}

func waitState(t *testing.T, d *Driver, sid sandbox.ID, want sandbox.State) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := d.Status(t.Context(), sid)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s (%s), want %s", st.State, st.Detail, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestLifecycle(t *testing.T) {
	d := newTestDriver(t)
	ctx := t.Context()
	agentID := strings.ToLower(id.New("ag"))
	// Every boot appends a line to a file in the home volume.
	spec := sandbox.Spec{
		AgentID:    agentID,
		Image:      testImage,
		Resources:  sandbox.Resources{CPUs: 0.5, MemoryMB: 128},
		Env:        map[string]string{"AGENT": agentID},
		Entrypoint: []string{"/bin/sh", "-c", "echo boot >> /home/agent/boots.log; exec sleep infinity"},
	}
	sid, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Destroy(context.Background(), sid, sandbox.DestroyOpts{}) })
	waitState(t, d, sid, sandbox.StateCreated)

	if err := d.Start(ctx, sid); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, sid, sandbox.StateRunning)

	insp, err := d.cli.ContainerInspect(ctx, string(sid), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hc := insp.Container.HostConfig
	if hc.NanoCPUs != 5e8 || hc.Memory != 128<<20 || hc.PidsLimit == nil || *hc.PidsLimit != 256 {
		t.Fatalf("resources not applied: %+v", hc.Resources)
	}
	netw, err := d.cli.NetworkInspect(ctx, d.networkName(agentID), client.NetworkInspectOptions{})
	if err != nil || !netw.Network.Internal {
		t.Fatalf("network internal=%v err=%v", netw.Network.Internal, err)
	}

	if err := d.Stop(ctx, sid); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, sid, sandbox.StateStopped)
	if err := d.Start(ctx, sid); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, sid, sandbox.StateRunning)
	if got := readFile(t, d, string(sid), "/home/agent/boots.log"); got != "boot\nboot\n" {
		t.Fatalf("boots.log after restart = %q", got)
	}

	// Rebuild keeps the home volume.
	if err := d.Rebuild(ctx, sid, spec); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, sid, sandbox.StateCreated)
	if err := d.Start(ctx, sid); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, sid, sandbox.StateRunning)
	if got := readFile(t, d, string(sid), "/home/agent/boots.log"); got != "boot\nboot\nboot\n" {
		t.Fatalf("boots.log after rebuild = %q", got)
	}

	if err := d.Destroy(ctx, sid, sandbox.DestroyOpts{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.Status(ctx, sid); st.State != sandbox.StateMissing {
		t.Fatalf("after destroy: %s", st.State)
	}
	if _, err := d.cli.VolumeInspect(ctx, d.volumeName(agentID), client.VolumeInspectOptions{}); err == nil {
		t.Fatal("home volume still exists")
	}
	if err := d.Destroy(ctx, sid, sandbox.DestroyOpts{}); err != nil {
		t.Fatalf("second destroy: %v", err)
	}
}

func TestNoInternetFromSandbox(t *testing.T) {
	d := newTestDriver(t)
	ctx := t.Context()
	agentID := strings.ToLower(id.New("ag"))
	sid, err := d.Create(ctx, sandbox.Spec{
		AgentID: agentID, Image: testImage,
		// bash /dev/tcp: try to open a TCP connection to a public address.
		Entrypoint: []string{"/bin/bash", "-c", "timeout 3 bash -c 'echo > /dev/tcp/1.1.1.1/80' && echo reachable > /home/agent/net || echo blocked > /home/agent/net; exec sleep infinity"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Destroy(context.Background(), sid, sandbox.DestroyOpts{}) })
	if err := d.Start(ctx, sid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		res, err := d.cli.CopyFromContainer(ctx, string(sid), client.CopyFromContainerOptions{SourcePath: "/home/agent/net"})
		if err == nil {
			res.Content.Close()
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got := strings.TrimSpace(readFile(t, d, string(sid), "/home/agent/net")); got != "blocked" {
		t.Fatalf("internet from sandbox: %s", got)
	}
}
