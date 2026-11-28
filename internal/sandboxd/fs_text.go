package sandboxd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Text file and search limits.
const (
	textReadDefault  = 512 << 10
	textReadMax      = 8 << 20
	textWriteMax     = 32 << 20
	textSniff        = 8 << 10
	textScanMax      = 64 << 20 // a line range scans at most this much
	searchDefault    = 50
	searchMax        = 500
	searchMaxEntries = 200_000
	searchTimeout    = 5 * time.Second
)

// homeFile validates a path for the text operations: inside the home
// (also after resolving symlinks), not the home itself, not denied.
func (d *Daemon) homeFile(p string, deny []string) (string, error) {
	abs, err := d.homePath(p)
	if err != nil {
		return "", err
	}
	if abs == filepath.Clean(d.acct.home) {
		return "", errors.New("the home is a directory")
	}
	if d.denied(abs, deny) {
		return "", errDenied
	}
	return abs, nil
}

// homeRelPath is abs (a clean path inside the home) relative to the home.
func (d *Daemon) homeRelPath(abs string) string {
	rel, err := filepath.Rel(filepath.Clean(d.acct.home), abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// sniffText reports whether b (the start of a file) looks like UTF-8 text.
func sniffText(b []byte, complete bool) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	if !complete {
		// The cut may split a character.
		for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	return utf8.Valid(b)
}

// mimeType guesses a file's type from its name and first bytes.
func mimeType(name string, head []byte, text bool) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	if text {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(head)
}

// validUTF8Prefix cuts b at the last complete character.
func validUTF8Prefix(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}

func (d *Daemon) fsReadText(st net.Conn, o tunnel.Open) error {
	abs, err := d.homeFile(o.Path, o.Deny)
	if err != nil {
		return err
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", abs)
	}
	limit := o.MaxBytes
	if limit <= 0 {
		limit = textReadDefault
	}
	limit = min(limit, textReadMax)
	out := tunnel.TextFile{Path: d.homeRelPath(abs), Size: info.Size(), ModTime: info.ModTime().Unix()}
	r := bufio.NewReaderSize(f, 64<<10)
	head, _ := r.Peek(textSniff)
	text := sniffText(head, int64(len(head)) >= info.Size())
	out.MimeType = mimeType(abs, head, text)
	if !text {
		out.IsBinary = true
		return tunnel.WriteJSONData(st, out)
	}

	if o.StartLine > 0 || o.EndLine > 0 {
		start, end := max(o.StartLine, 1), o.EndLine
		lr := bufio.NewReaderSize(io.LimitReader(r, textScanMax), 64<<10)
		var b strings.Builder
		line, scanned := 0, int64(0)
		for {
			l, err := lr.ReadString('\n')
			if len(l) > 0 {
				line++
				scanned += int64(len(l))
				if line >= start && (end <= 0 || line <= end) && !out.Truncated {
					if int64(b.Len()+len(l)) > limit {
						out.Truncated = true
					} else {
						if out.StartLine == 0 {
							out.StartLine = line
						}
						out.EndLine = line
						b.WriteString(l)
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return err
				}
				if scanned < textScanMax {
					out.TotalLines = line
				}
				break
			}
		}
		if !utf8.ValidString(b.String()) {
			out.IsBinary, out.Truncated = true, false
			out.StartLine, out.EndLine = 0, 0
			return tunnel.WriteJSONData(st, out)
		}
		out.Content = b.String()
		return tunnel.WriteJSONData(st, out)
	}

	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(buf)) > limit {
		out.Truncated = true
		buf = validUTF8Prefix(buf[:limit])
	}
	if !utf8.Valid(buf) || bytes.IndexByte(buf, 0) >= 0 {
		out.IsBinary, out.Truncated = true, false
		return tunnel.WriteJSONData(st, out)
	}
	out.Content = string(buf)
	return tunnel.WriteJSONData(st, out)
}

func (d *Daemon) fsWriteText(st net.Conn, o tunnel.Open) error {
	abs, err := d.homeFile(o.Path, o.Deny)
	if err != nil {
		return err
	}
	target, mode := abs, os.FileMode(0o644)
	if fi, err := os.Lstat(abs); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			// Write the file the link points at (homePath checked that it
			// is inside the home), keeping the link.
			if target, err = filepath.EvalSymlinks(abs); err != nil {
				return err
			}
			if fi, err = os.Stat(target); err != nil {
				return err
			}
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", abs)
		}
		mode = fi.Mode().Perm()
	}
	if err := d.mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".agenthold-edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tunnel.CopyData(&tunnel.CappedWriter{W: tmp, Max: textWriteMax}, st, nil); err != nil {
		_ = tmp.Close()
		if errors.Is(err, tunnel.ErrCapped) {
			return fmt.Errorf("the file is larger than %d bytes", textWriteMax)
		}
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	d.own(tmp.Name())
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	head := make([]byte, textSniff)
	n := 0
	if f, err := os.Open(target); err == nil {
		n, _ = io.ReadFull(f, head)
		_ = f.Close()
	}
	text := sniffText(head[:n], int64(n) >= info.Size())
	return tunnel.WriteJSON(st, tunnel.TextFile{
		Path: d.homeRelPath(abs), Size: info.Size(), ModTime: info.ModTime().Unix(),
		MimeType: mimeType(abs, head[:n], text), IsBinary: !text,
	})
}

