package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Exposure hosts reserve this path prefix for the gate.
const previewReserved = "/_agenthold/"

// Time limits of the preview proxy.
const (
	// previewWakeWait is how long a request waits for a sleeping machine
	// before a browser gets the "waking up" page.
	previewWakeWait = 20 * time.Second
	previewDial     = 15 * time.Second
	// previewHeaders bounds how long an app may take to start its response
	// (dev servers compile on the first request).
	previewHeaders = 5 * time.Minute
)

// previewCookieName is host-only on every exposure host. Over https it uses
// the __Host- prefix, which browsers refuse to accept with a Domain
// attribute, so another exposure on the same parent domain cannot plant one.
func (a *API) previewCookieName() string {
	if a.Preview.Hosts.Secure() {
		return "__Host-ah_preview"
	}
	return "ah_preview"
}

// PreviewHandler routes requests for exposure hosts (<name><suffix>) to the
// preview proxy. They never reach next, so the API and the web app are not
// served on exposure hosts.
func (a *API) PreviewHandler(next http.Handler) http.Handler {
	if !a.Preview.Enabled() {
		return next
	}
	proxy := a.previewProxy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := a.Preview.Hosts.Match(r.Host)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		a.servePreview(w, r, name, proxy)
	})
}

type previewTarget struct {
	exposure db.Exposure
	host     string
	user     *db.User // nil for public exposures
}

type previewKey struct{}

func (a *API) servePreview(w http.ResponseWriter, r *http.Request, name string, proxy http.Handler) {
	host := a.Preview.Hosts.Host(name)
	if strings.HasPrefix(r.URL.Path, previewReserved) {
		a.previewReserved(w, r, host)
		return
	}
	ctx := r.Context()
	e, err := a.Preview.Lookup(ctx, name)
	if errors.Is(err, preview.ErrNotFound) {
		previewFail(w, r, http.StatusNotFound, "Nothing here", "No port is exposed at this address.")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "preview lookup", "err", err)
		previewFail(w, r, http.StatusInternalServerError, "Error", "Something went wrong.")
		return
	}
	t := previewTarget{exposure: e, host: host}
	if preview.Access(e.Access) != preview.Public {
		u, err := a.previewUser(r, host)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agenthold"`)
			previewFail(w, r, http.StatusUnauthorized, "Not signed in", "invalid or expired token")
			return
		}
		if u == nil {
			if browserNavigation(r) {
				q := url.Values{"host": {host}, "return": {auth.SafeRedirect(r.URL.RequestURI())}}
				http.Redirect(w, r, a.PublicURL+"/api/v1/preview/authorize?"+q.Encode(), http.StatusFound)
				return
			}
			previewFail(w, r, http.StatusUnauthorized, "Not signed in", "sign in to open this preview")
			return
		}
		if ok, err := a.mayOpen(ctx, u, e); err != nil {
			slog.ErrorContext(ctx, "preview access", "err", err)
			previewFail(w, r, http.StatusInternalServerError, "Error", "Something went wrong.")
			return
		} else if !ok {
			previewFail(w, r, http.StatusForbidden, "No access", "You do not have access to this preview. Ask its owner to share the machine with you.")
			return
		}
		t.user = u
	}
	if a.Idle != nil {
		a.Idle.Tracker.Touch(e.AgentID)
		defer a.Idle.Tracker.Touch(e.AgentID)
	}
	if err := a.previewReady(ctx, e.AgentID); err != nil {
		if errors.Is(err, errWaking) {
			w.Header().Set("Retry-After", "2")
			if browserNavigation(r) {
				wakingPage(w)
				return
			}
			previewFail(w, r, http.StatusServiceUnavailable, "Waking up", "the machine is starting, retry shortly")
			return
		}
		_, msg := toHTTP(err)
		previewFail(w, r, http.StatusServiceUnavailable, "Unavailable", "The machine could not be started: "+msg)
		return
	}
	proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, previewKey{}, t)))
}

