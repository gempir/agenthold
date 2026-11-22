package runtimeassets

import (
	"fmt"
	"io/fs"
)

// Release is the release a bundled binary carries, as far as serving it to others needs.
type Release struct {
	Version string
	Arch    string
	// Manifest is the embedded runtime manifest, byte for byte as scripts/release/build.sh wrote it.
	Manifest []byte
	// KernelKey, BaseKey and HarnessesKey name the assets this release runs with.
	KernelKey, BaseKey, HarnessesKey string
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
	var harnessesKey string
	if m.Harnesses != nil {
		harnessesKey = m.Harnesses.Key
	}
	return &Release{Version: m.Version, Arch: m.Arch, Manifest: raw, KernelKey: m.Kernel.Key, BaseKey: m.Base.Key, HarnessesKey: harnessesKey}, nil
}
