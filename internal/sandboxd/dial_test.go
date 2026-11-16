package sandboxd_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

func TestDial(t *testing.T) {
	s, _ := setup(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := s.Dial(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Two requests on one connection: the stream is a plain byte pipe.
	br := bufio.NewReader(conn)
	for _, path := range []string{"/a", "/b"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://guest"+path, nil)
		if err := req.Write(conn); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(br, req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if string(body) != "hello "+path {
			t.Fatalf("body = %q", body)
		}
	}
}

func TestDialRefused(t *testing.T) {
	s, _ := setup(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	_, err := s.Dial(t.Context(), port)
	var de *tunnel.DialError
	if !errors.As(err, &de) || !de.Refused || !strings.Contains(de.Error(), "nothing is listening") {
		t.Fatalf("err = %v", err)
	}
}

func TestDialBadPort(t *testing.T) {
	s, _ := setup(t)
	if _, err := s.Dial(t.Context(), 70000); err == nil {
		t.Fatal("port 70000 accepted")
	}
	c, err := s.Open(t.Context(), tunnel.Open{Op: tunnel.OpDial})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var reply tunnel.DialReply
	if err := tunnel.ReadJSON(c, &reply); err != nil || reply.OK {
		t.Fatalf("port 0: %+v %v", reply, err)
	}
}

// A sandboxd that predates the dial op answers it with an error right away.
func TestUnknownOpFailsFast(t *testing.T) {
	s, _ := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	c, err := s.Open(ctx, tunnel.Open{Op: "no-such-op"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var ok tunnel.OK
	err = tunnel.ReadJSON(c, &ok)
	var re *tunnel.RemoteError
	if !errors.As(err, &re) || !strings.HasPrefix(re.Msg, "unknown operation") {
		t.Fatalf("err = %v", err)
	}
}
