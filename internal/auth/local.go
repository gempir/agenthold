package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/password"
)

// Local sign-in errors.
var (
	ErrLocalDisabled  = errors.New("local sign-in is not configured")
	ErrBadCredentials = errors.New("wrong username or password")
	ErrRateLimited    = errors.New("too many sign-in attempts, try again in a few minutes")
)

// Limits for password attempts. Every attempt counts (not only failures):
// each one costs an argon2id computation.
const (
	localWindow    = 5 * time.Minute
	localPerClient = 10 // attempts per client address and window
	localGlobal    = 60 // attempts per window in total
)

type localAdmin struct {
	username string
	hash     *password.Hash
	limiter  *limiter
}

func newLocalAdmin(l *config.Local) (*localAdmin, error) {
	h, err := password.Parse(l.PasswordHash)
	if err != nil {
		return nil, err
	}
	return &localAdmin{username: l.Username, hash: h, limiter: newLimiter(localWindow, localPerClient, localGlobal)}, nil
}

// LocalEnabled reports whether the local admin (auth.local) is configured.
func (s *Service) LocalEnabled() bool { return s.local != nil }

// LocalLogin checks the local admin's credentials and returns a new session
// token. client identifies the caller for rate limiting (its address).
func (s *Service) LocalLogin(ctx context.Context, username, pw, client, userAgent string) (string, error) {
	l := s.local
	if l == nil {
		return "", ErrLocalDisabled
	}
	if !l.limiter.allow(client, s.now()) {
		return "", ErrRateLimited
	}
	// Always run the hash so a wrong username takes as long as a wrong password.
	pwOK := l.hash.Verify(pw)
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(l.username)) == 1
	if !pwOK || !userOK {
		return "", ErrBadCredentials
	}
	userID, err := s.upsertUser(ctx, config.LocalProviderID, &Claims{Subject: l.username, Username: l.username, Name: l.username, ProviderRole: config.RoleAdmin}, config.RoleAdmin, true)
	if err != nil {
		return "", err
	}
	return s.CreateSession(ctx, userID, userAgent)
}

// limiter counts attempts in fixed windows, per key and in total.
type limiter struct {
	mu             sync.Mutex
	window         time.Duration
	perKey, global int
	keys           map[string]*window
	all            window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(w time.Duration, perKey, global int) *limiter {
	return &limiter{window: w, perKey: perKey, global: global, keys: map[string]*window{}}
}

func (w *window) take(now time.Time, d time.Duration, limit int) bool {
	if now.Sub(w.start) >= d {
		w.start, w.n = now, 0
	}
	if w.n >= limit {
		return false
	}
	w.n++
	return true
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.keys) > 10000 { // forget old windows, so the map stays small
		for k, w := range l.keys {
			if now.Sub(w.start) >= l.window {
				delete(l.keys, k)
			}
		}
	}
	w := l.keys[key]
	if w == nil {
		w = &window{start: now}
		l.keys[key] = w
	}
	if now.Sub(w.start) < l.window && w.n >= l.perKey {
		return false
	}
	if !l.all.take(now, l.window, l.global) {
		return false
	}
	return w.take(now, l.window, l.perKey)
}
