package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// DialError is a failed OpDial.
type DialError struct {
	Port int
	// Refused means nothing is listening on the port.
	Refused bool
	// Unsupported means the guest's sandboxd predates OpDial.
	Unsupported bool
	Msg         string
}

func (e *DialError) Error() string {
	switch {
	case e.Refused:
		return fmt.Sprintf("nothing is listening on port %d", e.Port)
	case e.Unsupported:
		return "this machine's sandboxd is too old to serve previews; restart the machine"
	}
	return fmt.Sprintf("connect to port %d: %s", e.Port, e.Msg)
}

// Dial connects to a TCP port on the guest's loopback interface and returns
// the stream as a plain byte connection. ctx bounds only the connecting: the
// connection outlives it (an HTTP transport pools it) until it is closed.
func (s *Session) Dial(ctx context.Context, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	// Some transports (a remote node's) tie a stream to the context it was
	// opened with, so open it with one that ends only when the conn closes.
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	detach := context.AfterFunc(ctx, cancel)
	c, err := s.Open(life, Open{Op: OpDial, Port: port})
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	// Until the reply, ctx also ends a stream that is not tied to life.
	unblock := context.AfterFunc(ctx, func() { _ = c.Close() })
	var reply DialReply
	err = ReadJSON(c, &reply)
	if !unblock() || !detach() {
		_ = c.Close()
		cancel()
		return nil, ctx.Err()
	}
	fail := func(err error) (net.Conn, error) {
		_ = c.Close()
		cancel()
		return nil, err
	}
	var re *RemoteError
	switch {
	case errors.As(err, &re) && strings.HasPrefix(re.Msg, "unknown operation"):
		return fail(&DialError{Port: port, Unsupported: true, Msg: re.Msg})
	case err != nil:
		return fail(err)
	case !reply.OK:
		return fail(&DialError{Port: port, Refused: reply.Refused, Msg: reply.Error})
	}
	nc, ok := c.(net.Conn)
	if !ok {
		nc = &streamConn{ReadWriteCloser: c}
	}
	return &dialConn{Conn: nc, cancel: cancel}, nil
}

type dialConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *dialConn) Close() error {
	err := c.Conn.Close()
	c.cancel()
	return err
}

// streamConn adapts a stream without deadlines (a remote node's) to net.Conn.
type streamConn struct{ io.ReadWriteCloser }

type streamAddr struct{}

func (streamAddr) Network() string { return "agenthold-tunnel" }
func (streamAddr) String() string  { return "guest" }

func (*streamConn) LocalAddr() net.Addr              { return streamAddr{} }
func (*streamConn) RemoteAddr() net.Addr             { return streamAddr{} }
func (*streamConn) SetDeadline(time.Time) error      { return nil }
func (*streamConn) SetReadDeadline(time.Time) error  { return nil }
func (*streamConn) SetWriteDeadline(time.Time) error { return nil }
