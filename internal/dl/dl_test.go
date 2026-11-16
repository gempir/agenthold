package dl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/runtimeassets"
)

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fixture struct {
	t        *testing.T
	dataDir  string
	exe      []byte
	manifest []byte
	base     []byte
	rel      *runtimeassets.Release
	h        *Handler
	srv      *httptest.Server
	logs     *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	f := &fixture{
		t: t, dataDir: t.TempDir(), logs: &syncBuffer{},
		exe:      bytes.Repeat([]byte("binary "), 1000),
		manifest: []byte(`{"format":1,"version":"v1.2.3"}`),
		base:     bytes.Repeat([]byte("compressed base "), 5000),
	}
	f.rel = &runtimeassets.Release{
		Version: "v1.2.3", Arch: runtime.GOARCH, Manifest: f.manifest,
		BaseSHA256: strings.Repeat("b", 64), CompressedSHA256: hexSum(f.base), CompressedSize: int64(len(f.base)),
	}
	exePath := filepath.Join(t.TempDir(), "agenthold")
	if err := os.WriteFile(exePath, f.exe, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DataDir: f.dataDir, Release: f.rel, Log: slog.New(slog.NewTextHandler(f.logs, nil)),
		Exe: func() (*os.File, error) { return os.Open(exePath) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	f.h = New(cfg)
	f.srv = httptest.NewServer(f.h)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) placeBase(data []byte) {
	f.t.Helper()
	p := f.rel.CompressedBasePath(f.dataDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) operatorFile(name string, data []byte) {
	f.t.Helper()
	p := filepath.Join(f.dataDir, "dl", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) do(method, path string, hdr map[string]string) (*http.Response, []byte) {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), method, f.srv.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (f *fixture) paths() (binary, manifest, base string) {
	v := "/dl/" + f.rel.Version + "/"
	return v + "agenthold-linux-" + runtime.GOARCH, v + "runtime-linux-" + runtime.GOARCH + ".manifest.json", v + "base-" + runtime.GOARCH + ".ext4.zst"
}

func TestIndex(t *testing.T) {
	f := newFixture(t, nil)
	resp, body := f.do("GET", "/dl/manifest.json", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("index: %d %v", resp.StatusCode, resp.Header)
	}
	var got struct {
		Format  int                          `json:"format"`
		Version string                       `json:"version"`
		Arch    map[string]map[string]string `json:"arch"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	a := runtime.GOARCH
	want := map[string]string{
		"binary":           "v1.2.3/agenthold-linux-" + a,
		"runtime_manifest": "v1.2.3/runtime-linux-" + a + ".manifest.json",
	}
	if got.Format != 1 || got.Version != "v1.2.3" || len(got.Arch) != 1 || len(got.Arch[a]) != 2 ||
		got.Arch[a]["binary"] != want["binary"] || got.Arch[a]["runtime_manifest"] != want["runtime_manifest"] {
		t.Fatalf("index without a base: %s", body)
	}
	f.placeBase(f.base)
	_, body = f.do("GET", "/dl/manifest.json", nil)
	if err := json.Unmarshal(body, &got); err != nil || got.Arch[a]["base"] != "v1.2.3/base-"+a+".ext4.zst" {
		t.Fatalf("index with a base: %s %v", body, err)
	}
	if resp, _ := f.do("HEAD", "/dl/manifest.json", nil); resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		t.Fatalf("HEAD: %d", resp.StatusCode)
	}
}

func TestDevelopmentBuildServesNothingGenerated(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Release = nil })
	for _, p := range []string{"/dl/manifest.json", "/dl/v1.2.3/agenthold-linux-" + runtime.GOARCH} {
		resp, body := f.do("GET", p, nil)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "not a release") {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
}

func TestServesBinaryWithRangesAndETag(t *testing.T) {
	f := newFixture(t, nil)
	bin, _, _ := f.paths()
	resp, body := f.do("GET", bin, nil)
	etag := `"` + hexSum(f.exe) + `"`
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, f.exe) || resp.Header.Get("ETag") != etag ||
		resp.Header.Get("Cache-Control") != immutable || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("binary: %d etag=%q cache=%q", resp.StatusCode, resp.Header.Get("ETag"), resp.Header.Get("Cache-Control"))
	}
	resp, body = f.do("GET", bin, map[string]string{"Range": "bytes=100-199"})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, f.exe[100:200]) {
		t.Fatalf("range: %d %d bytes", resp.StatusCode, len(body))
	}
	resp, body = f.do("GET", bin, map[string]string{"Range": "bytes=100-", "If-Range": etag})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, f.exe[100:]) {
		t.Fatalf("If-Range match: %d", resp.StatusCode)
	}
	resp, body = f.do("GET", bin, map[string]string{"Range": "bytes=100-", "If-Range": `"other"`})
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, f.exe) {
		t.Fatalf("If-Range mismatch must send everything: %d", resp.StatusCode)
	}
	if resp, _ = f.do("GET", bin, map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET: %d", resp.StatusCode)
	}
	resp, body = f.do("HEAD", bin, nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 || resp.ContentLength != int64(len(f.exe)) {
		t.Fatalf("HEAD: %d len=%d", resp.StatusCode, resp.ContentLength)
	}
}

func TestServesRuntimeManifestByteForByte(t *testing.T) {
	f := newFixture(t, nil)
	_, man, _ := f.paths()
	resp, body := f.do("GET", man, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, f.manifest) || resp.Header.Get("ETag") != `"`+hexSum(f.manifest)+`"` {
		t.Fatalf("manifest: %d %s", resp.StatusCode, body)
	}
}

func TestBaseServedOnlyWhenItIsTheVerifiedFile(t *testing.T) {
	f := newFixture(t, nil)
	_, _, base := f.paths()
	resp, body := f.do("GET", base, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no base image") {
		t.Fatalf("missing base: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(f.logs.String(), "does not exist") {
		t.Fatalf("the reason belongs in the log:\n%s", f.logs)
	}
	wrong := bytes.Repeat([]byte("x"), len(f.base)) // right size, wrong content
	f.placeBase(wrong)
	resp, body = f.do("GET", base, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(f.logs.String(), "does not match the sha256") {
		t.Fatalf("wrong base: %d %s\n%s", resp.StatusCode, body, f.logs)
	}
	for _, leak := range []string{f.dataDir, "sha256", "no such file", ".ext4.zst", f.rel.CompressedSHA256} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("response discloses %q: %s", leak, body)
		}
	}
	f.placeBase(f.base[:100])
	if resp, body = f.do("GET", base, nil); resp.StatusCode != http.StatusNotFound || !strings.Contains(f.logs.String(), "pins") ||
		strings.Contains(string(body), "pins") {
		t.Fatalf("short base: %d %s", resp.StatusCode, body)
	}
	f.placeBase(f.base)
	// The file was rewritten with the right content; the cached verdict follows (size, mtime).
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(f.rel.CompressedBasePath(f.dataDir), future, future); err != nil {
		t.Fatal(err)
	}
	resp, body = f.do("GET", base, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, f.base) || resp.Header.Get("ETag") != `"`+f.rel.CompressedSHA256+`"` {
		t.Fatalf("base: %d etag=%s", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp, body = f.do("GET", base, map[string]string{"Range": "bytes=1000-1999", "If-Range": `"` + f.rel.CompressedSHA256 + `"`})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, f.base[1000:2000]) {
		t.Fatalf("base range: %d", resp.StatusCode)
	}
	// Replaced by something else behind our back: not served any more.
	f.placeBase(wrong)
	later := future.Add(time.Hour)
	if err := os.Chtimes(f.rel.CompressedBasePath(f.dataDir), later, later); err != nil {
		t.Fatal(err)
	}
	if resp, _ = f.do("GET", base, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("replaced base still served: %d", resp.StatusCode)
	}
}

func TestOnlyOwnReleaseIsGenerated(t *testing.T) {
	f := newFixture(t, nil)
	for _, p := range []string{
		"/dl/v9.9.9/agenthold-linux-" + runtime.GOARCH,
		"/dl/v1.2.3/agenthold-linux-nonexistentarch",
		"/dl/v1.2.3/other",
		"/dl/v1.2.3/",
		"/dl/",
		"/dl/v1.2.3",
	} {
		if resp, _ := f.do("GET", p, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestOperatorDirectoryWinsAndIsSafe(t *testing.T) {
	f := newFixture(t, nil)
	bin, _, base := f.paths()
	f.operatorFile("manifest.json", []byte(`{"format":1,"version":"operator"}`))
	f.operatorFile("v2.0.0/agenthold-linux-arm64", []byte("other release"))
	f.operatorFile(strings.TrimPrefix(bin, "/dl/"), []byte("operator binary"))
	f.operatorFile("v2.0.0/with space.txt", []byte("spaced"))
	f.operatorFile(".hidden", []byte("secret"))
	f.operatorFile("v2.0.0/.hidden", []byte("secret"))
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(f.dataDir, "dl", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(f.dataDir, "dl", "escapedir")); err != nil {
		t.Fatal(err)
	}
	f.operatorFile("inside", []byte("inside"))
	if err := os.Symlink("inside", filepath.Join(f.dataDir, "dl", "alias")); err != nil {
		t.Fatal(err)
	}
	// A secret next to the data directory, reachable by ".." tricks if paths were not checked.
	if err := os.WriteFile(filepath.Join(f.dataDir, "master.key"), []byte("master"), 0o600); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"/dl/manifest.json":                `{"format":1,"version":"operator"}`,
		"/dl/v2.0.0/agenthold-linux-arm64": "other release",
		bin:                                "operator binary",
		"/dl/v2.0.0/with%20space.txt":      "spaced",
		"/dl/alias":                        "inside",
	} {
		resp, body := f.do("GET", path, nil)
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Errorf("%s: %d %q, want %q", path, resp.StatusCode, body, want)
		}
	}
	if resp, _ := f.do("GET", base, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("generated files not placed by the operator are still generated: %d", resp.StatusCode)
	}
	for _, path := range []string{
		"/dl/../master.key", "/dl/%2e%2e/master.key", "/dl/v2.0.0/../../master.key", "/dl/v2.0.0/%2e%2e/%2e%2e/master.key",
		"/dl/..%2fmaster.key", "/dl/%2e%2e%2fmaster.key", "/dl//etc/passwd", "/dl/\\..\\master.key", "/dl/v2.0.0/..\\x",
		"/dl/escape", "/dl/escapedir/secret", "/dl/.hidden", "/dl/v2.0.0/.hidden", "/dl/v2.0.0/", "/dl/v2.0.0", "/dl/%00",
		"/dl/v2.0.0/./agenthold-linux-arm64",
	} {
		resp, body := f.do("GET", path, nil)
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "master") || strings.Contains(string(body), "outside") {
			t.Errorf("%s: %d %q", path, resp.StatusCode, body)
		}
	}
	// No directory listings, whatever the path looks like.
	for _, path := range []string{"/dl/v2.0.0/", "/dl/v2.0.0", "/dl/"} {
		if resp, body := f.do("GET", path, nil); resp.StatusCode == http.StatusOK || strings.Contains(string(body), "agenthold-linux-arm64") {
			t.Errorf("listing at %s: %d %s", path, resp.StatusCode, body)
		}
	}
}

func TestReadOnly(t *testing.T) {
	f := newFixture(t, nil)
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		resp, _ := f.do(m, "/dl/manifest.json", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %d", m, resp.StatusCode)
		}
	}
}

// openBlocked starts a download of path as client and leaves it unread, so the handler stays busy.
func openBlocked(t *testing.T, f *fixture, path, ip string) (status int, closeBody func()) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+path, nil)
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, func() { resp.Body.Close() }
}

func TestPerAddressLimitOnLargeFiles(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.LargeBytes = 1 << 20 })
	big := make([]byte, 48<<20) // far more than the socket buffers hold, so the handler blocks
	f.operatorFile("v1/big.bin", big)
	f.operatorFile("v1/small.txt", []byte("small"))
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	for range DefaultMaxPerIP {
		status, closeBody := openBlocked(t, f, "/dl/v1/big.bin", "203.0.113.7")
		closers = append(closers, closeBody)
		if status != http.StatusOK {
			t.Fatalf("download within the limit: %d", status)
		}
	}
	resp, body := f.do("GET", "/dl/v1/big.bin", map[string]string{"X-Forwarded-For": "203.0.113.7"})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third download: %d %s", resp.StatusCode, body)
	}
	if resp, _ = f.do("GET", "/dl/v1/small.txt", map[string]string{"X-Forwarded-For": "203.0.113.7"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("small files are not limited: %d", resp.StatusCode)
	}
	if resp, _ = f.do("HEAD", "/dl/v1/big.bin", map[string]string{"X-Forwarded-For": "203.0.113.7"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD is not limited: %d", resp.StatusCode)
	}
	status, closeBody := openBlocked(t, f, "/dl/v1/big.bin", "198.51.100.9")
	closers = append(closers, closeBody)
	if status != http.StatusOK {
		t.Fatalf("another address shares the limit: %d", status)
	}
	closers[0]() // a finished (or dropped) download frees a slot
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, closeBody := openBlocked(t, f, "/dl/v1/big.bin", "203.0.113.7")
		if status == http.StatusOK {
			closers = append(closers, closeBody)
			break
		}
		closeBody()
		if time.Now().After(deadline) {
			t.Fatal("slot was not released after the download ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct{ remote, xff, want string }{
		{"203.0.113.5:4000", "", "203.0.113.5"},
		{"203.0.113.5:4000", "198.51.100.1", "203.0.113.5"}, // a public peer cannot choose its bucket
		{"127.0.0.1:4000", "", "127.0.0.1"},
		{"127.0.0.1:4000", "198.51.100.1", "198.51.100.1"},
		{"10.0.0.2:4000", "1.1.1.1, 198.51.100.1, 10.0.0.9", "198.51.100.1"},
		{"127.0.0.1:4000", "garbage", "127.0.0.1"},
		{"[2001:db8:1:2:3:4:5:6]:4000", "", "2001:db8:1:2::/64"},
		{"[::1]:4000", "2001:db8:1:2::9", "2001:db8:1:2::/64"},
		{"[::ffff:203.0.113.5]:4000", "", "203.0.113.5"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/dl/x", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := clientIP(r); got != tt.want {
			t.Errorf("remote=%s xff=%q: got %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestOperatorFileProblemsDoNotFallThrough(t *testing.T) {
	f := newFixture(t, nil)
	bin, _, _ := f.paths()
	name := strings.TrimPrefix(bin, "/dl/")

	// Unreadable: the generated binary must not silently answer instead of the operator's file.
	f.operatorFile(name, []byte("operator binary"))
	p := filepath.Join(f.dataDir, "dl", filepath.FromSlash(name))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(p); err == nil {
		_ = f.Close()
		t.Skip("running as a user that can read mode 0 files")
	}
	resp, body := f.do("GET", bin, nil)
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(string(body), f.dataDir) ||
		!strings.Contains(f.logs.String(), "cannot open an operator file") {
		t.Fatalf("unreadable operator file: %d %s", resp.StatusCode, body)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if resp, body = f.do("GET", bin, nil); resp.StatusCode != http.StatusOK || !bytes.Equal(body, f.exe) {
		t.Fatalf("without the operator file the generated one is served: %d", resp.StatusCode)
	}
}

func TestOperatorFifoIsRefusedWithoutBlocking(t *testing.T) {
	f := newFixture(t, nil)
	if err := os.MkdirAll(filepath.Join(f.dataDir, "dl", "v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.dataDir, "dl", "v1.0.0", "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan int, 1)
	go func() { resp, _ := f.do("GET", "/dl/v1.0.0/pipe", nil); done <- resp.StatusCode }()
	select {
	case status := <-done:
		if status != http.StatusNotFound {
			t.Fatalf("FIFO: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked the request")
	}
}

func TestOperatorCaching(t *testing.T) {
	f := newFixture(t, nil)
	for name, want := range map[string]string{
		"manifest.json":              "no-cache",
		"top.txt":                    "no-cache",
		"latest/agenthold":           "no-cache",
		"downloads/x":                "no-cache",
		"v2.0.0/agenthold-linux-arm": immutable,
		"0.3.1/x":                    immutable,
		"sha-0123abc/x":              immutable,
		"v1.2.3/other":               immutable, // this controller's own version directory
	} {
		f.operatorFile(name, []byte("data"))
		resp, _ := f.do("GET", "/dl/"+name, nil)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != want {
			t.Errorf("%s: %d cache=%q, want %q", name, resp.StatusCode, resp.Header.Get("Cache-Control"), want)
		}
	}
}

func TestAdvertisedURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://agents.example.com":      "https://agents.example.com/dl/",
		"https://agents.example.com/":     "https://agents.example.com/dl/",
		"http://10.0.0.5:8080":            "http://10.0.0.5:8080/dl/",
		"https://agents.example.com/base": "https://agents.example.com/base/dl/",
		"http://localhost:8080":           "", // the default when public_url is unset
		"http://LOCALHOST":                "",
		"http://127.0.0.1:8080":           "",
		"http://[::1]:8080":               "",
		"http://0.0.0.0:8080":             "",
		"http://app.localhost":            "",
		"":                                "",
		"agents.example.com":              "",
	} {
		got, why := AdvertisedURL(in)
		if got != want || (got == "") != (why != "") {
			t.Errorf("%q: got %q (%q), want %q", in, got, why, want)
		}
	}
}
