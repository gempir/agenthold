package api

import (
	"context"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// settledBy maps settled_by to the API's coarse reason.
func settledBy(by string) gen.SessionSettledBy {
	switch {
	case by == session.SettledByAuto:
		return gen.SessionSettledByAuto
	case strings.HasPrefix(by, "agent:"):
		return gen.SessionSettledByAgent
	}
	return gen.SessionSettledByUser
}

// SettleSession implements gen.StrictServerInterface.
func (a *API) SettleSession(ctx context.Context, req gen.SettleSessionRequestObject) (gen.SettleSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	s, changed, err := session.Settle(ctx, a.Store, s.ID, me(ctx).ID, time.Now())
	if err != nil {
		return nil, err
	}
	if changed {
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionSettle, s.ID, map[string]any{"session": s.Title})
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.SettleSession200JSONResponse(g), nil
}

// UnsettleSession implements gen.StrictServerInterface.
func (a *API) UnsettleSession(ctx context.Context, req gen.UnsettleSessionRequestObject) (gen.UnsettleSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	s, changed, err := session.Unsettle(ctx, a.Store, s.ID, time.Now())
	if err != nil {
		return nil, err
	}
	if changed {
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionUnsettle, s.ID, map[string]any{"session": s.Title})
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.UnsettleSession200JSONResponse(g), nil
}

func apiSettings(u db.User) gen.UserSettings {
	return gen.UserSettings{ThreadAutoSettleDays: int(u.ThreadAutoSettleDays)}
}

// GetMySettings implements gen.StrictServerInterface.
func (a *API) GetMySettings(ctx context.Context, _ gen.GetMySettingsRequestObject) (gen.GetMySettingsResponseObject, error) {
	u, err := a.Store.GetUser(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	return gen.GetMySettings200JSONResponse(apiSettings(u)), nil
}

// UpdateMySettings implements gen.StrictServerInterface.
func (a *API) UpdateMySettings(ctx context.Context, req gen.UpdateMySettingsRequestObject) (gen.UpdateMySettingsResponseObject, error) {
	id := me(ctx).ID
	if d := req.Body.ThreadAutoSettleDays; d != nil {
		if *d < 0 || *d > session.MaxAutoSettleDays {
			return nil, badRequest("threadAutoSettleDays must be 0 (never) to 365")
		}
		if err := a.Store.SetUserThreadAutoSettleDays(ctx, db.SetUserThreadAutoSettleDaysParams{
			ThreadAutoSettleDays: int64(*d), UpdatedAt: time.Now().Unix(), ID: id,
		}); err != nil {
			return nil, err
		}
	}
	u, err := a.Store.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.UpdateMySettings200JSONResponse(apiSettings(u)), nil
}
