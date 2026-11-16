package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

const dialTimeout = 5 * time.Second

// dial connects to a port on the guest's loopback and splices the stream to
// it. Only loopback: a preview must not become a way into other hosts.
func (d *Daemon) dial(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if o.Port < 1 || o.Port > 65535 {
		return tunnel.WriteJSON(st, tunnel.DialReply{Error: fmt.Sprintf("invalid port %d", o.Port)})
	}
	conn, err := dialLoopback(ctx, o.Port)
	if err != nil {
		return tunnel.WriteJSON(st, tunnel.DialReply{Refused: errors.Is(err, syscall.ECONNREFUSED), Error: err.Error()})
	}
	defer conn.Close()
	if err := tunnel.WriteJSON(st, tunnel.DialReply{OK: true}); err != nil {
		return err
	}
	splice(st, conn)
	return nil
}

func dialLoopback(ctx context.Context, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	p := strconv.Itoa(port)
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", p))
	if err == nil {
		return conn, nil
	}
	// Servers that bind only to ::1 (some dev servers on "localhost").
	if conn6, err6 := d.DialContext(ctx, "tcp", net.JoinHostPort("::1", p)); err6 == nil {
		return conn6, nil
	}
	return nil, err
}

// splice copies both ways until either side ends, then closes both. HTTP
// clients never half-close, so a full close on the first EOF is enough.
func splice(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