// previewUser identifies the caller on an exposure host: an agenthold API
// token, else the host's preview cookie. A bad token is an error; no
// credentials is (nil, nil).
func (a *API) previewUser(r *http.Request, host string) (*db.User, error) {
	if token, ok := agentholdBearer(r); ok {
		return a.Auth.TokenUser(r.Context(), token)
	}
	c, err := r.Cookie(a.previewCookieName())
	if err != nil || c.Value == "" {
		return nil, nil
	}
	u, err := a.Preview.SessionUser(r.Context(), c.Value, host)
	if err != nil {
		return nil, nil //nolint:nilerr // a stale cookie means: sign in again
	}
	return u, nil
}

// agentholdBearer returns an agenthold API token from the Authorization
// header. Other bearer tokens belong to the app and pass through.
func agentholdBearer(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	return token, ok && strings.HasPrefix(token, auth.TokenPrefix)
}

// mayOpen decides whether u may open e. Access is checked on every request,
// so unsharing or tightening access applies at once.
func (a *API) mayOpen(ctx context.Context, u *db.User, e db.Exposure) (bool, error) {
	switch preview.Access(e.Access) {
	case preview.Public, preview.Authenticated:
		return true, nil
	}
	ag, err := a.Store.GetAgent(ctx, e.AgentID)
	if err != nil {
		return false, err
	}
	role, err := a.authz.RoleOf(ctx, u.ID, ag)
	if err != nil {
		return false, err
	}
	return authz.Can(role, authz.AgentPreview), nil
}

// browserNavigation reports whether a request is a page load, which may be
// redirected to sign in or shown an HTML page.
func browserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// previewReserved serves /_agenthold/* on an exposure host.
func (a *API) previewReserved(w http.ResponseWriter, r *http.Request, host string) {
	cookie := func(value string, maxAge int) *http.Cookie {
		return &http.Cookie{
			Name: a.previewCookieName(), Value: value, Path: "/", MaxAge: maxAge,
			HttpOnly: true, Secure: a.Preview.Hosts.Secure(), SameSite: http.SameSiteLaxMode,
		}
	}
	switch r.URL.Path {
	case previewReserved + "callback":
		q := r.URL.Query()
		userID, err := a.Preview.TakeCode(r.Context(), q.Get("code"), host)
		if err != nil {
			if !errors.Is(err, preview.ErrBadCode) {
				slog.ErrorContext(r.Context(), "preview code", "err", err)
			}
			previewPage(w, http.StatusBadRequest, "Sign-in failed", preview.ErrBadCode.Error(), "/")
			return
		}
		token, err := a.Preview.CreateSession(r.Context(), userID, host)
		if err != nil {
			slog.ErrorContext(r.Context(), "preview session", "err", err)
			previewFail(w, r, http.StatusInternalServerError, "Error", "Something went wrong.")
			return
		}
		http.SetCookie(w, cookie(token, int(preview.SessionTTL.Seconds())))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, auth.SafeRedirect(q.Get("return")), http.StatusFound)
	case previewReserved + "logout":
		if c, err := r.Cookie(a.previewCookieName()); err == nil && c.Value != "" {
			_ = a.Preview.EndSession(r.Context(), c.Value)
		}
		http.SetCookie(w, cookie("", -1))
		previewPage(w, http.StatusOK, "Signed out", "You are signed out of this preview.", "/")
	default:
		previewFail(w, r, http.StatusNotFound, "Not found", "This path is reserved by agenthold.")
	}
}

// authorizePreview runs on the main host: it hands a signed-in user's
// identity to an exposure host as a single-use code.
func (a *API) authorizePreview(w http.ResponseWriter, r *http.Request) {
	if !a.Preview.Enabled() {
		previewPage(w, http.StatusNotFound, "Not found", preview.ErrDisabled.Error(), "")
		return
	}
	q := r.URL.Query()
	name, ok := a.Preview.Hosts.Match(q.Get("host"))
	if !ok {
		previewPage(w, http.StatusBadRequest, "Bad request", "That is not a preview address of this server.", "")
		return
	}
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	e, err := a.Preview.Lookup(r.Context(), name)
	if err != nil {
		previewPage(w, http.StatusNotFound, "Nothing here", "No port is exposed at this address.", "")
		return
	}
	if ok, err := a.mayOpen(r.Context(), u, e); err != nil {
		writeError(w, r, err)
		return
	} else if !ok {
		previewPage(w, http.StatusForbidden, "No access", "You do not have access to this preview. Ask its owner to share the machine with you.", "/")
		return
	}
	code, err := a.Preview.IssueCode(r.Context(), u.ID, a.Preview.Hosts.Host(name))
	if err != nil {
		writeError(w, r, err)
		return
	}
	back := url.Values{"code": {code}, "return": {auth.SafeRedirect(q.Get("return"))}}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.Preview.Hosts.Origin(name)+previewReserved+"callback?"+back.Encode(), http.StatusFound)
}

