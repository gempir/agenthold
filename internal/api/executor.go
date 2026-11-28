package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

const (
	executorReservedPrefix = "/_agenthold/"
	executorCodeTTL        = 2 * time.Minute
	executorSessionTTL     = 24 * time.Hour
)

type executorCode struct {
	userID    string
	stateHash string
	expiresAt time.Time
}

type executorSession struct {
	userID    string
	expiresAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
}

type executorGate struct {
	mu       sync.Mutex
	codes    map[string]executorCode
	sessions map[string]executorSession
}

func newExecutorGate() *executorGate {
	return &executorGate{
		codes:    map[string]executorCode{},
		sessions: map[string]executorSession{},
	}
}

func (g *executorGate) issueCode(userID, stateHash string, now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, c := range g.codes {
		if !c.expiresAt.After(now) {
			delete(g.codes, k)
		}
	}
	code := randomHexToken(24)
	g.codes[code] = executorCode{userID: userID, stateHash: stateHash, expiresAt: now.Add(executorCodeTTL)}
	return code
}

func (g *executorGate) takeCode(code, state string, now time.Time) (string, bool) {
	if code == "" || state == "" {
		return "", false
	}
	wantHash := sha256Hex(state)
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.codes[code]
	delete(g.codes, code)
	if !ok || !c.expiresAt.After(now) || c.stateHash != wantHash {
		return "", false
	}
	return c.userID, true
}

func (g *executorGate) createSession(userID string, now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, s := range g.sessions {
		if !s.expiresAt.After(now) {
			if s.cancel != nil {
				s.cancel()
			}
			delete(g.sessions, k)
		}
	}
	tok := randomHexToken(32)
	sctx, scancel := context.WithCancel(context.Background())
	g.sessions[sha256Hex(tok)] = executorSession{
		userID:    userID,
		expiresAt: now.Add(executorSessionTTL),
		ctx:       sctx,
		cancel:    scancel,
	}
	return tok
}

func (g *executorGate) sessionLookup(tok string, now time.Time) (string, context.Context, bool) {
	if tok == "" {
		return "", nil, false
	}
	key := sha256Hex(tok)
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.sessions[key]
	if !ok || !s.expiresAt.After(now) {
		if ok && s.cancel != nil {
			s.cancel()
		}
		delete(g.sessions, key)
		return "", nil, false
	}
	return s.userID, s.ctx, true
}

func (g *executorGate) endSession(tok string) string {
	if tok == "" {
		return ""
	}
	key := sha256Hex(tok)
	g.mu.Lock()
	if s, ok := g.sessions[key]; ok {
		if s.cancel != nil {
			s.cancel()
		}
		delete(g.sessions, key)
	}
	g.mu.Unlock()
	return key
}

func (g *executorGate) endUserSessions(userID string) {
	if userID == "" {
		return
	}
	g.mu.Lock()
	for k, s := range g.sessions {
		if s.userID == userID {
			if s.cancel != nil {
				s.cancel()
			}
			delete(g.sessions, k)
		}
	}
	for k, c := range g.codes {
		if c.userID == userID {
			delete(g.codes, k)
		}
	}
	g.mu.Unlock()
}

func randomHexToken(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func executorError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, executor.ErrDisabled) || errors.Is(err, executor.ErrUnavailable) {
		return errStatus(http.StatusServiceUnavailable, err.Error())
	}
	if errors.Is(err, executor.ErrCustomAppsDisabled) {
		return errStatus(http.StatusForbidden, err.Error())
	}
	var ue *executor.UpstreamError
	if errors.As(err, &ue) {
		switch ue.Status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
			return errStatus(ue.Status, ue.Error())
		default:
			return errStatus(http.StatusBadGateway, ue.Error())
		}
	}
	return errStatus(http.StatusBadGateway, err.Error())
}

