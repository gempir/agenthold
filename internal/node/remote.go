package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

const nodeWireLimit = 64 << 20

type wireRequest struct {
	Version   int                 `json:"version"`
	Method    string              `json:"method"`
	AgentID   string              `json:"agentId,omitempty"`
	ID        sandbox.ID          `json:"id,omitempty"`
	Spec      sandbox.Spec        `json:"spec,omitempty"`
	Boot      sandbox.Boot        `json:"boot,omitempty"`
	Destroy   sandbox.DestroyOpts `json:"destroy,omitempty"`
	Open      tunnel.Open         `json:"open,omitempty"`
	Configs   []AgentConfig       `json:"configs,omitempty"`
	Hash      string              `json:"hash,omitempty"`
	Intercept InterceptRequest    `json:"intercept,omitempty"`
	CACert    []byte              `json:"caCert,omitempty"`
	CAKey     []byte              `json:"caKey,omitempty"`
}
type wireResponse struct {
	Error       string         `json:"error,omitempty"`
	Code        string         `json:"code,omitempty"`
	Name        string         `json:"name,omitempty"`
	ID          sandbox.ID     `json:"id,omitempty"`
	Status      sandbox.Status `json:"status,omitempty"`
	Connected   bool           `json:"connected,omitempty"`
	Certificate []byte         `json:"certificate,omitempty"`
	Capacity    Capacity       `json:"capacity,omitempty"`
}
type remoteIncoming struct {
	info   Incoming
	stream io.ReadWriteCloser
}
type remoteIntercept struct {
	info   InterceptRequest
	stream io.ReadWriteCloser
}

// Remote is the controller's view of an edge node: every Node operation is a
// request on its own yamux stream over the one connection the node dialed
// (authentication happens before, in package enrollment). Tunnel streams are
// bridged byte for byte; node-originated streams (platform calls from
// sandboxd, login refresh intercepts) arrive as streams opened by the node.
type Remote struct {
	mux        *yamux.Session
	ctx        context.Context
	cancel     context.CancelFunc
	incoming   chan remoteIncoming
	intercepts chan remoteIntercept
}

var _ Node = (*Remote)(nil)

func wireConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.AcceptBacklog = 64
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.StreamOpenTimeout = 10 * time.Second
	cfg.StreamCloseTimeout = 5 * time.Second
	return cfg
}

// AcceptRemote establishes the controller side of an already authenticated
// node connection. Closing it makes that node's agents unreachable.
func AcceptRemote(ctx context.Context, conn net.Conn) (*Remote, error) {
	mux, err := yamux.Client(conn, wireConfig())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &Remote{mux: mux, ctx: ctx, cancel: cancel, incoming: make(chan remoteIncoming), intercepts: make(chan remoteIntercept)}
	go func() {
		select {
		case <-ctx.Done():
		case <-mux.CloseChan():
		}
		cancel()
		_ = mux.Close()
	}()
	go r.accept()
	return r, nil
}

// Close disconnects the node and closes its active streams.
func (r *Remote) Close() error { r.cancel(); return r.mux.Close() }

// Done closes when the node connection is lost.
func (r *Remote) Done() <-chan struct{} { return r.ctx.Done() }

