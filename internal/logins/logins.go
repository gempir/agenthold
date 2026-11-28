// Package logins keeps provider logins (Claude Code, Codex) on users'
// accounts and delivers them to the agents that use them.
//
// A user connects a login once. Each agent names whose account logins it
// uses (agents.login_user_id); only that user can choose it, for an agent
// they administer. When the agent's harness starts, the adapter turns the
// stored credential into placeholders for the sandbox (environment, files)
// and injections for the egress proxy, which puts the real value into the
// harness's requests. Expiring logins are refreshed here and only here, so
// any number of sandboxes can share one rotating refresh token.
package logins

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Audit actions.
const (
	ActionConnect       = "provider_login.connect"
	ActionDisconnect    = "provider_login.disconnect"
	ActionRefreshFailed = "provider_login.refresh_failed"
	ActionUse           = "provider_login.use"
	ActionBind          = "provider_login.bind"
	ActionUnbind        = "provider_login.unbind"
)

// Sources of an agent's harness login.
const (
	// SourceAccount: the account login of the agent's login user.
	SourceAccount = "account"
	// SourceSecret: an API key secret attached to the agent wins.
	SourceSecret = "secret"
	// SourceSandbox: whatever is signed in inside the sandbox.
	SourceSandbox = "sandbox"
	// SourceUnsupported: the harness has no account logins.
	SourceUnsupported = "unsupported"
)

// Errors.
var (
	ErrUnknownProvider = errors.New("this harness has no account logins")
	ErrUnknownKind     = errors.New("unknown login kind")
	ErrNotConnected    = errors.New("not connected")
)