func (a *API) executorStatus(userID string) gen.ExecutorStatus {
	if a.Executor == nil {
		reason := "Executor is not configured on this server."
		return gen.ExecutorStatus{Available: false, Enabled: false, Running: false, OauthReady: false, CustomAppsEnabled: false, Reason: &reason}
	}
	s := a.Executor.Status(userID)
	out := gen.ExecutorStatus{
		Available:         s.Available,
		Enabled:           s.Enabled,
		Running:           s.Running,
		OauthReady:        s.OAuthReady,
		CustomAppsEnabled: a.Executor.AllowPrivateFetch(),
	}
	if s.Version != "" {
		out.Version = &s.Version
	}
	if s.DashboardURL != "" {
		out.DashboardUrl = &s.DashboardURL
	}
	if s.Reason != "" {
		out.Reason = &s.Reason
	}
	return out
}

func apiExecutorAuthMethods(in []executor.AuthMethod) []gen.ExecutorAuthMethod {
	out := make([]gen.ExecutorAuthMethod, 0, len(in))
	for _, m := range in {
		gm := gen.ExecutorAuthMethod{
			Name: m.Name,
			Kind: gen.ExecutorAuthMethodKind(m.Kind),
		}
		if m.Label != "" {
			l := m.Label
			gm.Label = &l
		}
		if m.Guide != "" {
			g := m.Guide
			gm.Guide = &g
		}
		if len(m.Fields) > 0 {
			fields := make([]gen.ExecutorSecretField, 0, len(m.Fields))
			for _, f := range m.Fields {
				gf := gen.ExecutorSecretField{Name: f.Name, Required: f.Required}
				if f.Title != "" {
					t := f.Title
					gf.Title = &t
				}
				if f.Description != "" {
					d := f.Description
					gf.Description = &d
				}
				sec := f.Secret
				gf.Secret = &sec
				fields = append(fields, gf)
			}
			gm.Fields = &fields
		}
		out = append(out, gm)
	}
	return out
}

func apiExecutorApp(a executor.App) gen.ExecutorApp {
	reqs := make([]gen.ExecutorRequirement, 0, len(a.Requirements))
	for _, r := range a.Requirements {
		linked := r.LinkedAccountIDs
		if linked == nil {
			linked = []string{}
		}
		reqs = append(reqs, gen.ExecutorRequirement{
			Name:             r.Name,
			Scope:            gen.ExecutorRequirementScope(r.Scope),
			ProviderId:       r.ProviderID,
			ProviderName:     r.ProviderName,
			Many:             r.Many,
			AuthMethods:      apiExecutorAuthMethods(r.AuthMethods),
			LinkedAccountIds: linked,
		})
	}
	out := gen.ExecutorApp{
		Id:           a.ID,
		Slug:         a.Slug,
		Name:         a.Name,
		Requirements: reqs,
	}
	if a.ActiveVersion != "" {
		v := a.ActiveVersion
		out.ActiveVersion = &v
	}
	if a.Builtin {
		b := true
		out.Builtin = &b
	}
	return out
}

func apiExecutorAccount(c executor.Account) gen.ExecutorAccount {
	linked := c.LinkedApps
	if linked == nil {
		linked = []string{}
	}
	out := gen.ExecutorAccount{
		Id:           c.ID,
		ProviderId:   c.ProviderID,
		ProviderName: c.ProviderName,
		Label:        c.Label,
		Method:       c.Method,
		Status:       c.Status,
		LinkedApps:   linked,
	}
	if c.Description != "" {
		d := c.Description
		out.Description = &d
	}
	if c.Identity != "" {
		i := c.Identity
		out.Identity = &i
	}
	return out
}

func (a *API) requireExecutor() (*executor.Manager, error) {
	if a.Executor == nil {
		return nil, errStatus(http.StatusServiceUnavailable, executor.ErrUnavailable.Error())
	}
	if !a.Executor.Enabled() {
		return nil, errStatus(http.StatusServiceUnavailable, executor.ErrDisabled.Error())
	}
	if !a.Executor.Available() {
		return nil, errStatus(http.StatusServiceUnavailable, executor.ErrUnavailable.Error())
	}
	return a.Executor, nil
}

// GetExecutorStatus implements gen.StrictServerInterface.
func (a *API) GetExecutorStatus(ctx context.Context, _ gen.GetExecutorStatusRequestObject) (gen.GetExecutorStatusResponseObject, error) {
	return gen.GetExecutorStatus200JSONResponse(a.executorStatus(me(ctx).ID)), nil
}