// searchCandidate is a path (relative to the search base, slash-separated).
type searchCandidate struct {
	path  string
	isDir bool
}

// searchCandidates lists the files below base: git's files (tracked and
// untracked, without ignored ones) inside a repository, else a walk that
// skips dependency, cache and build directories.
func (d *Daemon) searchCandidates(ctx context.Context, base string) ([]searchCandidate, bool) {
	if _, ok := d.toplevel(ctx, base); ok {
		r := d.runGitCapped(ctx, searchTimeout, base, 32<<20, true,
			"-c", "core.quotePath=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		if r.err == nil {
			var out []searchCandidate
			dirs := map[string]bool{}
			for _, p := range strings.Split(r.out, "\x00") {
				if p == "" {
					continue
				}
				for dir := path.Dir(p); dir != "." && dir != "/" && !dirs[dir]; dir = path.Dir(dir) {
					dirs[dir] = true
					out = append(out, searchCandidate{path: dir, isDir: true})
				}
				out = append(out, searchCandidate{path: p})
				if len(out) >= searchMaxEntries {
					return out, true
				}
			}
			return out, r.truncated
		}
	}
	var out []searchCandidate
	truncated := false
	deadline := time.Now().Add(searchTimeout)
	_ = filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == base {
			return nil
		}
		if len(out) >= searchMaxEntries || time.Now().After(deadline) || ctx.Err() != nil {
			truncated = true
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(base, p)
		if rerr != nil {
			return nil
		}
		if e.IsDir() {
			if gitSkipDirs[e.Name()] {
				return filepath.SkipDir
			}
			out = append(out, searchCandidate{path: filepath.ToSlash(rel), isDir: true})
			return nil
		}
		out = append(out, searchCandidate{path: filepath.ToSlash(rel)})
		return nil
	})
	return out, truncated
}

// matchScore scores how well a path matches the query's lowercase words
// (0: not at all). Every word must match the path in order (as a
// subsequence); names that start with or contain a word score highest.
func matchScore(p string, words []string) int {
	lp := strings.ToLower(p)
	name := path.Base(lp)
	score := 0
	for _, w := range words {
		switch {
		case name == w:
			score += 1000
		case strings.HasPrefix(name, w):
			score += 800
		case strings.Contains(name, w):
			score += 600
		case strings.Contains(lp, w):
			score += 400
		default:
			s := subsequenceScore(lp, w)
			if s == 0 {
				return 0
			}
			score += s
		}
	}
	// Shorter and shallower paths first among equal matches.
	return score*16 - min(strings.Count(lp, "/")*4+len(lp)/8, 15)
}

// subsequenceScore is 1..300 when w's characters appear in s in order,
// higher for fewer gaps; 0 otherwise.
func subsequenceScore(s, w string) int {
	i, gaps, last := 0, 0, -1
	for j := 0; j < len(s) && i < len(w); j++ {
		if s[j] == w[i] {
			if last >= 0 && j != last+1 {
				gaps++
			}
			last = j
			i++
		}
	}
	if i < len(w) {
		return 0
	}
	return max(300-gaps*20, 1)
}

func (d *Daemon) fsSearch(ctx context.Context, st net.Conn, o tunnel.Open) error {
	base, err := d.homePath(o.Path)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", base)
	}
	limit := o.Limit
	if limit <= 0 {
		limit = searchDefault
	}
	limit = min(limit, searchMax)
	words := strings.Fields(strings.ToLower(o.Query))
	cands, truncated := d.searchCandidates(ctx, base)
	type scored struct {
		c     searchCandidate
		score int
	}
	matches := make([]scored, 0, min(len(cands), 4096))
	for _, c := range cands {
		if len(words) == 0 {
			matches = append(matches, scored{c: c, score: -strings.Count(c.path, "/")})
			continue
		}
		if s := matchScore(c.path, words); s > 0 {
			if c.isDir {
				s -= 8 // files first among equal matches
			}
			matches = append(matches, scored{c: c, score: s})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].c.path < matches[j].c.path
	})
	if len(matches) > limit {
		matches, truncated = matches[:limit], true
	}
	out := tunnel.FileSearchReply{Base: d.homeRelPath(base), Results: make([]tunnel.FileMatch, 0, len(matches)), Truncated: truncated}
	if out.Base == "." {
		out.Base = ""
	}
	for _, m := range matches {
		out.Results = append(out.Results, tunnel.FileMatch{Path: m.c.path, Name: path.Base(m.c.path), IsDir: m.c.isDir})
	}
	return tunnel.WriteJSONData(st, out)
}
