package sandboxd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Limits of the harness bridge.
var (
	// harnessStderrLine is the longest stderr line forwarded; the rest of a
	// longer line is dropped (and the harness keeps being drained).
	harnessStderrLine = 4096
	// harnessDrain is how long output may keep flowing after the harness
	// exited (helpers still holding its stdout), and how long a harness may
	// live after closing its stdout.
	harnessDrain = 2 * time.Second
)

// harness runs the agent harness with its stdio bridged to the stream: data
// frames in both directions carry raw ACP bytes, stderr lines and the exit
// code are sent as JSON frames.
//
// Whatever ends first (the harness exits, closes its stdout, the server
// closes the stream or the tunnel drops), everything the harness started is
// killed: its cgroup where cgroup v2 is available (in the microVM; this also
// catches helpers that left the process group with setsid), its process
// group otherwise. An exit frame is the last frame of a stream that is still
// open. The exit code of a harness killed by a signal is 128+signal, and
// Stopped tells whether sandboxd killed it itself.
func (d *Daemon) harness(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if len(o.Cmd) == 0 {
		return errors.New("harness: no command")
	}
	cg := newHarnessCgroup(d.log)
	defer cg.remove()
	// Own pipes rather than cmd's: Wait must not wait for helpers that
	// inherited them, and reading must not stop at Wait.
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return err
	}
	cmd, stdin, err := d.startHarness(o, outW, errW, cg)
	_ = outW.Close()
	_ = errW.Close()
	if err != nil {
		_ = outR.Close()
		_ = errR.Close()
		return err
	}
	pid := cmd.Process.Pid
	// Harnesses spawn helpers (MCP servers, shells): they all go.
	killAll := func() {
		cg.kill()
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	d.log.Info("harness started", "cmd", o.Cmd, "pid", pid, "cgroup", cg.path())

	var wmu sync.Mutex // frames from stdout and stderr interleave
	write := func(t tunnel.FrameType, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return tunnel.WriteFrame(st, t, b)
	}
	writeJSON := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		return tunnel.WriteJSON(st, v)
	}

	exited := make(chan struct{})
	var stopped atomic.Bool // sandboxd killed the harness itself
	killOnce := sync.OnceFunc(killAll)
	stop := func() {
		select {
		case <-exited:
		default:
			stopped.Store(true)
		}
		killOnce()
	}
	go func() {
		// Server -> harness stdin. When the server goes away, stop the harness.
		_, _ = tunnel.CopyData(stdin, st, nil)
		_ = stdin.Close()
		stop()
	}()
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-exited:
		}
	}()

	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				if werr := write(tunnel.FrameData, buf[:n]); werr != nil {
					stop()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(stderrDone)
		forwardLines(errR, harnessStderrLine, func(line string) {
			_ = writeJSON(tunnel.Stderr{Type: "stderr", Line: line})
		})
	}()
	go func() {
		// A harness that closed its stdout cannot speak ACP anymore.
		<-stdoutDone
		select {
		case <-exited:
		case <-time.After(harnessDrain):
			_ = writeJSON(tunnel.Stderr{Type: "stderr", Line: "agenthold: the harness closed its stdout but kept running, so it was stopped"})
			stop()
		}
	}()

	err = cmd.Wait()
	close(exited)
	// Helpers that outlived the harness hold its pipes open: give them a
	// moment to flush, then they go too.
	drained := time.After(harnessDrain)
	select {
	case <-stdoutDone:
	case <-drained:
	}
	stop()
	_ = outR.Close()
	<-stdoutDone
	select {
	case <-stderrDone:
	case <-drained:
	}
	_ = errR.Close()
	<-stderrDone

	code := exitCode(err)
	d.log.Info("harness exited", "pid", pid, "code", code, "stopped", stopped.Load())
	return writeJSON(tunnel.Exit{Type: "exit", Code: code, Stopped: stopped.Load()})
}

// startHarness starts the harness in cg (if any; without it when the
// kernel refuses) and in its own process group.
func (d *Daemon) startHarness(o tunnel.Open, stdout, stderr *os.File, cg *harnessCgroup) (*exec.Cmd, io.WriteCloser, error) {
	start := func(inCgroup bool) (*exec.Cmd, io.WriteCloser, error) {
		// Not CommandContext: everything the harness started is killed, not just it.
		cmd := exec.Command(o.Cmd[0], o.Cmd[1:]...) //nolint:noctx
		cmd.Dir = d.acct.home
		cmd.Env = d.env(o.Env, d.shell()[0])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if d.acct.switchTo {
			cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
		}
		if inCgroup {
			cg.attach(cmd.SysProcAttr)
		}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return cmd, stdin, nil
	}
	if cg.path() != "" {
		cmd, stdin, err := start(true)
		if err == nil {
			return cmd, stdin, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		d.log.Warn("starting the harness in its cgroup failed, using its process group only", "err", err)
		cg.remove()
	}
	return start(false)
}

// exitCode is the shell-style exit status: 128+signal for a killed process.
func exitCode(err error) int {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}

// forwardLines calls fn with each line of r, cut to max bytes, until r ends.
// It keeps reading whatever the line lengths: a writer is never blocked.
func forwardLines(r io.Reader, max int, fn func(string)) {
	br := bufio.NewReaderSize(r, max)
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(line) < max {
			line = append(line, chunk[:min(len(chunk), max-len(line))]...)
		}
		if err != nil {
			if len(line) > 0 {
				fn(string(line))
			}
			return
		}
		if !isPrefix {
			fn(string(line))
			line = line[:0]
		}
	}
}
