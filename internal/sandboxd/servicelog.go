package sandboxd

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// Service log limits: the log file is rotated to <log>.1 when it would grow
// past serviceLogMax, so a service keeps at most twice that on the home disk.
// serviceTailMax is the in-memory tail kept for when the file cannot be written.
var (
	serviceLogMax  int64 = 10 << 20
	serviceTailMax       = 64 << 10
)

// logSink receives a service's output: it appends to a size-capped rotating
// file, keeps an in-memory tail and fans out to followers.
type logSink struct {
	mu   sync.Mutex
	path string
	own  func(string)
	f    *os.File
	size int64
	tail []byte
	subs map[chan []byte]struct{}
}

func newLogSink(path string, own func(string)) *logSink {
	return &logSink{path: path, own: own, subs: map[chan []byte]struct{}{}}
}

// Write never fails: output that cannot be stored is still followed.
func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeFile(p)
	l.tail = append(l.tail, p...)
	if over := len(l.tail) - serviceTailMax; over > 0 {
		l.tail = append(l.tail[:0], l.tail[over:]...)
	}
	for ch := range l.subs {
		select {
		case ch <- append([]byte(nil), p...):
		default: // a follower that cannot keep up misses output
		}
	}
	return len(p), nil
}

func (l *logSink) writeFile(p []byte) {
	if l.path == "" {
		return
	}
	if l.f != nil && l.size+int64(len(p)) > serviceLogMax {
		_ = l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
	}
	if l.f == nil {
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		if fi, err := f.Stat(); err == nil {
			l.size = fi.Size()
		}
		l.own(l.path)
		l.f = f
	}
	n, _ := l.f.Write(p)
	l.size += int64(n)
}

// subscribe returns a channel receiving everything written from now on.
func (l *logSink) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 256)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// lastLines returns the last n lines of the log: from the files when they
// exist, else from the in-memory tail.
func (l *logSink) lastLines(n int) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path != "" {
		cur := readTail(l.path, 1<<20)
		if cur != nil {
			if bytes.Count(cur, []byte{'\n'}) < n {
				cur = append(readTail(l.path+".1", 1<<20), cur...)
			}
			return lastLines(cur, n)
		}
	}
	return lastLines(l.tail, n)
}

func (l *logSink) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// readTail reads up to limit bytes from the end of a file (nil if it is missing).
func readTail(path string, limit int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(fi.Size()-limit, 0)
	b := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
		return nil
	}
	if off > 0 { // drop the partial first line
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b
}

// lastLines returns the last n lines of b (a trailing partial line counts).
func lastLines(b []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	i := end
	for count := 0; i > 0; i-- {
		if b[i-1] == '\n' {
			count++
			if count == n {
				break
			}
		}
	}
	return append([]byte(nil), b[i:]...)
}
