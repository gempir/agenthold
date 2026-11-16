package runtimeassets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/compress/zstd"
)

func fastDownloads(t *testing.T) {
	t.Helper()
	tune = func(d *downloader) {
		d.backoffMin, d.backoffMax, d.stall, d.giveUp, d.progress = time.Millisecond, 5*time.Millisecond, 2*time.Second, 5*time.Second, 0
	}
	t.Cleanup(func() { tune = nil })
}

// bigFixture is fixture with an incompressible base, so a compressed file big enough to be cut
// in the middle exists.
func bigFixture(t *testing.T) (manifest, fstest.MapFS, []byte, []byte) {
	t.Helper()
	m, files, _, _ := fixture(t)
	raw := make([]byte, 1<<20)
	rng := rand.NewChaCha8([32]byte{1})
	_, _ = rng.Read(raw)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := enc.EncodeAll(raw, nil)
	_ = enc.Close()
	m.Base.artifact, m.Base.Compressed = digest(raw), digest(compressed)
	setManifest(t, files, m)
	return m, files, raw, compressed
}

type seenRequest struct{ rng, ifRange string }

type recorder struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (r *recorder) add(req *http.Request) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, seenRequest{req.Header.Get("Range"), req.Header.Get("If-Range")})
	return len(r.seen)
}

func (r *recorder) get() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenRequest(nil), r.seen...)
}

// serveBytes answers like a normal static file server (Range, If-Range, HEAD).
func serveBytes(w http.ResponseWriter, r *http.Request, data []byte, etag string) {
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// dropAfter sends the headers for the whole body, n bytes of it, then kills the connection.
func dropAfter(w http.ResponseWriter, data []byte, etag string, n int) {
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data[:n])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

// dropRange answers a "bytes=N-" request like a file server would (206) and cuts the connection
// after n bytes of the reply.
func dropRange(w http.ResponseWriter, r *http.Request, data []byte, etag string, n int) {
	var start int
	_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &start)
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
	w.Header().Set("Content-Length", fmt.Sprint(len(data)-start))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[start : start+n])
	w.(http.Flusher).Flush()
	panic(http.ErrAbortHandler)
}

func prepareFrom(t *testing.T, srv *httptest.Server, m manifest, files fstest.MapFS, dir string) error {
	t.Helper()
	m.Base.URL = srv.URL + "/base.zst"
	setManifest(t, files, m)
	_, err := prepare(t.Context(), dir, files, srv.Client(), Options{Log: slog.New(slog.DiscardHandler)})
	return err
}

func baseDirOf(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(canonical, "base")
}

func assertBase(t *testing.T, dir string, m manifest, raw, compressed []byte) {
	t.Helper()
	base := baseDirOf(t, dir)
	if got, err := os.ReadFile(filepath.Join(base, m.Base.SHA256+".ext4")); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("base image: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(base, m.Base.SHA256+".ext4.zst")); err != nil || !bytes.Equal(got, compressed) {
		t.Fatalf("compressed image is not kept: %v", err)
	}
	for _, name := range []string{".part", ".part.etag"} {
		if _, err := os.Stat(filepath.Join(base, "."+m.Base.SHA256+".ext4.zst"+name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s left behind: %v", name, err)
		}
	}
	assertNoTemps(t, dir)
}

func TestDownloadResumesAfterDroppedConnection(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch rec.add(r) {
		case 1:
			dropAfter(w, compressed, `"v1"`, 300_000)
		case 2:
			dropRange(w, r, compressed, `"v1"`, 100_000) // the 206 is cut short too
		default:
			serveBytes(w, r, compressed, `"v1"`)
		}
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	seen := rec.get()
	if len(seen) != 3 || seen[0].rng != "" {
		t.Fatalf("requests: %+v", seen)
	}
	if seen[1].rng != "bytes=300000-" || seen[2].rng != "bytes=400000-" || seen[1].ifRange != `"v1"` || seen[2].rng == "" || seen[2].ifRange != `"v1"` {
		t.Fatalf("resume requests need Range and If-Range: %+v", seen)
	}
}

func TestDownloadResumesAcrossProcesses(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	dir := privateDir(t)
	base := baseDirOf(t, dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// What an interrupted run left behind, plus a stale file of another release.
	part := partPath(base, m.Base.SHA256)
	if err := os.WriteFile(part, compressed[:200_000], 0o600); err != nil {
		t.Fatal(err)
	}
	saveValidator(part, `"v1"`)
	stale := partPath(base, strings.Repeat("a", 64))
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		serveBytes(w, r, compressed, `"v1"`)
	}))
	defer srv.Close()
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	if seen := rec.get(); len(seen) != 1 || seen[0].rng != "bytes=200000-" || seen[0].ifRange != `"v1"` {
		t.Fatalf("requests: %+v", seen)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale part file of another release was not cleaned up: %v", err)
	}
}

