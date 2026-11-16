package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store/db"
)

func (a *API) apiExposure(e db.Exposure) gen.Exposure {
	return gen.Exposure{
		Id: e.ID, AgentId: e.AgentID, Port: int(e.Port), Name: e.Name, Access: gen.ExposureAccess(e.Access),
		Url: a.Preview.URL(e), CreatedBy: e.CreatedBy, CreatedAt: ts(e.CreatedAt),
	}
}

// exposureError maps preview errors to API errors.
func exposureError(err error) error {
	switch {
	case errors.Is(err, preview.ErrDisabled):
		return errStatus(http.StatusConflict, err.Error())
	case errors.Is(err, preview.ErrNotFound):
		return errNotFound
	case errors.Is(err, preview.ErrNameTaken), errors.Is(err, preview.ErrPortTaken):
		return errStatus(http.StatusConflict, err.Error())
	}
	var invalid *preview.InvalidError
	if errors.As(err, &invalid) {
		return badRequest(invalid.Msg)
	}
	return err
}

// ListExposures implements gen.StrictServerInterface.
func (a *API) ListExposures(ctx context.Context, req gen.ListExposuresRequestObject) (gen.ListExposuresResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	out := gen.ListExposures200JSONResponse{Enabled: a.Preview.Enabled(), HostSuffix: a.Preview.Hosts.Suffix, Exposures: []gen.Exposure{}}
	if !out.Enabled {
		return out, nil
	}
	rows, err := a.Preview.List(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	for _, e := range rows {
		out.Exposures = append(out.Exposures, a.apiExposure(e))
	}
	return out, nil
}

// CreateExposure implements gen.StrictServerInterface.
func (a *API) CreateExposure(ctx context.Context, req gen.CreateExposureRequestObject) (gen.CreateExposureResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentExpose)
	if err != nil {
		return nil, err
	}
	p := preview.CreateParams{Agent: acc.Row.Agent, Port: req.Body.Port, CreatedBy: me(ctx).ID}
	if req.Body.Name != nil {
		p.Name = *req.Body.Name
	}
	if req.Body.Access != nil {
		p.Access = preview.Access(*req.Body.Access)
	}
	e, err := a.Preview.Create(ctx, p)
	if err != nil {
		return nil, exposureError(err)
	}
	a.record(ctx, acc.Row.Agent, audit.ExposeAdd, e.ID, map[string]any{"port": e.Port, "name": e.Name, "access": e.Access})
	return gen.CreateExposure201JSONResponse(a.apiExposure(e)), nil
}

// UpdateExposure implements gen.StrictServerInterface.
func (a *API) UpdateExposure(ctx context.Context, req gen.UpdateExposureRequestObject) (gen.UpdateExposureResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentExpose)
	if err != nil {
		return nil, err
	}
	e, err := a.Preview.Get(ctx, req.AgentId, req.ExposureId)
	if err != nil {
		return nil, exposureError(err)
	}
	var name string
	var access preview.Access
	if req.Body.Name != nil {
		name = *req.Body.Name
		if name == "" {
			return nil, badRequest("name must not be empty")
		}
	}
	if req.Body.Access != nil {
		access = preview.Access(*req.Body.Access)
	}
	e, err = a.Preview.Update(ctx, e, name, access)
	if err != nil {
		return nil, exposureError(err)
	}
	a.record(ctx, acc.Row.Agent, audit.ExposeUpdate, e.ID, map[string]any{"port": e.Port, "name": e.Name, "access": e.Access})
	return gen.UpdateExposure200JSONResponse(a.apiExposure(e)), nil
}

// DeleteExposure implements gen.StrictServerInterface.
func (a *API) DeleteExposure(ctx context.Context, req gen.DeleteExposureRequestObject) (gen.DeleteExposureResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentExpose)
	if err != nil {
		return nil, err
	}
	e, err := a.Preview.Get(ctx, req.AgentId, req.ExposureId)
	if err != nil {
		return nil, exposureError(err)
	}
	if err := a.Preview.Delete(ctx, e); err != nil {
		return nil, exposureError(err)
	}
	a.record(ctx, acc.Row.Agent, audit.ExposeRemove, e.ID, map[string]any{"port": e.Port, "name": e.Name})
	return gen.DeleteExposure204Response{}, nil
}