// jwks serves the keys of X-Agenthold-Identity tokens.
func (a *API) jwks(w http.ResponseWriter, _ *http.Request) {
	if a.Identity == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, a.Identity.JWKS())
}

var errWaking = errors.New("the machine is waking up")

type wakeCall struct {
	done chan struct{}
	err  error
}

// previewReady makes sure the machine's sandboxd is connected, waking it if
// it sleeps. It waits up to previewWakeWait, then returns errWaking while the
// wake goes on in the background.
func (a *API) previewReady(ctx context.Context, agentID string) error {
	if ok, err := a.Node.Connected(ctx, agentID); err == nil && ok {
		return nil
	}
	a.wakeMu.Lock()
	c, ok := a.pending[agentID]
	if !ok {
		c = &wakeCall{done: make(chan struct{})}
		a.pending[agentID] = c
		go func() {
			wctx, cancel := context.WithTimeout(context.Background(), wakeTimeout+10*time.Second)
			defer cancel()
			_, c.err = a.Wake(wctx, agentID, func(string) {})
			a.wakeMu.Lock()
			delete(a.pending, agentID)
			a.wakeMu.Unlock()
			close(c.done)
		}()
	}
	a.wakeMu.Unlock()
	t := time.NewTimer(previewWakeWait)
	defer t.Stop()
	select {
	case <-c.done:
		return c.err
	case <-t.C:
		return errWaking
	case <-ctx.Done():
		return ctx.Err()
	}
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
			hostOnlyCookies(res.Header, a.previewCookieName())
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

func (a *API) rewritePreview(pr *httputil.ProxyRequest) {
	t := pr.In.Context().Value(previewKey{}).(previewTarget)
	out := pr.Out
	out.URL.Scheme = "http"
	out.URL.Host = guestAddr(t.exposure.AgentID, int(t.exposure.Port))
	out.Host = pr.In.Host // the app sees its public hostname
	pr.SetXForwarded()
	out.Header.Set("X-Forwarded-Proto", a.Preview.Hosts.Scheme)
	for k := range out.Header {
		if strings.HasPrefix(k, "X-Agenthold-") {
			out.Header.Del(k)
		}
	}
	if _, ok := agentholdBearer(pr.In); ok {
		out.Header.Del("Authorization")
	}
	stripCookies(out.Header, a.previewCookieName(), auth.CookieName)
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

// hostOnlyCookies drops the Domain attribute from an app's cookies, so one
// exposure cannot set cookies for its siblings, and drops cookies that
// would shadow the gate's own.
func hostOnlyCookies(h http.Header, gateCookie string) {
	lines := h.Values("Set-Cookie")
	if len(lines) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, line := range lines {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			continue
		}
		if strings.EqualFold(c.Name, gateCookie) || strings.EqualFold(c.Name, "ah_preview") {
			continue
		}
		if c.Domain != "" {
			c.Domain = ""
			line = c.String()
		}
		h.Add("Set-Cookie", line)
	}
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

func previewPage(w http.ResponseWriter, status int, title, msg, link string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	body := "<p>" + html.EscapeString(msg) + "</p>"
	if link != "" {
		body += `<p><a href="` + html.EscapeString(link) + `">Try again</a></p>`
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\"><title>%s</title><style>%s</style><h1>%s</h1>%s\n",
		html.EscapeString(title), pageStyle, html.EscapeString(title), body)
}

func wakingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Refresh", "2")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=2><meta name=viewport content=\"width=device-width\"><title>Waking up…</title><style>%s</style><h1>Waking up…</h1><p>The machine was asleep and is starting. This page reloads by itself.</p>\n", pageStyle)
}

// wakes tracks background wakes started by preview requests.
type wakes struct {
	wakeMu  sync.Mutex
	pending map[string]*wakeCall
}
