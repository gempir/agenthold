package runtimeassets

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

// Release is the release a bundled binary carries, as far as serving it to others needs.
type Release struct {
	Version string
	Arch    string
	// Manifest is the embedded runtime manifest, byte for byte as scripts/release/build.sh wrote it.
	Manifest []byte
	// BaseSHA256 names the uncompressed base (base/<sha>.ext4).
	BaseSHA256 string
	// CompressedSHA256 and CompressedSize pin base-<arch>.ext4.zst.
	CompressedSHA256 string
	CompressedSize   int64
}

// Embedded returns the embedded release, or nil for development builds.
func Embedded() (*Release, error) {
	assets := bundledFiles()
	if assets == nil {
		return nil, nil
	}
	m, err := readManifest(assets)
	if err != nil {
		return nil, err
	}
	raw, err := fs.ReadFile(assets, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("runtime manifest: %w", err)
	}
	return &Release{
		Version: m.Version, Arch: m.Arch, Manifest: raw, BaseSHA256: m.Base.SHA256,
		CompressedSHA256: m.Base.Compressed.SHA256, CompressedSize: m.Base.Compressed.Size,
	}, nil
}

// CompressedBasePath is where this installation keeps the compressed base after fetching it (or
// where an operator may place it), verified against CompressedSHA256 before anything serves it.
func (r *Release) CompressedBasePath(dataDir string) string {
	return filepath.Join(dataDir, "base", r.BaseSHA256+".ext4.zst")
}
