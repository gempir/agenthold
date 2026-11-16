package enrollment

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type acceptAll struct{}

func (acceptAll) Join(context.Context, string, string, string) (Peer, error) {
	return Peer{ID: "n1", Name: "n"}, nil
}

func (acceptAll) Authenticate(context.Context, string) (Peer, error) {
	return Peer{ID: "n1", Name: "n"}, nil
}

// A node that predates download information rejects replies with fields it does not know, so the
// controller only adds them for nodes that say they understand them (hello.nodeVersion).
func TestWelcomeCarriesDownloadInfoOnlyForNodesThatAskedForIt(t *testing.T) {
	id, err := NewIdentity("controller")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := &Server{
		Identity: id, Registry: acceptAll{}, Version: "v1", DownloadURL: "https://c.example/dl/",
		Handle: func(_ context.Context, _ Peer, conn net.Conn) { _ = conn.Close() },
	}
	go func() { _ = srv.Serve(ctx, ln) }()

	node, err := NewIdentity("node")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(h hello) map[string]any {
		t.Helper()
		conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{node.Certificate}, InsecureSkipVerify: true, //nolint:gosec // test
		})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := writeFrame(conn, h); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := readFrame(conn, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	old := ask(hello{Version: protocolVersion, Kind: "connect"})
	if _, ok := old["downloadUrl"]; ok || old["ok"] != true {
		t.Fatalf("old node reply: %v", old)
	}
	cur := ask(hello{Version: protocolVersion, Kind: "connect", NodeVersion: "v1"})
	if cur["downloadUrl"] != "https://c.example/dl/" || cur["controllerVersion"] != "v1" {
		t.Fatalf("current node reply: %v", cur)
	}
}

// oldController behaves like a release that predates hello.nodeVersion: it decodes the hello
// strictly and drops the connection without a word when it has fields it does not know. With
// alwaysClose it never answers.
func oldController(t *testing.T, alwaysClose bool) (addr, fingerprint string, hellos func() int) {
	t.Helper()
	id, err := NewIdentity("old-controller")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var count atomic.Int32
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate}, ClientAuth: tls.RequireAnyClientCert}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn := tls.Server(raw, cfg)
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var h struct {
					Version int    `json:"version"`
					Kind    string `json:"kind"`
					Secret  string `json:"secret,omitempty"`
					Name    string `json:"name,omitempty"`
				}
				if readFrame(conn, &h) != nil || alwaysClose {
					count.Add(1)
					return
				}
				count.Add(1)
				_ = writeFrame(conn, struct {
					Version int    `json:"version"`
					OK      bool   `json:"ok"`
					NodeID  string `json:"nodeId"`
					Name    string `json:"name"`
				}{protocolVersion, true, "n1", "n"})
			}()
		}
	}()
	return ln.Addr().String(), id.Fingerprint, func() int { return int(count.Load()) }
}

func nodeState(t *testing.T, addr, fingerprint string) *State {
	t.Helper()
	node, err := NewIdentity("node")
	if err != nil {
		t.Fatal(err)
	}
	pemData, err := node.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	return &State{
		Endpoint: addr, ControllerFingerprint: fingerprint, Identity: string(pemData),
		DownloadURL: "https://stale.example/dl/", ControllerVersion: "v0",
	}
}

func TestNewNodeConnectsToOldController(t *testing.T) {
	addr, fp, hellos := oldController(t, false)
	s := nodeState(t, addr, fp)
	conn, err := Connect(t.Context(), s)
	if err != nil {
		t.Fatalf("a node newer than its controller must still connect: %v", err)
	}
	_ = conn.Close()
	if hellos() != 2 {
		t.Fatalf("hellos = %d, want the rejected one and the retry without nodeVersion", hellos())
	}
	if s.DownloadURL != "" || s.ControllerVersion != "" {
		t.Fatalf("an old controller serves no downloads; state still says %+v", s)
	}
}

func TestOldControllerThatNeverAnswersGetsAClearError(t *testing.T) {
	addr, fp, _ := oldController(t, true)
	_, err := Connect(t.Context(), nodeState(t, addr, fp))
	if err == nil || !strings.Contains(err.Error(), "older release than this node") {
		t.Fatalf("err = %v", err)
	}
	if IsDenied(err) {
		t.Fatal("not a denial: the node should keep retrying")
	}
}