func TestDownloadServerIgnoresRange(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec.add(r) == 1 {
			dropAfter(w, compressed, `"v1"`, 400_000)
		}
		_, _ = w.Write(compressed) // 200 with the whole body, whatever was asked
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	if len(rec.get()) != 2 {
		t.Fatalf("requests: %+v", rec.get())
	}
}

func TestDownloadChangedFileRestartsFromZero(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	stale := bytes.Repeat([]byte{7}, len(compressed))
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec.add(r) == 1 {
			dropAfter(w, stale, `"old"`, 300_000) // an older upload
		}
		serveBytes(w, r, compressed, `"new"`) // If-Range "old" no longer matches: full 200
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	seen := rec.get()
	if len(seen) != 2 || seen[1].ifRange != `"old"` {
		t.Fatalf("requests: %+v", seen)
	}
}

func TestDownloadRangeNotSatisfiableRestarts(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n := rec.add(r); {
		case n == 1:
			dropAfter(w, compressed, "", 100_000)
		case r.Header.Get("Range") != "":
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			_, _ = w.Write(compressed)
		}
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
}

func TestDownloadMixedBytesStartOverOnce(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	garbage := bytes.Repeat([]byte{9}, len(compressed))
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec.add(r) == 1 {
			dropAfter(w, garbage, `"v1"`, 300_000)
		}
		serveBytes(w, r, compressed, `"v1"`) // claims to be the same file, but is not
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	seen := rec.get()
	if len(seen) != 3 || seen[2].rng != "" {
		t.Fatalf("after the mismatch the third request must start from zero: %+v", seen)
	}
}

func TestDownloadCorruptBytesLeaveNothing(t *testing.T) {
	fastDownloads(t)
	m, files, _, compressed := bigFixture(t)
	bad := bytes.Clone(compressed)
	bad[len(bad)/2] ^= 0xFF
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveBytes(w, r, bad, `"v1"`) }))
	defer srv.Close()
	dir := privateDir(t)
	err := prepareFrom(t, srv, m, files, dir)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt download accepted: %v", err)
	}
	entries, rerr := os.ReadDir(baseDirOf(t, dir))
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("%s left in the base directory", e.Name())
		}
	}
}

func TestDownloadRetriesServerErrorsAndStopsOnNotFound(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			rec.add(r)
			http.NotFound(w, r)
			return
		}
		if n := rec.add(r); n <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		serveBytes(w, r, compressed, `"v1"`)
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	if n := len(rec.get()); n != 3 {
		t.Fatalf("requests = %d, want 3 (two 503 then success)", n)
	}

	m.Base.URL = srv.URL + "/missing"
	setManifest(t, files, m)
	before := len(rec.get())
	err := func() error {
		_, err := prepare(t.Context(), privateDir(t), files, srv.Client(), Options{Log: slog.New(slog.DiscardHandler)})
		return err
	}()
	if err == nil || !strings.Contains(err.Error(), "404") || len(rec.get())-before != 1 {
		t.Fatalf("404 must fail at once: err=%v requests=%d", err, len(rec.get())-before)
	}
}

func TestDownloadGivesUpWithoutProgress(t *testing.T) {
	fastDownloads(t)
	tune0 := tune
	tune = func(d *downloader) { tune0(d); d.giveUp = 50 * time.Millisecond }
	m, files, _, _ := bigFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := prepareFrom(t, srv, m, files, privateDir(t)); err == nil || !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadStalledConnectionIsRetried(t *testing.T) {
	fastDownloads(t)
	tune0 := tune
	tune = func(d *downloader) { tune0(d); d.stall = 100 * time.Millisecond }
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec.add(r) == 1 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(compressed)))
			_, _ = w.Write(compressed[:1000])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // hangs without closing
			return
		}
		serveBytes(w, r, compressed, `"v1"`)
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
}

