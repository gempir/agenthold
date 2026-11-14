package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/store/db"
)

type ctxKey struct{}

// WithUser returns ctx carrying user.
func WithUser(ctx context.Context, u *db.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
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
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
			return
		}
		if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			if u, err := s.SessionUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}
