// Package dl serves the release a controller runs under /dl/, so edge nodes and other installs
// download it from the controller instead of from a third-party host: the binary itself, the
// embedded runtime manifest and the compressed base image. It is public and read-only; every
// consumer checks the bytes against checksums it already trusts.
//
// Layout:
//
//	/dl/manifest.json                        index of what this controller serves
//	/dl/<version>/agenthold-linux-<arch>      the running binary
//	/dl/<version>/runtime-linux-<arch>.manifest.json
//	/dl/<version>/base-<arch>.ext4.zst        the compressed base, if this controller has it
//
// Files an operator puts into <data_dir>/dl/ are served under the same paths and win over the
// generated ones, which is how a build host publishes other versions and architectures.
package dl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/runtimeassets"
)

const (
	// DefaultMaxPerIP is how many large downloads one client address may run at once.
	DefaultMaxPerIP = 2
	// DefaultLargeBytes is the size from which a file counts as large.
	DefaultLargeBytes = 8 << 20

	immutable = "public, max-age=31536000, immutable"
)

// Config configures a Handler.
type Config struct {
	// DataDir holds base/<sha>.ext4.zst and the operator directory dl/.
	DataDir string
	// Release is the embedded release, nil for development builds (nothing generated is served).
	Release *runtimeassets.Release
	// Exe opens the running binary. Default: the process's own executable image.
	Exe func() (*os.File, error)
	// MaxPerIP and LargeBytes tune the concurrency limit; zero means the defaults.
	MaxPerIP   int
	LargeBytes int64
	Log        *slog.Logger
}

// Handler serves /dl/.
type Handler struct {
	cfg    Config
	log    *slog.Logger
	hashes hashCache
	limit  limiter

	mu    sync.Mutex
	noted string // last reason the base was reported unavailable
}

// New returns a handler for requests under /dl/.
func New(cfg Config) *Handler {
	if cfg.MaxPerIP <= 0 {
		cfg.MaxPerIP = DefaultMaxPerIP
	}
	if cfg.LargeBytes <= 0 {
		cfg.LargeBytes = DefaultLargeBytes
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Exe == nil {
		cfg.Exe = openSelf
	}
	return &Handler{cfg: cfg, log: cfg.Log.With("component", "dl"), hashes: hashCache{m: map[string]*hashEntry{}}, limit: limiter{n: map[string]int{}}}
}

// Start verifies the base image in the background and logs once what this controller can serve.
func (h *Handler) Start(ctx context.Context) {
	rel := h.cfg.Release
	if rel == nil {
		h.log.Info("downloads: not a release build, nothing of this controller is served under /dl/")
	}
	if dir := h.operatorDir(); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			h.log.Info("downloads: serving operator files", "dir", dir)
		}
	}
	if rel == nil {
		return
	}
	go func() {
		if reason := h.baseUnavailable(ctx); reason != "" {
			h.log.Warn("downloads: this controller cannot serve the base image (edge nodes will need base_url or a copy)",
				"version", rel.Version, "reason", reason)
			return
		}
		h.mu.Lock()
		h.noted = ""
		h.mu.Unlock()
		h.log.Info("downloads: serving the release to nodes and other installs", "version", rel.Version, "arch", rel.Arch, "base", true)
	}()
}

// ServeHTTP implements http.Handler for paths below /dl/.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "downloads are read-only")
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/dl/")
	if !ok || !validName(name) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	f, fi, err := h.openOperator(name)
	switch {
	case err == nil:
		defer f.Close()
		h.serve(w, r, name, f, fi, "", h.isVersionDir(name))
		return
	case errors.Is(err, errNotThere):
	case errors.Is(err, errRefused):
		h.log.Warn("downloads: refused an operator file", "name", name, "err", err)
		writeError(w, http.StatusNotFound, "not found")
		return
	default:
		// Not "does not exist": falling through would serve a generated file in place of the
		// operator's one without anybody noticing.
		h.log.Error("downloads: cannot open an operator file", "name", name, "err", err)
		writeError(w, http.StatusInternalServerError, "cannot read the file")
		return
	}
	if name == "manifest.json" {
		h.serveIndex(w, r)
		return
	}
	h.serveGenerated(w, r, name)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	rel := h.cfg.Release
	if rel == nil {
		writeError(w, http.StatusNotFound, "this controller is not a release (bundled) build and has nothing to serve")
		return
	}
	entry := indexArch{
		Binary:          rel.Version + "/" + binaryName(rel),
		RuntimeManifest: rel.Version + "/" + manifestName(rel),
	}
	if h.baseUnavailable(r.Context()) == "" {
		entry.Base = rel.Version + "/" + runtimeassets.BaseFileName(rel.Arch)
	}
	data, _ := json.Marshal(index{Format: 1, Version: rel.Version, Arch: map[string]indexArch{rel.Arch: entry}})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+sum(data)+`"`)
	http.ServeContent(w, r, "", time.Time{}, strings.NewReader(string(data)))
}