// StartExecutor implements gen.StrictServerInterface.
func (a *API) StartExecutor(ctx context.Context, _ gen.StartExecutorRequestObject) (gen.StartExecutorResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if _, err := mgr.Ensure(ctx, u.ID); err != nil {
		return nil, executorError(err)
	}
	return gen.StartExecutor200JSONResponse(a.executorStatus(u.ID)), nil
}

// RestartExecutor implements gen.StrictServerInterface.
func (a *API) RestartExecutor(ctx context.Context, _ gen.RestartExecutorRequestObject) (gen.RestartExecutorResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if _, err := mgr.Restart(ctx, u.ID); err != nil {
		return nil, executorError(err)
	}
	return gen.RestartExecutor200JSONResponse(a.executorStatus(u.ID)), nil
}

// GetExecutorOverview implements gen.StrictServerInterface.
func (a *API) GetExecutorOverview(ctx context.Context, _ gen.GetExecutorOverviewRequestObject) (gen.GetExecutorOverviewResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	ov, err := mgr.Overview(ctx, u.ID)
	if err != nil {
		return nil, executorError(err)
	}
	apps := make([]gen.ExecutorApp, 0, len(ov.Apps))
	for _, app := range ov.Apps {
		apps = append(apps, apiExecutorApp(app))
	}
	accounts := make([]gen.ExecutorAccount, 0, len(ov.Accounts))
	for _, acc := range ov.Accounts {
		accounts = append(accounts, apiExecutorAccount(acc))
	}
	return gen.GetExecutorOverview200JSONResponse{
		Status:   a.executorStatus(u.ID),
		Apps:     apps,
		Accounts: accounts,
	}, nil
}

// ListExecutorCatalog implements gen.StrictServerInterface.
func (a *API) ListExecutorCatalog(ctx context.Context, _ gen.ListExecutorCatalogRequestObject) (gen.ListExecutorCatalogResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	entries, err := mgr.Catalog(ctx, me(ctx).ID)
	if err != nil {
		return nil, executorError(err)
	}
	out := make([]gen.ExecutorCatalogEntry, 0, len(entries))
	for _, e := range entries {
		auths := e.Auth
		if auths == nil {
			auths = []string{}
		}
		item := gen.ExecutorCatalogEntry{Id: e.ID, Name: e.Name, Auth: auths}
		if e.Package != "" {
			p := e.Package
			item.Package = &p
		}
		if e.Description != "" {
			d := e.Description
			item.Description = &d
		}
		if e.Category != "" {
			c := e.Category
			item.Category = &c
		}
		if e.IconURL != "" {
			u := e.IconURL
			item.IconUrl = &u
		}
		out = append(out, item)
	}
	return gen.ListExecutorCatalog200JSONResponse{Apps: out}, nil
}

// ImportExecutorCatalogApp implements gen.StrictServerInterface.
func (a *API) ImportExecutorCatalogApp(ctx context.Context, req gen.ImportExecutorCatalogAppRequestObject) (gen.ImportExecutorCatalogAppResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Entry) == "" || strings.TrimSpace(req.Body.Name) == "" {
		return nil, badRequest("entry and name are required")
	}
	u := me(ctx)
	app, err := mgr.ImportCatalogApp(ctx, u.ID, strings.TrimSpace(req.Body.Entry), strings.TrimSpace(req.Body.Name))
	if err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAppImport, Target: app.ID, Details: map[string]any{"entry": req.Body.Entry, "name": app.Name}})
	return gen.ImportExecutorCatalogApp201JSONResponse(apiExecutorApp(app)), nil
}

