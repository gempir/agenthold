package runtimeassets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

func TestPrepareWithHarnessesAsset(t *testing.T) {
	dataDir := privateDir(t)
	arch := runtime.GOARCH
	hKey := strings.Repeat("e", 32)
	hRaw := []byte("harnesses-layer-ext4-bytes")
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	hZst := enc.EncodeAll(hRaw, nil)
	_ = enc.Close()

	hAsset := Asset{
		Format:     1,
		Kind:       KindHarnesses,
		Arch:       arch,
		Key:        hKey,
		SHA256:     digest(hRaw).SHA256,
		Size:       int64(len(hRaw)),
		Compressed: digest(hZst),
	}
	hManifestBytes, _ := json.Marshal(hAsset)

	baseRaw := []byte("base-image-bytes")
	enc2, _ := zstd.NewWriter(nil)
	baseZst := enc2.EncodeAll(baseRaw, nil)
	_ = enc2.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/" + arch + "/base-" + testBaseKey + ".zst":
			_, _ = w.Write(baseZst)
		case "/assets/" + arch + "/harnesses-" + hKey + ".json":
			_, _ = w.Write(hManifestBytes)
		case "/assets/" + arch + "/harnesses-" + hKey + ".zst":
			_, _ = w.Write(hZst)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc, jl := []byte("fc"), []byte("jl")
	files := fstest.MapFS{
		"firecracker": {Data: fc},
		"jailer":      {Data: jl},
	}
	fm := fixtureManifest{
		manifest: manifest{
			Format:    2,
			Version:   "v1.0.0",
			Arch:      arch,
			Files:     map[string]artifact{"firecracker": digest(fc), "jailer": digest(jl)},
			Harnesses: &assetRef{Key: hKey},
		},
		Base: fixtureBase{
			baseArtifact: baseArtifact{artifact: digest(baseRaw), Compressed: digest(baseZst)},
		},
	}
	setManifest(t, files, fm)
	// setManifest preserves Harnesses; seed kernel + base manifest as pull.sh does, and let harnesses fetch from srv.
	seedAssets(dataDir, files)

	paths, err := prepare(t.Context(), dataDir, files, srv.Client(), Options{AssetsURL: srv.URL + "/"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	wantPath := filepath.Join(dataDir, "harnesses", hAsset.SHA256+".ext4")
	if resolved, _ := filepath.EvalSymlinks(wantPath); paths.HarnessesImage != resolved {
		t.Fatalf("HarnessesImage = %q, want %q", paths.HarnessesImage, resolved)
	}
	got, err := os.ReadFile(paths.HarnessesImage)
	if err != nil || string(got) != string(hRaw) {
		t.Fatalf("harnesses content = (%q, %v)", got, err)
	}
}

func TestPinHarnesses(t *testing.T) {
	dataDir := privateDir(t)
	src := filepath.Join(t.TempDir(), "harnesses.ext4")
	content := []byte("dev-harnesses-ext4")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	pinned, err := PinHarnesses(t.Context(), dataDir, src)
	if err != nil {
		t.Fatalf("PinHarnesses: %v", err)
	}
	if !strings.Contains(pinned, string(filepath.Separator)+"harnesses"+string(filepath.Separator)) {
		t.Fatalf("pinned path %q not under harnesses/", pinned)
	}
	got, err := os.ReadFile(pinned)
	if err != nil || string(got) != string(content) {
		t.Fatalf("pinned content = (%q, %v)", got, err)
	}
}