type index struct {
	Format  int                  `json:"format"`
	Version string               `json:"version"`
	Arch    map[string]indexArch `json:"arch"`
}

type indexArch struct {
	Binary          string `json:"binary"`
	RuntimeManifest string `json:"runtime_manifest"`
	Base            string `json:"base,omitempty"`
}

func binaryName(rel *runtimeassets.Release) string { return "agenthold-linux-" + rel.Arch }
func manifestName(rel *runtimeassets.Release) string {
	return "runtime-linux-" + rel.Arch + ".manifest.json"
}

func (h *Handler) serveGenerated(w http.ResponseWriter, r *http.Request, name string) {
	rel := h.cfg.Release
	if rel == nil {
		writeError(w, http.StatusNotFound, "this controller is not a release (bundled) build and has nothing to serve")
		return
	}
	file, ok := strings.CutPrefix(name, rel.Version+"/")
	if !ok {
		writeError(w, http.StatusNotFound, "this controller serves release "+rel.Version+" only (see /dl/manifest.json)")
		return
	}
	switch file {
	case binaryName(rel):
		f, err := h.cfg.Exe()
		if err != nil {
			h.log.Error("open own binary", "err", err)
			writeError(w, http.StatusInternalServerError, "cannot open the running binary")
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot stat the running binary")
			return
		}
		h.serve(w, r, name, f, fi, "", true)
	case manifestName(rel):
		w.Header().Set("Cache-Control", immutable)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+sum(rel.Manifest)+`"`)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(string(rel.Manifest)))
	case runtimeassets.BaseFileName(rel.Arch):
		f, fi, reason := h.baseFile(r.Context())
		if reason != "" {
			// The reason names paths and system errors: it goes to the log, not to the client.
			h.noteBase(reason)
			writeError(w, http.StatusNotFound, "this controller has no base image of release "+rel.Version+" to serve")
			return
		}
		defer f.Close()
		h.serve(w, r, name, f, fi, rel.CompressedSHA256, true)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// serve sends f with range support. sha is the file's sha256 when already known (it becomes the
// ETag); otherwise it is computed once per (size, mtime) and cached. Immutable files may be cached
// forever.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, name string, f *os.File, fi fs.FileInfo, sha string, immutableFile bool) {
	if !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method == http.MethodGet && fi.Size() >= h.cfg.LargeBytes {
		ip := clientIP(r)
		if !h.limit.acquire(ip, h.cfg.MaxPerIP) {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("at most %d large downloads at a time per address", h.cfg.MaxPerIP))
			return
		}
		defer h.limit.release(ip)
	}
	if sha == "" {
		var err error
		if sha, err = h.hashes.sum(r.Context(), name, f, fi); err != nil {
			if r.Context().Err() == nil {
				h.log.Error("hash download", "file", name, "err", err)
				writeError(w, http.StatusInternalServerError, "cannot read the file")
			}
			return
		}
	}
	hdr := w.Header()
	hdr.Set("ETag", `"`+sha+`"`)
	if immutableFile {
		hdr.Set("Cache-Control", immutable)
	} else {
		hdr.Set("Cache-Control", "no-cache")
	}
	switch path.Ext(name) {
	case ".json":
		hdr.Set("Content-Type", "application/json")
	case ".zst":
		hdr.Set("Content-Type", "application/zstd")
	default:
		hdr.Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}

// baseFile opens the compressed base if it is there and is exactly the file the release pins.
// reason says why not; the checksum is computed once per (size, mtime).
func (h *Handler) baseFile(ctx context.Context) (*os.File, fs.FileInfo, string) {
	rel := h.cfg.Release
	p := rel.CompressedBasePath(h.cfg.DataDir)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Sprintf("%s does not exist (a node or install that downloaded the image keeps it there; "+
				"an operator can place base-%s.ext4.zst of this release at that path)", p, rel.Arch)
		}
		return nil, nil, err.Error()
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || fi.Size() != rel.CompressedSize) {
		err = fmt.Errorf("%s is not the %d byte file release %s pins", p, rel.CompressedSize, rel.Version)
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err.Error()
	}
	got, err := h.hashes.sum(ctx, "base:"+p, f, fi)
	if err != nil {
		_ = f.Close()
		return nil, nil, err.Error()
	}
	if got != rel.CompressedSHA256 {
		_ = f.Close()
		return nil, nil, fmt.Sprintf("%s does not match the sha256 release %s pins", p, rel.Version)
	}
	return f, fi, ""
}

// noteBase logs why the base cannot be served, when that changes (not on every request).
func (h *Handler) noteBase(reason string) {
	h.mu.Lock()
	changed := h.noted != reason
	h.noted = reason
	h.mu.Unlock()
	if changed {
		h.log.Warn("downloads: the base image is not served", "reason", reason)
	}
}

