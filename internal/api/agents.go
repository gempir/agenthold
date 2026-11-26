package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// GetSandboxLimits implements gen.StrictServerInterface.
func (a *API) GetSandboxLimits(context.Context, gen.GetSandboxLimitsRequestObject) (gen.GetSandboxLimitsResponseObject, error) {
	l := a.Limits
	return gen.GetSandboxLimits200JSONResponse{
		Driver: l.Driver, DefaultCpus: float32(l.DefaultCPUs),
		DefaultMemoryMb: l.DefaultMemoryMB, MaxCpus: float32(l.MaxCPUs), MaxMemoryMb: l.MaxMemoryMB,
		MinMemoryMb: config.MinMemoryMB,
	}, nil
}

func (a *API) apiAgent(ctx context.Context, ag db.Agent, sb db.Sandbox, role authz.Role, infos []node.Info) (gen.Agent, error) {
	owner, err := a.Store.GetUser(ctx, ag.OwnerID)
	if err != nil {
		return gen.Agent{}, err
	}
	login, err := a.agentLogin(ctx, ag)
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
	imgMeta := a.machineImageMeta(ctx, ag)
	result := gen.Agent{
		LastActivityAt: lastActivity, SleepsAt: sleepsAt,
		Id: ag.ID, Name: ag.Name, Owner: apiUser(owner),
		ImageId: &imgMeta.id, ImageName: &imgMeta.name, ImageRevision: &imgMeta.revision, ImageUpdateAvailable: &imgMeta.updateAvailable,
		Harness: ag.Harness, Cpus: float32(ag.Cpus), MemoryMb: ag.MemoryMb, IdleMinutes: int(ag.IdleMinutes),
		State: gen.SandboxState(sb.State), StateError: sb.Error, StateChangedAt: ts(sb.StateChangedAt),
		NeedsRebuild: a.Sandboxes.NeedsRebuild(ag, sb), Role: gen.AgentRole(role),
		Model: ag.Model, ModelOptions: session.ParseOptions(ag.ModelOptions), PermissionPolicy: gen.AgentPermissionPolicy(ag.PermissionPolicy),
		ExecutorEnabled: ag.ExecutorEnabled != 0, ExecutorApprovalMode: gen.ExecutorApprovalMode(executor.NormalizeApprovalMode(ag.ExecutorApprovalMode)),
		CredentialsInSandbox: ag.CredentialsInSandbox == 1, Login: login, IsTemplate: ag.IsTemplate == 1,
		CreatedAt: ts(ag.CreatedAt), UpdatedAt: ts(ag.UpdatedAt),
	}
	if ag.ClonedFrom != "" && role == authz.Owner {
		result.ClonedFrom = &ag.ClonedFrom
	}
	info := nodeByID(infos, sb.NodeID)
	result.NodeId, result.NodeName = &info.ID, &info.Name
	if !info.Connected {
		result.State = gen.SandboxStateUnreachable
		result.LastActivityAt = nil
		result.SleepsAt = nil
	}
	return result, nil
}

// agentSettings validates and normalizes agent settings.
type agentSettings struct {
	name        string
	cpus        float64
	memoryMB    int64
	idleMinutes int64
	harness     string
	model       string
	policy      string
}

