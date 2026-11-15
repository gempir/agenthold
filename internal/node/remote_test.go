package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/tunnel"
)

type wireTestBackend struct {
	*Local
	waiting  chan struct{}
	canceled chan struct{}
}

func (b *wireTestBackend) OpenStream(ctx context.Context, _ string, _ tunnel.Open) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, c := net.Pipe()
	go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
	return a, nil
}

func (b *wireTestBackend) WaitConnected(ctx context.Context, _ string) error {
	close(b.waiting)
	<-ctx.Done()
	close(b.canceled)
	return ctx.Err()
}

func remoteFixture(t *testing.T) (*Remote, *wireTestBackend, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	local := NewLocal(fake.New(t.TempDir()), nil)
	local.SetDataDir(t.TempDir())
	backend := &wireTestBackend{Local: local, waiting: make(chan struct{}), canceled: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- ServeRemote(t.Context(), b, backend, ServeOptions{}) }()
	remote, err := AcceptRemote(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = remote.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("node server did not stop")
		}
	})
	return remote, backend, done
}

func TestRemoteLifecycleAndProxySnapshot(t *testing.T) {
	r, b, _ := remoteFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	name, err := r.Name(ctx)
	if err != nil || name != "fake" {
		t.Fatalf("name: %q %v", name, err)
	}
	id, err := r.Create(ctx, sandbox.Spec{AgentID: "remote-agent", Env: map[string]string{"TEST": "placeholder"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx, id, sandbox.Boot{AgentID: string(id), Token: "token", TunnelPort: 8090}); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status(ctx, id)
	if err != nil || status.State != sandbox.StateRunning {
		t.Fatalf("status: %+v %v", status, err)
	}
	if err = r.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = r.Rebuild(ctx, id, sandbox.Spec{AgentID: string(id)}); err != nil {
		t.Fatal(err)
	}
	if err = r.Destroy(ctx, id, sandbox.DestroyOpts{}); err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx, id, sandbox.Boot{}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("sentinel error lost: %v", err)
	}
	configs := []AgentConfig{{ID: "remote-agent", Subnet: netip.MustParsePrefix("10.0.0.0/30"), Injections: []secrets.Injection{{Value: "host-only-value"}}}}
	if err = r.SetAgentConfigs(ctx, configs); err != nil {
		t.Fatal(err)
	}
	configs[0].Injections[0].Value = "caller-mutation"
	got, err := b.ProxyAgents().AgentForSource(ctx, netip.MustParseAddr("10.0.0.2"))
	if err != nil || got == nil || got.Injections[0].Value != "host-only-value" {
		t.Fatalf("proxy snapshot not copied: %+v %v", got, err)
	}
	cap, err := r.Capacity(ctx)
	if err != nil || cap.CPUs == 0 || cap.MemoryBytes == 0 || cap.DiskBytes == 0 {
		t.Fatalf("capacity: %+v %v", cap, err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	// Running agents keep their injected secrets while the controller is away;
	// the controller pushes a fresh snapshot after reconnecting.
	got, err = b.ProxyAgents().AgentForSource(ctx, netip.MustParseAddr("10.0.0.2"))
	if err != nil || got == nil {
		t.Fatalf("proxy rules dropped on disconnect: %+v %v", got, err)
	}
	if _, err = r.Name(ctx); !errors.Is(err, sandbox.ErrUnreachable) {
		t.Fatalf("closed remote must fail fast as unreachable: %v", err)
	}
}

func TestRemoteStreamAndBackendCancellation(t *testing.T) {
	r, b, _ := remoteFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := r.OpenStream(ctx, "remote-agent", tunnel.Open{Op: tunnel.OpHarness})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data := bytes.Repeat([]byte{0, 0, 0, 4, 't', 'e', 's', 't'}, 65536)
	sent := make(chan error, 1)
	go func() { _, err := stream.Write(data); sent <- err }()
	out := make([]byte, len(data))
	if _, err = io.ReadFull(stream, out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("stream framing consumed application bytes")
	}
	if err = <-sent; err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- r.WaitConnected(waitCtx, "remote-agent") }()
	select {
	case <-b.waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stop()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-b.canceled:
	case <-ctx.Done():
		t.Fatal("remote cancellation did not reach backend")
	}
}

func TestRemoteGuestOriginatedStreams(t *testing.T) {
	r, b, _ := remoteFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	source, guest := net.Pipe()
	defer guest.Close()
	done := make(chan struct{})
	go func() {
		select {
		case b.incoming <- incomingStream{info: Incoming{AgentID: "remote-agent", Open: tunnel.Open{Op: tunnel.OpPlatform, Tool: "ask"}}, stream: source, done: done}:
		case <-ctx.Done():
			_ = source.Close()
		}
	}()
	info, controller, err := r.NextIncoming(ctx)
	if err != nil || info.AgentID != "remote-agent" || info.Open.Tool != "ask" {
		t.Fatalf("incoming: %+v %v", info, err)
	}
	go func() { _, _ = guest.Write([]byte("guest request")) }()
	buf := make([]byte, len("guest request"))
	if _, err = io.ReadFull(controller, buf); err != nil || string(buf) != "guest request" {
		t.Fatalf("guest stream: %q %v", buf, err)
	}
	_ = controller.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("guest stream ownership was not released")
	}
	a, c := net.Pipe()
	defer c.Close()
	go func() {
		select {
		case b.proxy.requests <- interceptEvent{info: InterceptRequest{AgentID: "remote-agent", Index: 7}, stream: a}:
		case <-ctx.Done():
			_ = a.Close()
		}
	}()
	intercept, response, err := r.NextIntercept(ctx)
	if err != nil || intercept.Index != 7 {
		t.Fatalf("intercept: %+v %v", intercept, err)
	}
	go func() { _, _ = response.Write([]byte("controller reply")); _ = response.Close() }()
	buf = make([]byte, len("controller reply"))
	if _, err = io.ReadFull(c, buf); err != nil || string(buf) != "controller reply" {
		t.Fatalf("intercept stream: %q %v", buf, err)
	}
}

func TestNodeWireRejectsOversizedMessage(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], nodeWireLimit+1)
	var req wireRequest
	if err := readWire(bytes.NewReader(header[:]), &req); err == nil {
		t.Fatal("accepted oversized frame")
	}
}
