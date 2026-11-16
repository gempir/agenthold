package enrollment

import (
	"context"
	"crypto/tls"
	"net"
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
