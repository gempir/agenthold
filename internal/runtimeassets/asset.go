package runtimeassets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The guest kernel and the base image are assets: built natively on a host of their architecture
// (scripts/release/build-assets.sh), named by their key (the hash of their build inputs, which a
// release binary embeds) and described by an asset manifest:
//
//	{"format":1,"kind":"base","arch":"arm64","key":"<32 hex>",
//	 "sha256":"<uncompressed>","size":N,"compressed":{"sha256":"<.zst>","size":N}}
//
// An asset source serves, below its /dl/ root,
//
//	assets/<arch>/<kind>-<key>.json   the manifest
//	assets/<arch>/<kind>-<key>.zst    the zstd-compressed file
//
// In a data directory the manifest is kept as assets/<arch>/<kind>-<key>.json, the working copy
// as kernel/<sha256> or base/<sha256>.ext4 (agents' state names the base by this path) and the
// compressed file next to it with ".zst" appended, which is what /dl/ serves.
const (
	KindKernel = "kernel"
	KindBase   = "base"
)

// Asset is one asset manifest.
type Asset struct {
	Format     int      `json:"format"`
	Kind       string   `json:"kind"`
	Arch       string   `json:"arch"`
	Key        string   `json:"key"`
	SHA256     string   `json:"sha256"`
	Size       int64    `json:"size"`
	Compressed artifact `json:"compressed"`
}

// CompressedSHA256 and CompressedSize pin the compressed file.
func (a Asset) CompressedSHA256() string { return a.Compressed.SHA256 }
func (a Asset) CompressedSize() int64    { return a.Compressed.Size }

func (a Asset) file() baseArtifact {
	return baseArtifact{artifact: artifact{SHA256: a.SHA256, Size: a.Size}, Compressed: a.Compressed}
}

// AssetName is the file name of an asset below assets/<arch>/ without extension.
func AssetName(kind, key string) string { return kind + "-" + key }

// ManifestPath is where a data directory keeps an asset's manifest.
func ManifestPath(dataDir, arch, kind, key string) string {
	return filepath.Join(dataDir, "assets", arch, AssetName(kind, key)+".json")
}

// WorkPath is the verified uncompressed file of an asset in a data directory.
func (a Asset) WorkPath(dataDir string) string {
	if a.Kind == KindKernel {
		return filepath.Join(dataDir, "kernel", a.SHA256)
	}
	return filepath.Join(dataDir, "base", a.SHA256+".ext4")
}

// CompressedPath is the kept compressed file of an asset, served under /dl/assets/.
func (a Asset) CompressedPath(dataDir string) string { return a.WorkPath(dataDir) + ".zst" }

// ParseAsset decodes and checks an asset manifest. Empty wantKind, wantArch or wantKey accept any.
func ParseAsset(data []byte, wantKind, wantArch, wantKey string) (Asset, error) {
	var a Asset
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return a, fmt.Errorf("asset manifest: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return a, errors.New("asset manifest has trailing data")
	}
	maxSize := int64(32 << 30)
	if a.Kind == KindKernel {
		maxSize = 512 << 20
	}
	switch {
	case a.Format != 1:
		return a, fmt.Errorf("asset manifest format %d, want 1", a.Format)
	case a.Kind != KindKernel && a.Kind != KindBase || wantKind != "" && a.Kind != wantKind:
		return a, fmt.Errorf("asset manifest is a %q, want %q", a.Kind, wantKind)
	case a.Arch != "amd64" && a.Arch != "arm64" || wantArch != "" && a.Arch != wantArch:
		return a, fmt.Errorf("asset manifest is for %q, want %q", a.Arch, wantArch)
	case !validKey(a.Key) || wantKey != "" && a.Key != wantKey:
		return a, fmt.Errorf("asset manifest has key %q, want %q", a.Key, wantKey)
	}
	if err := validateArtifact(artifact{SHA256: a.SHA256, Size: a.Size}, maxSize); err != nil {
		return a, fmt.Errorf("asset manifest file: %w", err)
	}
	if err := validateArtifact(a.Compressed, 2_000_000_000); err != nil {
		return a, fmt.Errorf("asset manifest compressed file: %w", err)
	}
	return a, nil
}

