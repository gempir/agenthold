package dl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/runtimeassets"
)

func TestServesHarnessesAssetAndIndex(t *testing.T) {
	hKey := strings.Repeat("e", 32)
	hZst := bytes.Repeat([]byte("compressed harnesses "), 200)
	f := newFixture(t, func(cfg *Config) {
		cfg.Release.HarnessesKey = hKey
	})
	a, err := runtimeassets.ParseAsset([]byte(`{"format":1,"kind":"harnesses","arch":"`+runtime.GOARCH+`","key":"`+hKey+
		`","sha256":"`+strings.Repeat("1", 64)+`","size":8192,"compressed":{"sha256":"`+hexSum(hZst)+`","size":`+fmt.Sprint(len(hZst))+`}}`), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m := runtimeassets.ManifestPath(f.dataDir, a.Arch, a.Kind, a.Key)
	raw, _ := json.Marshal(a)
	for p, b := range map[string][]byte{m: raw, a.CompressedPath(f.dataDir): hZst} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(f.srv.URL + "/dl/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status %d", resp.StatusCode)
	}
	var idx index
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		t.Fatal(err)
	}
	wantPath := "assets/" + runtime.GOARCH + "/harnesses-" + hKey
	if got := idx.Arch[runtime.GOARCH].Harnesses; got != wantPath {
		t.Fatalf("index harnesses = %q, want %q", got, wantPath)
	}

	zresp, err := http.Get(f.srv.URL + "/dl/" + wantPath + ".zst")
	if err != nil {
		t.Fatal(err)
	}
	defer zresp.Body.Close()
	body, _ := io.ReadAll(zresp.Body)
	if zresp.StatusCode != http.StatusOK || !bytes.Equal(body, hZst) {
		t.Fatalf("harnesses.zst status=%d len=%d", zresp.StatusCode, len(body))
	}
}