func writeWire(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > nodeWireLimit {
		return errors.New("node message exceeds size limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = io.Copy(w, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	return err
}

func readWire(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > nodeWireLimit {
		return errors.New("invalid node message size")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func openWire(ctx context.Context, mux *yamux.Session) (*yamux.Stream, error) {
	type result struct {
		stream *yamux.Stream
		err    error
	}
	done := make(chan result)
	go func() {
		s, err := mux.OpenStream()
		select {
		case done <- result{s, err}:
		case <-ctx.Done():
			if s != nil {
				_ = s.Close()
			}
		}
	}()
	select {
	case out := <-done:
		return out.stream, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Remote) call(ctx context.Context, req wireRequest, streaming bool) (wireResponse, io.ReadWriteCloser, error) {
	var reply wireResponse
	if err := ctx.Err(); err != nil {
		return reply, nil, err
	}
	if r.ctx.Err() != nil {
		return reply, nil, fmt.Errorf("node connection closed: %w", errors.Join(sandbox.ErrUnreachable, tunnel.ErrNotConnected))
	}
	stream, err := openWire(ctx, r.mux)
	if err != nil {
		return reply, nil, wireContextError(ctx, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	success := false
	defer func() {
		if !success {
			stop()
			_ = stream.Close()
		}
	}()
	req.Version = 1
	if err = writeWire(stream, req); err != nil {
		return reply, nil, wireContextError(ctx, err)
	}
	if err = readWire(stream, &reply); err != nil {
		return reply, nil, wireContextError(ctx, err)
	}
	if reply.Error != "" {
		return reply, nil, decodeWireError(reply)
	}
	if !streaming {
		return reply, nil, nil
	}
	// This byte completes the opening handshake. Until it arrives the node can
	// watch for EOF/cancellation without consuming application data.
	if _, err = stream.Write([]byte{0}); err != nil {
		return reply, nil, wireContextError(ctx, err)
	}
	success = true
	return reply, &cancelStream{ReadWriteCloser: stream, stop: stop}, nil
}

type cancelStream struct {
	io.ReadWriteCloser
	stop func() bool
	once sync.Once
}

func (s *cancelStream) Close() error {
	s.once.Do(func() { s.stop() })
	return s.ReadWriteCloser.Close()
}

func wireContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("node connection: %w", errors.Join(sandbox.ErrUnreachable, tunnel.ErrNotConnected, err))
}

func encodeWireError(err error) wireResponse {
	out := wireResponse{}
	if err == nil {
		return out
	}
	out.Error = err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		out.Code = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		out.Code = "deadline"
	case errors.Is(err, sandbox.ErrNotFound):
		out.Code = "not_found"
	case errors.Is(err, sandbox.ErrUnreachable):
		out.Code = "unreachable"
	case errors.Is(err, tunnel.ErrNotConnected):
		out.Code = "not_connected"
	}
	return out
}

func decodeWireError(out wireResponse) error {
	var sentinel error
	switch out.Code {
	case "canceled":
		sentinel = context.Canceled
	case "deadline":
		sentinel = context.DeadlineExceeded
	case "not_found":
		sentinel = sandbox.ErrNotFound
	case "unreachable":
		sentinel = sandbox.ErrUnreachable
	case "not_connected":
		sentinel = tunnel.ErrNotConnected
	}
	if sentinel != nil {
		return fmt.Errorf("%s: %w", out.Error, sentinel)
	}
	return errors.New(out.Error)
}

func (r *Remote) accept() {
	for {
		stream, err := r.mux.AcceptStreamWithContext(r.ctx)
		if err != nil {
			return
		}
		go func() {
			var req wireRequest
			if readWire(stream, &req) != nil || req.Version != 1 {
				_ = stream.Close()
				return
			}
			switch req.Method {
			case "incoming":
				select {
				case r.incoming <- remoteIncoming{Incoming{AgentID: req.AgentID, Open: req.Open}, stream}:
				case <-r.ctx.Done():
					_ = stream.Close()
				}
			case "intercept":
				select {
				case r.intercepts <- remoteIntercept{req.Intercept, stream}:
				case <-r.ctx.Done():
					_ = stream.Close()
				}
			default:
				_ = stream.Close()
			}
		}()
	}
}

// ServeOptions are node-side hooks for pushes that are not Node methods.
type ServeOptions struct {
	// SetEgressCA receives the controller's egress CA (certificate and key,
	// PEM). It is held in memory only.
	SetEgressCA func(certPEM, keyPEM []byte) error
}

// ServeRemote serves an in-process node over its authenticated controller
// connection until it breaks. Pushed secrets and tunnel tokens stay in the
// backend's memory across reconnects (running agents keep working) and are
// never written to disk; the controller pushes a fresh snapshot after every
// reconnect. Running VMs remain owned by the local backend.
func ServeRemote(ctx context.Context, conn net.Conn, backend Node, opts ServeOptions) error {
	mux, err := yamux.Server(conn, wireConfig())
	if err != nil {
		return err
	}
	defer mux.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
		case <-mux.CloseChan():
		}
		cancel()
		_ = mux.Close()
	}()
	go forwardIncoming(ctx, mux, backend)
	go forwardIntercepts(ctx, mux, backend)
	for {
		stream, err := mux.AcceptStreamWithContext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go serveWire(ctx, stream, backend, opts)
	}
}

func serveWire(parent context.Context, stream *yamux.Stream, backend Node, opts ServeOptions) {
	defer stream.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	var req wireRequest
	if readWire(stream, &req) != nil {
		return
	}
	if req.Version != 1 {
		_ = writeWire(stream, encodeWireError(errors.New("unsupported node protocol version")))
		return
	}
	ready := make(chan bool, 1)
	// Unary calls send no more data. OpenStream sends a zero handshake byte
	// only after the reply. EOF at either stage cancels the backend operation.
	go func() {
		var marker [1]byte
		_, err := io.ReadFull(stream, marker[:])
		ok := err == nil && marker[0] == 0
		ready <- ok
		if !ok || req.Method != "open" {
			cancel()
		}
	}()
	var out wireResponse
	var err error
	switch req.Method {
	case "name":
		out.Name, err = backend.Name(ctx)
	case "create":
		out.ID, err = backend.Create(ctx, req.Spec)
	case "start":
		err = backend.Start(ctx, req.ID, req.Boot)
	case "stop":
		err = backend.Stop(ctx, req.ID)
	case "destroy":
		err = backend.Destroy(ctx, req.ID, req.Destroy)
	case "status":
		out.Status, err = backend.Status(ctx, req.ID)
	case "rebuild":
		err = backend.Rebuild(ctx, req.ID, req.Spec)
	case "clone":
		out.ID, err = backend.Clone(ctx, req.ID, req.Spec)
	case "attach":
		err = backend.Attach(ctx, req.ID)
	case "wait":
		err = backend.WaitConnected(ctx, req.AgentID)
	case "connected":
		out.Connected, err = backend.Connected(ctx, req.AgentID)
	case "disconnect":
		err = backend.Disconnect(ctx, req.AgentID)
	case "configs":
		err = backend.SetAgentConfigs(ctx, req.Configs)
	case "token":
		err = backend.SetTunnelToken(ctx, req.AgentID, req.Hash)
	case "ca":
		out.Certificate, err = backend.CACertificate(ctx)
	case "capacity":
		out.Capacity, err = backend.Capacity(ctx)
	case "egress_ca":
		if opts.SetEgressCA == nil {
			err = errors.New("node does not accept an egress CA")
		} else {
			err = opts.SetEgressCA(req.CACert, req.CAKey)
		}
	case "open":
		var target io.ReadWriteCloser
		target, err = backend.OpenStream(ctx, req.AgentID, req.Open)
		if err == nil && target == nil {
			err = errors.New("node returned nil stream")
		}
		if err != nil {
			_ = writeWire(stream, encodeWireError(err))
			return
		}
		defer target.Close()
		if writeWire(stream, out) != nil {
			return
		}
		select {
		case ok := <-ready:
			if !ok {
				return
			}
		case <-ctx.Done():
			return
		}
		bridgeWire(ctx, stream, target)
		return
	default:
		err = fmt.Errorf("unknown node operation %q", req.Method)
	}
	if err != nil {
		out = encodeWireError(err)
	}
	_ = writeWire(stream, out)
}

func bridgeWire(ctx context.Context, a, b io.ReadWriteCloser) {
	stop := context.AfterFunc(ctx, func() { _ = a.Close(); _ = b.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func forwardIncoming(ctx context.Context, mux *yamux.Session, backend Node) {
	for {
		info, source, err := backend.NextIncoming(ctx)
		if err != nil {
			return
		}
		if source == nil {
			return
		}
		go func() {
			defer source.Close()
			stream, err := openWire(ctx, mux)
			if err != nil {
				return
			}
			defer stream.Close()
			if writeWire(stream, wireRequest{Version: 1, Method: "incoming", AgentID: info.AgentID, Open: info.Open}) != nil {
				return
			}
			bridgeWire(ctx, stream, source)
		}()
	}
}

func forwardIntercepts(ctx context.Context, mux *yamux.Session, backend Node) {
	for {
		info, source, err := backend.NextIntercept(ctx)
		if err != nil {
			return
		}
		if source == nil {
			return
		}
		go func() {
			defer source.Close()
			stream, err := openWire(ctx, mux)
			if err != nil {
				return
			}
			defer stream.Close()
			if writeWire(stream, wireRequest{Version: 1, Method: "intercept", Intercept: info}) != nil {
				return
			}
			bridgeWire(ctx, stream, source)
		}()
	}
}

// Name implements Node.
func (r *Remote) Name(ctx context.Context) (string, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "name"}, false)
	return v.Name, e
}

// Create implements Node.
func (r *Remote) Create(ctx context.Context, s sandbox.Spec) (sandbox.ID, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "create", Spec: s}, false)
	return v.ID, e
}

// Start implements Node.
func (r *Remote) Start(ctx context.Context, id sandbox.ID, b sandbox.Boot) error {
	_, _, e := r.call(ctx, wireRequest{Method: "start", ID: id, Boot: b}, false)
	return e
}

// Stop implements Node.
func (r *Remote) Stop(ctx context.Context, id sandbox.ID) error {
	_, _, e := r.call(ctx, wireRequest{Method: "stop", ID: id}, false)
	return e
}

// Destroy implements Node.
func (r *Remote) Destroy(ctx context.Context, id sandbox.ID, o sandbox.DestroyOpts) error {
	_, _, e := r.call(ctx, wireRequest{Method: "destroy", ID: id, Destroy: o}, false)
	return e
}

// Status implements Node.
func (r *Remote) Status(ctx context.Context, id sandbox.ID) (sandbox.Status, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "status", ID: id}, false)
	return v.Status, e
}

