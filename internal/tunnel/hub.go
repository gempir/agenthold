package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Header names used by sandboxd when dialing.
const (
	HeaderAgent = "X-Agenthold-Agent"
	Subprotocol = "agenthold-tunnel.v1"
)

// ErrNotConnected is returned when an agent's sandboxd is not connected.
var ErrNotConnected = errors.New("sandbox is not connected")

// Authenticate checks a sandboxd connection attempt.
type Authenticate func(ctx context.Context, agentID, token string) error

// Hub accepts sandboxd connections and hands out sessions by agent id.
type Hub struct {
	auth Authenticate
	log  *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session
	waiters  map[string][]chan struct{}
	onChange []func(agentID string, connected bool)
	incoming IncomingHandler
}

// IncomingHandler serves a stream that sandboxd opened (platform calls).
// The Open frame has been read; s identifies the calling agent.
type IncomingHandler func(ctx context.Context, s *Session, stream net.Conn, o Open)

// HandleIncoming sets the handler for streams opened by sandboxes.
func (h *Hub) HandleIncoming(fn IncomingHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.incoming = fn
}

func (h *Hub) acceptIncoming(s *Session) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.mux.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		st, err := s.mux.AcceptStream()
		if err != nil {
			return
		}
		h.mu.Lock()
		fn := h.incoming
		h.mu.Unlock()
		go func() {
			defer st.Close()
			var o Open
			if err := ReadJSON(st, &o); err != nil || fn == nil {
				return
			}
			fn(ctx, s, st, o)
		}()
	}
}

// Session is one connected sandboxd.
type Session struct {
	AgentID     string
	Hello       Hello
	ConnectedAt time.Time
	mux         *yamux.Session
	hub         *Hub
	opener      func(context.Context, Open) (io.ReadWriteCloser, error)
}

// NewClient builds a protocol client over a stream-opening transport. The
// client has no access to the node's underlying tunnel session.
func NewClient(agentID string, opener func(context.Context, Open) (io.ReadWriteCloser, error)) *Session {
	return &Session{AgentID: agentID, opener: opener}
}

// Hub returns the hub the session belongs to.
func (s *Session) Hub() *Hub { return s.hub }

// NewHub returns a hub that authenticates connections with auth.
func NewHub(auth Authenticate, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{auth: auth, log: log, sessions: map[string]*Session{}, waiters: map[string][]chan struct{}{}}
}

// OnChange registers a callback for connects and disconnects.
func (h *Hub) OnChange(fn func(agentID string, connected bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onChange = append(h.onChange, fn)
}

// ServeHTTP accepts a sandboxd tunnel.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agentID := r.Header.Get(HeaderAgent)
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if agentID == "" || !ok || token == "" {
		http.Error(w, "missing credentials", http.StatusUnauthorized)
		return
	}
	if err := h.auth(r.Context(), agentID, token); err != nil {
		h.log.Warn("tunnel: rejected", "agent", agentID, "remote", r.RemoteAddr, "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		return
	}
	ws.SetReadLimit(-1)
	// The connection outlives the request context.
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	mux, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return
	}
	s := &Session{AgentID: agentID, ConnectedAt: time.Now(), mux: mux, hub: h}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.hello(ctx); err != nil {
		h.log.Warn("tunnel: hello failed", "agent", agentID, "err", err)
		_ = mux.Close()
		return
	}
	h.register(s)
	h.log.Info("tunnel: connected", "agent", agentID, "user", s.Hello.User, "sandboxd", s.Hello.Version)
	go h.acceptIncoming(s)
	<-mux.CloseChan()
	h.unregister(s)
	h.log.Info("tunnel: disconnected", "agent", agentID)
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = nil
	c.Logger = silentLogger()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 15 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.MaxStreamWindowSize = 1 << 20
	return c
}

func silentLogger() *yamuxLogger { return &yamuxLogger{} }

type yamuxLogger struct{}

func (*yamuxLogger) Print(...any)          {}
func (*yamuxLogger) Printf(string, ...any) {}
func (*yamuxLogger) Println(...any)        {}

func (s *Session) hello(ctx context.Context) error {
	c, err := s.Open(ctx, Open{Op: OpHello})
	if err != nil {
		return err
	}
	defer c.Close()
	if conn, ok := c.(net.Conn); ok {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	return ReadJSON(c, &s.Hello)
}

func (h *Hub) register(s *Session) {
	h.mu.Lock()
	old := h.sessions[s.AgentID]
	h.sessions[s.AgentID] = s
	ws := h.waiters[s.AgentID]
	delete(h.waiters, s.AgentID)
	cbs := append([]func(string, bool){}, h.onChange...)
	h.mu.Unlock()
	if old != nil {
		_ = old.mux.Close() // a new boot replaces the old connection
	}
	for _, w := range ws {
		close(w)
	}
	for _, cb := range cbs {
		cb(s.AgentID, true)
	}
}

func (h *Hub) unregister(s *Session) {
	h.mu.Lock()
	current := h.sessions[s.AgentID] == s
	if current {
		delete(h.sessions, s.AgentID)
	}
	cbs := append([]func(string, bool){}, h.onChange...)
	h.mu.Unlock()
	if current {
		for _, cb := range cbs {
			cb(s.AgentID, false)
		}
	}
}

// Session returns the agent's live session.
func (h *Hub) Session(agentID string) (*Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[agentID]
	return s, ok
}

// Wait blocks until the agent's sandboxd is connected or ctx ends.
func (h *Hub) Wait(ctx context.Context, agentID string) (*Session, error) {
	for {
		h.mu.Lock()
		if s, ok := h.sessions[agentID]; ok && !s.mux.IsClosed() {
			h.mu.Unlock()
			return s, nil
		}
		ch := make(chan struct{})
		h.waiters[agentID] = append(h.waiters[agentID], ch)
		h.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			h.mu.Lock()
			ws := h.waiters[agentID]
			for i, w := range ws {
				if w == ch {
					ws = append(ws[:i], ws[i+1:]...)
					break
				}
			}
			if len(ws) == 0 {
				delete(h.waiters, agentID)
			} else {
				h.waiters[agentID] = ws
			}
			h.mu.Unlock()
			return nil, fmt.Errorf("waiting for sandbox: %w", ctx.Err())
		}
	}
}

// Disconnect drops an agent's session (for example when its sandbox stops).
func (h *Hub) Disconnect(agentID string) {
	if s, ok := h.Session(agentID); ok {
		_ = s.mux.Close()
	}
}

// Open opens a channel to sandboxd.
func (s *Session) Open(ctx context.Context, o Open) (io.ReadWriteCloser, error) {
	if s.opener != nil {
		return s.opener(ctx, o)
	}
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		st, err := s.mux.OpenStream()
		if err != nil {
			ch <- result{nil, err}
			return
		}
		if err := WriteJSON(st, o); err != nil {
			_ = st.Close()
			ch <- result{nil, err}
			return
		}
		ch <- result{st, nil}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			r := <-ch
			if r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// Closed reports whether the session has ended.
func (s *Session) Closed() bool { return s.mux != nil && s.mux.IsClosed() }

// WaitConnected implements sandbox.Tunnel.
func (h *Hub) WaitConnected(ctx context.Context, agentID string) error {
	_, err := h.Wait(ctx, agentID)
	return err
}
