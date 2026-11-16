package sandboxd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Exec limits.
const (
	execDefaultTimeout = 60
	execMaxTimeout     = 600
	execDefaultOutput  = 64 << 10
	execMaxOutput      = 4 << 20
)

// cappedBuffer keeps the first max bytes written and counts the rest.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room < len(p) {
		c.truncated = true
		c.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// exec runs a command to completion as the agent account (in its own cgroup
// where available, so background processes it starts end with it) and
// answers with its output and exit code.
func (d *Daemon) exec(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if len(o.Cmd) == 0 {
		return errors.New("exec: no command")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = execDefaultTimeout
	}
	timeout = min(timeout, execMaxTimeout)
	limit := int(o.MaxBytes)
	if limit <= 0 {
		limit = execDefaultOutput
	}
	limit = min(limit, execMaxOutput)
	dir := d.resolve(o.Dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return errors.New("exec: working directory " + dir + " does not exist")
	}

	// The server closing the stream cancels the command.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	go func() {
		buf := make([]byte, 1)
		_, _ = st.Read(buf)
		cancel()
	}()

	out := &cappedBuffer{max: limit}
	cg := newHarnessCgroup(d.log)
	defer cg.remove()
	start := func(inCgroup bool) (*exec.Cmd, error) {
		cmd := exec.Command(o.Cmd[0], o.Cmd[1:]...) //nolint:noctx // killed with its cgroup below
		cmd.Dir = dir
		cmd.Env = d.env(o.Env, d.shell()[0])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if d.acct.switchTo {
			cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
		}
		if inCgroup {
			cg.attach(cmd.SysProcAttr)
		}
		cmd.Stdout, cmd.Stderr = out, out
		cmd.WaitDelay = time.Second
		return cmd, cmd.Start()
	}
	cmd, err := start(cg.path() != "")
	if err != nil && cg.path() != "" && !errors.Is(err, exec.ErrNotFound) {
		cg.remove()
		cmd, err = start(false)
	}
	if err != nil {
		return err
	}
	pid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timedOut := false
	select {
	case err = <-waited:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		cg.kill()
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		err = <-waited
	}
	cg.kill()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	out.mu.Lock()
	reply := tunnel.ExecReply{Output: out.buf.String(), Truncated: out.truncated, ExitCode: exitCode(err), TimedOut: timedOut}
	out.mu.Unlock()
	if ps := cmd.ProcessState; errors.Is(err, exec.ErrWaitDelay) && ps != nil {
		// It exited; helpers it left in the background held its output open.
		reply.ExitCode = ps.ExitCode()
	} else if err != nil && reply.ExitCode == 0 {
		reply.ExitCode = -1
	}
	return tunnel.WriteJSON(st, reply)
}