// Service manages account logins.
type Service struct {
	Store *store.Store
	Box   *secrets.Box
	Audit *audit.Log
	// HTTP calls providers (verification, refresh). It must not go through
	// the egress proxy. Default: a client with a 30s timeout.
	HTTP *http.Client
	Log  *slog.Logger
	Now  func() time.Time
	// Invalidate drops cached egress injections.
	Invalidate func()
	// Restart restarts an agent's harness once it is idle, so it starts with
	// its current login.
	Restart func(agentID string)
	// RefreshInterval is how often expiring logins are checked. Default 5 minutes.
	RefreshInterval time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Service) lock(loginID string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	l := s.locks[loginID]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[loginID] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Adapter returns the account-login side of a harness.
func Adapter(provider string) (harness.AccountLoginer, bool) {
	ad, ok := harness.Get(provider)
	if !ok || ad.Login == nil {
		return nil, false
	}
	return ad.Login, true
}

func (s *Service) open(l db.ProviderLogin) (harness.Credential, error) {
	b, err := s.Box.Open(l.Ciphertext, []byte(l.ID))
	if err != nil {
		return harness.Credential{}, err
	}
	return harness.Credential{Kind: l.Kind, Secret: b, Label: l.Label, ExpiresAt: unixTime(l.ExpiresAt)}, nil
}

func unixTime(t int64) time.Time {
	if t == 0 {
		return time.Time{}
	}
	return time.Unix(t, 0)
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Service) recordAccount(ctx context.Context, userID, action, provider string, details map[string]any) {
	s.Audit.Record(ctx, audit.Entry{UserID: userID, Action: action, Target: provider, Details: details})
}

func (s *Service) recordAgent(ctx context.Context, ag db.Agent, userID, action, target string, details map[string]any) {
	s.Audit.Record(ctx, audit.Entry{AgentID: ag.ID, UserID: userID, Action: action, Target: target, Details: details})
}

// changed tells caches and harnesses that the logins of userID's agents for
// provider ("" for all) changed.
func (s *Service) changed(ctx context.Context, userID, provider string) {
	if s.Invalidate != nil {
		s.Invalidate()
	}
	if s.Restart == nil {
		return
	}
	agents, err := s.Store.ListAgentsByLoginUser(ctx, userID)
	if err != nil {
		return
	}
	for _, ag := range agents {
		if provider == "" || ag.Harness == provider {
			s.Restart(ag.ID)
		}
	}
}

// Connect stores (or replaces) userID's login for provider.
func (s *Service) Connect(ctx context.Context, userID, provider, kind, input string) (db.ProviderLogin, error) {
	al, ok := Adapter(provider)
	if !ok {
		return db.ProviderLogin{}, ErrUnknownProvider
	}
	if _, ok := harness.FindKind(al, kind); !ok {
		return db.ProviderLogin{}, ErrUnknownKind
	}
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cred, err := al.ParseLogin(pctx, s.client(), kind, input)
	if err != nil {
		return db.ProviderLogin{}, &InputError{err}
	}
	unlock := s.lock(userID + "/" + provider)
	defer unlock()
	now := s.now()
	existing, err := s.Store.GetProviderLogin(ctx, db.GetProviderLoginParams{UserID: userID, Provider: provider})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		l := db.ProviderLogin{ID: id.New("pl"), UserID: userID, Provider: provider, Kind: cred.Kind, Seed: id.Random(16), Label: cred.Label, ExpiresAt: unix(cred.ExpiresAt), RefreshedAt: now.Unix(), Status: "ok", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
		if l.Ciphertext, err = s.Box.Seal(cred.Secret, []byte(l.ID)); err != nil {
			return l, err
		}
		if err := s.Store.CreateProviderLogin(ctx, db.CreateProviderLoginParams{
			ID: l.ID, UserID: userID, Provider: provider, Kind: l.Kind, Ciphertext: l.Ciphertext, Seed: l.Seed, Label: l.Label,
			ExpiresAt: l.ExpiresAt, RefreshedAt: l.RefreshedAt, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
		}); err != nil {
			return l, err
		}
		existing = l
	case err != nil:
		return db.ProviderLogin{}, err
	default:
		ct, err := s.Box.Seal(cred.Secret, []byte(existing.ID))
		if err != nil {
			return existing, err
		}
		existing.Kind, existing.Ciphertext, existing.Label = cred.Kind, ct, cred.Label
		existing.ExpiresAt, existing.RefreshedAt, existing.UpdatedAt = unix(cred.ExpiresAt), now.Unix(), now.Unix()
		existing.Status, existing.StatusMessage = "ok", ""
		if err := s.Store.ReplaceProviderLogin(ctx, db.ReplaceProviderLoginParams{
			Kind: existing.Kind, Ciphertext: ct, Label: existing.Label, ExpiresAt: existing.ExpiresAt,
			RefreshedAt: existing.RefreshedAt, UpdatedAt: existing.UpdatedAt, ID: existing.ID,
		}); err != nil {
			return existing, err
		}
	}
	s.recordAccount(ctx, userID, ActionConnect, provider, map[string]any{"kind": cred.Kind})
	s.log().Info("provider login connected", "user", userID, "provider", provider, "kind", cred.Kind)
	s.changed(ctx, userID, provider)
	return existing, nil
}

// InputError is a credential the adapter rejected; its message is safe to show.
type InputError struct{ Err error }

func (e *InputError) Error() string { return e.Err.Error() }
func (e *InputError) Unwrap() error { return e.Err }

// Disconnect deletes userID's login for provider.
func (s *Service) Disconnect(ctx context.Context, userID, provider string) error {
	n, err := s.Store.DeleteProviderLogin(ctx, db.DeleteProviderLoginParams{UserID: userID, Provider: provider})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotConnected
	}
	s.recordAccount(ctx, userID, ActionDisconnect, provider, nil)
	s.log().Info("provider login disconnected", "user", userID, "provider", provider)
	s.changed(ctx, userID, provider)
	return nil
}

// Bind makes ag use userID's account logins. The caller checks that userID
// is the acting user and administers the agent.
func (s *Service) Bind(ctx context.Context, ag db.Agent, userID string) error {
	if err := s.Store.SetAgentLoginUser(ctx, db.SetAgentLoginUserParams{LoginUserID: userID, ID: ag.ID}); err != nil {
		return err
	}
	s.recordAgent(ctx, ag, userID, ActionBind, userID, map[string]any{"previous": ag.LoginUserID})
	s.agentChanged(ag.ID)
	return nil
}

