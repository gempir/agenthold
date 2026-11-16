package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth/authtest"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/preview"
)

// upstream is an app listening on the host; fake sandboxes share the host's
// loopback, so sandboxd's dial reaches it.
type upstream struct {
	port int
	seen chan http.Header
}

func startUpstream(t *testing.T) *upstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &upstream{port: ln.Addr().(*net.TCPAddr).Port, seen: make(chan http.Header, 64)}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, append([]byte("echo "), b...)); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // the client must see the first event while this is still open
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Clone()
		h.Set("X-Test-Host", r.Host)
		h.Set("X-Test-URI", r.URL.RequestURI())
		u.seen <- h
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "1", Domain: "apps.test", Path: "/"})
		_, _ = io.WriteString(w, "app says "+r.URL.Path)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return u
}

func (u *upstream) last(t *testing.T) http.Header {
	t.Helper()
	select {
	case h := <-u.seen:
		return h
	case <-time.After(5 * time.Second):
		t.Fatal("upstream saw no request")
		return nil
	}
}

// browser returns an HTTP client sharing c's cookies that reaches the test
// server for any host name, like a browser with wildcard DNS would.
func (e *env) browser(c *client, follow bool) *http.Client {
	addr := e.srv.Listener.Addr().String()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	e.t.Cleanup(tr.CloseIdleConnections)
	hc := &http.Client{Transport: tr, Jar: c.http.Jar}
	if !follow {
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return hc
}

type pageResult struct {
	status int
	body   string
	res    *http.Response
}

// get loads a URL like a browser navigation (Accept: text/html).
func get(t *testing.T, hc *http.Client, rawURL string, hdr map[string]string) pageResult {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	req.Header.Set("Accept", "text/html,*/*")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return pageResult{res.StatusCode, string(b), res}
}

func (e *env) expose(c *client, agentID string, port int, access string) gen.Exposure {
	e.t.Helper()
	var ex gen.Exposure
	in := gen.ExposureInput{Port: port}
	if access != "" {
		a := gen.ExposureAccess(access)
		in.Access = &a
	}
	expect(e.t, "expose", c.do("POST", "/agents/"+agentID+"/exposures", in, &ex), 201)
	return ex
}

func TestPreviewPrivateEndToEnd(t *testing.T) {
	tm := newTeam(t)
	e, alice, bob, dave := tm.e, tm.alice, tm.bob, tm.dave
	ag := tm.agent
	app := startUpstream(t)

	ex := e.expose(alice, ag.Id, app.port, "")
	wantURL := fmt.Sprintf("http://shared-%d.apps.test:%s/", app.port, e.srv.URL[strings.LastIndex(e.srv.URL, ":")+1:])
	if ex.Url != wantURL || ex.Access != gen.Private || ex.CreatedBy != tm.ids["alice"] {
		t.Fatalf("exposure = %+v, want url %s", ex, wantURL)
	}
	host := strings.Split(strings.TrimPrefix(ex.Url, "http://"), ":")[0]

	// No cookie: a navigation goes to the main host to sign in, anything else is refused.
	anon := e.browser(e.anon(), false)
	r := get(t, anon, ex.Url+"hello?x=1", nil)
	loc, _ := url.Parse(r.res.Header.Get("Location"))
	if r.status != 302 || loc.Host != strings.TrimPrefix(e.srv.URL, "http://") || loc.Path != "/api/v1/preview/authorize" ||
		loc.Query().Get("host") != host || loc.Query().Get("return") != "/hello?x=1" {
		t.Fatalf("anonymous navigation: %d %s", r.status, loc)
	}
	r = get(t, anon, ex.Url, map[string]string{"Accept": "application/json"})
	if r.status != 401 || !strings.Contains(r.body, `"error"`) {
		t.Fatalf("anonymous fetch: %d %s", r.status, r.body)
	}
	// The authorize endpoint sends a signed-out browser to the login page and back.
	r = get(t, anon, loc.String(), nil)
	if r.status != 302 || !strings.HasPrefix(r.res.Header.Get("Location"), "/login?redirect=%2Fapi%2Fv1%2Fpreview%2Fauthorize") {
		t.Fatalf("authorize signed out: %d %s", r.status, r.res.Header.Get("Location"))
	}

	// The owner: main host -> code -> exposure host cookie -> the app.
	ab := e.browser(alice, true)
	r = get(t, ab, ex.Url+"hello?x=1", map[string]string{
		"X-Agenthold-User": "usr_forged", "X-Agenthold-Identity": "forged", "Cookie": "app=1",
	})
	if r.status != 200 || r.body != "app says /hello" {
		t.Fatalf("owner: %d %q", r.status, r.body)
	}
	h := app.last(t)
	if h.Get("X-Test-URI") != "/hello?x=1" || h.Get("X-Test-Host") != host+e.srv.URL[strings.LastIndex(e.srv.URL, ":"):] {
		t.Fatalf("upstream request: uri %q host %q", h.Get("X-Test-URI"), h.Get("X-Test-Host"))
	}
	if h.Get("X-Agenthold-User") != tm.ids["alice"] || h.Get("X-Agenthold-Email") != "a@example.com" || h.Get("X-Agenthold-Name") != "Alice" {
		t.Fatalf("identity headers: %v", h)
	}
	if c := h.Get("Cookie"); strings.Contains(c, "ah_preview") || strings.Contains(c, "agenthold_session") {
		t.Fatalf("gate cookies reached the app: %q", c)
	}
	// The JWT verifies against the main host's JWKS.
	var set preview.JWKS
	if res, err := http.Get(e.srv.URL + "/.well-known/agenthold/jwks.json"); err != nil {
		t.Fatal(err)
	} else {
		_ = json.NewDecoder(res.Body).Decode(&set)
		_ = res.Body.Close()
	}
	claims, err := preview.Verify(h.Get("X-Agenthold-Identity"), set, host, e.srv.URL, time.Now())
	if err != nil || claims.Subject != tm.ids["alice"] || claims.Email != "a@example.com" || claims.Expires-claims.IssuedAt != 300 {
		t.Fatalf("identity token: %+v %v", claims, err)
	}
	// The app's Domain cookie was made host-only.
	if sc := r.res.Header.Values("Set-Cookie"); len(sc) != 1 || strings.Contains(strings.ToLower(sc[0]), "domain") {
		t.Fatalf("app cookies: %q", sc)
	}

	// WebSockets and streaming responses pass through.
	wctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(wctx, strings.Replace(ex.Url, "http", "ws", 1)+"ws", &websocket.DialOptions{HTTPClient: ab})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"one", "two"} {
		if err := ws.Write(wctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		if _, b, err := ws.Read(wctx); err != nil || string(b) != "echo "+msg {
			t.Fatalf("ws read %q %v", b, err)
		}
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
	req, _ := http.NewRequestWithContext(wctx, http.MethodGet, ex.Url+"sse", nil)
	res, err := ab.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	_ = res.Body.Close()
	if err != nil || line != "data: first\n" {
		t.Fatalf("sse: %q %v", line, err)
	}

	// A shared user gets in (and is out again once unshared); a stranger is
	// refused at the main host.
	tm.share("bob")
	if r := get(t, e.browser(bob, true), ex.Url, nil); r.status != 200 {
		t.Fatalf("shared user: %d %s", r.status, r.body)
	}
	app.last(t)
	tm.unshare("bob")
	if r := get(t, e.browser(bob, true), ex.Url, nil); r.status != 403 {
		t.Fatalf("unshared user: %d", r.status)
	}
	if r := get(t, e.browser(dave, true), ex.Url, nil); r.status != 403 || !strings.Contains(r.body, "do not have access") {
		t.Fatalf("stranger: %d %s", r.status, r.body)
	}

	// API tokens work for scripts and are not passed on.
	var tok gen.CreatedAPIToken
	expect(t, "token", alice.do("POST", "/me/tokens", map[string]string{"name": "script"}, &tok), 201)
	plain := e.browser(e.anon(), false)
	r = get(t, plain, ex.Url, map[string]string{"Authorization": "Bearer " + tok.Token, "Accept": "*/*"})
	if r.status != 200 {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	if h := app.last(t); h.Get("Authorization") != "" || h.Get("X-Agenthold-User") != tm.ids["alice"] {
		t.Fatalf("token request upstream: %v", h)
	}
	if r := get(t, plain, ex.Url, map[string]string{"Authorization": "Bearer ahp_wrong", "Accept": "*/*"}); r.status != 401 {
		t.Fatalf("bad token: %d", r.status)
	}

	// Signing out of the preview drops its cookie.
	if r := get(t, ab, ex.Url+"_agenthold/logout", nil); r.status != 200 {
		t.Fatalf("logout: %d", r.status)
	}
	if r := get(t, e.browser(alice, false), ex.Url, nil); r.status != 302 {
		t.Fatalf("after logout: %d", r.status)
	}
}

func TestPreviewAccessModes(t *testing.T) {
	tm := newTeam(t)
	e, alice, bob, dave := tm.e, tm.alice, tm.bob, tm.dave
	app := startUpstream(t)
	ex := e.expose(alice, tm.agent.Id, app.port, "authenticated")
	tm.share("bob")

	// Shared users read the list but cannot change it.
	var list gen.ExposureList
	expect(t, "bob lists", bob.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, &list), 200)
	if !list.Enabled || list.HostSuffix != ".apps.test" || len(list.Exposures) != 1 {
		t.Fatalf("list = %+v", list)
	}
	expect(t, "bob exposes", bob.do("POST", "/agents/"+tm.agent.Id+"/exposures", gen.ExposureInput{Port: 1234}, nil), 403)
	expect(t, "dave lists", dave.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, nil), 404)
	expect(t, "bob patches", bob.do("PATCH", "/agents/"+tm.agent.Id+"/exposures/"+ex.Id, gen.ExposurePatch{Name: ptr("x")}, nil), 403)

	// authenticated: any signed-in user.
	if r := get(t, e.browser(dave, true), ex.Url, nil); r.status != 200 {
		t.Fatalf("authenticated, stranger: %d %s", r.status, r.body)
	}
	app.last(t)

	// public: anyone, without identity, and forged identity headers are dropped.
	pub := gen.Public
	var got gen.Exposure
	expect(t, "public", alice.do("PATCH", "/agents/"+tm.agent.Id+"/exposures/"+ex.Id, gen.ExposurePatch{Access: &pub, Name: ptr("site")}, &got), 200)
	if got.Name != "site" || !strings.HasPrefix(got.Url, "http://site.apps.test:") {
		t.Fatalf("patched = %+v", got)
	}
	anon := e.browser(e.anon(), false)
	if r := get(t, anon, ex.Url, nil); r.status != 404 {
		t.Fatalf("old name still served: %d", r.status)
	}
	r := get(t, anon, got.Url, map[string]string{"X-Agenthold-User": "usr_forged", "X-Agenthold-Email": "x"})
	if r.status != 200 {
		t.Fatalf("public: %d %s", r.status, r.body)
	}
	if h := app.last(t); h.Get("X-Agenthold-User") != "" || h.Get("X-Agenthold-Email") != "" || h.Get("X-Agenthold-Identity") != "" {
		t.Fatalf("public request carried identity: %v", h)
	}

	// Errors: name conflicts, bad input, nothing listening, unknown hosts.
	expect(t, "same port", alice.do("POST", "/agents/"+tm.agent.Id+"/exposures", gen.ExposureInput{Port: app.port}, nil), 409)
	expect(t, "taken name", alice.do("POST", "/agents/"+tm.agent.Id+"/exposures", gen.ExposureInput{Port: 1, Name: ptr("site")}, nil), 409)
	expect(t, "bad name", alice.do("POST", "/agents/"+tm.agent.Id+"/exposures", gen.ExposureInput{Port: 2, Name: ptr("Not_OK")}, nil), 400)
	expect(t, "bad port", alice.do("POST", "/agents/"+tm.agent.Id+"/exposures", gen.ExposureInput{Port: 70000}, nil), 400)
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	dead := e.expose(alice, tm.agent.Id, closedPort, "public")
	if r := get(t, anon, dead.Url, nil); r.status != 502 || !strings.Contains(r.body, fmt.Sprintf("Nothing is listening on port %d", closedPort)) {
		t.Fatalf("nothing listening: %d %s", r.status, r.body)
	}
	if r := get(t, anon, strings.Replace(got.Url, "site.", "nope.", 1)+"api/v1/me", nil); r.status != 404 || strings.Contains(r.body, "Alice") {
		t.Fatalf("unknown host: %d %s", r.status, r.body)
	}
	// The API is not served on exposure hosts, even with a main session.
	if r := get(t, e.browser(alice, false), got.Url+"api/v1/me", map[string]string{"Accept": "*/*"}); r.body != "app says /api/v1/me" {
		t.Fatalf("api on exposure host: %d %s", r.status, r.body)
	}
	app.last(t)

	expect(t, "delete", alice.do("DELETE", "/agents/"+tm.agent.Id+"/exposures/"+ex.Id, nil, nil), 204)
	if r := get(t, anon, got.Url, nil); r.status != 404 {
		t.Fatalf("deleted exposure served: %d", r.status)
	}
	var audit []gen.AuditEntry
	alice.do("GET", "/agents/"+tm.agent.Id+"/audit", nil, &audit)
	var actions []string
	for _, a := range audit {
		actions = append(actions, a.Action)
	}
	if !strings.Contains(strings.Join(actions, ","), "exposure.remove,exposure.add,exposure.update,share.add,exposure.add") {
		t.Fatalf("audit = %v", actions)
	}
}

func TestPreviewGateRedirects(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	app := startUpstream(t)
	ex := e.expose(alice, tm.agent.Id, app.port, "")
	other := e.expose(alice, tm.agent.Id, app.port+1, "")
	host := strings.Split(strings.TrimPrefix(ex.Url, "http://"), ":")[0]
	main := e.srv.URL

	ab := e.browser(alice, false)
	authorize := func(h, ret string) *url.URL {
		t.Helper()
		r := get(t, ab, main+"/api/v1/preview/authorize?"+url.Values{"host": {h}, "return": {ret}}.Encode(), nil)
		if r.status != 302 {
			t.Fatalf("authorize %s %s: %d %s", h, ret, r.status, r.body)
		}
		u, _ := url.Parse(r.res.Header.Get("Location"))
		return u
	}
	// Open redirects are reduced to "/" at both ends.
	for _, ret := range []string{"https://evil.example/", "//evil.example", "/\\evil.example"} {
		cb := authorize(host, ret)
		if cb.Host != strings.TrimPrefix(ex.Url[:len(ex.Url)-1], "http://") || cb.Path != "/_agenthold/callback" || cb.Query().Get("return") != "/" {
			t.Fatalf("callback for %q: %s", ret, cb)
		}
	}
	cb := authorize(host, "/ok")
	q := cb.Query()
	q.Set("return", "https://evil.example/")
	evil := *cb
	evil.RawQuery = q.Encode()
	r := get(t, ab, evil.String(), nil)
	if r.status != 302 || r.res.Header.Get("Location") != "/" {
		t.Fatalf("callback open redirect: %d %s", r.status, r.res.Header.Get("Location"))
	}
	// The code was used: a second exchange fails.
	if r := get(t, ab, cb.String(), nil); r.status != 400 {
		t.Fatalf("code reuse: %d", r.status)
	}
	// A code for one exposure host does not work on another.
	cb = authorize(host, "/")
	stolen := strings.Replace(cb.String(), host, strings.Split(strings.TrimPrefix(other.Url, "http://"), ":")[0], 1)
	if r := get(t, e.browser(e.anon(), false), stolen, nil); r.status != 400 {
		t.Fatalf("code on another host: %d", r.status)
	}
	// Hosts that are not exposures are refused.
	r = get(t, ab, main+"/api/v1/preview/authorize?host=evil.example&return=/", nil)
	if r.status != 400 {
		t.Fatalf("foreign host: %d", r.status)
	}
	r = get(t, ab, main+"/api/v1/preview/authorize?host=nope.apps.test", nil)
	if r.status != 404 {
		t.Fatalf("unknown exposure: %d", r.status)
	}
	// The preview cookie is host-only and HttpOnly; so is the main session cookie.
	cb = authorize(host, "/")
	r = get(t, ab, cb.String(), nil)
	sc := r.res.Header.Get("Set-Cookie")
	if !strings.HasPrefix(sc, "ah_preview=") || strings.Contains(strings.ToLower(sc), "domain") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") {
		t.Fatalf("preview cookie: %q", sc)
	}
	e.idp.SignInAs(authtest.User{Subject: "erin", Name: "Erin"})
	var sessionCookies []string
	jar, _ := cookiejar.New(nil)
	login := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		sessionCookies = append(sessionCookies, req.Response.Header.Values("Set-Cookie")...)
		return nil
	}}
	res, err := login.Get(main + "/api/v1/auth/login/oidc")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if len(sessionCookies) == 0 {
		t.Fatal("login set no cookie")
	}
	for _, c := range sessionCookies {
		if strings.Contains(strings.ToLower(c), "domain") {
			t.Fatalf("main session cookie is domain-scoped: %q", c)
		}
	}
	// Reserved paths.
	if r := get(t, ab, ex.Url+"_agenthold/other", nil); r.status != 404 {
		t.Fatalf("reserved: %d", r.status)
	}
}