// ReadAsset reads the manifest of an asset kept in dataDir.
func ReadAsset(dataDir, arch, kind, key string) (Asset, []byte, error) {
	p := ManifestPath(dataDir, arch, kind, key)
	fi, err := os.Lstat(p)
	if err != nil {
		return Asset{}, nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return Asset{}, nil, fmt.Errorf("%s is not a small regular file", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Asset{}, nil, err
	}
	a, err := ParseAsset(data, kind, arch, key)
	if err != nil {
		return a, nil, fmt.Errorf("%s: %w", p, err)
	}
	return a, data, nil
}

// ensureAsset makes the verified working copy of one asset exist and returns its path: from what
// the data directory has, else from a source.
func ensureAsset(ctx context.Context, dataDir, arch, kind, key string, src *sources, d *downloader) (string, error) {
	for _, dir := range []string{filepath.Join(dataDir, "assets"), filepath.Join(dataDir, "assets", arch), filepath.Join(dataDir, kind)} {
		if err := secureDir(dir); err != nil {
			return "", err
		}
	}
	a, _, err := ReadAsset(dataDir, arch, kind, key)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		data, from, ferr := src.manifest(ctx, arch, kind, key)
		if ferr != nil {
			return "", fmt.Errorf("%s %s is not in %s: %w", kind, key, filepath.Join(dataDir, "assets", arch), ferr)
		}
		if a, err = ParseAsset(data, kind, arch, key); err != nil {
			return "", fmt.Errorf("%s: %w", from, err)
		}
		if err := atomicWriteBytes(ctx, ManifestPath(dataDir, arch, kind, key), data); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("%w (remove it to fetch the manifest again)", err)
	}
	work := a.WorkPath(dataDir)
	sweepBase(filepath.Dir(work), a.SHA256)
	ok, err := verifyFile(ctx, work, a.file().artifact)
	if err != nil {
		return "", fmt.Errorf("%s: %w (remove the corrupt file or place the verified one)", work, err)
	}
	if ok {
		return work, os.Chmod(work, 0o644)
	}
	if err := d.fetchBase(ctx, work, func() ([]string, error) { return src.files(ctx, arch, kind, key) }, a.file()); err != nil {
		return "", err
	}
	return work, nil
}

// atomicWriteBytes replaces path with data (an asset manifest), never leaving a partial file.
func atomicWriteBytes(ctx context.Context, path string, data []byte) error {
	return atomicWrite(ctx, path, bytes.NewReader(data), digestOf(data), 0o644)
}

func digestOf(b []byte) artifact {
	h := sha256.Sum256(b)
	return artifact{SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
}

// sources resolves the /dl/ roots assets are fetched from, asking the controller at most once.
type sources struct {
	opts   Options
	client *http.Client
	roots  []string
	err    error
	done   bool
}

func (s *sources) resolve(ctx context.Context) ([]string, error) {
	if !s.done {
		s.roots, s.err = s.opts.roots(ctx)
		s.done = true
	}
	return s.roots, s.err
}

// files lists the URLs of an asset's compressed file, best source first.
func (s *sources) files(ctx context.Context, arch, kind, key string) ([]string, error) {
	roots, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	urls := make([]string, len(roots))
	for i, r := range roots {
		urls[i] = r + "assets/" + arch + "/" + AssetName(kind, key) + ".zst"
	}
	return urls, nil
}

// manifest fetches an asset's manifest from the first source that has it.
func (s *sources) manifest(ctx context.Context, arch, kind, key string) ([]byte, string, error) {
	roots, err := s.resolve(ctx)
	if err != nil {
		return nil, "", err
	}
	var errs []error
	for _, r := range roots {
		u := r + "assets/" + arch + "/" + AssetName(kind, key) + ".json"
		data, err := s.get(ctx, u)
		if err == nil {
			log(s.opts.Log).Info("fetched asset manifest", "url", u)
			return data, u, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
	}
	return nil, "", errors.Join(errs...)
}

func (s *sources) get(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("asset manifest larger than 64 KiB")
	}
	return data, nil
}
