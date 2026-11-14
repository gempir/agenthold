package sandboxd_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

func setup(t *testing.T) (*tunnel.Session, string) {
	t.Helper()
	hub := tunnel.NewHub(func(_ context.Context, agentID, token string) error {
		if agentID != "ag_1" || token != "secret" {
			return errors.New("bad token")
		}
		return nil
	}, nil)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	d, err := sandboxd.New(sandboxd.Config{
		Boot: sandboxd.Boot{AgentID: "ag_1", Token: "secret", TunnelURL: "ws" + strings.TrimPrefix(srv.URL, "http")},
		Home: home, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	wctx, wcancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer wcancel()
	s, err := hub.Wait(wctx, "ag_1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Hello.Version != "test" || s.Hello.Home != home {
		t.Fatalf("hello = %+v", s.Hello)
	}
	return s, home
}

func TestRejectsBadToken(t *testing.T) {
	hub := tunnel.NewHub(func(context.Context, string, string) error { return errors.New("no") }, nil)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := tunnel.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "ag_1", "wrong", nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestFiles(t *testing.T) {
	s, home := setup(t)
	ctx := t.Context()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("0123456789"), 300_000) // 3 MB: several frames
	if err := s.WriteFile(ctx, "projects/a/big.bin", 0o600, bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, "projects/a/big.bin")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", fi, err)
	}
	var out bytes.Buffer
	var size int64
	if err := s.ReadFile(ctx, "~/projects/a/big.bin", func(n int64) { size = n }, &out); err != nil {
		t.Fatal(err)
	}
	if size != int64(len(big)) || !bytes.Equal(out.Bytes(), big) {
		t.Fatalf("read back %d bytes (size %d)", out.Len(), size)
	}
	if err := s.Mkdir(ctx, "projects/b", false); err != nil {
		t.Fatal(err)
	}
	ls, err := s.List(ctx, "projects")
	if err != nil {
		t.Fatal(err)
	}
	if ls.Path != filepath.Join(home, "projects") || len(ls.Entries) != 2 || !ls.Entries[0].IsDir || ls.Entries[0].Name != "a" {
		t.Fatalf("list = %+v", ls)
	}
	var rerr *tunnel.RemoteError
	if err := s.ReadFile(ctx, "missing.txt", nil, &out); !errors.As(err, &rerr) {
		t.Fatalf("missing file err = %v", err)
	}
	if err := s.Remove(ctx, "projects", false); err == nil {
		t.Fatal("non-recursive remove of a non-empty dir succeeded")
	}
	if err := s.Remove(ctx, "projects", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "~", true); err == nil {
		t.Fatal("removing home succeeded")
	}
}

func TestPTY(t *testing.T) {
	s, home := setup(t)
	term, err := s.PTY(t.Context(), tunnel.Open{
		Cmd:  []string{"/bin/sh", "-c", `stty size; pwd; read line; echo "got:$line"; exit 3`},
		Cols: 120, Rows: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err := term.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := term.ReadOutput(&out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"40 120", home, "got:hello"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q does not contain %q", got, want)
		}
	}
	if code != 3 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestReconnectsAfterDrop(t *testing.T) {
	s, _ := setup(t)
	hub := s.Hub()
	hub.Disconnect("ag_1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		s2, err := hub.Wait(ctx, "ag_1")
		if err != nil {
			t.Fatal(err)
		}
		if s2 != s {
			if err := s2.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