func TestPreviewWakesSleepingMachine(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	app := startUpstream(t)
	ex := e.expose(alice, tm.agent.Id, app.port, "public")
	expect(t, "stop", alice.do("POST", "/agents/"+tm.agent.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("state %s", s)
	}
	e.clock.Advance(time.Hour)
	if r := get(t, e.browser(e.anon(), false), ex.Url, nil); r.status != 200 {
		t.Fatalf("wake: %d %s", r.status, r.body)
	}
	app.last(t)
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("state after preview %s", s)
	}
	// Browsing counts as activity.
	if last := e.tracker.Last(tm.agent.Id); !last.Equal(e.clock.Now()) {
		t.Fatalf("activity %v, now %v", last, e.clock.Now())
	}
}

func TestExposureTools(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	ctx := t.Context()
	call := func(tool string, args map[string]any) (string, error) {
		b, _ := json.Marshal(args)
		out, err := e.platform.Call(ctx, tm.agent.Id, "", tool, b)
		s, _ := out.(string)
		return s, err
	}
	tools, _ := e.platform.Call(ctx, tm.agent.Id, "", "_list_tools", nil)
	if b, _ := json.Marshal(tools); !strings.Contains(string(b), "expose_port") || !strings.Contains(string(b), "unexpose") {
		t.Fatalf("tools: %s", b)
	}
	if _, err := call("expose_port", map[string]any{"port": 8000, "access": "public"}); !errors.Is(err, platform.ErrPublic) {
		t.Fatalf("public: %v", err)
	}
	out, err := call("expose_port", map[string]any{"port": 8000})
	if err != nil || !strings.Contains(out, "http://shared-8000.apps.test:") || !strings.Contains(out, "access: private") {
		t.Fatalf("expose: %q %v", out, err)
	}
	if out, _ := call("expose_port", map[string]any{"port": 8000}); !strings.Contains(out, "already exposed") {
		t.Fatalf("again: %q", out)
	}
	if out, _ := call("list_exposures", nil); !strings.Contains(out, `"port": 8000`) {
		t.Fatalf("list: %q", out)
	}
	var list gen.ExposureList
	tm.alice.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, &list)
	if len(list.Exposures) != 1 || list.Exposures[0].CreatedBy != "agent:"+tm.agent.Id {
		t.Fatalf("list = %+v", list)
	}
	if out, err := call("unexpose", map[string]any{"port": 8000}); err != nil || !strings.Contains(out, "no longer exposed") {
		t.Fatalf("unexpose: %q %v", out, err)
	}
	if _, err := call("unexpose", map[string]any{"port": 8000}); err == nil {
		t.Fatal("unexpose twice succeeded")
	}
}
