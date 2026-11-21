package api

import (
	"context"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/authz"
)

// ListAgentActivity implements gen.StrictServerInterface.
func (a *API) ListAgentActivity(ctx context.Context, req gen.ListAgentActivityRequestObject) (gen.ListAgentActivityResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentPrompt); err != nil {
		return nil, err
	}
	var after int64
	if req.Params.After != nil {
		after = *req.Params.After
	}
	out := gen.ListAgentActivity200JSONResponse{}
	for _, e := range a.Activity.Since(req.AgentId, after) {
		out = append(out, gen.ActivityEntry{Seq: e.Seq, At: e.At.UTC(), Level: gen.ActivityEntryLevel(e.Level), Source: e.Source, Message: e.Message})
	}
	return out, nil
}
