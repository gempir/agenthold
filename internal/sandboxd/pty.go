package sandboxd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"syscall"

	"github.com/creack/pty"

	"github.com/gempir/agenthold/internal/tunnel"
)

func (d *Daemon) shell() []string {
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh, "-l"}
		}
	}
	return []string{"/bin/sh"}
}

func (d *Daemon) env(extra map[string]string, shell string) []string {
	env := map[string]string{
		"HOME":  d.acct.home,
		"USER":  d.acct.name,
		"SHELL": shell,
		"TERM":  "xterm-256color",
		"LANG":  "C.UTF-8",
		"PATH":  "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	// Keep what the image configured (PATH, proxies, CA bundles, ...).
	for _, kv := range os.Environ() {
		for i := range len(kv) {
			if kv[i] == '=' {
				if _, ok := env[kv[:i]]; !ok || kv[:i] == "PATH" {
					env[kv[:i]] = kv[i+1:]
				}
				break
			}
		}
	}
	for k, v := range extra {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func (d *Daemon) command(ctx context.Context, argv []string, env map[string]string) *exec.Cmd {
	if len(argv) == 0 {
		argv = d.shell()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = d.acct.home
	cmd.Env = d.env(env, argv[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if d.acct.switchTo {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
	}
	return cmd
}

func (d *Daemon) pty(ctx context.Context, st net.Conn, o tunnel.Open) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := d.command(ctx, o.Cmd, o.Env)
	size := &pty.Winsize{Cols: max(o.Cols, 10), Rows: max(o.Rows, 5)}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tunnel.WriteJSON(st, tunnel.OK{OK: true}); err != nil {
		return err
	}

	// Input from the server: data and resize messages.
	go func() {
		_, _ = tunnel.CopyData(f, st, func(b []byte) error {
			var r tunnel.Resize
			if err := json.Unmarshal(b, &r); err == nil && r.Type == "resize" && r.Cols > 0 && r.Rows > 0 {
				_ = pty.Setsize(f, &pty.Winsize{Cols: r.Cols, Rows: r.Rows})
			}
			return nil
		})
		// The server went away: end the process.
		cancel()
	}()

	// Output to the server until the pty closes (EIO after the process exits).
	_, copyErr := io.Copy(tunnel.DataWriter(st), f)
	waitErr := cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		code = ee.ExitCode()
	}
	_ = copyErr
	return tunnel.WriteJSON(st, tunnel.Exit{Type: "exit", Code: code})
}