func TestPrepareUsesPlacedCompressedImage(t *testing.T) {
	m, files, raw, compressed := fixture(t)
	dir := privateDir(t)
	base := baseDirOf(t, dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, m.Base.SHA256+".ext4.zst"), compressed, 0o644); err != nil {
		t.Fatal(err)
	}
	m.Base.URL = ""
	setManifest(t, files, m)
	paths, err := prepare(t.Context(), dir, files, &http.Client{Timeout: time.Millisecond}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(paths.BaseImage); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("base: %v", err)
	}
}

func TestConcurrentPreparesDownloadOnce(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(100 * time.Millisecond)
		serveBytes(w, r, compressed, `"v1"`)
	}))
	defer srv.Close()
	dir := privateDir(t)
	m.Base.URL = srv.URL + "/base.zst"
	setManifest(t, files, m)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() {
			_, err := prepare(t.Context(), dir, files, srv.Client(), Options{Log: slog.New(slog.DiscardHandler)})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("%d downloads, want 1", n)
	}
	assertBase(t, dir, m, raw, compressed)
}

func TestDownloadLogsProgress(t *testing.T) {
	fastDownloads(t)
	m, files, _, compressed := bigFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveBytes(w, r, compressed, `"v1"`) }))
	defer srv.Close()
	var out bytes.Buffer
	m.Base.URL = srv.URL
	setManifest(t, files, m)
	if _, err := prepare(t.Context(), privateDir(t), files, srv.Client(), Options{Log: slog.New(slog.NewTextHandler(&out, nil))}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "percent=") || !strings.Contains(out.String(), "of=") {
		t.Fatalf("no progress logged:\n%s", out.String())
	}
}

