package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// GetSandboxLimits implements gen.StrictServerInterface.
func (a *API) GetSandboxLimits(context.Context, gen.GetSandboxLimitsRequestObject) (gen.GetSandboxLimitsResponseObject, error) {
	l := a.Limits
	return gen.GetSandboxLimits200JSONResponse{
		Driver: l.Driver, DefaultImage: l.DefaultImage, DefaultCpus: float32(l.DefaultCPUs),
		DefaultMemoryMb: l.DefaultMemoryMB, MaxCpus: float32(l.MaxCPUs), MaxMemoryMb: l.MaxMemoryMB,
	}, nil
}

func (a *API) apiAgent(ctx context.Context, ag db.Agent, sb db.Sandbox, role authz.AgentRole) (gen.Agent, error) {
	owner, err := a.Store.GetUser(ctx, ag.OwnerID)
	if err != nil {
		return gen.Agent{}, err
	}
	var lastActivity, sleepsAt *time.Time
	if a.Idle != nil && sandbox.Phase(sb.State) == sandbox.PhaseRunning {
		if l := a.Idle.Tracker.Last(ag.ID); !l.IsZero() {
			lastActivity = &l
		}
		if s := a.Idle.SleepsAt(ag.ID, ag.IdleMinutes); !s.IsZero() {
			sleepsAt = &s
		}
	}
	return gen.Agent{
		LastActivityAt: lastActivity, SleepsAt: sleepsAt,
		Id: ag.ID, WorkspaceId: ag.WorkspaceID, Name: ag.Name, Owner: apiUser(owner), Image: ag.Image,
		Harness: ag.Harness, Cpus: float32(ag.Cpus), MemoryMb: ag.MemoryMb, IdleMinutes: int(ag.IdleMinutes),
		State: gen.SandboxState(sb.State), StateError: sb.Error, StateChangedAt: ts(sb.StateChangedAt),
		NeedsRebuild: a.Sandboxes.NeedsRebuild(ag, sb), Role: gen.AgentRole(role),
		Model: ag.Model, PermissionPolicy: gen.AgentPermissionPolicy(ag.PermissionPolicy),
		CredentialsInSandbox: ag.CredentialsInSandbox == 1,
		CreatedAt:            ts(ag.CreatedAt), UpdatedAt: ts(ag.UpdatedAt),
	}, nil
}

// agentSettings validates and normalizes agent settings.
type agentSettings struct {
	name, image string
	cpus        float64
	memoryMB    int64
	idleMinutes int64
	harness     string
	model       string
	policy      string
}

func (a *API) validateSettings(s *agentSettings) error {
	var err error
	if s.name, err = validName(s.name); err != nil {
		return err
	}
	s.image = strings.TrimSpace(s.image)
	if s.image == "" {
		s.image = a.Limits.DefaultImage
	}
	if strings.ContainsAny(s.image, " \t\n") || len(s.image) > 255 {
		return badRequest("image must be an OCI image reference")
	}
	if s.cpus <= 0 || s.cpus > a.Limits.MaxCPUs {
		return badRequest(fmt.Sprintf("cpus must be greater than 0 and at most %g", a.Limits.MaxCPUs))
	}
	if s.memoryMB < 64 || s.memoryMB > a.Limits.MaxMemoryMB {
		return badRequest(fmt.Sprintf("memoryMb must be between 64 and %d", a.Limits.MaxMemoryMB))
	}
	if s.idleMinutes < 1 || s.idleMinutes > 10080 {
		return badRequest("idleMinutes must be between 1 and 10080")
	}
	if s.harness == "" {
		s.harness = "claude-code"
	}
	if _, ok := harness.Get(s.harness); !ok {
		return badRequest("unknown harness")
	}
	s.model = strings.TrimSpace(s.model)
	if len(s.model) > 100 || strings.ContainsAny(s.model, " \t\n") {
		return badRequest("invalid model")
	}
	if s.policy == "" {
		s.policy = session.PolicyAutoApprove
	}
	if s.policy != session.PolicyAutoApprove && s.policy != session.PolicyAsk {
		return badRequest("permissionPolicy must be auto-approve or ask")
	}
	return nil
}

