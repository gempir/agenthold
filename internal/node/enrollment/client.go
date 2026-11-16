package enrollment

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/version"
)

// State is what a node persists after joining (mode 0600). Losing it means
// joining again with a new token.
type State struct {
	Endpoint              string `json:"endpoint"`
	ControllerFingerprint string `json:"controllerFingerprint"`
	NodeID                string `json:"nodeId,omitempty"`
	Name                  string `json:"name,omitempty"`
	Identity              string `json:"identity"` // PEM certificate and key
	// ControllerVersion and DownloadURL are what the controller last said about itself: the agenthold
	// version it runs and the /dl/ root it serves downloads from. They survive restarts so a node can
	// fetch its base image without reaching the controller first.
	ControllerVersion string `json:"controllerVersion,omitempty"`
	DownloadURL       string `json:"downloadUrl,omitempty"`
}

// LoadState reads a node's state file.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := ParseIdentity([]byte(s.Identity)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Save writes the state to path (mode 0600).
func (s *State) Save(path string) error { return s.save(path) }

// learn records what the controller said about itself in its welcome.
func (s *State) learn(w welcome) {
	if w.legacy {
		s.ControllerVersion, s.DownloadURL = "", ""
		return
	}
	if w.ControllerVersion != "" || w.DownloadURL != "" {
		s.ControllerVersion, s.DownloadURL = w.ControllerVersion, w.DownloadURL
	}
}

func (s *State) save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, data)
}

// Join redeems token with a freshly generated node identity, saves the state
// to statePath and returns the authenticated connection, ready for the node
// protocol. The identity is saved before the token is redeemed so that a
// crash right after joining does not lose the key the controller pinned.
func Join(ctx context.Context, statePath string, token Token, name string) (net.Conn, *State, error) {
	id, err := NewIdentity("agenthold-node")
	if err != nil {
		return nil, nil, err
	}
	pemData, err := id.MarshalPEM()
	if err != nil {
		return nil, nil, err
	}
	s := &State{Endpoint: token.Endpoint, ControllerFingerprint: token.Fingerprint, Identity: string(pemData)}
	if err := s.save(statePath); err != nil {
		return nil, nil, fmt.Errorf("save node state: %w", err)
	}
	conn, w, err := handshakeCompat(ctx, s, id, hello{Version: protocolVersion, Kind: "join", Secret: token.Secret, Name: name, NodeVersion: version.Version})
	if err != nil {
		_ = os.Remove(statePath)
		return nil, nil, err
	}
	s.NodeID, s.Name = w.NodeID, w.Name
	s.learn(w)
	if err := s.save(statePath); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("save node state: %w", err)
	}
	return conn, s, nil
}

// Connect authenticates a joined node to its controller. It refreshes s.ControllerVersion and
// s.DownloadURL from the controller's reply; saving s is up to the caller.
func Connect(ctx context.Context, s *State) (net.Conn, error) {
	id, err := ParseIdentity([]byte(s.Identity))
	if err != nil {
		return nil, err
	}
	conn, w, err := handshakeCompat(ctx, s, id, hello{Version: protocolVersion, Kind: "connect", NodeVersion: version.Version})
	if err == nil {
		s.learn(w)
	}
	return conn, err
}

// errPinMismatch is never retried away: the controller is not the one joined.
var errPinMismatch = errors.New("controller certificate does not match the pinned fingerprint")

// IsDenied reports errors after which retrying the same credentials is useless.
func IsDenied(err error) bool { return errors.Is(err, ErrDenied) || errors.Is(err, errPinMismatch) }

// errNoReply means the controller closed the connection without answering the hello. A controller
// that predates NodeVersion does so: it rejects hellos with fields it does not know.
var errNoReply = errors.New("the controller closed the connection without answering")

// handshakeCompat is handshake, but a controller that closes the connection on a hello with this
// node's version is tried once more without it, as a node of the old protocol would: it then
// connects normally and just learns nothing about downloads. Safe to repeat for a join, too: the
// old controller rejected the hello before it looked at the token.
func handshakeCompat(ctx context.Context, s *State, id *Identity, h hello) (net.Conn, welcome, error) {
	conn, w, err := handshake(ctx, s, id, h)
	if !errors.Is(err, errNoReply) || h.NodeVersion == "" {
		return conn, w, err
	}
	h.NodeVersion = ""
	conn, w, err = handshake(ctx, s, id, h)
	switch {
	case err == nil:
		w.legacy = true
		slog.Warn("the controller is older than this node (agenthold "+version.Version+") and does not say where it serves downloads from; "+
			"upgrade the controller first, nodes must not be newer than their controller", "controller", s.Endpoint)
	case errors.Is(err, errNoReply):
		err = fmt.Errorf("the controller closed the connection during the handshake, with and without this node's version (agenthold %s): "+
			"it is probably an older release than this node; upgrade the controller first: %w", version.Version, err)
	}
	return conn, w, err
}

func handshake(ctx context.Context, s *State, id *Identity, h hello) (net.Conn, welcome, error) {
	var w welcome
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.Certificate},
		// The controller certificate is self-signed; it is verified against
		// the fingerprint from the join token instead of a CA.
		InsecureSkipVerify: true, //nolint:gosec // pinned below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) != 1 || subtle.ConstantTimeCompare([]byte(Fingerprint(raw[0])), []byte(s.ControllerFingerprint)) != 1 {
				return errPinMismatch
			}
			return nil
		},
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: handshakeTimeout, KeepAlive: 30 * time.Second}, Config: cfg}
	raw, err := dialer.DialContext(ctx, "tcp", s.Endpoint)
	if err != nil {
		if errors.Is(err, errPinMismatch) {
			return nil, w, errPinMismatch
		}
		return nil, w, err
	}
	conn := raw.(*tls.Conn)
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := writeFrame(conn, h); err != nil {
		_ = conn.Close()
		return nil, w, err
	}
	if err := readFrame(conn, &w); err != nil {
		_ = conn.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
			return nil, w, fmt.Errorf("%w: %w", errNoReply, err)
		}
		return nil, w, err
	}
	if !w.OK || w.Version != protocolVersion {
		_ = conn.Close()
		return nil, w, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, w, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, w, nil
}
