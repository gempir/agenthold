package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/store/db"
)

// record writes an audit entry for an agent action by the current user.
func (a *API) record(ctx context.Context, ag db.Agent, action, target string, details map[string]any) {
	if via := auth.Via(ctx); via != "" {
		if details == nil {
			details = map[string]any{}
		}
		details["via"] = via
	}
	a.Audit.Record(ctx, audit.Entry{
		AgentID: ag.ID, UserID: me(ctx).ID,
		Action: action, Target: target, Details: details,
	})
}

// ListShares implements gen.StrictServerInterface.
func (a *API) ListShares(ctx context.Context, req gen.ListSharesRequestObject) (gen.ListSharesResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	owner, err := a.Store.GetUser(ctx, acc.Row.Agent.OwnerID)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentShares(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListShares200JSONResponse{Owner: apiUser(owner), Shares: []gen.Share{}}
	for _, r := range rows {
		out.Shares = append(out.Shares, gen.Share{User: apiUser(r.User), CreatedAt: ts(r.SharedAt)})
	}
	return out, nil
}

// ShareAgent implements gen.StrictServerInterface.
func (a *API) ShareAgent(ctx context.Context, req gen.ShareAgentRequestObject) (gen.ShareAgentResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentShare)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	email := strings.TrimSpace(req.Body.Email)
	if email == "" || len(email) > 320 || !strings.Contains(email, "@") {
		return nil, badRequest("email must be an email address")
	}
	users, err := a.Store.ListUsersByVerifiedEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	switch len(users) {
	case 0:
		return nil, errStatus(http.StatusNotFound, "nobody with this verified email has signed in yet")
	case 1:
	default:
		return nil, errStatus(http.StatusConflict, "several accounts use this email address")
	}
	target := users[0]
	if target.ID == ag.OwnerID {
		return nil, badRequest("you own this agent")
	}
	now := a.Now().Unix()
	if err := a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.ShareAgent(ctx, db.ShareAgentParams{AgentID: ag.ID, UserID: target.ID, CreatedAt: now}); err != nil {
			return err
		}
		// Atomically disable Executor whenever an agent is shared so a concurrent
		// update cannot leave the newly shared agent exposing owner accounts.
		return q.SetAgentExecutorEnabled(ctx, db.SetAgentExecutorEnabledParams{
			ExecutorEnabled: 0, UpdatedAt: now, ID: ag.ID,
		})
	}); err != nil {
		return nil, err
	}
	if a.Executor != nil {
		a.Executor.CancelAgentStreams(ag.ID)
	}
	sh, err := a.Store.GetShare(ctx, db.GetShareParams{AgentID: ag.ID, UserID: target.ID})
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.ShareAdd, target.ID, map[string]any{"userId": target.ID, "email": target.Email})
	return gen.ShareAgent200JSONResponse{User: apiUser(target), CreatedAt: ts(sh.CreatedAt)}, nil
}

// UnshareAgent implements gen.StrictServerInterface.
func (a *API) UnshareAgent(ctx context.Context, req gen.UnshareAgentRequestObject) (gen.UnshareAgentResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	if !authz.CanUnshare(acc.Role, u.ID, req.UserId) {
		return nil, authz.ErrForbidden
	}
	n, err := a.Store.UnshareAgent(ctx, db.UnshareAgentParams{AgentID: req.AgentId, UserID: req.UserId})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.record(ctx, acc.Row.Agent, audit.ShareRemove, req.UserId, map[string]any{"userId": req.UserId})
	return gen.UnshareAgent204Response{}, nil
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