// Unbind makes ag stop using account logins; actorID is who decided it.
func (s *Service) Unbind(ctx context.Context, ag db.Agent, actorID string) error {
	if err := s.Store.SetAgentLoginUser(ctx, db.SetAgentLoginUserParams{LoginUserID: "", ID: ag.ID}); err != nil {
		return err
	}
	s.recordAgent(ctx, ag, actorID, ActionUnbind, ag.LoginUserID, nil)
	s.agentChanged(ag.ID)
	return nil
}

func (s *Service) agentChanged(agentID string) {
	if s.Invalidate != nil {
		s.Invalidate()
	}
	if s.Restart != nil {
		s.Restart(agentID)
	}
}

// State is what an agent's login looks like, without the credential.
type State struct {
	Source string
	// User is whose account logins the agent uses ("" for none).
	User string
	// Login is that user's login for the agent's harness, if connected.
	Login *db.ProviderLogin
	// Reason explains SourceSandbox when a login user is set.
	Reason string
}

// StateOf works out which login an agent's harness gets.
func (s *Service) StateOf(ctx context.Context, ag db.Agent) (State, error) {
	if _, ok := Adapter(ag.Harness); !ok {
		return State{Source: SourceUnsupported, User: ag.LoginUserID}, nil
	}
	if ag.LoginUserID == "" {
		return State{Source: SourceSandbox}, nil
	}
	st := State{Source: SourceSandbox, User: ag.LoginUserID}
	// A key attached to this agent for the harness is an explicit,
	// per-agent choice: it wins.
	if overridden, err := s.secretOverrides(ctx, ag); err != nil {
		return st, err
	} else if overridden {
		st.Source = SourceSecret
		return st, nil
	}
	// The login user must still have access to the agent; when they leave
	// it, their login stops being used.
	role, err := authz.Checker{Q: s.Store.Queries}.RoleOf(ctx, ag.LoginUserID, ag)
	if err != nil {
		return st, err
	}
	if !authz.Can(role, authz.AgentRead) {
		st.Reason = "the login's owner no longer has access to this agent"
		return st, nil
	}
	st.Source = SourceAccount
	l, err := s.Store.GetProviderLogin(ctx, db.GetProviderLoginParams{UserID: ag.LoginUserID, Provider: ag.Harness})
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Login = &l
	return st, nil
}

func (s *Service) secretOverrides(ctx context.Context, ag db.Agent) (bool, error) {
	ad, ok := harness.Get(ag.Harness)
	if !ok {
		return false, nil
	}
	vars := map[string]bool{}
	for _, p := range secrets.Presets {
		for _, id := range ad.SecretPresets {
			if p.ID == id {
				vars[p.EnvVar] = true
			}
		}
	}
	rows, err := s.Store.ListAgentSecrets(ctx, ag.ID)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if vars[r.EnvVar] {
			return true, nil
		}
	}
	return false, nil
}

// delivery resolves the credential an agent's harness gets, if any.
func (s *Service) delivery(ctx context.Context, ag db.Agent) (*db.ProviderLogin, harness.AccountLoginer, harness.Delivery, error) {
	st, err := s.StateOf(ctx, ag)
	if err != nil || st.Source != SourceAccount || st.Login == nil {
		return nil, nil, harness.Delivery{}, err
	}
	al, _ := Adapter(ag.Harness)
	cred, err := s.open(*st.Login)
	if err != nil {
		return nil, nil, harness.Delivery{}, err
	}
	d, err := al.Deliver(cred, st.Login.Seed)
	if err != nil {
		return nil, nil, harness.Delivery{}, err
	}
	return st.Login, al, d, nil
}

