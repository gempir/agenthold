package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// CookieName is the session cookie.
const CookieName = "agenthold_session"

const loginStateTTL = 10 * time.Minute

// Errors.
var (
	ErrUnknownProvider = errors.New("unknown provider")
	ErrInvalidState    = errors.New("login expired or invalid, please try again")
	ErrUnauthenticated = errors.New("not signed in")
	ErrNotAllowed      = errors.New("this account is not allowed to sign in")
)

// Service owns providers and sessions.
type Service struct {
	store      *store.Store
	providers  map[string]Provider
	order      []string
	sessionTTL time.Duration
	secure     bool
	now        func() time.Time
	local      *localAdmin
	admins     config.Auth
}

// NewService builds the auth service from configuration.
func NewService(st *store.Store, cfg *config.Config) (*Service, error) {
	s := &Service{
		store:      st,
		providers:  map[string]Provider{},
		sessionTTL: cfg.Auth.SessionTTL,
		admins:     cfg.Auth,
		secure:     strings.HasPrefix(cfg.PublicURL, "https://"),
		now:        time.Now,
	}
	for _, pc := range cfg.Auth.Providers {
		p, err := NewProvider(pc, cfg.PublicURL+"/api/v1/auth/callback/"+url.PathEscape(pc.ID))
		if err != nil {
			return nil, err
		}
		s.AddProvider(p)
	}
	if cfg.Auth.Local != nil {
		l, err := newLocalAdmin(cfg.Auth.Local)
		if err != nil {
			return nil, fmt.Errorf("auth.local: %w", err)
		}
		s.local = l
	}
	return s, nil
}

// IsAdmin reports whether u manages the installation: the configured local
// admin (auth.local) or a user whose verified e-mail is in auth.admins.
func (s *Service) IsAdmin(ctx context.Context, u db.User) (bool, error) {
	if u.EmailVerified == 1 && s.admins.IsAdminEmail(u.Email) {
		return true, nil
	}
	if s.local == nil {
		return false, nil
	}
	n, err := s.store.CountUserIdentities(ctx, db.CountUserIdentitiesParams{UserID: u.ID, Provider: config.LocalProviderID})
	return n > 0, err
}

// AddProvider registers a provider (also used by tests).
func (s *Service) AddProvider(p Provider) {
	id := p.Info().ID
	if _, ok := s.providers[id]; !ok {
		s.order = append(s.order, id)
	}
	s.providers[id] = p
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Providers returns the configured providers in configuration order.
func (s *Service) Providers() []config.Provider {
	out := make([]config.Provider, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.providers[id].Info())
	}
	return out
}

// BeginLogin stores a login state and returns the provider URL to redirect to.
func (s *Service) BeginLogin(ctx context.Context, providerID, redirectTo string) (string, error) {
	p, ok := s.providers[providerID]
	if !ok {
		return "", ErrUnknownProvider
	}
	state := id.Random(24)
	verifier := oauth2.GenerateVerifier()
	nonce := id.Random(16)
	now := s.now()
	_ = s.store.DeleteExpiredLoginStates(ctx, now.Unix())
	if err := s.store.CreateLoginState(ctx, db.CreateLoginStateParams{
		State: state, Provider: providerID, Verifier: verifier, Nonce: nonce,
		RedirectTo: SafeRedirect(redirectTo), ExpiresAt: now.Add(loginStateTTL).Unix(),
	}); err != nil {
		return "", err
	}
	return p.AuthCodeURL(ctx, state, verifier, nonce)
}

