// Package api implements the HTTP API described in api/openapi.yaml.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/idle"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Deps are the services the API needs.
type Deps struct {
	Store     *store.Store
	Auth      *auth.Service
	PublicURL string
	Sandboxes *sandbox.Manager
	Tunnel    *tunnel.Hub
	Box       *secrets.Box
	Egress    *egress.Directory
	CA        *egress.CA
	Sessions  *session.Manager
	Idle      *idle.Sleeper
	// ShowTestHarness lists the scripted test harness (development).
	ShowTestHarness bool
	Limits          config.Sandbox
	Now             func() time.Time
}

// API implements gen.StrictServerInterface.
type API struct {
	Deps
	authz authz.Checker
}

var _ gen.StrictServerInterface = (*API)(nil)

// public operations do not need a signed-in user.
var public = map[string]bool{
	"GetVersion":        true,
	"ListAuthProviders": true,
}

// New builds the API.
func New(d Deps) *API {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &API{Deps: d, authz: authz.Checker{Q: d.Store.Queries}}
}

// Mount registers all /api/v1 routes on r.
func (a *API) Mount(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(a.Auth.Middleware)
		r.Get("/auth/login/{provider}", a.login)
		r.Get("/auth/callback/{provider}", a.callback)
		r.Post("/auth/logout", a.logout)
		r.Get("/agents/{agentId}/terminal", a.terminal)
		r.Get("/sessions/{sessionId}/stream", a.stream)

		strict := gen.NewStrictHandlerWithOptions(a, []gen.StrictMiddlewareFunc{requireUser}, gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeError(w, r, badRequest(err.Error()))
			},
			ResponseErrorHandlerFunc: writeError,
		})
		gen.HandlerWithOptions(strict, gen.ChiServerOptions{
			BaseRouter: r,
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				writeError(w, r, badRequest(err.Error()))
			},
		})
	})
}

func requireUser(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if public[operationID] {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		if auth.UserFrom(ctx) == nil {
			return nil, errUnauthorized
		}
		return f(ctx, w, r, req)
	}
}

func me(ctx context.Context) *db.User { return auth.UserFrom(ctx) }

func ts(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func apiUser(u db.User) gen.User {
	return gen.User{Id: u.ID, Email: u.Email, Name: u.Name, AvatarUrl: u.AvatarUrl}
}
