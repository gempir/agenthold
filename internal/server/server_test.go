package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

func testSite() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	if rec := get(t, New(Config{Web: testSite()}), "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMountsAPI(t *testing.T) {
	h := New(Config{Web: testSite(), API: func(r chi.Router) {
		r.Get("/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"pong":true}`) })
	}})
	var got map[string]bool
	if err := json.NewDecoder(get(t, h, "/api/v1/ping").Body).Decode(&got); err != nil || !got["pong"] {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	h := New(Config{Web: testSite(), API: func(r chi.Router) {
		r.Post("/api/v1/thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/thing", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestSPA(t *testing.T) {
	h := New(Config{Web: testSite()})
	tests := []struct {
		path     string
		status   int
		contains string
	}{
		{"/", http.StatusOK, "app"},
		{"/index.html", http.StatusOK, "app"},
		{"/agents/123", http.StatusOK, "app"}, // client-side route
		{"/assets/app.js", http.StatusOK, "console.log"},
		{"/assets/missing.js", http.StatusNotFound, ""},
		{"/api/unknown", http.StatusNotFound, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := get(t, h, tt.path)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if b := rec.Body.String(); !strings.Contains(b, tt.contains) {
				t.Fatalf("body %q does not contain %q", b, tt.contains)
			}
		})
	}
}

func TestSPANotBuilt(t *testing.T) {
	if rec := get(t, New(Config{Web: fstest.MapFS{}}), "/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestDevProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "vite:"+r.URL.Path)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{DevProxy: u})

	if b := get(t, h, "/src/main.tsx").Body.String(); b != "vite:/src/main.tsx" {
		t.Fatalf("proxied body = %q", b)
	}
	if rec := get(t, h, "/api/v1/version"); rec.Code != http.StatusNotFound || strings.HasPrefix(rec.Body.String(), "vite:") {
		t.Fatalf("api must not be proxied: %d %q", rec.Code, rec.Body.String())
	}
}

func TestMountsDownloadsOutsideAuthAndCSRF(t *testing.T) {
	dl := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "dl:"+r.URL.Path) })
	h := New(Config{Web: testSite(), DL: dl})
	if rec := get(t, h, "/dl/manifest.json"); rec.Code != http.StatusOK || rec.Body.String() != "dl:/dl/manifest.json" {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if rec := get(t, New(Config{Web: testSite()}), "/dl/manifest.json"); rec.Body.String() == "dl:/dl/manifest.json" {
		t.Fatal("downloads mounted without a handler")
	}
}