// FinishLogin completes the callback: it validates state, exchanges the code,
// finds or creates the user and returns a new session token and where to go.
func (s *Service) FinishLogin(ctx context.Context, providerID, code, state, userAgent string) (token, redirectTo string, err error) {
	p, ok := s.providers[providerID]
	if !ok {
		return "", "", ErrUnknownProvider
	}
	ls, err := s.store.TakeLoginState(ctx, db.TakeLoginStateParams{State: state, ExpiresAt: s.now().Unix()})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ls.Provider != providerID) {
		return "", "", ErrInvalidState
	}
	if err != nil {
		return "", "", err
	}
	claims, err := p.Exchange(ctx, code, ls.Verifier, ls.Nonce)
	if err != nil {
		return "", "", err
	}
	provider := p.Info()
	if provider.Type == config.ProviderGitHub && len(provider.GitHubAllowedIDs) > 0 && !contains(provider.GitHubAllowedIDs, claims.Subject) {
		return "", "", ErrNotAllowed
	}
	if claims.Subject == "" {
		return "", "", errors.New("provider returned no subject")
	}
	userID, err := s.upsertUser(ctx, providerID, claims)
	if err != nil {
		return "", "", err
	}
	token, err = s.CreateSession(ctx, userID, userAgent)
	if err != nil {
		return "", "", err
	}
	return token, ls.RedirectTo, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.TrimSpace(candidate) == value {
			return true
		}
	}
	return false
}

// upsertUser maps (provider, subject) to a user. Users are never linked by
// email across providers: an email address alone does not prove identity.
func (s *Service) upsertUser(ctx context.Context, providerID string, c *Claims) (string, error) {
	now := s.now().Unix()
	var userID string
	err := s.store.Tx(ctx, func(q *db.Queries) error {
		ident, err := q.GetIdentity(ctx, db.GetIdentityParams{Provider: providerID, Subject: c.Subject})
		switch {
		case err == nil:
			userID = ident.UserID
			if err := q.TouchIdentity(ctx, db.TouchIdentityParams{
				Email: c.Email, Username: c.Username, LastLoginAt: now, Provider: providerID, Subject: c.Subject,
			}); err != nil {
				return err
			}
			return q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{
				Email: c.Email, EmailVerified: b2i(c.EmailVerified), Name: displayName(c), AvatarUrl: c.AvatarURL, UpdatedAt: now, ID: userID,
			})
		case errors.Is(err, sql.ErrNoRows):
			userID = id.New("usr")
			if err := q.CreateUser(ctx, db.CreateUserParams{
				ID: userID, Email: c.Email, EmailVerified: b2i(c.EmailVerified), Name: displayName(c), AvatarUrl: c.AvatarURL, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
			return q.CreateIdentity(ctx, db.CreateIdentityParams{
				Provider: providerID, Subject: c.Subject, UserID: userID, Email: c.Email,
				Username: c.Username, CreatedAt: now, LastLoginAt: now,
			})
		default:
			return err
		}
	})
	return userID, err
}

func displayName(c *Claims) string {
	for _, v := range []string{c.Name, c.Username, c.Email} {
		if v != "" {
			return v
		}
	}
	return "user"
}

// CreateSession issues a session token for userID.
func (s *Service) CreateSession(ctx context.Context, userID, userAgent string) (string, error) {
	token := id.Random(32)
	now := s.now()
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	err := s.store.CreateSession(ctx, db.CreateSessionParams{
		TokenHash: HashToken(token), UserID: userID, CreatedAt: now.Unix(),
		ExpiresAt: now.Add(s.sessionTTL).Unix(), UserAgent: userAgent,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// SessionUser returns the user for a session token. Sessions slide: they are
// extended when less than half of their lifetime is left.
func (s *Service) SessionUser(ctx context.Context, token string) (*db.User, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	now := s.now()
	h := HashToken(token)
	row, err := s.store.GetSessionUser(ctx, db.GetSessionUserParams{TokenHash: h, ExpiresAt: now.Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if time.Unix(row.ExpiresAt, 0).Sub(now) < s.sessionTTL/2 {
		_ = s.store.ExtendSession(ctx, db.ExtendSessionParams{ExpiresAt: now.Add(s.sessionTTL).Unix(), TokenHash: h})
	}
	return &row.User, nil
}

// EndSession deletes a session.
func (s *Service) EndSession(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, HashToken(token))
}

// SessionCookie builds the cookie for a token; an empty token clears it.
func (s *Service) SessionCookie(token string) *http.Cookie {
	c := &http.Cookie{
		Name: CookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: s.secure, SameSite: http.SameSiteLaxMode,
	}
	if token == "" {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(s.sessionTTL.Seconds())
	}
	return c
}

// HashToken returns the stored form of a bearer secret.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SafeRedirect only allows local absolute paths, to prevent open redirects.
func SafeRedirect(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return p
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
