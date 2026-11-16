package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Time limits of the preview proxy.
const (
	previewDial = 15 * time.Second
	// previewHeaders bounds how long an app may take to start its response
	// (dev servers compile on the first request).
	previewHeaders = 5 * time.Minute
)

// gateCookies are never passed to apps, and apps may not set them.
var gateCookies = []string{
	"ah_preview", "__Host-ah_preview", "ah_preview_state", "__Host-ah_preview_state",
	auth.CookieName, auth.SecureCookieName,
}

// previewProxy proxies to guest ports through tunnel dial streams. The
// upstream URL host encodes agent and port, so pooled connections are never
// shared between machines.
func (a *API) previewProxy() http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			agentID, port, err := parseGuestAddr(addr)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, previewDial)
			defer cancel()
			return node.Client(a.Node, agentID).Dial(ctx, port)
		},
		MaxConnsPerHost:       previewConcurrency,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: previewHeaders,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // stream (SSE, chunked responses) without buffering
		Rewrite:       a.rewritePreview,
		ModifyResponse: func(res *http.Response) error {
			hostOnlyCookies(res.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var de *tunnel.DialError
			switch {
			case errors.As(err, &de) && de.Refused:
				previewFail(w, r, http.StatusBadGateway, "Nothing is listening", fmt.Sprintf("Nothing is listening on port %d in the machine. Start the server and reload.", de.Port))
			case errors.As(err, &de):
				previewFail(w, r, http.StatusBadGateway, "Bad gateway", de.Error())
			case errors.Is(err, tunnel.ErrNotConnected):
				previewFail(w, r, http.StatusServiceUnavailable, "Unavailable", "The machine is not running.")
			case errors.Is(err, context.Canceled):
				// The client went away.
			default:
				slog.WarnContext(r.Context(), "preview proxy", "host", r.Host, "err", err)
				previewFail(w, r, http.StatusBadGateway, "Bad gateway", "The app did not answer.")
			}
		},
	}
}

// headerKey normalises a header name the way some app stacks do (CGI, PHP,
// nginx with underscores_in_headers): lowercase, "_" as "-".
func headerKey(k string) string { return strings.ReplaceAll(strings.ToLower(k), "_", "-") }

func (a *API) rewritePreview(pr *httputil.ProxyRequest) {
	t := pr.In.Context().Value(previewKey{}).(previewTarget)
	out := pr.Out
	out.URL.Scheme = "http"
	out.URL.Host = guestAddr(t.exposure.AgentID, int(t.exposure.Port))
	out.Host = pr.In.Host // the app sees its public hostname
	for k := range out.Header {
		n := headerKey(k)
		if strings.HasPrefix(n, "x-agenthold-") || strings.HasPrefix(n, "x-forwarded-") || n == "forwarded" {
			out.Header.Del(k)
		}
	}
	pr.SetXForwarded()
	out.Header.Set("X-Forwarded-Proto", a.Preview.Hosts.Scheme)
	if carriesAgentholdToken(out.Header.Values("Authorization")) {
		out.Header.Del("Authorization")
	}
	stripCookies(out.Header, gateCookies...)
	if t.user == nil {
		return
	}
	u := t.user
	out.Header.Set("X-Agenthold-User", u.ID)
	out.Header.Set("X-Agenthold-Email", u.Email)
	out.Header.Set("X-Agenthold-Name", u.Name)
	now := a.Now()
	if a.Identity != nil {
		tok, err := a.Identity.Sign(preview.Claims{
			Issuer: a.PublicURL, Subject: u.ID, Audience: t.host, Email: u.Email, Name: u.Name,
			IssuedAt: now.Unix(), Expires: now.Add(preview.IdentityTTL).Unix(),
		})
		if err == nil {
			out.Header.Set("X-Agenthold-Identity", tok)
		}
	}
}

// carriesAgentholdToken reports whether Authorization values contain an
// agenthold API token, in any scheme (Basic credentials decoded).
func carriesAgentholdToken(values []string) bool {
	for _, v := range values {
		if strings.Contains(v, auth.TokenPrefix) {
			return true
		}
		scheme, cred, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(scheme, "basic") {
			if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred)); err == nil && strings.Contains(string(b), auth.TokenPrefix) {
				return true
			}
		}
	}
	return false
}

