package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

// record writes an audit entry for an agent action by the current user.
func (a *API) record(ctx context.Context, ag db.Agent, action, target string, details map[string]any) {
	a.Audit.Record(ctx, audit.Entry{
		WorkspaceID: ag.WorkspaceID, AgentID: ag.ID, UserID: me(ctx).ID,
		Action: action, Target: target, Details: details,
	})
}

func (a *API) apiGrant(ctx context.Context, g db.AgentGrant) (gen.Grant, error) {
	out := gen.Grant{Id: g.ID, SubjectType: gen.GrantSubjectType(g.SubjectType), Role: gen.AgentRole(g.Role), CreatedAt: ts(g.CreatedAt)}
	if g.SubjectType == "user" {
		u, err := a.Store.GetUser(ctx, g.SubjectID)
		if err != nil {
			return out, err
		}
		au := apiUser(u)
		out.User = &au
	}
	return out, nil
}

// ListGrants implements gen.StrictServerInterface.
func (a *API) ListGrants(ctx context.Context, req gen.ListGrantsRequestObject) (gen.ListGrantsResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	rows, err := a.Store.ListAgentGrants(ctx, ag.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListGrants200JSONResponse{Grants: []gen.Grant{}, Effective: []gen.AccessEntry{}}
	direct := map[string]bool{}
	wsGrant := false
	for _, g := range rows {
		ag, err := a.apiGrant(ctx, g)
		if err != nil {
			return nil, err
		}
		out.Grants = append(out.Grants, ag)
		if g.SubjectType == "user" {
			direct[g.SubjectID] = true
		} else {
			wsGrant = true
		}
	}
	members, err := a.Store.ListMembers(ctx, ag.WorkspaceID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		_, role, err := a.authz.AgentRoleOf(ctx, m.User.ID, ag)
		if err != nil {
			return nil, err
		}
		if !role.Valid() {
			continue
		}
		via := gen.AccessEntryVia("grant")
		switch {
		case m.User.ID == ag.OwnerID:
			via = "owner"
		case authz.WorkspaceRole(m.Role).AtLeast(authz.WorkspaceAdmin):
			via = "workspace_admin"
		case !direct[m.User.ID] && wsGrant:
			via = "workspace_grant"
		}
		out.Effective = append(out.Effective, gen.AccessEntry{User: apiUser(m.User), Role: gen.AgentRole(role), Via: via})
	}
	return out, nil
}

// SetGrant implements gen.StrictServerInterface.
func (a *API) SetGrant(ctx context.Context, req gen.SetGrantRequestObject) (gen.SetGrantResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentGrants)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	b := req.Body
	if !authz.AgentRole(b.Role).Valid() {
		return nil, badRequest("role must be viewer, operator or admin")
	}
	subjectID := ""
	switch b.SubjectType {
	case "workspace":
	case "user":
		if b.UserId == nil || *b.UserId == "" {
			return nil, badRequest("userId is required")
		}
		role, err := a.authz.WorkspaceRoleOf(ctx, *b.UserId, ag.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if !role.Valid() {
			return nil, badRequest("the user is not a member of this workspace")
		}
		subjectID = *b.UserId
	default:
		return nil, badRequest("subjectType must be user or workspace")
	}
	g := db.AgentGrant{ID: id.New("grt"), AgentID: ag.ID, SubjectType: string(b.SubjectType), SubjectID: subjectID, Role: string(b.Role), CreatedBy: me(ctx).ID, CreatedAt: a.Now().Unix()}
	if err := a.Store.UpsertGrant(ctx, db.UpsertGrantParams(g)); err != nil {
		return nil, err
	}
	// The upsert may have kept an existing row; return what is stored.
	rows, err := a.Store.ListAgentGrants(ctx, ag.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.SubjectType == g.SubjectType && r.SubjectID == g.SubjectID {
			g = r
		}
	}
	a.record(ctx, ag, audit.GrantSet, g.SubjectType+":"+g.SubjectID, map[string]any{"role": g.Role, "subjectType": g.SubjectType, "userId": g.SubjectID})
	out, err := a.apiGrant(ctx, g)
	if err != nil {
		return nil, err
	}
	return gen.SetGrant200JSONResponse(out), nil
}

// DeleteGrant implements gen.StrictServerInterface.
func (a *API) DeleteGrant(ctx context.Context, req gen.DeleteGrantRequestObject) (gen.DeleteGrantResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentGrants)
	if err != nil {
		return nil, err
	}
	g, err := a.Store.GetGrant(ctx, db.GetGrantParams{ID: req.GrantId, AgentID: req.AgentId})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.Store.DeleteGrant(ctx, db.DeleteGrantParams{ID: g.ID, AgentID: g.AgentID}); err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.GrantDelete, g.SubjectType+":"+g.SubjectID, map[string]any{"role": g.Role, "subjectType": g.SubjectType, "userId": g.SubjectID})
	return gen.DeleteGrant204Response{}, nil
}

// ListAgentAudit implements gen.StrictServerInterface.
func (a *API) ListAgentAudit(ctx context.Context, req gen.ListAgentAuditRequestObject) (gen.ListAgentAuditResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentReadAudit); err != nil {
		return nil, err
	}
	before := int64(math.MaxInt64)
	if req.Params.Before != nil {
		before = *req.Params.Before
	}
	limit := 100
	if req.Params.Limit != nil {
		limit = min(max(*req.Params.Limit, 1), 500)
	}
	rows, err := a.Store.ListAgentAudit(ctx, db.ListAgentAuditParams{AgentID: req.AgentId, ID: before, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	users := map[string]gen.User{}
	out := gen.ListAgentAudit200JSONResponse{}
	for _, r := range rows {
		u, ok := users[r.UserID]
		if !ok {
			if du, err := a.Store.GetUser(ctx, r.UserID); err == nil {
				u = apiUser(du)
			} else {
				u = gen.User{Id: r.UserID, Name: "(deleted user)"}
			}
			users[r.UserID] = u
		}
		var details map[string]any
		_ = json.Unmarshal([]byte(r.Details), &details)
		out = append(out, gen.AuditEntry{Id: r.ID, At: time.UnixMilli(r.At).UTC(), User: u, Action: r.Action, Target: r.Target, Details: details})
	}
	return out, nil
}