// ImportExecutorCustomApp implements gen.StrictServerInterface.
func (a *API) ImportExecutorCustomApp(ctx context.Context, req gen.ImportExecutorCustomAppRequestObject) (gen.ImportExecutorCustomAppResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Name) == "" || strings.TrimSpace(req.Body.Url) == "" {
		return nil, badRequest("name and url are required")
	}
	parsed, err := url.Parse(strings.TrimSpace(req.Body.Url))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, badRequest("url must be an http(s) URL")
	}
	if !mgr.AllowPrivateFetch() {
		return nil, errStatus(http.StatusForbidden, executor.ErrCustomAppsDisabled.Error())
	}
	u := me(ctx)
	app, err := mgr.ImportCustomApp(ctx, u.ID, strings.TrimSpace(req.Body.Name), strings.TrimSpace(req.Body.Url))
	if err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAppImport, Target: app.ID, Details: map[string]any{"name": app.Name, "url": redactURL(req.Body.Url)}})
	return gen.ImportExecutorCustomApp201JSONResponse(apiExecutorApp(app)), nil
}

// DeleteExecutorApp implements gen.StrictServerInterface.
func (a *API) DeleteExecutorApp(ctx context.Context, req gen.DeleteExecutorAppRequestObject) (gen.DeleteExecutorAppResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if err := mgr.DeleteApp(ctx, u.ID, req.AppId); err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAppDelete, Target: req.AppId})
	return gen.DeleteExecutorApp204Response{}, nil
}

// StartExecutorAccountConnect implements gen.StrictServerInterface.
func (a *API) StartExecutorAccountConnect(ctx context.Context, req gen.StartExecutorAccountConnectRequestObject) (gen.StartExecutorAccountConnectResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	reconnect := ""
	if req.Body != nil && req.Body.AccountId != nil {
		reconnect = strings.TrimSpace(*req.Body.AccountId)
	}
	sess, err := mgr.StartAccountConnect(ctx, me(ctx).ID, req.AppId, req.Scope, req.Requirement, reconnect)
	if err != nil {
		return nil, executorError(err)
	}
	out := gen.StartExecutorAccountConnect201JSONResponse{
		ConnectionId: sess.ConnectionID,
		Token:        sess.Token,
		ProviderId:   sess.ProviderID,
		ProviderName: sess.ProviderName,
		Methods:      apiExecutorAuthMethods(sess.Methods),
	}
	if sess.BrowserURL != "" {
		u := sess.BrowserURL
		out.BrowserUrl = &u
	}
	if !sess.ExpiresAt.IsZero() {
		exp := sess.ExpiresAt.UTC()
		out.ExpiresAt = &exp
	}
	return out, nil
}

// SubmitExecutorAccountConnect implements gen.StrictServerInterface.
func (a *API) SubmitExecutorAccountConnect(ctx context.Context, req gen.SubmitExecutorAccountConnectRequestObject) (gen.SubmitExecutorAccountConnectResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	if req.Body == nil || req.Body.ConnectionId == "" || req.Body.Token == "" || req.Body.Method == "" {
		return nil, badRequest("connectionId, token and method are required")
	}
	label := ""
	if req.Body.Label != nil {
		label = *req.Body.Label
	}
	description := ""
	if req.Body.Description != nil {
		description = *req.Body.Description
	}
	u := me(ctx)
	acc, err := mgr.SubmitAccountConnect(ctx, u.ID, req.Body.ConnectionId, req.Body.Token, req.Body.Method, label, description, req.Body.Fields)
	if err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAccountLink, Target: acc.ID, Details: map[string]any{"provider": acc.ProviderName, "method": acc.Method}})
	return gen.SubmitExecutorAccountConnect200JSONResponse(apiExecutorAccount(acc)), nil
}

// LinkExecutorAccount implements gen.StrictServerInterface.
func (a *API) LinkExecutorAccount(ctx context.Context, req gen.LinkExecutorAccountRequestObject) (gen.LinkExecutorAccountResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if err := mgr.LinkAccount(ctx, u.ID, req.AppId, req.Scope, req.Requirement, req.AccountId); err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAccountLink, Target: req.AccountId, Details: map[string]any{"app": req.AppId, "requirement": req.Requirement}})
	return gen.LinkExecutorAccount204Response{}, nil
}