func (a *API) validateSettings(ctx context.Context, userID, existingHarness string, s *agentSettings) error {
	var err error
	if s.name, err = validName(s.name); err != nil {
		return err
	}
	if s.cpus < 1 || s.cpus > a.Limits.MaxCPUs {
		return badRequest(fmt.Sprintf("cpus must be between 1 and %g", a.Limits.MaxCPUs))
	}
	if s.memoryMB < config.MinMemoryMB || s.memoryMB > a.Limits.MaxMemoryMB {
		return badRequest(fmt.Sprintf("memoryMb must be between %d and %d", config.MinMemoryMB, a.Limits.MaxMemoryMB))
	}
	if s.idleMinutes < 1 || s.idleMinutes > 10080 {
		return badRequest("idleMinutes must be between 1 and 10080")
	}
	if s.harness == "" {
		s.harness = harness.Default
	}
	var ok bool
	if existingHarness != "" && s.harness == existingHarness {
		_, ok, err = a.resolveAdapter(ctx, s.harness)
	} else {
		ok, err = a.ownerHasHarness(ctx, userID, s.harness)
	}
	if err != nil {
		return err
	}
	if !ok {
		return badRequest("unknown harness")
	}
	s.model = strings.TrimSpace(s.model)
	if !validModelID(s.model) {
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
	rows, err := a.Store.ListUserAgents(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	want := gen.False
	if req.Params.Template != nil {
		want = *req.Params.Template
	}
	if !want.Valid() {
		return nil, badRequest("template must be true, false or all")
	}
	out := gen.ListAgents200JSONResponse{}
	for _, r := range rows {
		if r.Agent.Kind == apps.KindApp {
			continue // listed at /apps
		}
		// Machines that are not templates, unless templates were asked for.
		switch t := r.Agent.IsTemplate == 1; {
		case want == gen.All:
		case want == gen.True && !t, want == gen.False && t:
			continue
		}
		role := authz.RoleOf(u.ID, r.Agent.OwnerID, true) // listed: owned or shared
		ag, err := a.apiAgent(ctx, r.Agent, r.Sandbox, role, infos)
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
	b := req.Body
	s := agentSettings{name: b.Name, cpus: a.Limits.DefaultCPUs, memoryMB: a.Limits.DefaultMemoryMB, idleMinutes: 15}
	if b.Cpus != nil && *b.Cpus != 0 {
		s.cpus = float64(*b.Cpus)
	}
	if b.MemoryMb != nil && *b.MemoryMb != 0 {
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
	execEnabled := false
	if b.ExecutorEnabled != nil {
		execEnabled = *b.ExecutorEnabled
	}
	execMode := executor.ApprovalModel
	if b.ExecutorApprovalMode != nil {
		execMode = string(*b.ExecutorApprovalMode)
		if !executor.ValidApprovalMode(execMode) {
			return nil, badRequest("executorApprovalMode must be model, automatic or browser")
		}
	}
	if err := a.validateSettings(ctx, u.ID, "", &s); err != nil {
		return nil, err
	}
	var imageID string
	var imageRev int64
	var allowedArches []string
	if a.Images != nil {
		reqImg := ""
		if b.ImageId != nil {
			reqImg = *b.ImageId
		}
		var err error
		imageID, imageRev, allowedArches, err = a.Images.ResolveForNewMachine(ctx, reqImg)
		if err != nil {
			return nil, mapImageErr(err)
		}
	} else if b.ImageId != nil && strings.TrimSpace(*b.ImageId) != "" && strings.TrimSpace(*b.ImageId) != "base" {
		return nil, badRequest("custom images are not configured on this server")
	}
	// Choosing the node is for admins, like seeing nodes at all.
	placeReq := sandbox.PlaceRequest{AllowedArches: allowedArches}
	if b.NodeId != nil && strings.TrimSpace(*b.NodeId) != "" {
		if err := a.requireAdmin(ctx); err != nil {
			return nil, err
		}
		placeReq.NodeID = strings.TrimSpace(*b.NodeId)
		placeReq.MemoryMB = s.memoryMB
	}
	// Placement queries the store and the nodes, so it happens before the transaction.
	place, err := a.Sandboxes.PlaceNew(ctx, placeReq)
	if err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	agentID := id.New("ag")
	var provision func()
	defer a.agentLock(u.ID)()
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := a.checkAgentQuota(ctx, q, *u); err != nil {
			return err
		}
		// New agents use their owner's account logins (shown on the agent).
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, OwnerID: u.ID, Name: s.name, Image: "base",
			Harness: s.harness, Model: s.model, Cpus: s.cpus, MemoryMb: s.memoryMB, IdleMinutes: s.idleMinutes,
			PermissionPolicy: s.policy, LoginUserID: u.ID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if execEnabled || execMode != executor.ApprovalModel {
			if err := q.SetAgentExecutor(ctx, db.SetAgentExecutorParams{
				ExecutorEnabled: b2i(execEnabled), ExecutorApprovalMode: execMode, UpdatedAt: now, ID: agentID,
			}); err != nil {
				return err
			}
		}
		if imageID != "" {
			if err := q.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: imageID, ImageRevision: imageRev, UpdatedAt: now, ID: agentID}); err != nil {
				return err
			}
		}
		var err error
		provision, err = a.Sandboxes.ProvisionAt(ctx, q, agentID, place)
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
	a.record(ctx, row.Agent, audit.AgentCreate, row.Agent.Name, map[string]any{"harness": s.harness})
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := a.apiAgent(ctx, row.Agent, row.Sandbox, authz.Owner, infos)
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
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := a.apiAgent(ctx, acc.Row.Agent, acc.Row.Sandbox, acc.Role, infos)
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
		name: ag.Name, cpus: ag.Cpus, memoryMB: ag.MemoryMb, idleMinutes: ag.IdleMinutes,
		harness: ag.Harness, model: ag.Model, policy: ag.PermissionPolicy,
	}
	b := req.Body
	if b.Harness != nil {
		s.harness = *b.Harness
	}
	if b.Model != nil {
		s.model = *b.Model
	} else if b.Harness != nil && *b.Harness != ag.Harness {
		s.model = "" // another harness's model id means nothing to this one
	}
	options := session.ParseOptions(ag.ModelOptions)
	if b.ModelOptions != nil {
		options = *b.ModelOptions
	} else if b.Harness != nil && *b.Harness != ag.Harness {
		options = map[string]string{}
	}
	if b.PermissionPolicy != nil {
		s.policy = string(*b.PermissionPolicy)
	}
	if b.Name != nil {
		s.name = *b.Name
	}
	if b.Cpus != nil && *b.Cpus != 0 {
		s.cpus = float64(*b.Cpus)
	}
	if b.MemoryMb != nil && *b.MemoryMb != 0 {
		s.memoryMB = *b.MemoryMb
	}
	if b.IdleMinutes != nil {
		s.idleMinutes = int64(*b.IdleMinutes)
	}
	execEnabled := ag.ExecutorEnabled != 0
	execMode := executor.NormalizeApprovalMode(ag.ExecutorApprovalMode)
	execEnabledSet := false
	execModeSet := false
	if b.ExecutorEnabled != nil {
		execEnabled = *b.ExecutorEnabled
		execEnabledSet = true
	}
	if b.ExecutorApprovalMode != nil {
		mode := string(*b.ExecutorApprovalMode)
		if !executor.ValidApprovalMode(mode) {
			return nil, badRequest("executorApprovalMode must be model, automatic or browser")
		}
		execMode = mode
		execModeSet = true
	}
	execChanged := (execEnabledSet && execEnabled != (ag.ExecutorEnabled != 0)) ||
		(execModeSet && execMode != executor.NormalizeApprovalMode(ag.ExecutorApprovalMode))
	if err := a.validateSettings(ctx, me(ctx).ID, ag.Harness, &s); err != nil {
		return nil, err
	}
	newImageID, newImageRev, imageChanged, err := a.resolveMachineImagePatch(ctx, ag, b.ImageId, b.ImageRevision)
	if err != nil {
		return nil, err
	}
	harnessChanged := s.harness != ag.Harness
	choice := session.Choice{Model: s.model, Options: options}
	modelChanged := harnessChanged || s.model != ag.Model || session.EncodeOptions(options) != session.EncodeOptions(session.ParseOptions(ag.ModelOptions))
	if modelChanged && !harnessChanged {
		if err := a.validateChoice(ctx, ag, choice, session.AgentChoice(ag), true); err != nil {
			return nil, err
		}
	} else if harnessChanged && s.model != "" {
		target := ag
		target.Harness = s.harness
		if err := a.validateChoice(ctx, target, choice, session.Choice{}, true); err != nil {
			return nil, err
		}
	}
	if harnessChanged && a.Sessions.Busy(ag.ID) {
		// Stopping the harness would end the turns in progress.
		return nil, errStatus(http.StatusConflict, "a conversation of this agent is still running: let it finish or stop it before switching the harness")
	}
	// Everything is validated before anything changes: the template change
	// has preconditions of its own, and stopping the machine is the only step
	// with an effect outside the transaction below.
	toTemplate := b.IsTemplate != nil && *b.IsTemplate && ag.IsTemplate == 0
	toAgent := b.IsTemplate != nil && !*b.IsTemplate && ag.IsTemplate == 1
	if toTemplate || toAgent {
		if err := a.checkTemplateChange(acc.Row, toTemplate); err != nil {
			return nil, err
		}
	}
	if toTemplate && sandbox.Phase(acc.Row.Sandbox.State) != sandbox.PhaseSleeping {
		if err := a.Sandboxes.Stop(ctx, ag.ID); err != nil {
			return nil, err
		}
	}
	// A running harness switches its sessions to a new default at their next
	// turn; one that offers no model choice only reads it when it starts.
	restart := modelChanged && !harnessChanged && !a.Sessions.LiveSwitchable(ag.ID)
	now := a.Now().Unix()
	if err := a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateAgent(ctx, db.UpdateAgentParams{
			Name: s.name, Image: ag.Image, Cpus: s.cpus, MemoryMb: s.memoryMB, IdleMinutes: s.idleMinutes,
			UpdatedAt: now, ID: ag.ID,
		}); err != nil {
			return err
		}
		if imageChanged {
			if err := q.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: newImageID, ImageRevision: newImageRev, UpdatedAt: now, ID: ag.ID}); err != nil {
				return err
			}
		}
		if err := q.SetPermissionPolicy(ctx, db.SetPermissionPolicyParams{PermissionPolicy: s.policy, UpdatedAt: now, ID: ag.ID}); err != nil {
			return err
		}
		if execEnabledSet {
			if err := q.SetAgentExecutorEnabled(ctx, db.SetAgentExecutorEnabledParams{
				ExecutorEnabled: b2i(execEnabled), UpdatedAt: now, ID: ag.ID,
			}); err != nil {
				return err
			}
		}
		if execModeSet {
			if err := q.SetAgentExecutorApprovalMode(ctx, db.SetAgentExecutorApprovalModeParams{
				ExecutorApprovalMode: execMode, UpdatedAt: now, ID: ag.ID,
			}); err != nil {
				return err
			}
		}
		switch {
		case harnessChanged:
			if err := q.SetAgentHarness(ctx, db.SetAgentHarnessParams{Harness: s.harness, Model: s.model, ModelOptions: session.EncodeOptions(options), UpdatedAt: now, ID: ag.ID}); err != nil {
				return err
			}
			if err := q.ResetSessionModels(ctx, ag.ID); err != nil {
				return err
			}
		case modelChanged:
			if err := q.SetAgentModel(ctx, db.SetAgentModelParams{Model: s.model, ModelOptions: session.EncodeOptions(options), UpdatedAt: now, ID: ag.ID}); err != nil {
				return err
			}
		}
		if toTemplate || toAgent {
			if err := q.SetAgentTemplate(ctx, db.SetAgentTemplateParams{IsTemplate: b2i(toTemplate), UpdatedAt: now, ID: ag.ID}); err != nil {
				return err
			}
		}
		if toTemplate {
			// A template is never prompted: its triggers would only fail. They
			// stay off when it becomes an agent again; the owner turns on what
			// still makes sense.
			if err := q.DisableAgentSchedules(ctx, db.DisableAgentSchedulesParams{UpdatedAt: now, AgentID: ag.ID}); err != nil {
				return err
			}
			return q.DisableAgentWebhooks(ctx, db.DisableAgentWebhooksParams{UpdatedAt: now, AgentID: ag.ID})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if harnessChanged {
		a.Sessions.Disconnect(ag.ID) // the next prompt starts the new harness
		a.Egress.Invalidate()        // and the proxy injects that harness's login
	} else if restart {
		a.Sessions.Restart(ag.ID)
	}
	if toTemplate || toAgent {
		convertedTo := "agent"
		if toTemplate {
			convertedTo = "template"
		}
		a.record(ctx, ag, audit.AgentUpdate, ag.Name, map[string]any{"convertedTo": convertedTo})
	}
	if execChanged {
		if execEnabledSet && !execEnabled && a.Executor != nil {
			a.Executor.CancelAgentStreams(ag.ID)
		}
		a.record(ctx, ag, audit.ExecutorAgentUpdate, ag.Name, map[string]any{
			"executorEnabled": execEnabled, "executorApprovalMode": execMode,
		})
	}
	g, err := a.reloadAgent(ctx, ag.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.AgentUpdate, ag.Name, map[string]any{
		"name": s.name, "cpus": s.cpus, "memoryMb": s.memoryMB, "idleMinutes": s.idleMinutes,
		"harness": s.harness, "model": s.model, "modelOptions": options, "permissionPolicy": s.policy,
		"executorEnabled": execEnabled, "executorApprovalMode": execMode,
	})
	return gen.UpdateAgent200JSONResponse(g), nil
}

