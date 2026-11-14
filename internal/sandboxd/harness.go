package sandboxd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"sync"
	"syscall"

	"github.com/gempir/agenthold/internal/tunnel"
)

// harness runs the agent harness with its stdio bridged to the stream: data
// frames in both directions carry raw ACP bytes, stderr lines and the exit
// code are sent as JSON frames.
func (d *Daemon) harness(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if len(o.Cmd) == 0 {
		return errors.New("harness: no command")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.Cmd[0], o.Cmd[1:]...)
	cmd.Dir = d.acct.home
	cmd.Env = d.env(o.Env, d.shell()[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if d.acct.switchTo {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
	}
	// Kill the whole process group: harnesses spawn helpers.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	d.log.Info("harness started", "cmd", o.Cmd, "pid", cmd.Process.Pid)

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

	go func() {
		// Server -> harness stdin. When the server goes away, stop the harness.
		_, _ = tunnel.CopyData(stdin, st, nil)
		_ = stdin.Close()
		cancel()
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if werr := write(tunnel.FrameData, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(io.LimitReader(stderr, 1<<30))
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			_ = writeJSON(tunnel.Stderr{Type: "stderr", Line: sc.Text()})
		}
	}()
	wg.Wait()
	err = cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	d.log.Info("harness exited", "code", code)
	return writeJSON(tunnel.Exit{Type: "exit", Code: code})
}
