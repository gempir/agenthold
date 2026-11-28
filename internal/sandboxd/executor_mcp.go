package sandboxd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// ExecutorSocket is where the Executor MCP unix socket lives, relative to the
// agent's home. The harness starts "sandboxd executor-mcp" which connects here.
const ExecutorSocket = ".agenthold/executor.sock"

type executorSessionHeader struct {
	Session string `json:"_agenthold_session"`
}

// ServeExecutorMCP runs the Executor MCP stdio bridge inside the sandbox
// ("sandboxd executor-mcp"). It connects stdin/stdout to sandboxd's local
// executor socket, which relays the JSON-RPC stream through the authenticated
// tunnel to the acting caller's Executor instance on the controller.
func ServeExecutorMCP(in io.Reader, out io.Writer) error {
	home, _ := os.UserHomeDir()
	if h := os.Getenv("HOME"); h != "" {
		home = h
	}
	sock := filepath.Join(home, ExecutorSocket)
	if s := os.Getenv("AGENTHOLD_EXECUTOR_SOCKET"); s != "" {
		sock = s
	}
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return fmt.Errorf("agenthold executor unavailable: %w", err)
	}
	defer c.Close()

	if sess := strings.TrimSpace(os.Getenv("AGENTHOLD_SESSION")); sess != "" {
		if hdr, err := json.Marshal(executorSessionHeader{Session: sess}); err == nil {
			_, _ = c.Write(append(hdr, '\n'))
		}
	}

	go func() {
		_, _ = io.Copy(c, in)
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, copyErr := io.Copy(out, c)
	_ = c.Close()
	return copyErr
}

func (d *Daemon) executorSocketPath() string {
	if d.cfg.PlatformSocket != "" {
		return strings.TrimSuffix(d.cfg.PlatformSocket, ".sock") + "-exec.sock"
	}
	return filepath.Join(d.acct.home, ExecutorSocket)
}

// serveExecutor listens on the Executor unix socket and bridges each client
// onto a tunnel.OpExecutor stream to the controller.
func (d *Daemon) serveExecutor(ctx context.Context) {
	path := d.executorSocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.log.Warn("executor socket", "err", err)
		return
	}
	d.own(filepath.Dir(path))
	_ = os.Remove(path)
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		d.log.Warn("executor socket", "err", err)
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
		go d.executorConn(ctx, c)
	}
}

func (d *Daemon) executorConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	open := d.opener.get()
	if open == nil {
		return
	}
	br := bufio.NewReader(c)
	var sessionID string
	var src io.Reader = br
	// Peek an optional session header sent by ServeExecutorMCP so the controller
	// can attribute Executor MCP calls to the exact session and acting user.
	if prefix, err := br.Peek(22); err == nil && bytes.HasPrefix(prefix, []byte(`{"_agenthold_session":`)) {
		if line, err := br.ReadBytes('\n'); err == nil {
			var hdr executorSessionHeader
			if json.Unmarshal(bytes.TrimSpace(line), &hdr) == nil && hdr.Session != "" {
				sessionID = hdr.Session
			} else {
				src = io.MultiReader(bytes.NewReader(line), br)
			}
		}
	}
	st, err := open(ctx, tunnel.Open{Op: tunnel.OpExecutor, Session: sessionID})
	if err != nil {
		d.log.Warn("open executor stream", "err", err)
		return
	}
	defer st.Close()

	stop := context.AfterFunc(ctx, func() {
		if dl, ok := st.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = dl.SetReadDeadline(time.Now())
		}
		_ = st.Close()
		_ = c.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(st, src)
		if cw, ok := st.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = st.Close()
		}
	}()
	_, _ = io.Copy(c, st)
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	_ = st.Close()
	_ = c.Close()
	wg.Wait()
}