func (a *API) reloadAgent(ctx context.Context, agentID string, role authz.Role) (gen.Agent, error) {
	row, err := a.Store.GetAgentWithSandbox(ctx, agentID)
	if err != nil {
		return gen.Agent{}, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return gen.Agent{}, err
	}
	return a.apiAgent(ctx, row.Agent, row.Sandbox, role, infos)
}

// DeleteAgent implements gen.StrictServerInterface.
func (a *API) DeleteAgent(ctx context.Context, req gen.DeleteAgentRequestObject) (gen.DeleteAgentResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentDelete)
	if err != nil {
		return nil, err
	}
	if err := a.deleteMachine(ctx, acc.Row.Agent); err != nil {
		return nil, err
	}
	if a.Executor != nil {
		a.Executor.CancelAgentStreams(acc.Row.Agent.ID)
	}
	a.record(ctx, acc.Row.Agent, audit.AgentDelete, acc.Row.Agent.Name, nil)
	return gen.DeleteAgent202Response{}, nil
}

func (a *API) lifecycle(ctx context.Context, agentID string, need authz.Action, action string, op func(context.Context, string) error) (gen.Agent, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, agentID, need)
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
	g, err := a.lifecycle(ctx, req.AgentId, authz.AgentLifecycle, audit.AgentStart, a.startChecked)
	if err != nil {
		return nil, err
	}
	return gen.StartAgent202JSONResponse(g), nil
}