func TestCancelledDownloadKeepsPartForResume(t *testing.T) {
	fastDownloads(t)
	m, files, _, compressed := bigFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(compressed)))
		_, _ = w.Write(compressed[:5000])
		w.(http.Flusher).Flush()
		time.AfterFunc(50*time.Millisecond, cancel)
		<-r.Context().Done()
	}))
	defer srv.Close()
	m.Base.URL = srv.URL
	setManifest(t, files, m)
	dir := privateDir(t)
	_, err := prepare(ctx, dir, files, srv.Client(), Options{Log: slog.New(slog.DiscardHandler)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	fi, err := os.Stat(partPath(baseDirOf(t, dir), m.Base.SHA256))
	if err != nil || fi.Size() == 0 {
		t.Fatalf("part file: %v", err)
	}
}

func TestBaseSourceResolution(t *testing.T) {
	m := manifest{Version: "v1.2.3", Arch: runtime.GOARCH}
	file := "v1.2.3/base-" + runtime.GOARCH + ".ext4.zst"
	tests := []struct {
		name    string
		embed   string
		opts    Options
		want    []string
		wantErr string
	}{
		{name: "embedded only", embed: "https://github.example/base.zst", want: []string{"https://github.example/base.zst"}},
		{
			name: "config wins over controller and embedded", embed: "https://github.example/base.zst",
			opts: Options{BaseURL: "https://demo.example/dl/", Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v1.2.3"}},
			want: []string{"https://demo.example/dl/" + file},
		},
		{name: "config direct file url", opts: Options{BaseURL: "https://mirror.example/x/base.zst"}, want: []string{"https://mirror.example/x/base.zst"}},
		{
			name: "controller wins over embedded", embed: "https://github.example/base.zst",
			opts: Options{Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v1.2.3"}},
			want: []string{"https://ctl.example/dl/" + file, "https://github.example/base.zst"},
		},
		{
			name: "controller only", opts: Options{Controller: Controller{DownloadURL: "https://ctl.example/prefix/dl/", Version: "v1.2.3"}},
			want: []string{"https://ctl.example/prefix/dl/" + file},
		},
		{
			name: "relative embedded url joins the root", embed: "v1.2.3/custom.zst",
			opts: Options{BaseURL: "https://demo.example/dl/"}, want: []string{"https://demo.example/dl/v1.2.3/custom.zst"},
		},
		{
			name: "version skew without fallback", opts: Options{Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v2.0.0", Live: true}},
			wantErr: "runs agenthold v1.2.3 but its controller runs v2.0.0",
		},
		{
			name: "persisted version is only last seen", opts: Options{Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v2.0.0"}},
			wantErr: "its controller was last seen running v2.0.0",
		},
		{
			name: "unusable controller url falls through to embedded", embed: "https://github.example/base.zst",
			opts: Options{Controller: Controller{DownloadURL: "ftp://ctl.example/dl/", Version: "v1.2.3"}, Log: slog.New(slog.DiscardHandler)},
			want: []string{"https://github.example/base.zst"},
		},
		{
			name: "unusable controller url without fallback", opts: Options{Controller: Controller{DownloadURL: "ctl.example/dl/", Version: "v1.2.3"}},
			wantErr: "download URL is unusable",
		},
		{
			name: "refreshed controller replaces the persisted one", embed: "https://github.example/base.zst",
			opts: Options{Controller: Controller{
				DownloadURL: "https://old.example/dl/", Version: "v1.0.0",
				Refresh: func(context.Context) (Controller, error) {
					return Controller{DownloadURL: "https://new.example/dl/", Version: "v1.2.3"}, nil
				},
			}, Log: slog.New(slog.DiscardHandler)},
			want: []string{"https://new.example/dl/" + file, "https://github.example/base.zst"},
		},
		{
			name: "failed refresh keeps what was learned", opts: Options{Controller: Controller{
				DownloadURL: "https://old.example/dl/", Version: "v1.2.3",
				Refresh: func(context.Context) (Controller, error) { return Controller{}, errors.New("down") },
			}, Log: slog.New(slog.DiscardHandler)},
			want: []string{"https://old.example/dl/" + file},
		},
		{
			name: "version skew falls back to embedded", embed: "https://github.example/base.zst",
			opts: Options{Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v2.0.0"}, Log: slog.New(slog.DiscardHandler)},
			want: []string{"https://github.example/base.zst"},
		},
		{
			name: "config ignores skew", opts: Options{BaseURL: "https://demo.example/dl/", Controller: Controller{DownloadURL: "https://ctl.example/dl/", Version: "v2"}},
			want: []string{"https://demo.example/dl/" + file},
		},
		{name: "nothing", wantErr: "base_url"},
		{name: "relative embedded without root", embed: "v1.2.3/custom.zst", wantErr: "base_url"},
		{name: "bad config", opts: Options{BaseURL: "ftp://x/"}, wantErr: "sandbox.firecracker.base_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := m
			m.Base.URL = tt.embed
			got, err := tt.opts.candidates(t.Context(), m)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestPrepareTriesNextSourceWhenControllerCannotServe(t *testing.T) {
	fastDownloads(t)
	m, files, raw, compressed := bigFixture(t)
	var controller, github atomic.Int32
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller.Add(1)
		http.NotFound(w, r)
	}))
	defer ctl.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		github.Add(1)
		serveBytes(w, r, compressed, `"v1"`)
	}))
	defer gh.Close()
	m.Base.URL = gh.URL + "/base.zst"
	setManifest(t, files, m)
	dir := privateDir(t)
	opts := Options{Controller: Controller{DownloadURL: ctl.URL + "/dl/", Version: m.Version}, Log: slog.New(slog.DiscardHandler)}
	if _, err := prepare(t.Context(), dir, files, http.DefaultClient, opts); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
	if controller.Load() != 1 || github.Load() != 1 {
		t.Fatalf("controller=%d github=%d", controller.Load(), github.Load())
	}
}

func TestManifestBaseURLForms(t *testing.T) {
	for url, ok := range map[string]bool{
		"": true, "https://x.example/b.zst": true, "http://x.example/b.zst": true, "v1/base.zst": true,
		"/abs/path": false, "../x": false, "ftp://x.example/b": false, "https://u:p@x.example/b": false,
	} {
		m, files, _, _ := fixture(t)
		m.Base.URL = url
		setManifest(t, files, m)
		_, err := readManifest(files)
		if (err == nil) != ok {
			t.Errorf("base url %q: err=%v, want ok=%v", url, err, ok)
		}
	}
}

func TestControllerIsOnlyAskedWhenADownloadIsNeeded(t *testing.T) {
	m, files, _, _ := fixture(t)
	dir := privateDir(t)
	base := baseDirOf(t, dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("base disk fixture\n"), 100)
	if err := os.WriteFile(filepath.Join(base, m.Base.SHA256+".ext4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	asked := false
	opts := Options{Controller: Controller{
		DownloadURL: "https://c.example/dl/", Version: m.Version,
		Refresh: func(context.Context) (Controller, error) { asked = true; return Controller{}, errors.New("down") },
	}}
	if _, err := prepare(t.Context(), dir, files, &http.Client{Timeout: time.Millisecond}, opts); err != nil || asked {
		t.Fatalf("err=%v asked=%v: a node with its base must start without the controller", err, asked)
	}
}

// A server that ignores Range and cuts the connection every time restarts from zero forever. Bytes
// it re-sends below the high-water mark are no progress, so the give-up clock keeps running.
func TestDownloadRestartingFromZeroDoesNotCountAsProgress(t *testing.T) {
	fastDownloads(t)
	tune0 := tune
	tune = func(d *downloader) { tune0(d); d.giveUp = 300 * time.Millisecond }
	m, files, _, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 300_000
		if rec.add(r) == 1 {
			n = 400_000
		}
		dropAfter(w, compressed, "", n) // always a plain 200 from byte zero
	}))
	defer srv.Close()
	done := make(chan error, 1)
	go func() { done <- prepareFrom(t, srv, m, files, privateDir(t)) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no progress") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never gave up")
	}
	if n := len(rec.get()); n < 3 {
		t.Fatalf("only %d requests: it did not retry", n)
	}
}

func TestDownloadGrowthBeyondTheHighWaterMarkIsProgress(t *testing.T) {
	fastDownloads(t)
	tune0 := tune
	tune = func(d *downloader) { tune0(d); d.giveUp = 300 * time.Millisecond }
	m, files, raw, compressed := bigFixture(t)
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rec.add(r)
		if n < 6 {
			time.Sleep(100 * time.Millisecond) // longer than giveUp in total: only growth keeps it alive
			dropAfter(w, compressed, "", 100_000*n)
		}
		_, _ = w.Write(compressed)
	}))
	defer srv.Close()
	dir := privateDir(t)
	if err := prepareFrom(t, srv, m, files, dir); err != nil {
		t.Fatal(err)
	}
	assertBase(t, dir, m, raw, compressed)
}

