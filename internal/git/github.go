package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

// Connecting a GitHub account.
//
// The user starts GitHub's device flow from Settings → Git: the server asks
// GitHub for a device code, shows the user the short user code and GitHub's
// verification page, and polls GitHub until the user approved it there. The
// device code never leaves the server and a flow belongs to the user who
// started it. The resulting token is stored like a pasted token (sealed, only
// injected by the egress proxy for the agents the user owns), so neither the
// browser nor any sandbox ever sees it. Expiring GitHub App user tokens are
// refreshed here, never in a sandbox.

// Device flow states, as Poll reports them.
const (
	FlowPending   = "pending"
	FlowConnected = "connected"
	FlowDenied    = "denied"
	FlowExpired   = "expired"
)

// Errors.
var (
	// ErrGitHubDisabled: the server has no git.github app configured.
	ErrGitHubDisabled = errors.New("connecting GitHub accounts is not configured on this server (git.github.client_id)")
	// ErrFlowNotFound: no such device flow for this user (finished, expired or never started).
	ErrFlowNotFound = errors.New("GitHub connection attempt not found; start again")
)

// Connector talks to GitHub (or a GitHub Enterprise Server) for device flows,
// token refreshes and revocation.
type Connector struct {
	Config config.GitHubConnect
	// HTTP calls GitHub from the server (never through the egress proxy).
	// Default: a client with a 30 s timeout.
	HTTP *http.Client
	Log  *slog.Logger

	mu    sync.Mutex
	flows map[string]*deviceFlow // by flow id
}

// refreshAhead is how long before expiry a token is refreshed.
const refreshAhead = 15 * time.Minute

// maxFlowsPerUser bounds pending device flows (a new one replaces the oldest).
const maxFlowsPerUser = 1

type deviceFlow struct {
	id, userID string
	deviceCode string
	interval   time.Duration
	next       time.Time // no poll to GitHub before this
	expires    time.Time
	polling    sync.Mutex
}

