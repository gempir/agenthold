package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

type ctxKey struct{}

// WithUser returns ctx carrying user.
func WithUser(ctx context.Context, u *db.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

type viaTokenKey struct{}

// ViaToken reports whether the request was authenticated with an API token.
func ViaToken(ctx context.Context) bool {
	v, _ := ctx.Value(viaTokenKey{}).(bool)
	return v
}

// UserFrom returns the authenticated user, or nil.
func UserFrom(ctx context.Context) *db.User {
	u, _ := ctx.Value(ctxKey{}).(*db.User)
	return u
}

// Middleware identifies the user from a bearer token (API clients) or the
// session cookie (browsers). It never rejects requests; handlers decide what
// needs a user. An invalid bearer token is answered with 401 right away.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			u, err := s.TokenUser(r.Context(), strings.TrimSpace(token))
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("WWW-Authenticate", `Bearer realm="agenthold"`)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid or expired token"}` + "\n"))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(WithUser(r.Context(), u), viaTokenKey{}, true)))
			return
		}
		cookies := r.CookiesNamed(s.SessionCookieName())
		if len(cookies) > 1 {
			// Only a planted cookie makes a second one: refuse to guess
			// which is real, and clear what can be cleared.
			for _, c := range s.clearPlanted(r.URL.Path) {
				http.SetCookie(w, c)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"conflicting session cookies; sign in again"}` + "\n"))
			return
		}
		if len(cookies) == 1 && cookies[0].Value != "" {
			if u, err := s.SessionUser(r.Context(), cookies[0].Value); err == nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

type viaKey struct{}

// WithVia marks ctx as acting through an integration (such as "slack"),
// which audit entries record.
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// Via returns the integration ctx acts through, or "".
func Via(ctx context.Context) string {
	v, _ := ctx.Value(viaKey{}).(string)
	return v
}

// UserContext returns ctx carrying the user with userID, for services that
// call the API handlers in-process on a user's behalf (the Slack app). The
// handlers then check that user's permissions as for a request of theirs.
func (s *Service) UserContext(ctx context.Context, userID string) (context.Context, *db.User, error) {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return ctx, nil, err
	}
	if Role(u) == config.RoleDisabled {
		return ctx, nil, ErrDisabled
	}
	return WithUser(ctx, &u), &u, nil
}