func TestUnreachableSourceDoesNotBlockTheNextOne(t *testing.T) {
	fastDownloads(t)
	tune0 := tune
	tune = func(d *downloader) {
		tune0(d)
		d.unreachable, d.unreachableOnly = 150*time.Millisecond, 400*time.Millisecond
		d.giveUp = time.Minute
	}
	m, files, raw, compressed := bigFixture(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // connection refused from now on
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveBytes(w, r, compressed, `"v1"`) }))
	defer good.Close()
	m.Base.URL = good.URL + "/base.zst"
	setManifest(t, files, m)
	dir := privateDir(t)
	start := time.Now()
	opts := Options{Controller: Controller{DownloadURL: deadURL + "/dl/", Version: m.Version}, Log: slog.New(slog.DiscardHandler)}
	if _, err := prepare(t.Context(), dir, files, good.Client(), opts); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	assertBase(t, dir, m, raw, compressed)

	// The only source gets the longer allowance, then the error says so.
	m.Base.URL = ""
	setManifest(t, files, m)
	start = time.Now()
	_, err := prepare(t.Context(), privateDir(t), files, good.Client(), Options{BaseURL: deadURL + "/x.zst", Log: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "delivered no data") || time.Since(start) < 400*time.Millisecond {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestSweepRemovesOnlyOldIdleLeftovers(t *testing.T) {
	m, files, _, _ := fixture(t)
	dir := privateDir(t)
	base := baseDirOf(t, dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("base disk fixture\n"), 100)
	if err := os.WriteFile(filepath.Join(base, m.Base.SHA256+".ext4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("c", 64)
	oldTemp, newTemp := filepath.Join(base, ".agenthold-old"), filepath.Join(base, ".agenthold-new")
	oldLock, ownLock := filepath.Join(base, "."+other+".lock"), filepath.Join(base, "."+m.Base.SHA256+".lock")
	part := partPath(base, m.Base.SHA256)
	for _, p := range []string{oldTemp, newTemp, oldLock, ownLock, part} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{oldTemp, oldLock, ownLock, part} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// Somebody else holds the lock of a third release: temp files might be theirs.
	busy := filepath.Join(base, "."+strings.Repeat("d", 64)+".lock")
	if err := os.WriteFile(busy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(busy, old, old); err != nil {
		t.Fatal(err)
	}
	unlock, ok, err := tryLock(busy)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := prepare(t.Context(), dir, files, http.DefaultClient, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldTemp); err != nil {
		t.Fatal("temp file removed while another process holds a lock")
	}
	unlock()
	if _, err := prepare(t.Context(), dir, files, http.DefaultClient, Options{}); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{oldTemp: false, newTemp: true, oldLock: false, ownLock: true, part: true, busy: false} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