// StopAgent implements gen.StrictServerInterface.
func (a *API) StopAgent(ctx context.Context, req gen.StopAgentRequestObject) (gen.StopAgentResponseObject, error) {
	g, err := a.lifecycle(ctx, req.AgentId, authz.AgentLifecycle, audit.AgentStop, a.Sandboxes.Stop)
	if err != nil {
		return nil, err
	}
	return gen.StopAgent202JSONResponse(g), nil
}

// RebuildAgent implements gen.StrictServerInterface.
func (a *API) RebuildAgent(ctx context.Context, req gen.RebuildAgentRequestObject) (gen.RebuildAgentResponseObject, error) {
	g, err := a.lifecycle(ctx, req.AgentId, authz.AgentRebuild, audit.AgentRebuild, a.Sandboxes.Rebuild)
	if err != nil {
		return nil, err
	}
	return gen.RebuildAgent202JSONResponse(g), nil
}

// errMemoryTooSmall is the error for an agent whose stored memory predates the
// floor: such a guest is OOM-killed, so it is not booted.
func errMemoryTooSmall(mb int64) error {
	return errStatus(http.StatusConflict, fmt.Sprintf(
		"this agent has only %d MB of memory, too little to run a harness; raise it to at least %d MB in the agent's settings, then start it",
		mb, config.MinMemoryMB))
}

