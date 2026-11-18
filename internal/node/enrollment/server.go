package enrollment

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
)

const handshakeTimeout = 15 * time.Second

// ErrDenied is returned to nodes whose token or certificate is not accepted.
// It deliberately does not say which check failed.
var ErrDenied = errors.New("node not accepted by the controller")

// Peer is an authenticated node.
type Peer struct {
	ID   string
	Name string
	// Version is the agenthold version the node announced in its handshake
	// (empty for nodes of earlier releases).
	Version string
}

// Registry decides which nodes are accepted. Implementations persist nodes.
type Registry interface {
	// Join redeems a token secret (by hash) exactly once and registers a node
	// with the given certificate fingerprint. It returns ErrDenied for unknown,
	// used or expired tokens.
	Join(ctx context.Context, secretHash, fingerprint, name string) (Peer, error)
	// Authenticate looks up a registered node by certificate fingerprint.
	Authenticate(ctx context.Context, fingerprint string) (Peer, error)
}

// Server accepts node connections on a dedicated TLS listener (never the plain
// HTTP frontend). After the handshake Handle owns the connection.
type Server struct {
	Identity *Identity
	Registry Registry
	Handle   func(ctx context.Context, p Peer, conn net.Conn)
	Log      *slog.Logger
	// Version and DownloadURL tell nodes which release this controller runs and where it serves
	// that release from (its /dl/ root), so a node can fetch its base image from its controller.
	// Leave DownloadURL empty when this controller does not serve downloads.
	Version     string
	DownloadURL string
}

// Serve accepts connections until ctx ends or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{s.Identity.Certificate},
		// Node certificates are self-signed; trust comes from the pinned
		// fingerprint checked against the registry after the handshake.
		ClientAuth: tls.RequireAnyClientCert,
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := tls.Server(raw, cfg)
			peer, err := s.accept(ctx, conn)
			if err != nil {
				s.Log.Info("node connection rejected", "remote", raw.RemoteAddr().String(), "err", err)
				_ = conn.Close()
				return
			}
			s.Handle(ctx, peer, conn)
		}()
	}
}

func (s *Server) accept(ctx context.Context, conn *tls.Conn) (Peer, error) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		return Peer{}, err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) != 1 {
		return Peer{}, errors.New("node must present exactly one certificate")
	}
	fingerprint := Fingerprint(certs[0].Raw)
	var h hello
	if err := readFrame(conn, &h); err != nil {
		return Peer{}, err
	}
	reply := func(p Peer, err error) (Peer, error) {
		w := welcome{Version: protocolVersion, OK: err == nil, NodeID: p.ID, Name: p.Name}
		if err != nil {
			w.Error = ErrDenied.Error()
		} else if h.NodeVersion != "" {
			w.ControllerVersion, w.DownloadURL = s.Version, s.DownloadURL
		}
		if werr := writeFrame(conn, w); werr != nil && err == nil {
			err = werr
		}
		if err != nil {
			return Peer{}, err
		}
		_ = conn.SetDeadline(time.Time{})
		p.Version = cleanVersion(h.NodeVersion)
		return p, nil
	}
	if h.Version != protocolVersion {
		return reply(Peer{}, errors.New("unsupported node protocol version"))
	}
	switch h.Kind {
	case "join":
		name := cleanName(h.Name)
		if h.Secret == "" || name == "" {
			return reply(Peer{}, errors.New("join without secret or name"))
		}
		return reply(s.Registry.Join(ctx, HashSecret(h.Secret), fingerprint, name))
	case "connect":
		return reply(s.Registry.Authenticate(ctx, fingerprint))
	default:
		return reply(Peer{}, errors.New("unknown handshake"))
	}
}

// cleanVersion bounds a version string the node announced.
func cleanVersion(s string) string {
	s = cleanName(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// cleanName returns a printable, trimmed node name of at most 64 runes.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}