// Rebuild implements Node.
func (r *Remote) Rebuild(ctx context.Context, id sandbox.ID, s sandbox.Spec) error {
	_, _, e := r.call(ctx, wireRequest{Method: "rebuild", ID: id, Spec: s}, false)
	return e
}

// Clone implements Node. The request carries the source id and the spec of
// the new sandbox.
func (r *Remote) Clone(ctx context.Context, src sandbox.ID, s sandbox.Spec) (sandbox.ID, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "clone", ID: src, Spec: s}, false)
	return v.ID, e
}

// Attach implements Node.
func (r *Remote) Attach(ctx context.Context, id sandbox.ID) error {
	_, _, e := r.call(ctx, wireRequest{Method: "attach", ID: id}, false)
	return e
}

// WaitConnected implements Node.
func (r *Remote) WaitConnected(ctx context.Context, id string) error {
	_, _, e := r.call(ctx, wireRequest{Method: "wait", AgentID: id}, false)
	return e
}

// Connected implements Node.
func (r *Remote) Connected(ctx context.Context, id string) (bool, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "connected", AgentID: id}, false)
	return v.Connected, e
}

// Disconnect implements Node.
func (r *Remote) Disconnect(ctx context.Context, id string) error {
	_, _, e := r.call(ctx, wireRequest{Method: "disconnect", AgentID: id}, false)
	return e
}

