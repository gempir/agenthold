package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

func apiServiceStatus(st tunnel.ServiceState) *gen.ServiceStatus {
	out := &gen.ServiceStatus{State: gen.ServiceStatusState(st.State), Restarts: st.Restarts, ExitCode: st.ExitCode}
	if st.Pid != 0 {
		out.Pid = &st.Pid
	}
	if st.Signal != "" {
		out.Signal = &st.Signal
	}
	if st.Error != "" {
		out.Error = &st.Error
	}
	if st.StartedAt != 0 {
		t := ts(st.StartedAt)
		out.StartedAt = &t
	}
	return out
}

func apiService(r db.Service, live map[string]tunnel.ServiceState) gen.Service {
	s := apps.SpecOf(r)
	out := gen.Service{
		Name: s.Name, Command: s.Command, Workdir: s.Workdir, Env: s.Env, Enabled: s.Enabled,
		CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt),
	}
	if st, ok := live[s.Name]; ok {
		out.Status = apiServiceStatus(st)
	}
	return out
}

func statesByName(sts []tunnel.ServiceState) map[string]tunnel.ServiceState {
	out := make(map[string]tunnel.ServiceState, len(sts))
	for _, st := range sts {
		out[st.Name] = st
	}
	return out
}

// service returns one stored service with the given live states (or fresh ones).
func (a *API) serviceOut(ctx context.Context, agentID, name string, live []tunnel.ServiceState) (gen.Service, error) {
	r, err := a.Store.GetService(ctx, db.GetServiceParams{AgentID: agentID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return gen.Service{}, apps.ErrNotFound
	}
	if err != nil {
		return gen.Service{}, err
	}
	states := statesByName(live)
	if live == nil {
		states, _, _ = a.Apps.Status(ctx, agentID)
	}
	return apiService(r, states), nil
}

// ListServices implements gen.StrictServerInterface.
func (a *API) ListServices(ctx context.Context, req gen.ListServicesRequestObject) (gen.ListServicesResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Apps.List(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListServices200JSONResponse{Services: []gen.Service{}}
	live, running, err := a.Apps.Status(ctx, req.AgentId)
	out.Running = running
	if err != nil {
		msg := err.Error()
		out.Error = &msg
	}
	for _, r := range rows {
		out.Services = append(out.Services, apiService(r, live))
	}
	return out, nil
}

// GetService implements gen.StrictServerInterface.
func (a *API) GetService(ctx context.Context, req gen.GetServiceRequestObject) (gen.GetServiceResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	s, err := a.serviceOut(ctx, req.AgentId, req.ServiceName, nil)
	if err != nil {
		return nil, err
	}
	return gen.GetService200JSONResponse(s), nil
}

// PutService implements gen.StrictServerInterface.
func (a *API) PutService(ctx context.Context, req gen.PutServiceRequestObject) (gen.PutServiceResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	spec := apps.Spec{Name: req.ServiceName, Command: req.Body.Command, Enabled: true}
	if req.Body.Workdir != nil {
		spec.Workdir = *req.Body.Workdir
	}
	if req.Body.Env != nil {
		spec.Env = *req.Body.Env
	}
	if req.Body.Enabled != nil {
		spec.Enabled = *req.Body.Enabled
	}
	_, live, err := a.Apps.Put(ctx, req.AgentId, spec)
	var applyErr *apps.ApplyError
	if err != nil && !errors.As(err, &applyErr) {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.ServicePut, spec.Name, map[string]any{"command": spec.Command, "workdir": spec.Workdir, "enabled": spec.Enabled})
	if err != nil {
		// Saved; the running machine refused it (an old sandboxd).
		return nil, err
	}
	s, err := a.serviceOut(ctx, req.AgentId, spec.Name, live)
	if err != nil {
		return nil, err
	}
	return gen.PutService200JSONResponse(s), nil
}

// DeleteService implements gen.StrictServerInterface.
func (a *API) DeleteService(ctx context.Context, req gen.DeleteServiceRequestObject) (gen.DeleteServiceResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	err = a.Apps.Delete(ctx, req.AgentId, req.ServiceName)
	var applyErr *apps.ApplyError
	if err != nil && !errors.As(err, &applyErr) {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.ServiceDelete, req.ServiceName, nil)
	if err != nil {
		return nil, err
	}
	return gen.DeleteService204Response{}, nil
}

func (a *API) serviceAction(ctx context.Context, agentID, name, action string, op func() ([]tunnel.ServiceState, error)) (gen.Service, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, agentID, authz.AgentConfigure)
	if err != nil {
		return gen.Service{}, err
	}
	live, err := op()
	if err != nil {
		return gen.Service{}, err
	}
	a.record(ctx, acc.Row.Agent, action, name, nil)
	return a.serviceOut(ctx, agentID, name, live)
}

// StartService implements gen.StrictServerInterface.
func (a *API) StartService(ctx context.Context, req gen.StartServiceRequestObject) (gen.StartServiceResponseObject, error) {
	s, err := a.serviceAction(ctx, req.AgentId, req.ServiceName, audit.ServiceStart, func() ([]tunnel.ServiceState, error) {
		return a.Apps.SetEnabled(ctx, req.AgentId, req.ServiceName, true)
	})
	if err != nil {
		return nil, err
	}
	return gen.StartService200JSONResponse(s), nil
}

// StopService implements gen.StrictServerInterface.
func (a *API) StopService(ctx context.Context, req gen.StopServiceRequestObject) (gen.StopServiceResponseObject, error) {
	s, err := a.serviceAction(ctx, req.AgentId, req.ServiceName, audit.ServiceStop, func() ([]tunnel.ServiceState, error) {
		return a.Apps.SetEnabled(ctx, req.AgentId, req.ServiceName, false)
	})
	if err != nil {
		return nil, err
	}
	return gen.StopService200JSONResponse(s), nil
}

// RestartService implements gen.StrictServerInterface.
func (a *API) RestartService(ctx context.Context, req gen.RestartServiceRequestObject) (gen.RestartServiceResponseObject, error) {
	s, err := a.serviceAction(ctx, req.AgentId, req.ServiceName, audit.ServiceRestart, func() ([]tunnel.ServiceState, error) {
		return a.Apps.Restart(ctx, req.AgentId, req.ServiceName)
	})
	if err != nil {
		return nil, err
	}
	return gen.RestartService200JSONResponse(s), nil
}

// GetServiceLogs implements gen.StrictServerInterface.
func (a *API) GetServiceLogs(ctx context.Context, req gen.GetServiceLogsRequestObject) (gen.GetServiceLogsResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	lines := 200
	if req.Params.Lines != nil {
		lines = *req.Params.Lines
	}
	if lines < 1 || lines > 10000 {
		return nil, badRequest("lines must be between 1 and 10000")
	}
	var buf bytes.Buffer
	if err := a.Apps.Logs(ctx, req.AgentId, req.ServiceName, lines, false, &buf); err != nil {
		return nil, err
	}
	return gen.GetServiceLogs200TextResponse(buf.String()), nil
}

// streamServiceLogs follows a service's log over a WebSocket.
func (a *API) streamServiceLogs(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeError(w, r, errUnauthorized)
		return
	}
	agentID, name := chi.URLParam(r, "agentId"), chi.URLParam(r, "serviceName")
	if _, err := a.authz.Agent(r.Context(), u.ID, agentID, authz.AgentRead); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := a.Apps.Get(r.Context(), agentID, name); err != nil {
		writeError(w, r, err)
		return
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 || lines > 10000 {
		lines = 200
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx := ws.CloseRead(r.Context())
	defer a.guardSocket(ctx, u.ID, ws)()
	err = a.Apps.Logs(ctx, agentID, name, lines, true, wsText{ctx: ctx, ws: ws})
	if err != nil && ctx.Err() == nil {
		_, msg := toHTTP(err)
		wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = wsjson.Write(wctx, ws, map[string]string{"type": "error", "message": msg})
		cancel()
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
}

type wsText struct {
	ctx context.Context
	ws  *websocket.Conn
}

func (w wsText) Write(p []byte) (int, error) {
	if err := w.ws.Write(w.ctx, websocket.MessageText, bytes.ToValidUTF8(p, []byte("�"))); err != nil {
		return 0, err
	}
	return len(p), nil
}