// UnlinkExecutorAccount implements gen.StrictServerInterface.
func (a *API) UnlinkExecutorAccount(ctx context.Context, req gen.UnlinkExecutorAccountRequestObject) (gen.UnlinkExecutorAccountResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if err := mgr.UnlinkAccount(ctx, u.ID, req.AppId, req.Scope, req.Requirement, req.AccountId); err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAccountUnlink, Target: req.AccountId, Details: map[string]any{"app": req.AppId, "requirement": req.Requirement}})
	return gen.UnlinkExecutorAccount204Response{}, nil
}

// UpdateExecutorAccount implements gen.StrictServerInterface.
func (a *API) UpdateExecutorAccount(ctx context.Context, req gen.UpdateExecutorAccountRequestObject) (gen.UpdateExecutorAccountResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	acc, err := mgr.UpdateAccount(ctx, me(ctx).ID, req.AccountId, req.Body.Label, req.Body.Description != nil, req.Body.Description)
	if err != nil {
		return nil, executorError(err)
	}
	return gen.UpdateExecutorAccount200JSONResponse(apiExecutorAccount(acc)), nil
}

// CheckExecutorAccountHealth implements gen.StrictServerInterface.
func (a *API) CheckExecutorAccountHealth(ctx context.Context, req gen.CheckExecutorAccountHealthRequestObject) (gen.CheckExecutorAccountHealthResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	h, err := mgr.CheckAccountHealth(ctx, me(ctx).ID, req.AccountId)
	if err != nil {
		return nil, executorError(err)
	}
	out := gen.CheckExecutorAccountHealth200JSONResponse{Status: h.Status}
	if h.Message != "" {
		msg := h.Message
		out.Message = &msg
	}
	return out, nil
}

// DeleteExecutorAccount implements gen.StrictServerInterface.
func (a *API) DeleteExecutorAccount(ctx context.Context, req gen.DeleteExecutorAccountRequestObject) (gen.DeleteExecutorAccountResponseObject, error) {
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	if err := mgr.DeleteAccount(ctx, u.ID, req.AccountId); err != nil {
		return nil, executorError(err)
	}
	a.Audit.Record(ctx, audit.Entry{UserID: u.ID, Action: audit.ExecutorAccountDelete, Target: req.AccountId})
	return gen.DeleteExecutorAccount204Response{}, nil
}

// --- Dedicated Executor Dashboard Host Handler ---

func (a *API) executorDashboardOrigin() (*url.URL, bool) {
	if a.Executor == nil || !a.Executor.Available() {
		return nil, false
	}
	raw := a.Executor.DashboardOrigin()
	if raw == "" {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

func (a *API) executorCookieName(secure bool) string {
	if secure {
		return "__Host-ah_executor"
	}
	return "ah_executor"
}

func (a *API) executorStateCookieName(secure bool) string {
	if secure {
		return "__Host-ah_executor_state"
	}
	return "ah_executor_state"
}

func executorCookie(name, value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
}

func sameHost(a, b string) bool {
	return strings.EqualFold(stripDefaultPort(a), stripDefaultPort(b))
}

func stripDefaultPort(host string) string {
	host = strings.TrimSpace(host)
	if h, ok := strings.CutSuffix(host, ":443"); ok {
		return h
	}
	if h, ok := strings.CutSuffix(host, ":80"); ok {
		return h
	}
	return host
}

// ExecutorHostHandler intercepts requests addressed to the dedicated Executor
// dashboard origin (executor<preview.host_suffix> or executor.public_url),
// enforces Agenthold sign-in, and reverse-proxies each user to their own
// isolated Executor instance with automatic browser pairing.
func (a *API) ExecutorHostHandler(next http.Handler) http.Handler {
	origin, ok := a.executorDashboardOrigin()
	if !ok {
		return next
	}
	secure := origin.Scheme == "https"
	sessCookieName := a.executorCookieName(secure)
	stateCookieName := a.executorStateCookieName(secure)
	stripNames := []string{
		sessCookieName, stateCookieName,
		"ah_executor", "__Host-ah_executor", "ah_executor_state", "__Host-ah_executor_state",
		"ah_preview", "__Host-ah_preview", "ah_preview_state", "__Host-ah_preview_state",
		auth.CookieName, auth.SecureCookieName,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameHost(r.Host, origin.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, executorReservedPrefix) {
			a.serveExecutorReserved(w, r, origin, sessCookieName, stateCookieName, secure)
			return
		}
		u, sessionKey, sessionCtx := a.executorHostSession(r, sessCookieName)
		if u == nil {
			if browserNavigation(r) {
				state := ""
				if c, err := r.Cookie(stateCookieName); err == nil && len(c.Value) >= 32 && len(c.Value) <= 128 {
					state = c.Value
				} else {
					state = id.Random(24)
				}
				http.SetCookie(w, executorCookie(stateCookieName, state, int(stateCookieTTL.Seconds()), secure))
				q := url.Values{
					"return": {auth.SafeRedirect(r.URL.RequestURI())},
					"state":  {sha256Hex(state)},
				}
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, a.PublicURL+"/api/v1/executor/authorize?"+q.Encode(), http.StatusFound)
				return
			}
			previewFail(w, r, http.StatusUnauthorized, "Not signed in", "sign in to agenthold to open Executor")
			return
		}
		if !executorCSRFOk(r, origin) {
			previewFail(w, r, http.StatusForbidden, "Forbidden", "cross-origin requests to Executor are not allowed")
			return
		}
		// Tie active proxy/SSE requests to the gate session's lifetime so logout
		// or user disable terminates live streams immediately.
		reqCtx, reqCancel := context.WithCancel(r.Context())
		defer reqCancel()
		stop := context.AfterFunc(sessionCtx, reqCancel)
		defer stop()
		a.Executor.ProxyRequest(w, r.WithContext(reqCtx), u.ID, sessionKey, stripNames)
	})
}

