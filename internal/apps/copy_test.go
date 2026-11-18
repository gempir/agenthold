package apps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// fakeSource answers fs.tar with send (which writes data frames until it
// returns); fakeSink answers fs.untar by reading data frames to the end.
func fakeSession(t *testing.T, serve func(srv net.Conn, o tunnel.Open)) *tunnel.Session {
	t.Helper()
	return tunnel.NewClient("ag", func(ctx context.Context, o tunnel.Open) (io.ReadWriteCloser, error) {
		srv, cli := net.Pipe()
		go func() { defer srv.Close(); serve(srv, o) }()
		return cli, nil
	})
}

func sink(t *testing.T) *tunnel.Session {
	return fakeSession(t, func(srv net.Conn, _ tunnel.Open) {
		_ = tunnel.WriteJSON(srv, tunnel.OK{OK: true})
		if _, err := tunnel.CopyData(io.Discard, srv, nil); err != nil {
			return
		}
		_ = tunnel.WriteJSON(srv, tunnel.OK{OK: true})
	})
}

func limits(max int64) config.Apps {
	n := 3
	return config.Apps{DeployMaxBytes: max, MaxPerAgent: &n, MaxPerOwner: &n}
}

func TestRelayStopsAtTheByteCap(t *testing.T) {
	old := relayOverhead
	relayOverhead = 1 << 16
	t.Cleanup(func() { relayOverhead = old })
	endless := fakeSession(t, func(srv net.Conn, _ tunnel.Open) {
		_ = tunnel.WriteJSON(srv, tunnel.OK{OK: true})
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for tunnel.WriteFrame(srv, tunnel.FrameData, chunk) == nil {
		}
	})
	m := &Manager{Limits: limits(1 << 16)}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err := m.copy(ctx, endless, "src", nil, sink(t), "dst")
	if !errors.Is(err, errRelayTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayIdleTimeout(t *testing.T) {
	old := RelayIdle
	RelayIdle = 200 * time.Millisecond
	t.Cleanup(func() { RelayIdle = old })
	stalled := fakeSession(t, func(srv net.Conn, _ tunnel.Open) {
		_ = tunnel.WriteJSON(srv, tunnel.OK{OK: true})
		_ = tunnel.WriteFrame(srv, tunnel.FrameData, []byte("a little"))
		_, _ = io.Copy(io.Discard, srv) // then nothing until closed
	})
	m := &Manager{Limits: limits(1 << 20)}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	err := m.copy(ctx, stalled, "src", nil, sink(t), "dst")
	if err == nil || !strings.Contains(err.Error(), "nothing was copied") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the idle relay was not cut short")
	}
}

// A deploy that fails releases the app's deploy lock.
func TestDeployLockReleased(t *testing.T) {
	boom := errors.New("cannot wake")
	m := &Manager{Limits: limits(1 << 20), Wake: func(context.Context, string, func(string)) (*tunnel.Session, error) { return nil, boom }}
	p := DeployParams{App: db.Agent{ID: "ap_1", Kind: KindApp}, SourceID: "ag", SourceDir: "src", Command: "true", Port: 8080}
	for range 2 {
		if _, err := m.Deploy(t.Context(), p); !errors.Is(err, boom) {
			t.Fatalf("err = %v (want the wake error, not a busy lock)", err)
		}
	}
}
