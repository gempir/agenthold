// Package runtimeassets prepares the verified runtime and base image embedded by release builds.
package runtimeassets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Paths is a release's extracted runtime. Unbundled development builds return Bundled=false and
// empty paths, so callers keep their explicitly configured development artifacts.
type Paths struct {
	Bundled     bool
	Version     string
	Firecracker string
	Jailer      string
	Kernel      string
	Sandboxd    string
	BaseImage   string
}

type artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type baseArtifact struct {
	artifact
	URL        string   `json:"url"`
	Compressed artifact `json:"compressed"`
}

type manifest struct {
	Format  int                 `json:"format"`
	Version string              `json:"version"`
	Arch    string              `json:"arch"`
	Files   map[string]artifact `json:"files"`
	Base    baseArtifact        `json:"base"`
}

var prepareGate = make(chan struct{}, 1)

// Prepare extracts the bundled runtime into dataDir/runtime/<release>/ and verifies the base at
// dataDir/base/<sha256>.ext4. A missing base is downloaded from the release's pinned URL, checked
// as compressed bytes, decompressed, and checked again before an atomic rename. A verified
// pre-placed base needs no network access. The data directories must be owned by the current user
// (root in production), with no group/other write permission. Existing corrupt bases are refused.
func Prepare(ctx context.Context, dataDir string) (Paths, error) {
	assets := bundledFiles()
	if assets == nil {
		return Paths{}, nil
	}
	select {
	case prepareGate <- struct{}{}:
		defer func() { <-prepareGate }()
	case <-ctx.Done():
		return Paths{}, ctx.Err()
	}
	return prepare(ctx, dataDir, assets, http.DefaultClient)
}

func prepare(ctx context.Context, dataDir string, assets fs.FS, client *http.Client) (Paths, error) {
	var paths Paths
	if err := ctx.Err(); err != nil {
		return paths, err
	}
	m, err := readManifest(assets)
	if err != nil {
		return paths, err
	}
	if dataDir == "" {
		return paths, errors.New("runtime assets: data directory is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return paths, err
	}
	if err := secureDir(abs); err != nil {
		return paths, err
	}
	// Resolve trusted parent aliases (for example macOS /tmp) once, then reject symlinks in our
	// own directory tree. These directories are never writable by another user.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return paths, err
	}
	runtimeDir := filepath.Join(abs, "runtime")
	versionDir := filepath.Join(runtimeDir, m.Version)
	baseDir := filepath.Join(abs, "base")
	for _, dir := range []string{runtimeDir, versionDir, baseDir} {
		if err := secureDir(dir); err != nil {
			return paths, err
		}
	}
	for _, name := range []string{"firecracker", "jailer", "kernel", "sandboxd"} {
		path := filepath.Join(versionDir, name)
		want := m.Files[name]
		ok, err := verifyFile(ctx, path, want)
		if err != nil && !errors.Is(err, errChecksum) {
			return paths, err
		}
		mode := os.FileMode(0o755)
		if name == "kernel" {
			mode = 0o644
		}
		if !ok {
			f, err := assets.Open(name)
			if err != nil {
				return paths, fmt.Errorf("embedded %s: %w", name, err)
			}
			err = atomicWrite(ctx, path, f, want, mode)
			_ = f.Close()
			if err != nil {
				return paths, fmt.Errorf("extract %s: %w", name, err)
			}
		} else if err := os.Chmod(path, mode); err != nil {
			return paths, err
		}
	}
	base := filepath.Join(baseDir, m.Base.SHA256+".ext4")
	ok, err := verifyFile(ctx, base, m.Base.artifact)
	if err != nil {
		return paths, fmt.Errorf("base image %s: %w (remove the corrupt file or pre-place the verified release image)", base, err)
	}
	if !ok {
		if err := downloadBase(ctx, client, base, m.Base); err != nil {
			return paths, fmt.Errorf("prepare base image: %w", err)
		}
	} else if err := os.Chmod(base, 0o644); err != nil {
		return paths, err
	}
	return Paths{
		Bundled: true, Version: m.Version,
		Firecracker: filepath.Join(versionDir, "firecracker"), Jailer: filepath.Join(versionDir, "jailer"),
		Kernel: filepath.Join(versionDir, "kernel"), Sandboxd: filepath.Join(versionDir, "sandboxd"), BaseImage: base,
	}, nil
}