func executorCSRFOk(r *http.Request, origin *url.URL) bool {
	isUpgrade := strings.TrimSpace(r.Header.Get("Upgrade")) != ""
	isSafe := (r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions) && !isUpgrade
	if isSafe {
		return true
	}
	// Reject cross-site and sibling-subdomain same-site mutations/upgrades.
	if sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	reqOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if reqOrigin != "" {
		ou, err := url.Parse(reqOrigin)
		if err != nil || !strings.EqualFold(ou.Scheme, origin.Scheme) || !sameHost(ou.Host, origin.Host) {
			return false
		}
		return true
	}
	// Require either a valid Origin or same-origin/none Sec-Fetch-Site on cookie-authenticated mutations.
	sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	return sfs == "same-origin" || sfs == "none"
}

func (a *API) executorHostSession(r *http.Request, cookieName string) (*db.User, string, context.Context) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, "", nil
	}
	uid, sctx, ok := a.execGate.sessionLookup(c.Value, a.Now())
	if !ok {
		return nil, "", nil
	}
	dbu, err := a.Store.GetUser(r.Context(), uid)
	if err != nil {
		return nil, "", nil
	}
	if auth.Role(dbu) == config.RoleDisabled {
		return nil, "", nil
	}
	return &dbu, sha256Hex(c.Value), sctx
}

func (a *API) serveExecutorReserved(w http.ResponseWriter, r *http.Request, origin *url.URL, sessCookie, stateCookie string, secure bool) {
	switch r.URL.Path {
	case executorReservedPrefix + "callback":
		q := r.URL.Query()
		state := ""
		if c, err := r.Cookie(stateCookie); err == nil {
			state = c.Value
		}
		userID, ok := a.execGate.takeCode(q.Get("code"), state, a.Now())
		if !ok {
			previewPage(w, http.StatusBadRequest, "Sign-in failed", "The sign-in link expired or was not issued for this browser.", "/")
			return
		}
		tok := a.execGate.createSession(userID, a.Now())
		http.SetCookie(w, executorCookie(stateCookie, "", -1, secure))
		http.SetCookie(w, executorCookie(sessCookie, tok, int(executorSessionTTL.Seconds()), secure))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, auth.SafeRedirect(q.Get("return")), http.StatusFound)
	case executorReservedPrefix + "logout":
		if r.Method != http.MethodPost {
			previewPage(w, http.StatusMethodNotAllowed, "Method not allowed", "Use POST to sign out.", "/")
			return
		}
		if !executorCSRFOk(r, origin) {
			previewFail(w, r, http.StatusForbidden, "Forbidden", "cross-origin logout is not allowed")
			return
		}
		if c, err := r.Cookie(sessCookie); err == nil && c.Value != "" {
			if key := a.execGate.endSession(c.Value); key != "" && a.Executor != nil {
				a.Executor.ClearSessionPairing(key)
			}
		}
		http.SetCookie(w, executorCookie(sessCookie, "", -1, secure))
		previewPage(w, http.StatusOK, "Signed out", "You are signed out of Executor.", "/")
	default:
		previewFail(w, r, http.StatusNotFound, "Not found", "This path is reserved by agenthold.")
	}
}