// Prepare runs before an agent's harness starts: it writes the login's
// files into the sandbox (or removes ones left from an earlier login) and
// returns the environment to add to the harness.
func (s *Service) Prepare(ctx context.Context, ag db.Agent, ts *tunnel.Session) (map[string]string, error) {
	l, al, d, err := s.delivery(ctx, ag)
	if err != nil {
		return nil, err
	}
	managed := map[string]bool{}
	if al == nil {
		al, _ = Adapter(ag.Harness)
	}
	if al != nil {
		for _, p := range al.ManagedFiles() {
			managed[p] = true
		}
	}
	for p := range d.Files {
		managed[p] = true
	}
	for p := range managed {
		var cur bytes.Buffer
		exists := ts.ReadFile(ctx, p, nil, &cur) == nil
		ours := bytes.Contains(cur.Bytes(), []byte(harness.PlaceholderMarker))
		content, deliver := d.Files[p]
		switch {
		case deliver:
			if exists && !ours {
				// A login made inside the sandbox: keep a copy rather than destroy it.
				if err := ts.WriteFile(ctx, p+".agenthold-backup", 0o600, bytes.NewReader(cur.Bytes())); err != nil {
					return nil, fmt.Errorf("back up %s: %w", p, err)
				}
			}
			if err := ts.WriteFile(ctx, p, 0o600, bytes.NewReader(content)); err != nil {
				return nil, fmt.Errorf("write %s: %w", p, err)
			}
		case exists && ours:
			_ = ts.Remove(ctx, p, false)
		}
	}
	if l == nil {
		return nil, nil
	}
	now := s.now()
	_ = s.Store.TouchProviderLogin(ctx, db.TouchProviderLoginParams{LastUsedAt: now.Unix(), ID: l.ID})
	s.recordAgent(ctx, ag, l.UserID, ActionUse, l.Provider, map[string]any{"kind": l.Kind, "loginUser": l.UserID})
	if s.Invalidate != nil {
		s.Invalidate()
	}
	return d.Env, nil
}

// Egress implements egress.AccountLogins. It delivers injections and refresh
// responders for the agent's own harness, plus any other harness pinned on a
// sub-thread of this agent (e.g. a Codex sub-thread on a Claude Code agent)
// when the agent is linked to a user's account logins.
func (s *Service) Egress(ctx context.Context, ag db.Agent) ([]secrets.Injection, []egress.Responder, error) {
	harnesses := []string{ag.Harness}
	seen := map[string]bool{ag.Harness: true}
	if ag.LoginUserID != "" {
		if sessions, err := s.Store.ListAgentSessions(ctx, ag.ID); err == nil {
			for _, sess := range sessions {
				if sess.Harness != "" && !seen[sess.Harness] {
					seen[sess.Harness] = true
					harnesses = append(harnesses, sess.Harness)
				}
			}
		}
	}
	var injs []secrets.Injection
	var resp []egress.Responder
	for _, h := range harnesses {
		cur := ag
		cur.Harness = h
		l, al, d, err := s.delivery(ctx, cur)
		if err != nil {
			if h == ag.Harness {
				return nil, nil, err
			}
			continue
		}
		if l == nil {
			continue
		}
		for _, in := range d.Injections {
			injs = append(injs, secrets.Injection{
				SecretID: "login:" + l.ID, Rule: secrets.Rule{Hosts: in.Hosts, Header: in.Header},
				Value: in.Value, Placeholder: in.Placeholder, HeaderOnly: true, PathPrefixes: in.PathPrefixes,
			})
		}
		rf, canRefresh := al.(harness.Refresher)
		for _, ic := range d.Intercepts {
			if !canRefresh {
				continue
			}
			resp = append(resp, egress.Responder{Host: ic.Host, Method: ic.Method, Path: ic.Path, Placeholder: ic.Placeholder, Handle: s.interceptHandler(ic, l.ID, rf)})
		}
	}
	return injs, resp, nil
}

// interceptHandler answers the harness's own token refresh: the server
// refreshes (at most once per minute however many sandboxes ask) and
// replies with the same placeholders.
func (s *Service) interceptHandler(ic harness.Intercept, loginID string, rf harness.Refresher) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != ic.Method || r.URL.Path != ic.Path {
			return false
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil || !strings.Contains(string(body), ic.Placeholder) {
			return false // not ours (for example a login made inside the sandbox)
		}
		l, cred, err := s.refresh(r.Context(), loginID, time.Minute)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case errors.Is(err, harness.ErrLoginRevoked) || errors.Is(err, sql.ErrNoRows):
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"agenthold: connect this login again in your account settings"}`)
			return true
		case err != nil:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"temporarily_unavailable"}`)
			return true
		}
		out, err := rf.InterceptResponse(cred, l.Seed)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		_, _ = w.Write(out)
		return true
	}
}

