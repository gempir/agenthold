package api

import (
	"encoding/json"
	"errors"
	"io"
)

func ioPipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

func copyBuffer(w io.Writer, r io.Reader) (int64, error) {
	return io.CopyBuffer(w, r, make([]byte, 64*1024))
}

var errTooLarge = errors.New("upload too large")

// limitReader fails instead of silently truncating.
func limitReader(r io.Reader, n int64) io.Reader { return &limited{r: r, n: n} }

type limited struct {
	r io.Reader
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.n+1 {
		p = p[:l.n+1]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n, errTooLarge
	}
	return n, err
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