// checkMemory refuses to boot an agent whose memory is below the floor.
func (a *API) checkMemory(ctx context.Context, agentID string) error {
	ag, err := a.Store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if ag.MemoryMb < config.MinMemoryMB {
		return errMemoryTooSmall(ag.MemoryMb)
	}
	return nil
}

func (a *API) startChecked(ctx context.Context, agentID string) error {
	if err := a.checkMemory(ctx, agentID); err != nil {
		return err
	}
	return a.Sandboxes.Start(ctx, agentID)
}

// checkTemplateChange says whether a machine may be converted now. Only
// machines at rest are: a turn in progress would be killed, a machine being
// created or copied is busy.
func (a *API) checkTemplateChange(row db.GetAgentWithSandboxRow, toTemplate bool) error {
	phase := sandbox.Phase(row.Sandbox.State)
	if !toTemplate {
		if phase == sandbox.PhaseStopping || phase == sandbox.PhaseCreating {
			// A clone of it may be copying its disks, or it is still being created.
			return errStatus(http.StatusConflict, "the template is busy; try again in a moment")
		}
		return nil
	}
	if a.Sessions.AgentBusy(row.Agent.ID) {
		return errStatus(http.StatusConflict, "the machine is working on a prompt; wait for it to finish before making it a template, because it is stopped first")
	}
	switch phase {
	case sandbox.PhaseSleeping:
	case sandbox.PhaseRunning, sandbox.PhaseError:
		if row.Sandbox.DriverID == "" {
			return errStatus(http.StatusConflict, "the machine has no sandbox to make a template of")
		}
	default:
		return errStatus(http.StatusConflict, "the machine is busy; try again when it is running or asleep")
	}
	return nil
}

