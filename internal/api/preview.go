package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
)

// Exposure hosts reserve this path prefix for the gate.
const previewReserved = "/_agenthold/"

// Limits of the preview router.
const (
	// previewWakeWait is how long a request waits for a sleeping machine
	// before a browser gets the "waking up" page.
	previewWakeWait = 20 * time.Second
	// visitorWakeEvery bounds how often visitors who are not members of a
	// machine (on exposures with wake_on_request) start it.
	visitorWakeEvery = time.Minute
	// previewConcurrency bounds concurrent requests (and open WebSockets)
	// per exposure; more get 429.
	previewConcurrency = 128
	stateCookieTTL     = 10 * time.Minute
)

// Cookie names on exposure hosts. Over https they carry the __Host- prefix,
// which browsers refuse to accept with a Domain attribute, so another
// exposure on the same parent domain cannot plant one.
func (a *API) previewCookieName() string { return a.previewCookie("ah_preview") }
func (a *API) stateCookieName() string   { return a.previewCookie("ah_preview_state") }

func (a *API) previewCookie(base string) string {
	if a.Preview.Hosts.Secure() {
		return "__Host-" + base
	}
	return base
}

// PreviewHandler routes every request for a host under the preview suffix
// to the preview router, valid exposure name or not. They never reach next,
// so the API and the web app are not served on exposure hosts.
func (a *API) PreviewHandler(next http.Handler) http.Handler {
	if !a.Preview.Enabled() {
		return next
	}
	proxy := a.previewProxy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, under := a.Preview.Hosts.Classify(r.Host)
		if !under {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.Preview.Hosts.Match(r.Host); !ok {
			previewFail(w, r, http.StatusNotFound, "Nothing here", "No port is exposed at this address.")
			return
		}
		a.servePreview(w, r, name, proxy)
	})
}

type previewTarget struct {
	exposure db.Exposure
	host     string
	user     *db.User // set for private and authenticated exposures
}

type previewKey struct{}

// visitor is who is asking for an exposure.
type visitor struct {
	user   *db.User
	member bool // owner or shared user of the machine
}

func (a *API) servePreview(w http.ResponseWriter, r *http.Request, name string, proxy http.Handler) {
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
	if strings.HasPrefix(r.URL.Path, previewReserved) {
		a.previewReserved(w, r, e)
		return
	}
	v, err := a.previewVisitor(r, e)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agenthold"`)
		previewFail(w, r, http.StatusUnauthorized, "Not signed in", "invalid or expired token")
		return
	}
	t := previewTarget{exposure: e, host: a.Preview.Hosts.Host(e.Name)}
	switch preview.Access(e.Access) {
	case preview.Public:
	default:
		if v.user == nil {
			if browserNavigation(r) {
				a.redirectToAuthorize(w, r, e)
				return
			}
			previewFail(w, r, http.StatusUnauthorized, "Not signed in", "sign in to open this preview")
			return
		}
		if preview.Access(e.Access) == preview.Private && !v.member {
			previewFail(w, r, http.StatusForbidden, "No access", "You do not have access to this preview. Ask its owner to share the machine with you.")
			return
		}
		t.user = v.user
	}

	// Only members (and anyone, when the owner allows it) wake the machine
	// and keep it awake.
	mayWake := v.member || e.WakeOnRequest != 0
	if mayWake && a.Idle != nil {
		a.Idle.Tracker.Touch(e.AgentID)
		defer a.Idle.Tracker.Touch(e.AgentID)
	}
	if err := a.previewReady(ctx, e.AgentID, mayWake, !v.member); err != nil {
		switch {
		case errors.Is(err, errAsleep):
			previewFail(w, r, http.StatusServiceUnavailable, "Asleep", "This app is asleep. Its owner can wake it.")
		case errors.Is(err, errWaking):
			w.Header().Set("Retry-After", "2")
			if browserNavigation(r) {
				wakingPage(w)
				return
			}
			previewFail(w, r, http.StatusServiceUnavailable, "Waking up", "the machine is starting, retry shortly")
		default:
			_, msg := toHTTP(err)
			previewFail(w, r, http.StatusServiceUnavailable, "Unavailable", "The machine could not be started: "+msg)
		}
		return
	}
	release, ok := a.previewSlot(e.ID)
	if !ok {
		w.Header().Set("Retry-After", "1")
		previewFail(w, r, http.StatusTooManyRequests, "Busy", "Too many requests to this preview at once.")
		return
	}
	defer release()
	proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, previewKey{}, t)))
}

