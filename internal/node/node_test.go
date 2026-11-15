package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/tunnel"
)

func TestLocalLifecycleAndToken(t *testing.T) {
	ctx := context.Background()
	n := NewLocal(fake.New(t.TempDir()), nil)
	id, err := n.Create(ctx, sandbox.Spec{AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err = n.Start(ctx, id, sandbox.Boot{AgentID: "agent"}); err != nil {
		t.Fatal(err)
	}
	st, err := n.Status(ctx, id)
	if err != nil || st.State != sandbox.StateRunning {
		t.Fatalf("status %v %v", st, err)
	}
	if err = n.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, err = n.Status(ctx, id)
	if err != nil || st.State != sandbox.StateStopped {
		t.Fatalf("status %v %v", st, err)
	}
	sum := sha256.Sum256([]byte("token"))
	if err = n.SetTunnelToken(ctx, "agent", hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err = n.AuthenticateTunnel(ctx, "agent", "token"); err != nil {
		t.Fatal(err)
	}
	if err = n.AuthenticateTunnel(ctx, "agent", "wrong"); err == nil {
		t.Fatal("accepted wrong token")
	}
	_ = n.SetTunnelToken(ctx, "agent", "")
	if err = n.AuthenticateTunnel(ctx, "agent", "token"); err == nil {
		t.Fatal("accepted revoked token")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = n.Capacity(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err = n.NextIncoming(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConfigPushAndIntercept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	n := NewLocal(fake.New(t.TempDir()), nil)
	cfg := []AgentConfig{{ID: "a", Subnet: netip.MustParsePrefix("10.1.0.0/24"), Injections: []secrets.Injection{{Value: "secret"}}, Intercepts: []Intercept{{Host: "example.org", Method: "POST", Path: "/refresh", Placeholder: "placeholder"}}}}
	if err := n.SetAgentConfigs(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg[0].Injections[0].Value = "changed"
	a, err := n.ProxyAgents().AgentForSource(ctx, netip.MustParseAddr("10.1.0.2"))
	if err != nil || a == nil || a.Injections[0].Value != "secret" {
		t.Fatalf("snapshot: %#v %v", a, err)
	}
	done := make(chan error, 1)
	go func() {
		info, stream, err := n.NextIntercept(ctx)
		if err != nil {
			done <- err
			return
		}
		defer stream.Close()
		if info.AgentID != "a" || info.Index != 0 {
			done <- errors.New("bad intercept metadata")
			return
		}
		req, err := http.ReadRequest(bufio.NewReader(stream))
		if err != nil {
			done <- err
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || !bytes.Equal(body, []byte("placeholder")) {
			done <- errors.New("bad request body")
			return
		}
		if err = json.NewEncoder(stream).Encode(interceptResult{Handled: true}); err != nil {
			done <- err
			return
		}
		resp := &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("ok"))), ContentLength: 2}
		done <- resp.Write(stream)
	}()
	req := httptest.NewRequest(http.MethodPost, "https://example.org/refresh", bytes.NewBufferString("placeholder")).WithContext(ctx)
	w := httptest.NewRecorder()
	if !a.Responders[0].Handle(w, req) || w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("response %d %q", w.Code, w.Body.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err = n.SetAgentConfigs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a, err = n.ProxyAgents().AgentForSource(ctx, netip.MustParseAddr("10.1.0.2"))
	if err != nil || a != nil {
		t.Fatal("stale config", a, err)
	}
}

func TestCanceledNodeOperations(t *testing.T) {
	n := NewLocal(fake.New(t.TempDir()), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := n.NextIntercept(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("NextIntercept: %v", err)
	}
	if err := n.WaitConnected(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitConnected: %v", err)
	}
	if _, err := n.OpenStream(ctx, "a", tunnel.Open{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := n.SetAgentConfigs(context.Background(), []AgentConfig{{ID: "a", Subnet: netip.MustParsePrefix("10.1.0.0/24"), Intercepts: []Intercept{{Host: "example.org", Method: "POST", Path: "/", Placeholder: "placeholder"}}}}); err != nil {
		t.Fatal(err)
	}
	a, err := n.ProxyAgents().AgentForSource(context.Background(), netip.MustParseAddr("10.1.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://example.org/", bytes.NewBufferString("placeholder")).WithContext(ctx)
	if a.Responders[0].Handle(httptest.NewRecorder(), req) {
		t.Fatal("canceled request should not be handled")
	}
}