// CloneAgent implements gen.StrictServerInterface.
func (a *API) CloneAgent(ctx context.Context, req gen.CloneAgentRequestObject) (gen.CloneAgentResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentClone)
	if err != nil {
		return nil, err
	}
	src := acc.Row.Agent
	name, err := validName(req.Body.Name)
	if err != nil {
		return nil, err
	}
	// Cloning is machine-level: an app's clone is an app (see the spec).
	isApp := src.Kind == apps.KindApp
	if isApp {
		if !apps.ValidAppName(name) {
			return nil, badRequest("an app name is 1 to 40 lowercase letters, digits and hyphens, starting and ending with a letter or digit")
		}
		defer a.Apps.OwnerLock(u.ID)()
	} else {
		defer a.agentLock(u.ID)()
		// Said before the checks that need the machine; checked again with the insert.
		if err := a.checkAgentQuota(ctx, a.Store.Queries, *u); err != nil {
			return nil, err
		}
	}
	if err := a.Sandboxes.Reachable(ctx, src.ID); err != nil {
		return nil, err
	}
	if a.Sessions.AgentBusy(src.ID) {
		return nil, errStatus(http.StatusConflict, "the machine is working on a prompt; wait for it to finish or stop it first, because it is stopped while it is copied")
	}
	if err := a.Sandboxes.CheckCloneable(ctx, src.ID); err != nil {
		return nil, err
	}
	// A clone lives on its source's node and has its resources. Like
	// placement, which only considers nodes that can report their capacity,
	// do not start a copy on a node that cannot.
	if err := a.nodeCanHost(ctx, src.ID); err != nil {
		return nil, err
	}
	// Cloning a template that failed to create has nothing to copy: ProvisionClone says so.
	asTemplate := req.Body.AsTemplate != nil && *req.Body.AsTemplate
	// Only the owner clones, so the clone's account logins are the owner's.
	loginUser := ""
	if src.LoginUserID == u.ID {
		loginUser = u.ID
	}
	now := a.Now().Unix()
	dstID := id.New("ag")
	var provision func()
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if isApp {
			if err := a.Apps.CheckOwnerLimit(ctx, q, u.ID); err != nil {
				return err
			}
		} else if err := a.checkAgentQuota(ctx, q, *u); err != nil {
			return err
		}
		if err := q.CreateClonedAgent(ctx, db.CreateClonedAgentParams{
			ID: dstID, OwnerID: u.ID, Name: name, Image: src.Image, Harness: src.Harness, Model: src.Model,
			Cpus: src.Cpus, MemoryMb: src.MemoryMb, IdleMinutes: src.IdleMinutes, PermissionPolicy: src.PermissionPolicy,
			// The home disk is copied, so a login completed inside the sandbox goes with it.
			CredentialsInSandbox: src.CredentialsInSandbox, LoginUserID: loginUser,
			IsTemplate: b2i(asTemplate), ClonedFrom: src.ID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := q.SetAgentModel(ctx, db.SetAgentModelParams{Model: src.Model, ModelOptions: src.ModelOptions, UpdatedAt: now, ID: dstID}); err != nil {
			return err
		}
		if err := q.SetAgentExecutor(ctx, db.SetAgentExecutorParams{
			ExecutorEnabled: src.ExecutorEnabled, ExecutorApprovalMode: executor.NormalizeApprovalMode(src.ExecutorApprovalMode), UpdatedAt: now, ID: dstID,
		}); err != nil {
			return err
		}
		if src.ImageID != "" {
			if err := q.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: src.ImageID, ImageRevision: src.ImageRevision, UpdatedAt: now, ID: dstID}); err != nil {
				return err
			}
		}
		if isApp {
			if err := q.MakeAppClone(ctx, dstID); err != nil {
				if store.IsUniqueViolation(err) {
					return apps.ErrNameTaken
				}
				return err
			}
		}
		if err := copyAttachments(ctx, q, src.ID, dstID, now); err != nil {
			return err
		}
		// Its services come along (they act on the copied disks); its
		// exposures do not: URLs are not cloned.
		if err := q.CopyServices(ctx, db.CopyServicesParams{DstID: dstID, Now: now, SrcID: src.ID}); err != nil {
			return err
		}
		var err error
		provision, err = a.Sandboxes.ProvisionClone(ctx, q, src.ID, dstID)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.Egress.Invalidate() // the clone's secrets are proxied like its source's
	provision()
	row, err := a.Store.GetAgentWithSandbox(ctx, dstID)
	if err != nil {
		return nil, err
	}
	a.record(ctx, src, audit.AgentClone, row.Agent.Name, map[string]any{"cloneId": dstID, "asTemplate": asTemplate})
	a.record(ctx, row.Agent, audit.AgentCreate, row.Agent.Name, map[string]any{"harness": row.Agent.Harness, "clonedFrom": src.ID})
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out, err := a.apiAgent(ctx, row.Agent, row.Sandbox, authz.Owner, infos)
	if err != nil {
		return nil, err
	}
	return gen.CloneAgent201JSONResponse(out), nil
}