// previewVisitor identifies the caller on an exposure host: an agenthold
// API token, else the exposure's preview cookie. A bad token is an error on
// gated exposures (and ignored on public ones); no credentials is no user.
func (a *API) previewVisitor(r *http.Request, e db.Exposure) (visitor, error) {
	var u *db.User
	if token, ok := agentholdBearer(r); ok {
		tu, err := a.Auth.TokenUser(r.Context(), token)
		if err != nil {
			if preview.Access(e.Access) == preview.Public {
				return visitor{}, nil
			}
			return visitor{}, err
		}
		u = tu
	} else if c, err := r.Cookie(a.previewCookieName()); err == nil && c.Value != "" {
		u, _ = a.Preview.SessionUser(r.Context(), c.Value, e) // a stale cookie means: sign in again
	}
	if u == nil {
		return visitor{}, nil
	}
	member, err := a.isMember(r.Context(), u, e)
	if err != nil {
		return visitor{}, err
	}
	return visitor{user: u, member: member}, nil
}

// isMember reports whether u owns the exposure's machine or it is shared
// with u. Checked on every request, so unsharing applies at once.
func (a *API) isMember(ctx context.Context, u *db.User, e db.Exposure) (bool, error) {
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

// agentholdBearer returns an agenthold API token from the Authorization
// header (scheme case-insensitive). Other tokens belong to the app.
func agentholdBearer(r *http.Request) (string, bool) {
	scheme, token, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	return token, strings.EqualFold(scheme, "bearer") && strings.HasPrefix(token, auth.TokenPrefix)
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

// topLevelNavigation reports whether a request may start the sign-in hand
// over: a top-level page load, not an image, frame or fetch. Browsers that
// send no fetch metadata are let through; the consent page still stands
// between them and exposures of machines they have no access to.
func topLevelNavigation(r *http.Request) bool {
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && d != "document" {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
		return false
	}
	return true
}

// redirectToAuthorize starts the sign-in hand over. The state cookie binds
// the code to this browser: a sign-in link from someone else's browser is
// refused at the callback.
func (a *API) redirectToAuthorize(w http.ResponseWriter, r *http.Request, e db.Exposure) {
	state := ""
	if c, err := r.Cookie(a.stateCookieName()); err == nil && len(c.Value) >= 32 && len(c.Value) <= 128 {
		state = c.Value // keep it, so sign-ins in several tabs do not undo each other
	} else {
		state = id.Random(24)
	}
	http.SetCookie(w, a.exposureCookie(a.stateCookieName(), state, int(stateCookieTTL.Seconds())))
	q := url.Values{
		"host":   {a.Preview.Hosts.Host(e.Name)},
		"return": {auth.SafeRedirect(r.URL.RequestURI())},
		"state":  {preview.Hash(state)},
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.PublicURL+"/api/v1/preview/authorize?"+q.Encode(), http.StatusFound)
}

func (a *API) exposureCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.Preview.Hosts.Secure(), SameSite: http.SameSiteLaxMode,
	}
}

// previewReserved serves /_agenthold/* on an exposure host.
func (a *API) previewReserved(w http.ResponseWriter, r *http.Request, e db.Exposure) {
	switch r.URL.Path {
	case previewReserved + "callback":
		q := r.URL.Query()
		state := ""
		if c, err := r.Cookie(a.stateCookieName()); err == nil {
			state = c.Value
		}
		userID, err := a.Preview.TakeCode(r.Context(), q.Get("code"), e, state)
		if err != nil {
			if !errors.Is(err, preview.ErrBadCode) {
				slog.ErrorContext(r.Context(), "preview code", "err", err)
			}
			previewPage(w, http.StatusBadRequest, "Sign-in failed", preview.ErrBadCode.Error(), "/")
			return
		}
		token, err := a.Preview.CreateSession(r.Context(), userID, e)
		if err != nil {
			slog.ErrorContext(r.Context(), "preview session", "err", err)
			previewFail(w, r, http.StatusInternalServerError, "Error", "Something went wrong.")
			return
		}
		http.SetCookie(w, a.exposureCookie(a.stateCookieName(), "", -1))
		http.SetCookie(w, a.exposureCookie(a.previewCookieName(), token, int(preview.SessionTTL.Seconds())))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, auth.SafeRedirect(q.Get("return")), http.StatusFound)
	case previewReserved + "logout":
		if r.Method != http.MethodPost {
			logoutPage(w)
			return
		}
		if c, err := r.Cookie(a.previewCookieName()); err == nil && c.Value != "" {
			_ = a.Preview.EndSession(r.Context(), c.Value)
		}
		http.SetCookie(w, a.exposureCookie(a.previewCookieName(), "", -1))
		previewPage(w, http.StatusOK, "Signed out", "You are signed out of this preview.", "/")
	default:
		previewFail(w, r, http.StatusNotFound, "Not found", "This path is reserved by agenthold.")
	}
}

