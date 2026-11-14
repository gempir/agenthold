// Package server wires the HTTP API and the embedded web app together.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config controls how the server is assembled.
type Config struct {
	// Web is the built single-page app. Ignored when DevProxy is set.
	Web fs.FS
	// DevProxy, when set, is the URL of a Vite dev server. Requests outside
	// the API are proxied to it so the UI reloads live during development.
	DevProxy *url.URL
	// API mounts the /api routes.
	API    func(chi.Router)
	Logger *slog.Logger
}

// New returns the root HTTP handler.
func New(cfg Config) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, securityHeaders(cfg.DevProxy != nil))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Group(func(r chi.Router) {
		// Browsers authenticate with a cookie, so reject cross-origin
		// state-changing requests (CSRF).
		r.Use(http.NewCrossOriginProtection().Handler)
		if cfg.API != nil {
			cfg.API(r)
		}
		r.HandleFunc("/api/*", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		})
	})

	if cfg.DevProxy != nil {
		r.Handle("/*", httputil.NewSingleHostReverseProxy(cfg.DevProxy))
	} else {
		r.Handle("/*", spaHandler(cfg.Web))
	}
	return r
}

// spaHandler serves static files and falls back to index.html for client-side routes.
func spaHandler(site fs.FS) http.Handler {
	files := http.FileServerFS(site)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "index.html" {
			serveIndex(w, site)
			return
		}
		if _, err := fs.Stat(site, name); errors.Is(err, fs.ErrNotExist) {
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, site)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, site fs.FS) {
	data, err := fs.ReadFile(site, "index.html")
	if err != nil {
		http.Error(w, "web app not built: run `make web`", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// appCSP allows only the app's own scripts and connections. The API reference
// page (/api/docs) sets its own policy.
const appCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; font-src 'self' data:; connect-src 'self' ws: wss:; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// securityHeaders sets protective headers. In development (Vite dev server)
// the CSP is left out: Vite injects inline scripts for hot reload.
func securityHeaders(dev bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			if !dev && !strings.HasPrefix(r.URL.Path, "/api/") {
				h.Set("Content-Security-Policy", appCSP)
			}
			next.ServeHTTP(w, r)
		})
	}
}
