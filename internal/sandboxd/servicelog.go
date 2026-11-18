package sandboxd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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
// The file is opened and rotated through an os.Root of the home (sandboxd
// writes it as root; the agent controls the home, so a symlink it puts at
// the log path must not redirect the writes elsewhere).
type logSink struct {
	mu   sync.Mutex
	home string // the agent's home ("" for no file)
	rel  string // the log file, relative to home
	path string // home/rel, for reading
	own  func(root *os.Root, name string)
	f    *os.File
	size int64
	tail []byte
	subs map[chan []byte]struct{}
}

func newLogSink(home, rel string, own func(*os.Root, string)) *logSink {
	l := &logSink{home: home, rel: rel, own: own, subs: map[chan []byte]struct{}{}}
	if home != "" {
		l.path = filepath.Join(home, rel)
	}
	return l
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
	if l.home == "" {
		return
	}
	rotate := l.f != nil && l.size+int64(len(p)) > serviceLogMax
	if rotate {
		_ = l.f.Close()
		l.f = nil
	}
	if l.f == nil {
		root, err := os.OpenRoot(l.home)
		if err != nil {
			return
		}
		if rotate {
			_ = root.Rename(l.rel, l.rel+".1")
		}
		f, err := root.OpenFile(l.rel, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err == nil {
			l.own(root, l.rel)
		}
		_ = root.Close()
		if err != nil {
			return
		}
		if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
			l.size = fi.Size()
		} else {
			_ = f.Close()
			return
		}
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
// exist (read through the home's root, regular files only), else from the
// in-memory tail.
func (l *logSink) lastLines(n int) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.home != "" && l.f != nil {
		if root, err := os.OpenRoot(l.home); err == nil {
			cur := readTail(root, l.rel, 1<<20)
			if cur != nil && bytes.Count(cur, []byte{'\n'}) < n {
				cur = append(readTail(root, l.rel+".1", 1<<20), cur...)
			}
			_ = root.Close()
			if cur != nil {
				return lastLines(cur, n)
			}
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

// readTail reads up to limit bytes from the end of a regular file inside
// root (nil if it is missing or not a regular file).
func readTail(root *os.Root, name string, limit int64) []byte {
	if fi, err := root.Lstat(name); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
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
