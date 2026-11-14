// Package tunnel implements the connection between the server and sandboxd.
//
// sandboxd dials the server's tunnel listener over WebSocket and authenticates
// with the agent id and a per-boot token. The WebSocket carries a yamux
// session; each yamux stream is one channel (a terminal, a file operation,
// ...). A stream starts with an Open frame naming the operation, followed by
// operation-specific frames.
package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// FrameType tags a frame.
type FrameType byte

// Frame types.
const (
	FrameJSON  FrameType = 1 // a JSON control message
	FrameData  FrameType = 2 // raw bytes
	FrameEOF   FrameType = 3 // no more data frames in this direction
	FrameError FrameType = 4 // payload is an error message; the stream ends
)

// MaxFrame bounds a single frame's payload.
const MaxFrame = 1 << 20

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// WriteJSON writes a JSON frame.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameJSON, b)
}

// WriteError writes an error frame.
func WriteError(w io.Writer, err error) error {
	return WriteFrame(w, FrameError, []byte(err.Error()))
}

// ReadFrame reads one frame. buf is reused if large enough.
func ReadFrame(r io.Reader, buf []byte) (FrameType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return FrameType(hdr[0]), buf, nil
}

// RemoteError is an error reported by the other side.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// ReadJSON reads a frame that must be JSON (or an error frame) into v.
func ReadJSON(r io.Reader, v any) error {
	t, b, err := ReadFrame(r, nil)
	if err != nil {
		return err
	}
	switch t {
	case FrameJSON:
		return json.Unmarshal(b, v)
	case FrameError:
		return &RemoteError{Msg: string(b)}
	}
	return fmt.Errorf("expected JSON frame, got type %d", t)
}

// DataWriter returns a writer that wraps each Write in a data frame.
func DataWriter(w io.Writer) io.Writer { return dataWriter{w} }

type dataWriter struct{ w io.Writer }

func (d dataWriter) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += MaxFrame {
		end := min(off+MaxFrame, len(p))
		if err := WriteFrame(d.w, FrameData, p[off:end]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

// CopyData reads data frames from r into w until an EOF frame. JSON frames are
// passed to onJSON (which may be nil to reject them).
func CopyData(w io.Writer, r io.Reader, onJSON func([]byte) error) (int64, error) {
	var n int64
	buf := make([]byte, 32*1024)
	for {
		t, b, err := ReadFrame(r, buf)
		if err != nil {
			return n, err
		}
		switch t {
		case FrameData:
			m, err := w.Write(b)
			n += int64(m)
			if err != nil {
				return n, err
			}
		case FrameEOF:
			return n, nil
		case FrameJSON:
			if onJSON == nil {
				return n, errors.New("unexpected JSON frame")
			}
			if err := onJSON(b); err != nil {
				return n, err
			}
		case FrameError:
			return n, &RemoteError{Msg: string(b)}
		default:
			return n, fmt.Errorf("unknown frame type %d", t)
		}
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