// authorizeExecutor runs on the main Agenthold host: it hands the signed-in
// user's identity to the dedicated Executor host as a single-use code.
func (a *API) authorizeExecutor(w http.ResponseWriter, r *http.Request) {
	origin, ok := a.executorDashboardOrigin()
	if !ok {
		previewPage(w, http.StatusNotFound, "Not found", "Executor dashboard host is not configured on this server.", "")
		return
	}
	if !topLevelNavigation(r) {
		previewPage(w, http.StatusForbidden, "Refused", "Executor sign-in only starts from a page load.", "")
		return
	}
	ret := auth.SafeRedirect(r.FormValue("return"))
	state := r.FormValue("state")
	if len(state) != 64 {
		previewPage(w, http.StatusBadRequest, "Bad request", "Invalid Executor sign-in link.", "")
		return
	}
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	if auth.Role(*u) == config.RoleDisabled {
		previewPage(w, http.StatusForbidden, "No access", "Your account does not have access to this agenthold.", "")
		return
	}
	code := a.execGate.issueCode(u.ID, state, a.Now())
	back := url.Values{"code": {code}, "return": {ret}}
	w.Header().Set("Cache-Control", "no-store")
	slog.DebugContext(r.Context(), "issued executor dashboard code", "user", u.ID)
	http.Redirect(w, r, strings.TrimRight(origin.String(), "/")+executorReservedPrefix+"callback?"+back.Encode(), http.StatusSeeOther)
}

// ResolveExecutorCaller determines which user's Executor instance must handle
// an incoming Executor MCP call for agentID/sessionID.
//
// When a turn is active for a collaborator or shared user, it routes strictly
// to that acting user's Executor instance — NEVER falling back to the agent
// owner's personal OAuth accounts. It falls back to the agent owner only when
// the acting caller is the owner or a system/idle call with no non-owner turn.
func ResolveExecutorCaller(ctx context.Context, st *store.Store, sessions *session.Manager, agentID, sessionID string) (string, error) {
	ag, err := st.GetAgent(ctx, agentID)
	if err != nil {
		return "", errors.New("agent not found")
	}
	targetUserID := ag.OwnerID
	if sessions != nil {
		uid, active, ambiguous := sessions.ActiveCaller(agentID, sessionID)
		if ambiguous {
			return "", errors.New("cannot route executor call: multiple users are running concurrent turns without session attribution")
		}
		if active && uid != "" {
			targetUserID = uid
		}
	}
	u, err := st.GetUser(ctx, targetUserID)
	if err != nil {
		return "", errors.New("acting user not found")
	}
	if authz.UserRole(u) == config.RoleDisabled {
		return "", errors.New("acting user is disabled")
	}
	if targetUserID != ag.OwnerID {
		chk := authz.Checker{Q: st.Queries}
		if sessionID != "" {
			if _, err := chk.Session(ctx, targetUserID, sessionID, authz.SessionPrompt); err != nil {
				return "", errors.New("acting user does not have prompt access to this session")
			}
		} else {
			role, err := chk.RoleOf(ctx, targetUserID, ag)
			if err != nil {
				return "", err
			}
			if !authz.Can(role, authz.AgentPrompt) {
				// Allow collaborator thread shares if the acting turn is on a shared thread.
				shared, err := chk.HasSessionShareOnAgent(ctx, targetUserID, ag.ID)
				if err != nil || !shared {
					return "", errors.New("acting user does not have access to this agent")
				}
			}
		}
	}
	return targetUserID, nil
}