// DeviceFlow is what the user needs to approve a connection: the code to
// enter at VerificationURI. The device code stays on the server.
type DeviceFlow struct {
	ID              string `json:"id"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresAt       int64  `json:"expiresAt"`
	Interval        int    `json:"interval"`
}

// PollResult is the state of a device flow.
type PollResult struct {
	Status     string      `json:"status"`
	Credential *Credential `json:"credential,omitempty"`
}

func (g *Connector) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *Connector) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// oauthReply is GitHub's answer on /login/device/code and /login/oauth/access_token.
type oauthReply struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int    `json:"interval"`

	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (r oauthReply) err() error {
	if r.ErrorDescription != "" {
		return fmt.Errorf("github: %s (%s)", r.ErrorDescription, r.Error)
	}
	return fmt.Errorf("github: %s", r.Error)
}

// postForm posts form to path on the GitHub web host and decodes the JSON answer.
func (g *Connector) postForm(ctx context.Context, path string, form url.Values) (oauthReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Config.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := g.client().Do(req)
	if err != nil {
		return oauthReply{}, fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return oauthReply{}, fmt.Errorf("github: %w", err)
	}
	var out oauthReply
	if err := json.Unmarshal(body, &out); err != nil {
		return oauthReply{}, fmt.Errorf("github: POST %s: %s", path, res.Status)
	}
	if res.StatusCode >= 500 {
		return oauthReply{}, fmt.Errorf("github: POST %s: %s", path, res.Status)
	}
	return out, nil
}

// user returns the login of the token's account.
func (g *Connector) user(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Config.APIURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := g.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: GET /user: %s", res.Status)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&u); err != nil {
		return "", fmt.Errorf("github: GET /user: %w", err)
	}
	if u.Login == "" {
		return "", errors.New("github: GET /user: no login")
	}
	return u.Login, nil
}

// revoke deletes token at GitHub (OAuth and GitHub Apps; needs the client
// secret). Best effort: the credential is deleted here either way.
func (g *Connector) revoke(ctx context.Context, token string) {
	if g.Config.ClientSecret == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"access_token": token})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		g.Config.APIURL+"/applications/"+url.PathEscape(g.Config.ClientID)+"/token", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.SetBasicAuth(g.Config.ClientID, g.Config.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := g.client().Do(req)
	if err != nil {
		g.log().Warn("github: revoke a disconnected token", "err", err)
		return
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
		g.log().Warn("github: revoke a disconnected token", "status", res.Status)
	}
}

// StartGitHub starts a device flow for the user.
func (s *Service) StartGitHub(ctx context.Context, userID string) (DeviceFlow, error) {
	g := s.GitHub
	if g == nil || !g.Config.Enabled() {
		return DeviceFlow{}, ErrGitHubDisabled
	}
	r, err := g.postForm(ctx, "/login/device/code", url.Values{
		"client_id": {g.Config.ClientID},
		"scope":     {strings.Join(g.Config.Scopes, " ")},
	})
	if err != nil {
		return DeviceFlow{}, err
	}
	if r.Error != "" {
		return DeviceFlow{}, r.err()
	}
	if r.DeviceCode == "" || r.UserCode == "" || r.VerificationURI == "" {
		return DeviceFlow{}, errors.New("github: incomplete device code response")
	}
	// The page the user is sent to must be the configured GitHub's.
	if vu, err := url.Parse(r.VerificationURI); err != nil || !strings.EqualFold(vu.Hostname(), g.Config.Host()) || (vu.Scheme != "https" && !strings.HasPrefix(g.Config.URL, "http://")) {
		return DeviceFlow{}, fmt.Errorf("github: unexpected verification page %q", r.VerificationURI)
	}
	interval := time.Duration(max(r.Interval, 5)) * time.Second
	ttl := time.Duration(r.ExpiresIn) * time.Second
	if ttl <= 0 || ttl > 30*time.Minute {
		ttl = 15 * time.Minute
	}
	now := s.now()
	f := &deviceFlow{
		id: id.New("ghf"), userID: userID, deviceCode: r.DeviceCode,
		interval: interval, next: now.Add(interval), expires: now.Add(ttl),
	}
	g.mu.Lock()
	if g.flows == nil {
		g.flows = map[string]*deviceFlow{}
	}
	var mine []*deviceFlow
	for fid, other := range g.flows {
		if now.After(other.expires) {
			delete(g.flows, fid)
		} else if other.userID == userID {
			mine = append(mine, other)
		}
	}
	for len(mine) >= maxFlowsPerUser {
		oldest := 0
		for i := range mine {
			if mine[i].expires.Before(mine[oldest].expires) {
				oldest = i
			}
		}
		delete(g.flows, mine[oldest].id)
		mine = append(mine[:oldest], mine[oldest+1:]...)
	}
	g.flows[f.id] = f
	g.mu.Unlock()
	return DeviceFlow{
		ID: f.id, UserCode: r.UserCode, VerificationURI: r.VerificationURI,
		ExpiresAt: f.expires.Unix(), Interval: int(interval / time.Second),
	}, nil
}

func (g *Connector) flow(userID, flowID string, now time.Time) *deviceFlow {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.flows[flowID]
	if f == nil || f.userID != userID {
		return nil
	}
	if now.After(f.expires) {
		delete(g.flows, flowID)
		return nil
	}
	return f
}

func (g *Connector) drop(flowID string) {
	g.mu.Lock()
	delete(g.flows, flowID)
	g.mu.Unlock()
}

// CancelGitHub forgets a device flow of the user.
func (s *Service) CancelGitHub(userID, flowID string) {
	if g := s.GitHub; g != nil && g.flow(userID, flowID, s.now()) != nil {
		g.drop(flowID)
	}
}

// PollGitHub checks whether the user approved the device flow. It contacts
// GitHub at most once per the interval GitHub asked for; earlier calls report
// pending. On approval the account is stored as the user's credential for
// the GitHub host (replacing a token stored for it).
func (s *Service) PollGitHub(ctx context.Context, userID, flowID string) (PollResult, error) {
	g := s.GitHub
	if g == nil || !g.Config.Enabled() {
		return PollResult{}, ErrGitHubDisabled
	}
	f := g.flow(userID, flowID, s.now())
	if f == nil {
		return PollResult{}, ErrFlowNotFound
	}
	if !f.polling.TryLock() {
		return PollResult{Status: FlowPending}, nil
	}
	defer f.polling.Unlock()
	if s.now().Before(f.next) {
		return PollResult{Status: FlowPending}, nil
	}
	r, err := g.postForm(ctx, "/login/oauth/access_token", url.Values{
		"client_id":   {g.Config.ClientID},
		"device_code": {f.deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
	f.next = s.now().Add(f.interval)
	if err != nil {
		return PollResult{}, err
	}
	switch r.Error {
	case "":
	case "authorization_pending":
		return PollResult{Status: FlowPending}, nil
	case "slow_down":
		f.interval = max(f.interval+5*time.Second, time.Duration(r.Interval)*time.Second)
		f.next = s.now().Add(f.interval)
		return PollResult{Status: FlowPending}, nil
	case "access_denied":
		g.drop(flowID)
		return PollResult{Status: FlowDenied}, nil
	case "expired_token", "incorrect_device_code":
		g.drop(flowID)
		return PollResult{Status: FlowExpired}, nil
	default:
		g.drop(flowID)
		return PollResult{}, r.err()
	}
	if r.AccessToken == "" {
		return PollResult{}, errors.New("github: no access token in the response")
	}
	g.drop(flowID)
	login, err := g.user(ctx, r.AccessToken)
	if err != nil {
		return PollResult{}, err
	}
	now := s.now().Unix()
	c := stored{
		host: g.Config.Host(), username: DefaultUsername("github.com"), label: "@" + login,
		source: SourceGitHub, account: login, scopes: r.Scope, token: r.AccessToken,
	}
	if r.ExpiresIn > 0 {
		c.expiresAt = now + r.ExpiresIn
		c.refresh = r.RefreshToken
		if r.RefreshTokenExpiresIn > 0 {
			c.refreshExp = now + r.RefreshTokenExpiresIn
		}
	}
	cred, err := s.put(ctx, userID, c)
	if err != nil {
		return PollResult{}, err
	}
	s.Audit.Record(ctx, audit.Entry{
		UserID: userID, Action: audit.GitHubConnect, Target: cred.ID,
		Details: map[string]any{"host": cred.Host, "account": login, "scopes": r.Scope, "expires": c.expiresAt > 0},
	})
	return PollResult{Status: FlowConnected, Credential: &cred}, nil
}

// RefreshDue renews connected GitHub tokens that expire soon. Tokens whose
// refresh fails keep working until they expire; their user reconnects then.
func (s *Service) RefreshDue(ctx context.Context) {
	g := s.GitHub
	if g == nil || !g.Config.Enabled() || g.Config.ClientSecret == "" {
		return
	}
	due, err := s.Store.ListRefreshableGitCredentials(ctx, s.now().Add(refreshAhead).Unix())
	if err != nil {
		g.log().Error("github: list tokens to refresh", "err", err)
		return
	}
	for _, c := range due {
		if err := s.refresh(ctx, c); err != nil {
			g.log().Warn("github: refresh a connected token", "credential", c.ID, "err", err)
			s.Audit.Record(ctx, audit.Entry{
				UserID: c.UserID, Action: audit.GitHubRefreshFailed, Target: c.ID,
				Details: map[string]any{"account": c.Account, "error": err.Error()},
			})
		}
	}
}

func (s *Service) refresh(ctx context.Context, c db.GitCredential) error {
	g := s.GitHub
	rt, err := s.Box.Open(c.RefreshCiphertext, refreshAAD(c.ID))
	if err != nil {
		return err
	}
	if c.RefreshExpiresAt > 0 && s.now().Unix() >= c.RefreshExpiresAt {
		return errors.New("the refresh token expired; reconnect GitHub")
	}
	r, err := g.postForm(ctx, "/login/oauth/access_token", url.Values{
		"client_id":     {g.Config.ClientID},
		"client_secret": {g.Config.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {string(rt)},
	})
	if err != nil {
		return err
	}
	if r.Error != "" {
		return r.err()
	}
	if r.AccessToken == "" {
		return errors.New("github: no access token in the refresh response")
	}
	now := s.now().Unix()
	ct, err := s.Box.Seal([]byte(r.AccessToken), []byte(c.ID))
	if err != nil {
		return err
	}
	rct, expiresAt, refreshExp := []byte{}, int64(0), int64(0)
	if r.ExpiresIn > 0 {
		expiresAt = now + r.ExpiresIn
	}
	if r.RefreshToken != "" {
		if rct, err = s.Box.Seal([]byte(r.RefreshToken), refreshAAD(c.ID)); err != nil {
			return err
		}
		if r.RefreshTokenExpiresIn > 0 {
			refreshExp = now + r.RefreshTokenExpiresIn
		}
	}
	n, err := s.Store.RefreshGitCredential(ctx, db.RefreshGitCredentialParams{
		Ciphertext: ct, RefreshCiphertext: rct, ExpiresAt: expiresAt, RefreshExpiresAt: refreshExp,
		UpdatedAt: max(now, c.UpdatedAt+1), ID: c.ID, PrevUpdatedAt: c.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		// Replaced or deleted meanwhile (the user reconnected): the new
		// credential wins; GitHub already rotated this one's refresh token.
		return nil
	}
	s.invalidate()
	return nil
}

// Run refreshes connected GitHub tokens until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.GitHub == nil || !s.GitHub.Config.Enabled() || s.GitHub.Config.ClientSecret == "" {
		return
	}
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		s.RefreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
