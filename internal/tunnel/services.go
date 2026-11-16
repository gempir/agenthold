package tunnel

import (
	"context"
	"errors"
	"io"
	"strings"
)

// ErrTooOld is returned when the guest's sandboxd does not know an operation
// (it predates it). Restarting the machine, or resetting its system when its
// base image is older, brings a current sandboxd.
var ErrTooOld = errors.New("this machine's sandboxd is too old for this; restart the machine (or reset its system) to update it")

// unsupported maps "unknown operation" errors of old guests to ErrTooOld.
func unsupported(err error) error {
	var re *RemoteError
	if errors.As(err, &re) && strings.HasPrefix(re.Msg, "unknown operation") {
		return ErrTooOld
	}
	return err
}

// Services sends a service request that is answered with a ServicesReply.
func (s *Session) Services(ctx context.Context, req ServiceRequest) ([]ServiceState, error) {
	c, err := s.Open(ctx, Open{Op: OpService, Service: &req})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out ServicesReply
	if err := ReadJSON(c, &out); err != nil {
		return nil, unsupported(err)
	}
	return out.Services, nil
}

// ServiceLogs copies the last lines of a service's log to w and, with
// follow, everything it writes afterwards until ctx ends.
func (s *Session) ServiceLogs(ctx context.Context, name string, lines int, follow bool, w io.Writer) error {
	c, err := s.Open(ctx, Open{Op: OpService, Service: &ServiceRequest{Action: ServiceLogs, Name: name, Lines: lines, Follow: follow}})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var ok OK
	if err := ReadJSON(c, &ok); err != nil {
		return unsupported(err)
	}
	_, err = CopyData(w, c, nil)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// Tar streams the directory path out of the sandbox as a tar archive into w.
func (s *Session) Tar(ctx context.Context, path string, exclude []string, maxBytes int64, w io.Writer) error {
	c, err := s.Open(ctx, Open{Op: OpFSTar, Path: path, Exclude: exclude, MaxBytes: maxBytes})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var ok OK
	if err := ReadJSON(c, &ok); err != nil {
		return unsupported(err)
	}
	_, err = CopyData(w, c, nil)
	return err
}

// Untar extracts the tar archive read from r into the directory path (which
// must not exist yet, or be empty).
func (s *Session) Untar(ctx context.Context, path string, maxBytes int64, r io.Reader) error {
	c, err := s.Open(ctx, Open{Op: OpFSUntar, Path: path, MaxBytes: maxBytes})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var ready OK
	if err := ReadJSON(c, &ready); err != nil {
		return unsupported(err)
	}
	// sandboxd answers early with an error when extracting fails; keep the
	// reader going so the error, not a broken pipe, is what is returned.
	errc := make(chan error, 1)
	go func() {
		var ok OK
		errc <- ReadJSON(c, &ok)
	}()
	if _, err := io.Copy(DataWriter(c), r); err != nil {
		_ = c.Close()
		if rerr := <-errc; rerr != nil {
			var re *RemoteError
			if errors.As(rerr, &re) {
				return rerr
			}
		}
		return err
	}
	if err := WriteFrame(c, FrameEOF, nil); err != nil {
		return err
	}
	return <-errc
}

// Symlink points the symlink path at target, atomically replacing what is there.
func (s *Session) Symlink(ctx context.Context, path, target string) error {
	err := s.simple(ctx, Open{Op: OpFSSymlink, Path: path, Target: target})
	return unsupported(err)
}

// Exec runs argv as the agent account in dir (default the home) and returns
// its output (stdout and stderr, cut at maxBytes) and exit code.
func (s *Session) Exec(ctx context.Context, argv []string, dir string, env map[string]string, timeoutSec int, maxBytes int64) (ExecReply, error) {
	var out ExecReply
	c, err := s.Open(ctx, Open{Op: OpExec, Cmd: argv, Dir: dir, Env: env, Timeout: timeoutSec, MaxBytes: maxBytes})
	if err != nil {
		return out, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	err = ReadJSON(c, &out)
	return out, unsupported(err)
}