// authorizePreview runs on the main host: it hands a signed-in user's
// identity to an exposure host as a single-use code. GET shows the consent
// page where one is needed; POST is that page's form.
func (a *API) authorizePreview(w http.ResponseWriter, r *http.Request) {
	if !a.Preview.Enabled() {
		previewPage(w, http.StatusNotFound, "Not found", preview.ErrDisabled.Error(), "")
		return
	}
	post := r.Method == http.MethodPost
	if post {
		if err := r.ParseForm(); err != nil {
			previewPage(w, http.StatusBadRequest, "Bad request", "Malformed form.", "")
			return
		}
	} else if !topLevelNavigation(r) {
		previewPage(w, http.StatusForbidden, "Refused", "Sign-in to a preview only starts from a page load.", "")
		return
	}
	host, ret, state := r.FormValue("host"), auth.SafeRedirect(r.FormValue("return")), r.FormValue("state")
	name, ok := a.Preview.Hosts.Match(host)
	if !ok || len(state) != 64 {
		previewPage(w, http.StatusBadRequest, "Bad request", "That is not a preview sign-in link of this server.", "")
		return
	}
	u := auth.UserFrom(r.Context())
	if u == nil {
		if post {
			previewPage(w, http.StatusUnauthorized, "Not signed in", "Sign in and try again.", "")
			return
		}
		http.Redirect(w, r, "/login?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	e, err := a.Preview.Lookup(r.Context(), name)
	if err != nil {
		previewPage(w, http.StatusNotFound, "Nothing here", "No port is exposed at this address.", "")
		return
	}
	member, err := a.isMember(r.Context(), u, e)
	if err != nil {
		writeError(w, r, err)
		return
	}
	switch preview.Access(e.Access) {
	case preview.Private:
		if !member {
			previewPage(w, http.StatusForbidden, "No access", "You do not have access to this preview. Ask its owner to share the machine with you.", "")
			return
		}
	case preview.Authenticated:
		if !member {
			consented, err := a.Preview.Consented(r.Context(), u.ID, e)
			if err != nil {
				writeError(w, r, err)
				return
			}
			if !consented && !post {
				a.consentPage(w, r, e, host, ret, state)
				return
			}
			if !consented {
				if err := a.Preview.Consent(r.Context(), u.ID, e); err != nil {
					writeError(w, r, err)
					return
				}
			}
		}
	}
	code, err := a.Preview.IssueCode(r.Context(), u.ID, e, state)
	if err != nil {
		writeError(w, r, err)
		return
	}
	back := url.Values{"code": {code}, "return": {ret}}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.Preview.Hosts.Origin(e.Name)+previewReserved+"callback?"+back.Encode(), http.StatusSeeOther)
}

// consentPage asks a user before their identity goes to an app on a
// machine they have no access to.
func (a *API) consentPage(w http.ResponseWriter, r *http.Request, e db.Exposure, host, ret, state string) {
	ag, err := a.Store.GetAgent(r.Context(), e.AgentID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	owner, err := a.Store.GetUser(r.Context(), ag.OwnerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	who := owner.Name
	if owner.Email != "" {
		who += " (" + owner.Email + ")"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	h := html.EscapeString
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width"><title>Continue to %s?</title><style>%s button{font:inherit;padding:.4rem 1rem}</style>
<h1>Continue to %s?</h1><p>This app is run by %s. It will see your name and email address.</p>
<form method=post action="/api/v1/preview/authorize"><input type=hidden name=host value="%s"><input type=hidden name=return value="%s"><input type=hidden name=state value="%s"><button type=submit>Continue</button></form>
`, h(host), pageStyle, h(host), h(who), h(host), h(ret), h(state))
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

var (
	errWaking = errors.New("the machine is waking up")
	errAsleep = errors.New("the machine is asleep")
)

type wakeCall struct {
	done chan struct{}
	err  error
}

// wakes tracks background wakes and request slots of the preview router.
type wakes struct {
	wakeMu      sync.Mutex
	pending     map[string]*wakeCall
	visitorWake map[string]time.Time // last wake started by a non-member, per agent
	slots       map[string]chan struct{}
}

// previewReady makes sure the machine's sandboxd is connected. If it sleeps
// and mayWake, it wakes it (non-members at most once per visitorWakeEvery),
// waiting up to previewWakeWait before returning errWaking while the wake
// goes on. Without mayWake a sleeping machine is errAsleep.
func (a *API) previewReady(ctx context.Context, agentID string, mayWake, visitor bool) error {
	if ok, err := a.Node.Connected(ctx, agentID); err == nil && ok {
		return nil
	}
	a.wakeMu.Lock()
	c, ok := a.pending[agentID]
	if !ok {
		if !mayWake {
			a.wakeMu.Unlock()
			return errAsleep
		}
		if visitor {
			if last, seen := a.visitorWake[agentID]; seen && a.Now().Sub(last) < visitorWakeEvery {
				a.wakeMu.Unlock()
				return errWaking
			}
			a.visitorWake[agentID] = a.Now()
		}
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

// previewSlot takes one of an exposure's concurrent request slots.
func (a *API) previewSlot(exposureID string) (func(), bool) {
	a.wakeMu.Lock()
	ch, ok := a.slots[exposureID]
	if !ok {
		ch = make(chan struct{}, previewConcurrency)
		a.slots[exposureID] = ch
	}
	a.wakeMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	default:
		return nil, false
	}
}