// guestAddr encodes a machine and port as a dial address.
func guestAddr(agentID string, port int) string {
	return net.JoinHostPort(hex.EncodeToString([]byte(agentID))+".guest", strconv.Itoa(port))
}

func parseGuestAddr(addr string) (string, int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	raw, err := hex.DecodeString(strings.TrimSuffix(host, ".guest"))
	if err != nil {
		return "", 0, fmt.Errorf("bad guest address %q", addr)
	}
	port, err := strconv.Atoi(p)
	return string(raw), port, err
}

// stripCookies removes the named cookies from a request's Cookie headers.
func stripCookies(h http.Header, names ...string) {
	lines := h.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for part := range strings.SplitSeq(line, ";") {
			part = strings.TrimSpace(part)
			name, _, _ := strings.Cut(part, "=")
			drop := part == ""
			for _, n := range names {
				drop = drop || strings.EqualFold(strings.TrimSpace(name), n)
			}
			if !drop {
				kept = append(kept, part)
			}
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// hostOnlyCookies rewrites an app's Set-Cookie headers so they stay on the
// exposure host: every cookie is re-serialised without Domain, and cookies
// whose Domain is ambiguous (repeated, unparsable, non-ASCII) or that would
// shadow the gate's own are dropped.
func hostOnlyCookies(h http.Header) {
	lines := h.Values("Set-Cookie")
	if len(lines) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, line := range lines {
		if out, ok := hostOnlyCookie(line); ok {
			h.Add("Set-Cookie", out)
		}
	}
}

func hostOnlyCookie(line string) (string, bool) {
	c, err := http.ParseSetCookie(line)
	if err != nil {
		return "", false
	}
	for _, g := range gateCookies {
		if strings.EqualFold(c.Name, g) {
			return "", false
		}
	}
	domains := 0
	_, attrs, _ := strings.Cut(line, ";")
	for attr := range strings.SplitSeq(attrs, ";") {
		k, v, _ := strings.Cut(attr, "=")
		if strings.EqualFold(strings.TrimSpace(k), "domain") {
			domains++
			for i := 0; i < len(v); i++ {
				if v[i] >= 0x80 {
					return "", false
				}
			}
		}
	}
	if domains > 1 {
		return "", false
	}
	for _, u := range c.Unparsed {
		k, _, _ := strings.Cut(u, "=")
		if strings.EqualFold(strings.TrimSpace(k), "domain") {
			return "", false
		}
	}
	c.Domain = ""
	out := c.String()
	return out, out != ""
}

// previewFail answers a browser navigation with a small page and anything
// else with JSON.
func previewFail(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	if browserNavigation(r) {
		previewPage(w, status, title, msg, "")
		return
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

const pageStyle = `body{font:15px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#222}` +
	`@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}a{color:#8ab4f8}}h1{font-size:1.25rem}`

func pageHeaders(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
}

func previewPage(w http.ResponseWriter, status int, title, msg, link string) {
	pageHeaders(w, status)
	body := "<p>" + html.EscapeString(msg) + "</p>"
	if link != "" {
		body += `<p><a href="` + html.EscapeString(link) + `">Try again</a></p>`
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\"><title>%s</title><style>%s</style><h1>%s</h1>%s\n",
		html.EscapeString(title), pageStyle, html.EscapeString(title), body)
}

func logoutPage(w http.ResponseWriter) {
	pageHeaders(w, http.StatusOK)
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\"><title>Sign out</title><style>%s</style><h1>Sign out of this preview?</h1><form method=post action=\"/_agenthold/logout\"><button type=submit>Sign out</button></form>\n", pageStyle)
}

func wakingPage(w http.ResponseWriter) {
	w.Header().Set("Refresh", "2")
	pageHeaders(w, http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=2><meta name=viewport content=\"width=device-width\"><title>Waking up…</title><style>%s</style><h1>Waking up…</h1><p>The machine was asleep and is starting. This page reloads by itself.</p>\n", pageStyle)
}
