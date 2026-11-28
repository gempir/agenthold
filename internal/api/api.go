// Package api implements the HTTP API described in api/openapi.yaml.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/acpstore"
	"github.com/gempir/agenthold/internal/activity"

	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/idle"
	"github.com/gempir/agenthold/internal/images"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/project"
	"github.com/gempir/agenthold/internal/push"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/trigger"
	"github.com/gempir/agenthold/internal/usage"
	"github.com/gempir/agenthold/internal/workflows"
)

// Deps are the services the API needs.
type Deps struct {
	Push      *push.Service
	Store     *store.Store
	Auth      *auth.Service
	PublicURL string
	Sandboxes *sandbox.Manager
	Node      node.Node
	// Nodes manages edge nodes; nil when the server has only an in-process node.
	Nodes    NodeAdmin
	Box      *secrets.Box
	Egress   *egress.Directory
	Sessions *session.Manager
	Idle     *idle.Sleeper
	Audit    *audit.Log
	Triggers *trigger.Runner
	Logins   *logins.Service
	// Git keeps users' git settings; nil disables the account endpoints.
	Git *git.Service
	// Projects works with projects on agents' machines; nil disables them.
	Projects *project.Service
	// PullRequests is the pull request hub; nil disables its endpoints.
	PullRequests *pr.Service
	// Executor manages per-user Executor instances for accounts and tools.
	Executor *executor.Manager
	// Apps manages services and apps.
	Apps *apps.Manager
	// Images manages custom enterprise VM images and builds.
	Images *images.Manager
	// Preview serves exposures; it is never nil (disabled without a host suffix).
	Preview *preview.Service
	// Identity signs X-Agenthold-Identity tokens; nil when previews are off.
	Identity *preview.Signer
	// Stats reads a running machine's live stats for the admin overview. Nil
	// asks the machine's sandboxd through its node.
	Stats StatsFunc
	// Slack is the Slack integration; nil disables its endpoints.
	Slack Slack
	// Activity is the live per-agent activity log (nil: always empty).
	Activity *activity.Log
	// ACPStore searches and installs agents from the official ACP Registry.
	ACPStore *acpstore.Catalog
	// Harnesses resolves built-in and user-added custom harnesses.
	Harnesses *acpstore.Resolver
	// Usage aggregates per-turn token and cost analytics.
	Usage *usage.Service
	// Workflows runs always-on durable multi-stage pipelines.
	Workflows *workflows.Engine
	// AntigravityLoginCmd overrides the command launched by StartHarnessAuth (tests).
	AntigravityLoginCmd []string
	// ShowTestHarness lists the scripted test harness (development).
	ShowTestHarness bool
	// UserMaxAgents is auth.user_max_agents: how many agents a user with the
	// User role may own. Nil means no limit.
	UserMaxAgents *int
	Org           config.Org
	Limits        config.Sandbox
	Now           func() time.Time
}

// API implements gen.StrictServerInterface.
type API struct {
	Deps
	authz authz.Checker
	usage *usageCache
	wakes
	agentLocks keyedLocks
	authFlows  *harnessAuthManager
	execGate   *executorGate
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
	if d.Preview == nil {
		d.Preview = &preview.Service{Store: d.Store}
	}
	if d.ACPStore == nil {
		d.ACPStore = acpstore.NewCatalog("")
	}
	if d.Harnesses == nil {
		d.Harnesses = &acpstore.Resolver{Store: d.Store, Catalog: d.ACPStore}
	}
	a := &API{
		Deps:      d,
		authz:     authz.Checker{Q: d.Store.Queries},
		wakes:     wakes{pending: map[string]*wakeCall{}, visitorWake: map[string]time.Time{}, slots: map[string]chan struct{}{}},
		authFlows: newHarnessAuthManager(),
		execGate:  newExecutorGate(),
	}
	a.usage = newUsageCache(a.machineStats, time.Now)
	return a
}

