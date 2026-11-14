package api

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/store/db"
)

// GetEgressCA implements gen.StrictServerInterface.
func (a *API) GetEgressCA(context.Context, gen.GetEgressCARequestObject) (gen.GetEgressCAResponseObject, error) {
	pem := a.CA.CertPEM()
	return gen.GetEgressCA200ApplicationxPemFileResponse{Body: bytes.NewReader(pem), ContentLength: int64(len(pem))}, nil
}

func apiPolicy(p egress.Policy) gen.NetworkPolicy {
	allow := append([]string{}, p.Allow...)
	return gen.NetworkPolicy{Mode: gen.NetworkPolicyMode(p.Mode), Allow: allow}
}

// GetNetworkPolicy implements gen.StrictServerInterface.
func (a *API) GetNetworkPolicy(ctx context.Context, req gen.GetNetworkPolicyRequestObject) (gen.GetNetworkPolicyResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	return gen.GetNetworkPolicy200JSONResponse(apiPolicy(egress.ParsePolicy(acc.Row.Agent.NetworkPolicy))), nil
}

// SetNetworkPolicy implements gen.StrictServerInterface.
func (a *API) SetNetworkPolicy(ctx context.Context, req gen.SetNetworkPolicyRequestObject) (gen.SetNetworkPolicyResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure); err != nil {
		return nil, err
	}
	p := egress.Policy{Mode: string(req.Body.Mode)}
	for _, e := range req.Body.Allow {
		if e = trimSpace(e); e != "" {
			p.Allow = append(p.Allow, e)
		}
	}
	if err := p.Validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	j, _ := json.Marshal(p)
	if err := a.Store.SetNetworkPolicy(ctx, db.SetNetworkPolicyParams{NetworkPolicy: string(j), UpdatedAt: a.Now().Unix(), ID: req.AgentId}); err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	return gen.SetNetworkPolicy200JSONResponse(apiPolicy(p)), nil
}

// ListEgressLog implements gen.StrictServerInterface.
func (a *API) ListEgressLog(ctx context.Context, req gen.ListEgressLogRequestObject) (gen.ListEgressLogResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
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
	rows, err := a.Store.ListEgressEvents(ctx, db.ListEgressEventsParams{AgentID: req.AgentId, ID: before, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := gen.ListEgressLog200JSONResponse{}
	for _, r := range rows {
		out = append(out, gen.EgressEvent{
			Id: r.ID, At: time.UnixMilli(r.At).UTC(), Kind: gen.EgressEventKind(r.Kind), Host: r.Host, Ip: r.Ip,
			Port: int(r.Port), Method: r.Method, Path: r.Path, Status: int(r.Status), Allowed: r.Allowed == 1,
			Reason: r.Reason, Injected: r.Injected == 1, BytesUp: r.BytesUp, BytesDown: r.BytesDown, DurationMs: r.DurationMs,
		})
	}
	return out, nil
}
