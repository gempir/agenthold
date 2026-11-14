package tunnel

import (
	"context"
	"io"
	"net"
	"os"
)

// List lists a directory in the sandbox.
func (s *Session) List(ctx context.Context, path string) (ListReply, error) {
	var out ListReply
	c, err := s.Open(ctx, Open{Op: OpFSList, Path: path})
	if err != nil {
		return out, err
	}
	defer c.Close()
	stopOnCancel(ctx, c)
	err = ReadJSON(c, &out)
	return out, err
}

// ReadFile streams a file out of the sandbox. size is called with the file
// size before any data is written to w.
func (s *Session) ReadFile(ctx context.Context, path string, size func(int64), w io.Writer) error {
	c, err := s.Open(ctx, Open{Op: OpFSRead, Path: path})
	if err != nil {
		return err
	}
	defer c.Close()
	stopOnCancel(ctx, c)
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
	stopOnCancel(ctx, c)
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
	stopOnCancel(ctx, c)
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
type Terminal struct{ c net.Conn }

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

func stopOnCancel(ctx context.Context, c net.Conn) {
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
}
