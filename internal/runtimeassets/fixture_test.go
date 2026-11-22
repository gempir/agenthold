package runtimeassets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

// The tests describe a release by fixtureManifest: the embedded manifest plus the base asset it
// names and the URL its compressed file is served from (any path of that server).
type fixtureManifest struct {
	manifest
	Base fixtureBase
}

type fixtureBase struct {
	baseArtifact
	URL string
}

var (
	testKernelKey = strings.Repeat("a", 32)
	testBaseKey   = strings.Repeat("b", 32)
	testKernel    = []byte("verified kernel")
)

func (m fixtureManifest) asset() Asset {
	return Asset{Format: 1, Kind: KindBase, Arch: m.Arch, Key: testBaseKey, SHA256: m.Base.SHA256, Size: m.Base.Size, Compressed: m.Base.Compressed}
}

func setManifest(t *testing.T, files fstest.MapFS, m fixtureManifest) {
	t.Helper()
	real := m.manifest
	real.Format, real.Kernel, real.Base = 2, assetRef{Key: testKernelKey}, assetRef{Key: testBaseKey}
	data, err := json.Marshal(real)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = &fstest.MapFile{Data: data}
	asset, err := json.Marshal(m.asset())
	if err != nil {
		t.Fatal(err)
	}
	files["test/base.json"] = &fstest.MapFile{Data: asset}
	files["test/base-url"] = &fstest.MapFile{Data: []byte(m.Base.URL)}
	files["test/arch"] = &fstest.MapFile{Data: []byte(m.Arch)}
}

// seedAssets puts what pull.sh places before a release starts into dir: the kernel (manifest and
// file) and the base's manifest. Best effort: unsafe-directory tests fail inside prepare.
func seedAssets(dir string, files fstest.MapFS) {
	arch := string(files["test/arch"].Data)
	if arch != "amd64" && arch != "arm64" {
		return
	}
	enc, _ := zstd.NewWriter(nil)
	compressed := enc.EncodeAll(testKernel, nil)
	_ = enc.Close()
	k := Asset{Format: 1, Kind: KindKernel, Arch: arch, Key: testKernelKey, SHA256: digest(testKernel).SHA256, Size: int64(len(testKernel)), Compressed: digest(compressed)}
	kdata, _ := json.Marshal(k)
	writeAtomic := func(p string, b []byte) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.seed")
		if err != nil {
			return
		}
		tmp := f.Name()
		_, werr := f.Write(b)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(tmp)
			return
		}
		_ = os.Chmod(tmp, 0o644)
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
		}
	}
	writeOnce := func(p string, b []byte) {
		if _, err := os.Lstat(p); err == nil {
			return
		}
		writeAtomic(p, b)
	}
	writeOnce(ManifestPath(dir, arch, KindKernel, testKernelKey), kdata)
	writeOnce(k.WorkPath(dir), testKernel)
	// The base manifest follows the fixture, which a test may change between runs. Replace it
	// atomically: tests run prepareT concurrently on one data directory, and a prepare that
	// found it missing would fetch it from the asset source.
	p := ManifestPath(dir, arch, KindBase, testBaseKey)
	data := files["test/base.json"].Data
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, data) {
		return
	}
	writeAtomic(p, data)
}

// prepareT runs prepare like a node whose assets were placed by pull.sh and whose asset source
// (the fixture's base URL, reached through a redirect from <root>assets/...) serves the base.
func prepareT(ctx context.Context, dir string, files fstest.MapFS, client *http.Client, opts Options) (Paths, error) {
	seedAssets(dir, files)
	if u := files["test/base-url"]; u != nil && len(u.Data) > 0 && opts.AssetsURL == "" {
		target := string(u.Data)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		}))
		defer srv.Close()
		opts.AssetsURL = srv.URL + "/"
	}
	return prepare(ctx, dir, files, client, opts)
}
