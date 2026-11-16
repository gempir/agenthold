package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// fakeGuest answers one dial stream like sandboxd would; reply nil means an
// old sandboxd that does not know the op.
func fakeGuest(t *testing.T, reply *DialReply) (*Session, chan Open) {
	t.Helper()
	opens := make(chan Open, 1)
	s := NewClient("ag_1", func(ctx context.Context, o Open) (io.ReadWriteCloser, error) {
		srv, cli := net.Pipe()
		opens <- o
		go func() {
			defer srv.Close()
			if reply == nil {
				_ = WriteError(srv, errors.New(`unknown operation "dial"`))
				return
			}
			_ = WriteJSON(srv, *reply)
			if reply.OK {
				_, _ = io.Copy(srv, srv) // echo the raw bytes
			}
		}()
		return cli, nil
	})
	return s, opens
}

func TestDialRawPipe(t *testing.T) {
	s, opens := fakeGuest(t, &DialReply{OK: true})
	ctx, cancel := context.WithCancel(context.Background())
	c, err := s.Dial(ctx, 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if o := <-opens; o.Op != OpDial || o.Port != 8080 {
		t.Fatalf("open = %+v", o)
	}
	// The connection outlives the context it was dialled with.
	cancel()
	time.Sleep(10 * time.Millisecond)
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("read %q %v", buf, err)
	}
}

func TestDialErrors(t *testing.T) {
	s, _ := fakeGuest(t, &DialReply{Refused: true, Error: "connection refused"})
	var de *DialError
	if _, err := s.Dial(context.Background(), 1); !errors.As(err, &de) || !de.Refused || de.Port != 1 {
		t.Fatalf("refused: %v", err)
	}
	s, _ = fakeGuest(t, nil)
	if _, err := s.Dial(context.Background(), 1); !errors.As(err, &de) || !de.Unsupported {
		t.Fatalf("old sandboxd: %v", err)
	}
	if _, err := s.Dial(context.Background(), 0); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestDialCancelledDuringHandshake(t *testing.T) {
	s := NewClient("ag_1", func(ctx context.Context, o Open) (io.ReadWriteCloser, error) {
		_, cli := net.Pipe() // never answers
		return cli, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Dial(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
