package firecracker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnixAPI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ahapi-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"fault_message":"bad config"}`))
			return
		}
		if r.URL.Path == "/wait" {
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON content type")
		}
		seen <- r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	a := newAPI(socket)
	defer a.close()
	if err = a.action(context.Background(), "InstanceStart"); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "PUT /actions" {
		t.Fatal(got)
	}
	err = a.put(context.Background(), "/fail", map[string]string{"token": "never-echo-me"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "never-echo-me") {
		t.Fatalf("bad error %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = a.put(ctx, "/wait", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation %v", err)
	}
}