// baseUnavailable is "" when the compressed base can be served, else the reason it cannot.
func (h *Handler) baseUnavailable(ctx context.Context) string {
	f, _, reason := h.baseFile(ctx)
	if f != nil {
		_ = f.Close()
	}
	if reason != "" {
		h.noteBase(reason)
	}
	return reason
}

func (h *Handler) operatorDir() string {
	if h.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(h.cfg.DataDir, "dl")
}

var (
	errNotThere = errors.New("no such operator file")
	errRefused  = errors.New("operator file refused")
)

// openOperator opens name below <data_dir>/dl/. os.Root refuses paths that leave the directory,
// including through symlinks. It returns errNotThere when the generated files may answer instead
// (nothing at that path, or a directory), errRefused for what is never served (escaping links,
// devices, FIFOs: opened non-blocking and only looked at) and any other error as it is.
func (h *Handler) openOperator(name string) (*os.File, fs.FileInfo, error) {
	dir := h.operatorDir()
	if dir == "" {
		return nil, nil, errNotThere
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, classify(err)
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return nil, nil, classify(err)
	}
	fi, err := f.Stat()
	switch {
	case err != nil:
		_ = f.Close()
		return nil, nil, err
	case fi.IsDir():
		_ = f.Close()
		return nil, nil, errNotThere
	case !fi.Mode().IsRegular():
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s is not a regular file", errRefused, name)
	}
	return f, fi, nil
}

func classify(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return errNotThere
	case errors.Is(err, syscall.ELOOP), strings.Contains(err.Error(), "escapes from parent"):
		return fmt.Errorf("%w: %w", errRefused, err)
	}
	return err
}

// versionDir matches the first path segment of the generated layout: a release version or a
// "sha-<commit>" build.
var versionDir = regexp.MustCompile(`^(v?[0-9]+(\.[0-9]+)+[A-Za-z0-9._+-]*|sha-[0-9a-f]{7,40})$`)

// isVersionDir reports whether name lies in a version directory (<version>/...), whose files
// are published as immutable; everything else an operator provides is revalidated by clients.
func (h *Handler) isVersionDir(name string) bool {
	first, _, ok := strings.Cut(name, "/")
	if !ok {
		return false
	}
	return versionDir.MatchString(first) || h.cfg.Release != nil && first == h.cfg.Release.Version
}

// validName accepts clean relative slash paths without dot segments: no "..", no hidden files,
// no backslashes, no control characters.
func validName(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
		for _, c := range seg {
			if c < ' ' || c == 0x7f {
				return false
			}
		}
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// hashCache remembers the sha256 of files by (key, size, mtime) so a large file is hashed once.
type hashCache struct {
	mu sync.Mutex
	m  map[string]*hashEntry
}

type hashEntry struct {
	mu    sync.Mutex
	size  int64
	mtime time.Time
	sha   string
}

func (c *hashCache) sum(ctx context.Context, key string, f *os.File, fi fs.FileInfo) (string, error) {
	c.mu.Lock()
	e := c.m[key]
	if e == nil {
		e = &hashEntry{}
		c.m[key] = e
	}
	c.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sha != "" && e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) {
		return e.sha, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, io.NewSectionReader(f, 0, fi.Size())}); err != nil {
		return "", err
	}
	e.sha, e.size, e.mtime = hex.EncodeToString(h.Sum(nil)), fi.Size(), fi.ModTime()
	return e.sha, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// limiter counts running large downloads per client address.
type limiter struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *limiter) acquire(ip string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[ip] >= max {
		return false
	}
	l.n[ip]++
	return true
}

func (l *limiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[ip]--; l.n[ip] <= 0 {
		delete(l.n, ip)
	}
}

// clientIP is the address a download is attributed to. Behind a reverse proxy on this host or
// network the connection comes from a private address; then the right-most public address of
// X-Forwarded-For (the one the proxy itself saw) is used. The limit is fairness, not security, so
// a spoofed header only lets a client pick its own bucket.
func clientIP(r *http.Request) string {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				continue
			}
			if a = a.Unmap(); !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() {
				ip = a
				break
			}
		}
	}
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

// AdvertisedURL is the /dl/ root edge nodes are told to fetch their base image from, derived from
// public_url. It is empty, with the reason, when a node could not use it: public_url unset (it
// defaults to http://localhost:8080) or pointing at loopback would send every node to its own
// machine.
func AdvertisedURL(publicURL string) (root, reason string) {
	u, err := url.Parse(publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", "public_url is not an absolute HTTP(S) URL"
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return "", "public_url is unset or points at this machine (" + host + "), where no edge node can reach it; set public_url to the address nodes can open"
	}
	return strings.TrimRight(publicURL, "/") + "/dl/", ""
}