// ListAgents implements gen.StrictServerInterface.
func (a *API) ListAgents(ctx context.Context, req gen.ListAgentsRequestObject) (gen.ListAgentsResponseObject, error) {
	u := me(ctx)
	if _, err := a.authz.Workspace(ctx, u.ID, req.WorkspaceId, authz.WorkspaceListAll); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListWorkspaceAgents(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	out := gen.ListAgents200JSONResponse{}
	for _, r := range rows {
		_, role, err := a.authz.AgentRoleOf(ctx, u.ID, r.Agent)
		if err != nil {
			return nil, err
		}
		if !authz.CanAgent(role, authz.AgentRead) {
			continue
		}
		ag, err := a.apiAgent(ctx, r.Agent, r.Sandbox, role)
		if err != nil {
			return nil, err
		}
		out = append(out, ag)
	}
	return out, nil
}

// CreateAgent implements gen.StrictServerInterface.
func (a *API) CreateAgent(ctx context.Context, req gen.CreateAgentRequestObject) (gen.CreateAgentResponseObject, error) {
	u := me(ctx)
	if _, err := a.authz.Workspace(ctx, u.ID, req.WorkspaceId, authz.AgentCreate); err != nil {
		return nil, err
	}
	b := req.Body
	s := agentSettings{name: b.Name, cpus: a.Limits.DefaultCPUs, memoryMB: a.Limits.DefaultMemoryMB, idleMinutes: 15}
	if b.Image != nil {
		s.image = *b.Image
	}
	if b.Cpus != nil {
		s.cpus = float64(*b.Cpus)
	}
	if b.MemoryMb != nil {
		s.memoryMB = *b.MemoryMb
	}
	if b.IdleMinutes != nil {
		s.idleMinutes = int64(*b.IdleMinutes)
	}
	if b.Harness != nil {
		s.harness = *b.Harness
	}
	if b.Model != nil {
		s.model = *b.Model
	}
	if b.PermissionPolicy != nil {
		s.policy = string(*b.PermissionPolicy)
	}
	if err := a.validateSettings(&s); err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	agentID := id.New("ag")
	var provision func()
	err := a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, WorkspaceID: req.WorkspaceId, OwnerID: u.ID, Name: s.name, Image: s.image,
			Harness: s.harness, Model: s.model, Cpus: s.cpus, MemoryMb: s.memoryMB, IdleMinutes: s.idleMinutes,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := q.SetPermissionPolicy(ctx, db.SetPermissionPolicyParams{PermissionPolicy: s.policy, UpdatedAt: now, ID: agentID}); err != nil {
			return err
		}
		var err error
		provision, err = a.Sandboxes.Provision(ctx, q, agentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	provision()
	row, err := a.Store.GetAgentWithSandbox(ctx, agentID)
	if err != nil {
		return nil, err
	}
	a.record(ctx, row.Agent, audit.AgentCreate, row.Agent.Name, map[string]any{"image": s.image, "harness": s.harness})
	out, err := a.apiAgent(ctx, row.Agent, row.Sandbox, authz.AgentAdmin)
	if err != nil {
		return nil, err
	}
	return gen.CreateAgent201JSONResponse(out), nil
}

// GetAgent implements gen.StrictServerInterface.
func (a *API) GetAgent(ctx context.Context, req gen.GetAgentRequestObject) (gen.GetAgentResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	out, err := a.apiAgent(ctx, acc.Row.Agent, acc.Row.Sandbox, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.GetAgent200JSONResponse(out), nil
}

// UpdateAgent implements gen.StrictServerInterface.
func (a *API) UpdateAgent(ctx context.Context, req gen.UpdateAgentRequestObject) (gen.UpdateAgentResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	s := agentSettings{
		name: ag.Name, image: ag.Image, cpus: ag.Cpus, memoryMB: ag.MemoryMb, idleMinutes: ag.IdleMinutes,
		harness: ag.Harness, model: ag.Model, policy: ag.PermissionPolicy,
	}
	b := req.Body
	if b.Harness != nil {
		s.harness = *b.Harness
	}
	if b.Model != nil {
		s.model = *b.Model
	}
	if b.PermissionPolicy != nil {
		s.policy = string(*b.PermissionPolicy)
	}
	if b.Name != nil {
		s.name = *b.Name
	}
	if b.Image != nil {
		s.image = *b.Image
	}
	if b.Cpus != nil {
		s.cpus = float64(*b.Cpus)
	}
	if b.MemoryMb != nil {
		s.memoryMB = *b.MemoryMb
	}
	if b.IdleMinutes != nil {
		s.idleMinutes = int64(*b.IdleMinutes)
	}
	if err := a.validateSettings(&s); err != nil {
		return nil, err
	}
	if err := a.Store.UpdateAgent(ctx, db.UpdateAgentParams{
		Name: s.name, Image: s.image, Cpus: s.cpus, MemoryMb: s.memoryMB, IdleMinutes: s.idleMinutes,
		UpdatedAt: a.Now().Unix(), ID: ag.ID,
	}); err != nil {
		return nil, err
	}
	if err := a.Store.SetPermissionPolicy(ctx, db.SetPermissionPolicyParams{PermissionPolicy: s.policy, UpdatedAt: a.Now().Unix(), ID: ag.ID}); err != nil {
		return nil, err
	}
	if s.harness != ag.Harness || s.model != ag.Model {
		if err := a.Store.SetAgentHarness(ctx, db.SetAgentHarnessParams{Harness: s.harness, Model: s.model, UpdatedAt: a.Now().Unix(), ID: ag.ID}); err != nil {
			return nil, err
		}
		a.Sessions.Disconnect(ag.ID) // the next prompt starts the new harness
	}
	g, err := a.reloadAgent(ctx, ag.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.AgentUpdate, ag.Name, map[string]any{
		"name": s.name, "image": s.image, "cpus": s.cpus, "memoryMb": s.memoryMB, "idleMinutes": s.idleMinutes,
		"harness": s.harness, "model": s.model, "permissionPolicy": s.policy,
	})
	return gen.UpdateAgent200JSONResponse(g), nil
}

func (a *API) reloadAgent(ctx context.Context, agentID string, role authz.AgentRole) (gen.Agent, error) {
	row, err := a.Store.GetAgentWithSandbox(ctx, agentID)
	if err != nil {
		return gen.Agent{}, err
	}
	return a.apiAgent(ctx, row.Agent, row.Sandbox, role)
}

// DeleteAgent implements gen.StrictServerInterface.
func (a *API) DeleteAgent(ctx context.Context, req gen.DeleteAgentRequestObject) (gen.DeleteAgentResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentDelete)
	if err != nil {
		return nil, err
	}
	if err := a.Sandboxes.Destroy(ctx, req.AgentId); err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.AgentDelete, acc.Row.Agent.Name, nil)
	return gen.DeleteAgent202Response{}, nil
}

func (a *API) lifecycle(ctx context.Context, agentID, action string, op func(context.Context, string) error) (gen.Agent, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, agentID, authz.AgentLifecycle)
	if err != nil {
		return gen.Agent{}, err
	}
	if err := op(ctx, agentID); err != nil {
		return gen.Agent{}, err
	}
	a.record(ctx, acc.Row.Agent, action, "", nil)
	return a.reloadAgent(ctx, agentID, acc.Role)
}

// StartAgent implements gen.StrictServerInterface.
func (a *API) StartAgent(ctx context.Context, req gen.StartAgentRequestObject) (gen.StartAgentResponseObject, error) {
	g, err := a.lifecycle(ctx, req.AgentId, audit.AgentStart, a.Sandboxes.Start)
	if err != nil {
		return nil, err
	}
	return gen.StartAgent202JSONResponse(g), nil
}

// StopAgent implements gen.StrictServerInterface.
func (a *API) StopAgent(ctx context.Context, req gen.StopAgentRequestObject) (gen.StopAgentResponseObject, error) {
	g, err := a.lifecycle(ctx, req.AgentId, audit.AgentStop, a.Sandboxes.Stop)
	if err != nil {
		return nil, err
	}
	return gen.StopAgent202JSONResponse(g), nil
}

// RebuildAgent implements gen.StrictServerInterface.
func (a *API) RebuildAgent(ctx context.Context, req gen.RebuildAgentRequestObject) (gen.RebuildAgentResponseObject, error) {
	g, err := a.lifecycle(ctx, req.AgentId, audit.AgentRebuild, a.Sandboxes.Rebuild)
	if err != nil {
		return nil, err
	}
	return gen.RebuildAgent202JSONResponse(g), nil
}