// copyAttachments gives dst the library MCP servers, skills and secrets
// attached to src (attachments only: the secrets stay the owner's) and its
// own MCP servers.
func copyAttachments(ctx context.Context, q *db.Queries, src, dst string, now int64) error {
	secrets, err := q.ListAgentSecrets(ctx, src)
	if err != nil {
		return err
	}
	for _, s := range secrets {
		if err := q.AttachSecret(ctx, db.AttachSecretParams{AgentID: dst, SecretID: s.Secret.ID, EnvVar: s.EnvVar, CreatedAt: now}); err != nil {
			return err
		}
	}
	servers, err := q.ListAgentMCPServers(ctx, src)
	if err != nil {
		return err
	}
	for _, s := range servers {
		if s.AgentID == "" {
			if err := q.AttachMCPServer(ctx, db.AttachMCPServerParams{AgentID: dst, ServerID: s.ID}); err != nil {
				return err
			}
			continue
		}
		if err := q.CreateMCPServer(ctx, db.CreateMCPServerParams{
			ID: id.New("mcp"), OwnerID: s.OwnerID, AgentID: dst, Name: s.Name, Transport: s.Transport, Command: s.Command,
			Args: s.Args, Env: s.Env, Url: s.Url, Headers: s.Headers, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}
	skills, err := q.ListAgentSkills(ctx, src)
	if err != nil {
		return err
	}
	for _, s := range skills {
		if err := q.AttachSkill(ctx, db.AttachSkillParams{AgentID: dst, SkillID: s.ID}); err != nil {
			return err
		}
	}
	return nil
}

// nodeCanHost applies to a clone what placement applies to a new agent: its
// node has to answer a capacity query.
func (a *API) nodeCanHost(ctx context.Context, agentID string) error {
	var err error
	if n, ok := a.Node.(interface {
		AgentCapacity(context.Context, string) (node.Capacity, error)
	}); ok {
		_, err = n.AgentCapacity(ctx, agentID)
	} else {
		_, err = a.Node.Capacity(ctx)
	}
	if errors.Is(err, sandbox.ErrUnreachable) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: its node could not report its capacity: %w", sandbox.ErrNoCapacity, err)
	}
	return nil
}