func readManifest(assets fs.FS) (manifest, error) {
	var m manifest
	f, err := assets.Open("manifest.json")
	if err != nil {
		return m, fmt.Errorf("runtime manifest: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("runtime manifest: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return m, errors.New("runtime manifest has trailing data")
	}
	if m.Format != 1 || m.Arch != runtime.GOARCH || !safeVersion(m.Version) || len(m.Files) != 4 {
		return m, errors.New("runtime manifest has invalid format, architecture, release or file list")
	}
	for _, name := range []string{"firecracker", "jailer", "kernel", "sandboxd"} {
		if err := validateArtifact(m.Files[name], 256<<20); err != nil {
			return m, fmt.Errorf("runtime manifest %s: %w", name, err)
		}
	}
	if err := validateArtifact(m.Base.artifact, 32<<30); err != nil {
		return m, fmt.Errorf("runtime manifest base: %w", err)
	}
	if err := validateArtifact(m.Base.Compressed, 2_000_000_000); err != nil {
		return m, fmt.Errorf("runtime manifest compressed base: %w", err)
	}
	u, err := url.Parse(m.Base.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return m, errors.New("runtime manifest base URL must be an absolute HTTP(S) URL without credentials")
	}
	return m, nil
}

func validateArtifact(a artifact, maxSize int64) error {
	h, err := hex.DecodeString(a.SHA256)
	if err != nil || len(h) != sha256.Size || strings.ToLower(a.SHA256) != a.SHA256 || a.Size <= 0 || a.Size > maxSize {
		return errors.New("invalid sha256 or size")
	}
	return nil
}

func safeVersion(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	for _, c := range s {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
		if !allowed {
			return false
		}
	}
	return true
}

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&0o022 != 0 || !ownedByCurrentUser(fi) {
		return fmt.Errorf("runtime directory %s must be a real directory owned by uid %d without group/other write permission", path, os.Geteuid())
	}
	return nil
}

var errChecksum = errors.New("size or sha256 mismatch")

func verifyFile(ctx context.Context, path string, want artifact) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || !ownedByCurrentUser(fi) {
		return false, fmt.Errorf("%s must be a regular file owned by uid %d", path, os.Geteuid())
	}
	if fi.Size() != want.Size {
		return false, errChecksum
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return false, err
	}
	if hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return false, errChecksum
	}
	return true, nil
}

func atomicWrite(ctx context.Context, path string, r io.Reader, want artifact, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agenthold-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, r}, want.Size+1))
	if err != nil {
		return err
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return errChecksum
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func downloadBase(ctx context.Context, client *http.Client, path string, b baseArtifact) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	// Keep compressed bytes temporary: verifying before decoding also bounds the input available
	// to the decompressor, while the decoder window and output size are bounded independently.
	f, err := os.CreateTemp(filepath.Dir(path), ".download-*.zst")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_ = f.Close()
	defer os.Remove(tmp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("base download HTTP %d from %s", resp.StatusCode, b.URL)
	}
	if err := atomicWrite(ctx, tmp, resp.Body, b.Compressed, 0o600); err != nil {
		return fmt.Errorf("compressed image: %w", err)
	}
	compressed, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer compressed.Close()
	decoder, err := zstd.NewReader(contextReader{ctx, compressed}, zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return err
	}
	defer decoder.Close()
	return atomicWrite(ctx, path, decoder, b.artifact, 0o644)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// PinBase copies an explicitly configured development image into the immutable content-addressed
// base store. Rebuilding sourcePath afterwards cannot change an existing agent's lower layer.
func PinBase(ctx context.Context, dataDir, sourcePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if dataDir == "" || sourcePath == "" {
		return "", errors.New("pin base: data directory and source image are required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if err := secureDir(abs); err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	baseDir := filepath.Join(abs, "base")
	if err := secureDir(baseDir); err != nil {
		return "", err
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > 32<<30 {
		return "", errors.New("pin base: source must be a regular image between 1 byte and 32 GiB")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, fi.Size()+1))
	if err != nil {
		return "", err
	}
	if n != fi.Size() {
		return "", errors.New("pin base: source size changed while hashing")
	}
	want := artifact{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	path := filepath.Join(baseDir, want.SHA256+".ext4")
	ok, err := verifyFile(ctx, path, want)
	if err != nil {
		return "", fmt.Errorf("pinned base %s: %w", path, err)
	}
	if ok {
		if err := os.Chmod(path, 0o644); err != nil {
			return "", err
		}
		return path, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := atomicWrite(ctx, path, f, want, 0o644); err != nil {
		return "", fmt.Errorf("pin base: %w", err)
	}
	return path, nil
}