// resolveAdapter resolves a built-in or custom harness adapter by id.
func (a *API) resolveAdapter(ctx context.Context, harnessID string) (*harness.Adapter, bool, error) {
	if a.Harnesses == nil {
		ad, ok := harness.Get(harnessID)
		if !ok || (ad.Hidden && !a.ShowTestHarness) {
			return nil, false, nil
		}
		return ad, true, nil
	}
	ad, ok, err := a.Harnesses.AdapterFor(ctx, harnessID)
	if err != nil || !ok {
		return nil, false, err
	}
	if ad.Hidden && !a.ShowTestHarness {
		return nil, false, nil
	}
	return ad, true, nil
}

// Mount registers all /api/v1 routes on r.
func (a *API) Mount(r chi.Router) {
	r.Get("/api/docs", apiDocs)
	r.Get("/.well-known/agenthold/jwks.json", a.jwks)
	r.Route("/api/v1", func(r chi.Router) {
		// Public endpoints that must not interpret Authorization headers.
		r.Post("/hooks/{token}", a.deliver)
		r.Get("/guest/{token}", a.getGuestThread)
		r.Post("/guest/{token}/requests", a.createGuestRequest)
		r.Get("/guest/{token}/stream", a.streamGuestThread)
		r.Post("/workflow-hooks/{token}", a.deliverWorkflowHook)
		r.Get("/openapi.yaml", openAPI)
		r.Group(func(r chi.Router) { a.mountAuthenticated(r) })
	})
}

func (a *API) mountAuthenticated(r chi.Router) {
	{
		r.Use(a.Auth.Middleware, blockDisabled, limitBodies)
		r.Get("/auth/login/{provider}", a.login)
		r.Get("/auth/callback/{provider}", a.callback)
		r.Post("/auth/local", a.localLogin)
		r.Post("/auth/logout", a.logout)
		r.Get("/preview/authorize", a.authorizePreview)
		r.Post("/preview/authorize", a.authorizePreview)
		r.Get("/executor/authorize", a.authorizeExecutor)
		r.Get("/agents/{agentId}/terminal", a.terminal)
		r.Get("/agents/{agentId}/services/{serviceName}/logs/stream", a.streamServiceLogs)
		r.Get("/sessions/{sessionId}/stream", a.stream)

		strict := gen.NewStrictHandlerWithOptions(a, []gen.StrictMiddlewareFunc{a.machineKinds, requireUser}, gen.StrictHTTPServerOptions{
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
	}
}

func requireUser(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if public[operationID] {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		u := auth.UserFrom(ctx)
		if u == nil {
			return nil, errUnauthorized
		}
		// Also checked here, not only in blockDisabled, for callers that
		// reach the handlers another way.
		if !allowedForDisabled[operationID] && auth.Role(*u) == config.RoleDisabled {
			return nil, errDisabled
		}
		return f(ctx, w, r, req)
	}
}

// allowedForDisabled are the operations a Disabled user may still call:
// who am I (the web shows the "no access" page from it) and the public ones.
var allowedForDisabled = map[string]bool{"GetMe": true, "GetVersion": true, "ListAuthProviders": true}

var errDisabled = errStatus(http.StatusForbidden, authz.ErrDisabledMessage)

// blockDisabled answers every request of a Disabled user with 403, except
// GET /me, signing in and out, and the public endpoints. The user row is
// read for every request, so a downgrade applies at once.
func blockDisabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := auth.UserFrom(r.Context()); u != nil && auth.Role(*u) == config.RoleDisabled {
			p := strings.TrimPrefix(r.URL.Path, "/api/v1")
			ok := strings.HasPrefix(p, "/auth/") || (r.Method == http.MethodGet && (p == "/me" || p == "/version"))
			if !ok {
				writeError(w, r, errDisabled)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func me(ctx context.Context) *db.User { return auth.UserFrom(ctx) }

func ts(unix int64) time.Time { return time.Unix(unix, 0).UTC() }

func apiUser(u db.User) gen.User {
	return gen.User{Id: u.ID, Email: u.Email, Name: u.Name, AvatarUrl: u.AvatarUrl}
}

// limitBodies caps request bodies. File uploads have their own, larger limit.
func limitBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upload := r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/files/content")
		if r.Body != nil && !upload {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		}
		next.ServeHTTP(w, r)
	})
}
