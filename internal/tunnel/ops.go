package tunnel

import (
	"context"
	"io"
	"os"
	"sync"
)

// List lists a directory in the sandbox.
func (s *Session) List(ctx context.Context, path string) (ListReply, error) {
	var out ListReply
	c, err := s.Open(ctx, Open{Op: OpFSList, Path: path})
	if err != nil {
		return out, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	err = ReadJSON(c, &out)
	return out, err
}

// ReadFile streams a file out of the sandbox. size is called with the file
// size before any data is written to w.
func (s *Session) ReadFile(ctx context.Context, path string, size func(int64), w io.Writer, deny ...string) error {
	c, err := s.Open(ctx, Open{Op: OpFSRead, Path: path, Deny: deny})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var rr ReadReply
	if err := ReadJSON(c, &rr); err != nil {
		return err
	}
	if size != nil {
		size(rr.Size)
	}
	_, err = CopyData(w, c, nil)
	return err
}

// WriteFile streams r into a file in the sandbox, replacing it atomically.
func (s *Session) WriteFile(ctx context.Context, path string, mode os.FileMode, r io.Reader) error {
	c, err := s.Open(ctx, Open{Op: OpFSWrite, Path: path, Mode: mode})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	if _, err := io.Copy(DataWriter(c), r); err != nil {
		return err
	}
	if err := WriteFrame(c, FrameEOF, nil); err != nil {
		return err
	}
	var ok OK
	return ReadJSON(c, &ok)
}

// Mkdir creates a directory.
func (s *Session) Mkdir(ctx context.Context, path string, parents bool) error {
	return s.simple(ctx, Open{Op: OpFSMkdir, Path: path, Recursive: parents})
}

// Remove deletes a file or (with recursive) a directory tree.
func (s *Session) Remove(ctx context.Context, path string, recursive bool) error {
	return s.simple(ctx, Open{Op: OpFSRemove, Path: path, Recursive: recursive})
}

// Ping checks that sandboxd answers.
func (s *Session) Ping(ctx context.Context) error { return s.simple(ctx, Open{Op: OpPing}) }

func (s *Session) simple(ctx context.Context, o Open) error {
	c, err := s.Open(ctx, o)
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var ok OK
	return ReadJSON(c, &ok)
}

// PTY opens a terminal. Use the returned Terminal to exchange data.
func (s *Session) PTY(ctx context.Context, o Open) (*Terminal, error) {
	o.Op = OpPTY
	c, err := s.Open(ctx, o)
	if err != nil {
		return nil, err
	}
	var ok OK
	if err := ReadJSON(c, &ok); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &Terminal{c: c}, nil
}

// Terminal is an open pty channel.
type Terminal struct{ c io.ReadWriteCloser }

// Write sends input.
func (t *Terminal) Write(p []byte) (int, error) { return DataWriter(t.c).Write(p) }

// Resize changes the window size.
func (t *Terminal) Resize(cols, rows uint16) error {
	return WriteJSON(t.c, Resize{Type: "resize", Cols: cols, Rows: rows})
}

// ReadOutput copies output to w until the process exits and returns its exit code.
func (t *Terminal) ReadOutput(w io.Writer) (int, error) {
	code := -1
	buf := make([]byte, 32*1024)
	for {
		typ, b, err := ReadFrame(t.c, buf)
		if err != nil {
			return code, err
		}
		switch typ {
		case FrameData:
			if _, err := w.Write(b); err != nil {
				return code, err
			}
		case FrameJSON:
			var e Exit
			if err := jsonUnmarshal(b, &e); err == nil && e.Type == "exit" {
				return e.Code, nil
			}
		case FrameError:
			return code, &RemoteError{Msg: string(b)}
		}
	}
}

// Close ends the terminal (the process is killed if still running).
func (t *Terminal) Close() error { return t.c.Close() }

func stopOnCancel(ctx context.Context, c io.ReadWriteCloser) func() bool {
	return context.AfterFunc(ctx, func() { _ = c.Close() })
}

// Process is a harness process in the sandbox: Stdout and Stdin carry its
// raw stdio.
type Process struct {
	wmu     sync.Mutex
	c       io.ReadWriteCloser
	Stdout  *io.PipeReader
	out     *io.PipeWriter
	done    chan struct{}
	code    int
	stopped bool
	err     error
}

// StartHarness starts cmd in the sandbox. stderr receives its stderr lines.
func (s *Session) StartHarness(ctx context.Context, cmd []string, env map[string]string, stderr func(string)) (*Process, error) {
	c, err := s.Open(ctx, Open{Op: OpHarness, Cmd: cmd, Env: env})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	p := &Process{c: c, Stdout: pr, out: pw, done: make(chan struct{}), code: -1}
	go p.read(stderr)
	return p, nil
}

func (p *Process) read(stderr func(string)) {
	defer close(p.done)
	buf := make([]byte, 64*1024)
	for {
		typ, b, err := ReadFrame(p.c, buf)
		if err != nil {
			p.err = err
			_ = p.out.CloseWithError(io.ErrUnexpectedEOF)
			return
		}
		switch typ {
		case FrameData:
			if _, err := p.out.Write(b); err != nil {
				p.err = err
				return
			}
		case FrameJSON:
			var m struct {
				Type    string `json:"type"`
				Line    string `json:"line"`
				Code    int    `json:"code"`
				Stopped bool   `json:"stopped"`
			}
			if jsonUnmarshal(b, &m) != nil {
				continue
			}
			switch m.Type {
			case "stderr":
				if stderr != nil {
					stderr(m.Line)
				}
			case "exit":
				p.code, p.stopped = m.Code, m.Stopped
				_ = p.out.Close()
				return
			}
		case FrameError:
			p.err = &RemoteError{Msg: string(b)}
			_ = p.out.CloseWithError(p.err)
			return
		}
	}
}

// Write sends bytes to the process's stdin.
func (p *Process) Write(b []byte) (int, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return DataWriter(p.c).Write(b)
}

// Done is closed when the process has exited or the connection is lost.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitCode returns the exit code (-1 if unknown) and any transport error.
// Call it after Done.
func (p *Process) ExitCode() (int, error) { return p.code, p.err }

// StoppedBySandbox reports whether sandboxd killed the process itself
// rather than it exiting or being killed by something else. Call it after Done.
func (p *Process) StoppedBySandbox() bool { return p.stopped }

// Close kills the process. Output not read yet is dropped, so the reader
// never blocks the stream.
func (p *Process) Close() error {
	_ = p.Stdout.CloseWithError(io.ErrClosedPipe)
	return p.c.Close()
}

// Stats returns the sandbox's resource usage.
func (s *Session) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	c, err := s.Open(ctx, Open{Op: OpStats})
	if err != nil {
		return out, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	err = ReadJSON(c, &out)
	return out, err
}

// Shutdown asks sandboxd to power the sandbox off. It returns once sandboxd
// has acknowledged; the tunnel then drops and, for a microVM, the VMM exits.
func (s *Session) Shutdown(ctx context.Context) error {
	c, err := s.Open(ctx, Open{Op: OpShutdown})
	if err != nil {
		return err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var ok OK
	return ReadJSON(c, &ok)
}

// Suspend prepares sandboxd for a memory snapshot (see OpSuspend). It returns
// once sandboxd has flushed the guest's filesystems.
func (s *Session) Suspend(ctx context.Context) error {
	return s.simple(ctx, Open{Op: OpSuspend})
}
