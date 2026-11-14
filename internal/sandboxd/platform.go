package sandboxd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// PlatformSocket is where the platform socket lives, relative to the agent's
// home. The MCP server the harness starts ("sandboxd mcp") connects to it.
const PlatformSocket = ".agenthold/platform.sock"

// PlatformRequest is one platform tool call over the socket (one JSON line).
type PlatformRequest struct {
	Tool    string          `json:"tool"`
	Session string          `json:"session"`
	Args    json.RawMessage `json:"args"`
}

// PlatformResponse answers a request (one JSON line).
type PlatformResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type opener struct {
	mu sync.Mutex
	fn tunnel.Opener
}

func (o *opener) set(fn tunnel.Opener) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fn = fn
}

func (o *opener) get() tunnel.Opener {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.fn
}

// servePlatform listens on the platform socket and relays calls to the server.
func (d *Daemon) servePlatform(ctx context.Context) {
	path := d.socketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.log.Warn("platform socket", "err", err)
		return
	}
	d.own(filepath.Dir(path))
	_ = os.Remove(path)
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		d.log.Warn("platform socket", "err", err)
		return
	}
	d.own(path)
	_ = os.Chmod(path, 0o600)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go d.platformConn(ctx, c)
	}
}

func (d *Daemon) platformConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req PlatformRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(PlatformResponse{Error: "bad request"})
			continue
		}
		_ = enc.Encode(d.platformCall(ctx, req))
	}
}

func (d *Daemon) platformCall(ctx context.Context, req PlatformRequest) PlatformResponse {
	open := d.opener.get()
	if open == nil {
		return PlatformResponse{Error: "not connected to agenthold"}
	}
	// ask() can wait for another agent's whole turn.
	ctx, cancel := context.WithTimeout(ctx, 35*time.Minute)
	defer cancel()
	st, err := open(ctx, tunnel.Open{Op: tunnel.OpPlatform, Tool: req.Tool, Session: req.Session, Args: req.Args})
	if err != nil {
		return PlatformResponse{Error: err.Error()}
	}
	defer st.Close()
	go func() {
		<-ctx.Done()
		_ = st.Close()
	}()
	var out json.RawMessage
	if err := tunnel.ReadJSON(st, &out); err != nil {
		var re *tunnel.RemoteError
		if errors.As(err, &re) {
			return PlatformResponse{Error: re.Msg}
		}
		return PlatformResponse{Error: err.Error()}
	}
	return PlatformResponse{Result: out}
}

func (d *Daemon) socketPath() string {
	if d.cfg.PlatformSocket != "" {
		return d.cfg.PlatformSocket
	}
	return filepath.Join(d.acct.home, PlatformSocket)
}
