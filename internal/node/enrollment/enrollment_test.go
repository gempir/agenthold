package enrollment_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/store"
)

type controller struct {
	cluster  *node.Cluster
	identity *enrollment.Identity
	addr     string
	peers    chan enrollment.Peer
}

// startController serves enrollment on loopback with the cluster's database
// as registry. Accepted connections are reported on peers and then closed.
func startController(t *testing.T) *controller {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	identity, err := enrollment.LoadOrCreateIdentity(filepath.Join(t.TempDir(), "controller.pem"), "test-controller")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cluster, err := node.NewCluster(ctx, node.ClusterConfig{Store: st, Endpoint: ln.Addr().String(), Fingerprint: identity.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	c := &controller{cluster: cluster, identity: identity, addr: ln.Addr().String(), peers: make(chan enrollment.Peer, 8)}
	srv := &enrollment.Server{Identity: identity, Registry: cluster, Handle: func(_ context.Context, p enrollment.Peer, conn net.Conn) {
		c.peers <- p
		_ = conn.Close()
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func (c *controller) token(t *testing.T, name string, ttl time.Duration) enrollment.Token {
	t.Helper()
	jt, err := c.cluster.CreateJoinToken(t.Context(), "admin", name, ttl)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := enrollment.ParseToken(jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestJoinThenReconnectWithPinnedIdentity(t *testing.T) {
	c := startController(t)
	statePath := filepath.Join(t.TempDir(), "node", "state.json")
	conn, state, err := enrollment.Join(t.Context(), statePath, c.token(t, "", time.Hour), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	peer := <-c.peers
	if peer.ID == "" || peer.ID != state.NodeID || state.Name != "edge-1" || state.ControllerFingerprint != c.identity.Fingerprint {
		t.Fatalf("join: peer %+v state %+v", peer, state)
	}
	if fi, err := os.Stat(statePath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file must be private: %v %v", fi.Mode(), err)
	}
	loaded, err := enrollment.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = enrollment.Connect(t.Context(), loaded)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if again := <-c.peers; again.ID != peer.ID {
		t.Fatalf("reconnect authenticated as %+v, want %+v", again, peer)
	}
	infos, err := c.cluster.Nodes(t.Context())
	if err != nil || len(infos) != 1 || infos[0].ID != peer.ID || infos[0].Name != "edge-1" {
		t.Fatalf("nodes: %+v %v", infos, err)
	}
}

func TestTokenIsSingleUse(t *testing.T) {
	c := startController(t)
	tok := c.token(t, "named-by-admin", time.Hour)
	conn, state, err := enrollment.Join(t.Context(), filepath.Join(t.TempDir(), "a.json"), tok, "proposed")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	<-c.peers
	if state.Name != "named-by-admin" {
		t.Fatalf("token name must win over the proposed name, got %q", state.Name)
	}
	second := filepath.Join(t.TempDir(), "b.json")
	if _, _, err = enrollment.Join(t.Context(), second, tok, "replay"); !errors.Is(err, enrollment.ErrDenied) {
		t.Fatalf("replayed token: %v", err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("a refused join must not leave a state file behind")
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	c := startController(t)
	tok := c.token(t, "", time.Second)
	time.Sleep(2100 * time.Millisecond) // expiry has second resolution
	if _, _, err := enrollment.Join(t.Context(), filepath.Join(t.TempDir(), "s.json"), tok, "late"); !errors.Is(err, enrollment.ErrDenied) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestNodePinsControllerCertificate(t *testing.T) {
	c := startController(t)
	tok := c.token(t, "", time.Hour)
	impostor, err := enrollment.NewIdentity("impostor")
	if err != nil {
		t.Fatal(err)
	}
	forged := tok
	forged.Fingerprint = impostor.Fingerprint // a token pointing at the real address but pinning another cert
	_, _, err = enrollment.Join(t.Context(), filepath.Join(t.TempDir(), "s.json"), forged, "n")
	if err == nil || !enrollment.IsDenied(err) || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("node accepted a controller with an unpinned certificate: %v", err)
	}
	// The secret was not disclosed to the wrong controller, so the genuine token still works.
	conn, _, err := enrollment.Join(t.Context(), filepath.Join(t.TempDir(), "ok.json"), tok, "n")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestUnknownNodeCertificateIsRejected(t *testing.T) {
	c := startController(t)
	statePath := filepath.Join(t.TempDir(), "s.json")
	conn, state, err := enrollment.Join(t.Context(), statePath, c.token(t, "", time.Hour), "n")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	<-c.peers
	// Same pinned controller, but a key the controller never saw.
	other, err := enrollment.NewIdentity("other")
	if err != nil {
		t.Fatal(err)
	}
	pemData, err := other.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	forged := *state
	forged.Identity = string(pemData)
	if _, err = enrollment.Connect(t.Context(), &forged); !errors.Is(err, enrollment.ErrDenied) {
		t.Fatalf("unknown certificate: %v", err)
	}
	// A removed node is rejected too.
	if err = c.cluster.RemoveNode(t.Context(), state.NodeID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = enrollment.Connect(t.Context(), state); !errors.Is(err, enrollment.ErrDenied) {
		t.Fatalf("removed node: %v", err)
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "ahnode1_", "ahnode1_!!!", "nope_eyJ9", "ahnode1_e30"} {
		if _, err := enrollment.ParseToken(s); !errors.Is(err, enrollment.ErrInvalidToken) {
			t.Errorf("ParseToken(%q) = %v", s, err)
		}
	}
	tok, err := enrollment.NewToken("controller.example:8091", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	back, err := enrollment.ParseToken(tok.String())
	if err != nil || back != tok {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestIdentityPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id.pem")
	a, err := enrollment.LoadOrCreateIdentity(path, "x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := enrollment.LoadOrCreateIdentity(path, "x")
	if err != nil || a.Fingerprint != b.Fingerprint {
		t.Fatalf("identity changed across loads: %v", err)
	}
}
