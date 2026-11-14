package auth

import (
	"context"
	"net/http"

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

// Middleware identifies the user from the session cookie, if any. It never
// rejects requests; handlers decide what needs a user.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			if u, err := s.SessionUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}