// OpenStream implements Node.
func (r *Remote) OpenStream(ctx context.Context, id string, o tunnel.Open) (io.ReadWriteCloser, error) {
	_, stream, e := r.call(ctx, wireRequest{Method: "open", AgentID: id, Open: o}, true)
	return stream, e
}

// NextIncoming implements Node.
func (r *Remote) NextIncoming(ctx context.Context) (Incoming, io.ReadWriteCloser, error) {
	select {
	case e := <-r.incoming:
		return e.info, e.stream, nil
	case <-ctx.Done():
		return Incoming{}, nil, ctx.Err()
	case <-r.ctx.Done():
		return Incoming{}, nil, tunnel.ErrNotConnected
	}
}

// SetAgentConfigs implements Node.
func (r *Remote) SetAgentConfigs(ctx context.Context, c []AgentConfig) error {
	_, _, e := r.call(ctx, wireRequest{Method: "configs", Configs: c}, false)
	return e
}

// NextIntercept implements Node.
func (r *Remote) NextIntercept(ctx context.Context) (InterceptRequest, io.ReadWriteCloser, error) {
	select {
	case e := <-r.intercepts:
		return e.info, e.stream, nil
	case <-ctx.Done():
		return InterceptRequest{}, nil, ctx.Err()
	case <-r.ctx.Done():
		return InterceptRequest{}, nil, tunnel.ErrNotConnected
	}
}

// SetTunnelToken implements Node.
func (r *Remote) SetTunnelToken(ctx context.Context, id, hash string) error {
	_, _, e := r.call(ctx, wireRequest{Method: "token", AgentID: id, Hash: hash}, false)
	return e
}

// CACertificate implements Node.
func (r *Remote) CACertificate(ctx context.Context) ([]byte, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "ca"}, false)
	return v.Certificate, e
}

// SetEgressCA pushes the controller's egress CA to the node's proxy.
func (r *Remote) SetEgressCA(ctx context.Context, certPEM, keyPEM []byte) error {
	_, _, e := r.call(ctx, wireRequest{Method: "egress_ca", CACert: certPEM, CAKey: keyPEM}, false)
	return e
}

// Capacity implements Node.
func (r *Remote) Capacity(ctx context.Context) (Capacity, error) {
	v, _, e := r.call(ctx, wireRequest{Method: "capacity"}, false)
	return v.Capacity, e
}
