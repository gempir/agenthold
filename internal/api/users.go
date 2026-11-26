package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

// maxAgents is the agent quota of a role: nil for no limit.
func (a *API) maxAgents(role config.Role) *int {
	if role == config.RoleAdmin || a.UserMaxAgents == nil {
		return nil
	}
	n := *a.UserMaxAgents
	return &n
}

// checkAgentQuota refuses another agent past auth.user_max_agents. It runs
// in the creating transaction, under agentLock, so parallel creates by one
// user cannot both pass.
func (a *API) checkAgentQuota(ctx context.Context, q *db.Queries, u db.User) error {
	limit := a.maxAgents(auth.Role(u))
	if limit == nil {
		return nil
	}
	// Other API processes (PostgreSQL) wait here until this transaction ends.
	if err := q.LockUser(ctx, u.ID); err != nil {
		return err
	}
	n, err := q.CountOwnedAgents(ctx, u.ID)
	if err != nil {
		return err
	}
	if n >= int64(*limit) {
		if *limit == 0 {
			return errStatus(http.StatusForbidden, "your account cannot create agents; ask an admin")
		}
		return errStatus(http.StatusForbidden, fmt.Sprintf("you already have %d of %d agents (templates and clones count); delete one or ask an admin for more", n, *limit))
	}
	return nil
}

// keyedLocks is one mutex per key, created on demand.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// agentLock serializes agent creation per owner; it returns the unlock.
func (a *API) agentLock(ownerID string) func() {
	k := &a.agentLocks
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	m := k.locks[ownerID]
	if m == nil {
		m = &sync.Mutex{}
		k.locks[ownerID] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func apiAdminUser(r db.ListUsersWithIdentityRow) gen.AdminUser {
	u := db.User{
		ID: r.ID, Email: r.Email, EmailVerified: r.EmailVerified, Name: r.Name, AvatarUrl: r.AvatarUrl,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Role: r.Role, RoleOverride: r.RoleOverride,
	}
	configRole, ok := config.ParseRole(r.Role)
	if !ok {
		configRole = config.RoleDisabled
	}
	out := gen.AdminUser{
		User: apiUser(u), Role: gen.Role(auth.Role(u)), ConfigRole: gen.Role(configRole),
		Provider: r.Provider, Username: r.Username, Agents: int(r.Agents),
		Local: r.Provider == config.LocalProviderID, CreatedAt: ts(r.CreatedAt),
	}
	if o, ok := config.ParseRole(r.RoleOverride); ok {
		role := gen.Role(o)
		out.Override = &role
	}
	if r.LastLoginAt > 0 {
		t := ts(r.LastLoginAt)
		out.LastLoginAt = &t
	}
	return out
}

// ListAdminUsers implements gen.StrictServerInterface.
func (a *API) ListAdminUsers(ctx context.Context, _ gen.ListAdminUsersRequestObject) (gen.ListAdminUsersResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListUsersWithIdentity(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListAdminUsers200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiAdminUser(r))
	}
	return out, nil
}

// SetUserRoleOverride implements gen.StrictServerInterface.
func (a *API) SetUserRoleOverride(ctx context.Context, req gen.SetUserRoleOverrideRequestObject) (gen.SetUserRoleOverrideResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	override := ""
	if req.Body.Override != nil {
		r, ok := config.ParseRole(string(*req.Body.Override))
		if !ok {
			return nil, badRequest("override: want admin, user, disabled or null")
		}
		override = string(r)
	}
	if req.UserId == me(ctx).ID {
		return nil, errStatus(http.StatusConflict, "you cannot change your own role; ask another admin")
	}
	if _, err := a.Store.GetUser(ctx, req.UserId); errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	} else if err != nil {
		return nil, err
	}
	local, err := a.Auth.IsLocalAdmin(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	if local {
		return nil, errStatus(http.StatusConflict, "the local admin (auth.local) is always an admin")
	}
	if err := a.Store.SetUserRoleOverride(ctx, db.SetUserRoleOverrideParams{RoleOverride: override, UpdatedAt: a.Now().Unix(), ID: req.UserId}); err != nil {
		return nil, err
	}
	if override == string(config.RoleDisabled) {
		if a.execGate != nil {
			a.execGate.endUserSessions(req.UserId)
		}
		if a.Executor != nil {
			a.Executor.CancelUserStreams(req.UserId)
			_ = a.Executor.Stop(req.UserId)
		}
	}
	slog.InfoContext(ctx, "user role override set", "user", req.UserId, "override", override, "by", me(ctx).ID)
	rows, err := a.Store.ListUsersWithIdentity(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == req.UserId {
			return gen.SetUserRoleOverride200JSONResponse(apiAdminUser(r)), nil
		}
	}
	return nil, errNotFound
}
