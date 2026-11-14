package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Handler serves one stream opened by the server. The Open frame has already been read.
type Handler func(ctx context.Context, stream net.Conn, o Open)

// Opener opens a stream to the other side and sends o as its first frame.
type Opener func(ctx context.Context, o Open) (net.Conn, error)

// Dial connects to the server's tunnel endpoint and serves streams until the
// connection ends or ctx is cancelled. onConnect (optional) receives an Opener
// for streams to the server while connected, and nil after disconnecting.
func Dial(ctx context.Context, url, agentID, token string, handle Handler, onConnect ...func(Opener)) error {
	hdr := http.Header{}
	hdr.Set(HeaderAgent, agentID)
	hdr.Set("Authorization", "Bearer "+token)
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr, Subprotocols: []string{Subprotocol}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	ws.SetReadLimit(-1)
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	mux, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer mux.Close()
	go func() {
		select {
		case <-ctx.Done():
			_ = mux.Close()
		case <-mux.CloseChan():
		}
	}()
	for _, fn := range onConnect {
		fn(muxOpener(mux))
		defer fn(nil)
	}
	for {
		st, err := mux.AcceptStream()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("tunnel closed: %w", err)
		}
		go func() {
			defer st.Close()
			var o Open
			if err := ReadJSON(st, &o); err != nil {
				return
			}
			handle(ctx, st, o)
		}()
	}
}

func muxOpener(mux *yamux.Session) Opener {
	return func(ctx context.Context, o Open) (net.Conn, error) {
		type res struct {
			c   net.Conn
			err error
		}
		ch := make(chan res, 1)
		go func() {
			st, err := mux.OpenStream()
			if err != nil {
				ch <- res{nil, err}
				return
			}
			if err := WriteJSON(st, o); err != nil {
				_ = st.Close()
				ch <- res{nil, err}
				return
			}
			ch <- res{st, nil}
		}()
		select {
		case r := <-ch:
			return r.c, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