// refresh renews a login if it is due, or (with minAge > 0) if it was last
// refreshed longer than minAge ago. Concurrent callers wait for one refresh.
func (s *Service) refresh(ctx context.Context, loginID string, minAge time.Duration) (db.ProviderLogin, harness.Credential, error) {
	unlock := s.lock(loginID)
	defer unlock()
	l, err := s.Store.GetProviderLoginByID(ctx, loginID)
	if err != nil {
		return l, harness.Credential{}, err
	}
	cred, err := s.open(l)
	if err != nil {
		return l, cred, err
	}
	al, ok := Adapter(l.Provider)
	rf, ok2 := al.(harness.Refresher)
	if !ok || !ok2 {
		return l, cred, nil
	}
	now := s.now()
	due := rf.RefreshDue(cred, now)
	if minAge > 0 {
		due = now.Sub(unixTime(l.RefreshedAt)) >= minAge
	}
	if !due {
		return l, cred, nil
	}
	if l.Status == "error" {
		return l, cred, harness.ErrLoginRevoked
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	next, err := rf.Refresh(rctx, s.client(), cred)
	if err != nil {
		if errors.Is(err, harness.ErrLoginRevoked) {
			_ = s.Store.SetProviderLoginStatus(ctx, db.SetProviderLoginStatusParams{Status: "error", StatusMessage: harness.ErrLoginRevoked.Error(), ID: l.ID})
			s.recordAccount(context.WithoutCancel(ctx), l.UserID, ActionRefreshFailed, l.Provider, nil)
		}
		s.log().Warn("provider login refresh failed", "login", l.ID, "provider", l.Provider, "err", err)
		return l, cred, err
	}
	ct, err := s.Box.Seal(next.Secret, []byte(l.ID))
	if err != nil {
		return l, cred, err
	}
	n, err := s.Store.StoreRefreshedLogin(ctx, db.StoreRefreshedLoginParams{
		Ciphertext: ct, Label: next.Label, ExpiresAt: unix(next.ExpiresAt), RefreshedAt: now.Unix(), ID: l.ID, OldCiphertext: l.Ciphertext,
	})
	if err != nil {
		return l, cred, err
	}
	if n == 0 {
		// Reconnected meanwhile: the new login wins.
		l, err = s.Store.GetProviderLoginByID(ctx, loginID)
		if err != nil {
			return l, cred, err
		}
		cred, err = s.open(l)
		return l, cred, err
	}
	l.Ciphertext, l.Label, l.ExpiresAt, l.RefreshedAt, l.Status, l.StatusMessage = ct, next.Label, unix(next.ExpiresAt), now.Unix(), "ok", ""
	s.log().Info("provider login refreshed", "login", l.ID, "provider", l.Provider)
	if s.Invalidate != nil {
		s.Invalidate()
	}
	return l, next, nil
}

// RefreshDue refreshes every login that is about to expire.
func (s *Service) RefreshDue(ctx context.Context) {
	rows, err := s.Store.ListProviderLogins(ctx)
	if err != nil {
		s.log().Error("provider logins", "err", err)
		return
	}
	for _, l := range rows {
		if l.Status != "ok" {
			continue
		}
		if al, ok := Adapter(l.Provider); ok {
			if _, ok := al.(harness.Refresher); ok {
				_, _, _ = s.refresh(ctx, l.ID, 0)
			}
		}
	}
}

// Run refreshes expiring logins until ctx ends.
func (s *Service) Run(ctx context.Context) {
	every := s.RefreshInterval
	if every == 0 {
		every = 5 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	s.RefreshDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RefreshDue(ctx)
		}
	}
}
