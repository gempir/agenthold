package sandboxd_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

// harnessExit sends the harness EOF on stdin (what the server closing the
// stream does) and returns the exit frame.
func harnessExit(t *testing.T, c interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
},
) tunnel.Exit {
	t.Helper()
	if err := tunnel.WriteFrame(c, tunnel.FrameEOF, nil); err != nil {
		t.Fatal(err)
	}
	for {
		typ, b, err := tunnel.ReadFrame(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		var e tunnel.Exit
		if typ == tunnel.FrameJSON && json.Unmarshal(b, &e) == nil && e.Type == "exit" {
			return e
		}
	}
}

// A harness killed from outside (the OOM killer) that sandboxd stops while
// it is dead but not yet reaped keeps its own exit status: sandboxd's kill
// did not end it. The hooks hold the wait until the stop happened, which is
// the window the race needs.
func TestOutsideKillIsNotSandboxdsOwn(t *testing.T) {
	pids := make(chan int, 1)
	stopped := make(chan struct{})
	sandboxd.SetHarnessTestHooks(func(pid int) { pids <- pid; <-stopped }, func() { close(stopped) })
	t.Cleanup(func() { sandboxd.SetHarnessTestHooks(nil, nil) })
	s, _ := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := s.Open(ctx, tunnel.Open{Op: tunnel.OpHarness, Cmd: []string{"sh", "-c", "kill -9 $$"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pid := <-pids
	for !sandboxd.ProcessExited(pid) {
		if ctx.Err() != nil {
			t.Fatal("the harness did not die")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e := harnessExit(t, c); e.Code != 128+9 || e.Stopped {
		t.Fatalf("exit %+v: want code 137, not stopped by sandboxd", e)
	}
}

// A harness that is still running when the server goes away is sandboxd's kill.
func TestSandboxdStopIsOwn(t *testing.T) {
	s, _ := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := s.Open(ctx, tunnel.Open{Op: tunnel.OpHarness, Cmd: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if e := harnessExit(t, c); e.Code != 128+9 || !e.Stopped {
		t.Fatalf("exit %+v: want code 137, stopped by sandboxd", e)
	}
}
