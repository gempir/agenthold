package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Operations on /agents/{agentId} that only agents have: an app id is not
// found there (it lives at /apps/{appId}).
var agentOnlyOps = map[string]bool{
	"GetAgent": true, "UpdateAgent": true, "DeleteAgent": true,
	"StartAgent": true, "StopAgent": true, "RebuildAgent": true,
}

// Sub-resources that need a harness: refused for apps.
var harnessOps = map[string]bool{
	"GetHarness": true, "StartHarnessAuth": true, "SubmitHarnessAuth": true, "CancelHarnessAuth": true,
	"ListSessions": true, "CreateSession": true,
	"ListSchedules": true, "CreateSchedule": true, "ListWebhooks": true, "CreateWebhook": true,
	"UseMyLogins": true, "StopUsingLogins": true,
	"ListAgentMCPServers": true, "CreateAgentMCPServer": true, "AttachMCPServer": true, "DetachMCPServer": true,
	"ListAgentSkills": true, "AttachSkill": true, "DetachSkill": true,
}

var errAppNoHarness = errStatus(http.StatusConflict, "this machine is an app: it runs services, not a harness, so it has no sessions, schedules, webhooks, logins, MCP servers or skills")

// machineKinds applies the agent/app rule of the API (see the OpenAPI
// description) to operations under /agents/{agentId}.
func (a *API) machineKinds(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if !agentOnlyOps[operationID] && !harnessOps[operationID] {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		if id := chi.URLParam(r, "agentId"); id != "" {
			if ag, err := a.Store.GetAgent(ctx, id); err == nil && ag.Kind == apps.KindApp {
				if agentOnlyOps[operationID] {
					return nil, errNotFound
				}
				return nil, errAppNoHarness
			}
		}
		return f(ctx, w, r, req)
	}
}

// appAccess loads an app like authz.Agent loads an agent; ids of other
// machines are not found.
func (a *API) appAccess(ctx context.Context, appID string, action authz.Action) (authz.AgentAccess, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, appID, action)
	if acc.Row.Agent.ID != "" && acc.Row.Agent.Kind != apps.KindApp {
		return authz.AgentAccess{}, errNotFound
	}
	return acc, err
}

func (a *API) apiApp(ctx context.Context, ag db.Agent, sb db.Sandbox, role authz.Role, infos []node.Info) (gen.App, error) {
	owner, err := a.Store.GetUser(ctx, ag.OwnerID)
	if err != nil {
		return gen.App{}, err
	}
	imgMeta := a.machineImageMeta(ctx, ag)
	base := "/api/v1/agents/" + ag.ID
	out := gen.App{
		Id: ag.ID, Name: ag.Name, Owner: apiUser(owner),
		ImageId: &imgMeta.id, ImageName: &imgMeta.name, ImageRevision: &imgMeta.revision, ImageUpdateAvailable: &imgMeta.updateAvailable,
		Cpus: float32(ag.Cpus), MemoryMb: ag.MemoryMb,
		IdleMinutes: int(ag.IdleMinutes), State: gen.SandboxState(sb.State), StateError: sb.Error,
		StateChangedAt: ts(sb.StateChangedAt), NeedsRebuild: a.Sandboxes.NeedsRebuild(ag, sb), Role: gen.AgentRole(role),
		IsTemplate: ag.IsTemplate == 1,
		CreatedAt:  ts(ag.CreatedAt), UpdatedAt: ts(ag.UpdatedAt),
	}
	out.Links.Services, out.Links.Exposures, out.Links.Files = base+"/services", base+"/exposures", base+"/files"
	out.Links.Terminal, out.Links.Shares, out.Links.Secrets, out.Links.Audit = base+"/terminal", base+"/shares", base+"/secrets", base+"/audit"
	if ag.CreatedByAgentID != "" {
		name := ""
		if c, err := a.Store.GetAgent(ctx, ag.CreatedByAgentID); err == nil {
			name = c.Name
		}
		out.CreatedByAgent = &struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		}{ag.CreatedByAgentID, name}
	}
	if a.Preview.Enabled() {
		if rows, err := a.Preview.List(ctx, ag.ID); err == nil && len(rows) > 0 {
			primary := rows[0]
			for _, e := range rows {
				if strings.HasPrefix(e.CreatedBy, "deploy:") {
					primary = e
					break
				}
			}
			u := a.Preview.URL(primary)
			out.Url = &u
		}
	}
	if a.Idle != nil && sandbox.Phase(sb.State) == sandbox.PhaseRunning {
		if l := a.Idle.Tracker.Last(ag.ID); !l.IsZero() {
			out.LastActivityAt = &l
		}
		if ag.IdleMinutes > 0 {
			if s := a.Idle.SleepsAt(ag.ID, ag.IdleMinutes); !s.IsZero() {
				out.SleepsAt = &s
			}
		}
	}
	info := nodeByID(infos, sb.NodeID)
	out.NodeId, out.NodeName = &info.ID, &info.Name
	if !info.Connected {
		out.State = gen.SandboxStateUnreachable
		out.LastActivityAt, out.SleepsAt = nil, nil
	}
	return out, nil
}

func (a *API) reloadApp(ctx context.Context, appID string, role authz.Role) (gen.App, error) {
	row, err := a.Store.GetAgentWithSandbox(ctx, appID)
	if err != nil {
		return gen.App{}, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return gen.App{}, err
	}
	return a.apiApp(ctx, row.Agent, row.Sandbox, role, infos)
}

type appSettings struct {
	name        string
	cpus        float64
	memoryMB    int64
	idleMinutes int64
}

func (a *API) validateApp(s *appSettings) error {
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
	if s.idleMinutes < 0 || s.idleMinutes > 10080 {
		return badRequest("idleMinutes must be between 0 (always on) and 10080")
	}
	return nil
}

// ListApps implements gen.StrictServerInterface.
func (a *API) ListApps(ctx context.Context, _ gen.ListAppsRequestObject) (gen.ListAppsResponseObject, error) {
	u := me(ctx)
	rows, err := a.Store.ListUserAgents(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListApps200JSONResponse{}
	for _, r := range rows {
		if r.Agent.Kind != apps.KindApp {
			continue
		}
		ap, err := a.apiApp(ctx, r.Agent, r.Sandbox, authz.RoleOf(u.ID, r.Agent.OwnerID, true), infos)
		if err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, nil
}

// CreateApp implements gen.StrictServerInterface.
func (a *API) CreateApp(ctx context.Context, req gen.CreateAppRequestObject) (gen.CreateAppResponseObject, error) {
	u := me(ctx)
	b := req.Body
	s := appSettings{name: b.Name, cpus: a.Apps.Limits.DefaultCPUs, memoryMB: a.Apps.Limits.DefaultMemoryMB}
	if b.Cpus != nil && *b.Cpus != 0 {
		s.cpus = float64(*b.Cpus)
	}
	if b.MemoryMb != nil && *b.MemoryMb != 0 {
		s.memoryMB = *b.MemoryMb
	}
	if b.IdleMinutes != nil {
		s.idleMinutes = int64(*b.IdleMinutes)
	}
	if err := a.validateApp(&s); err != nil {
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
	ag, err := a.Apps.Create(ctx, apps.CreateParams{
		OwnerID: u.ID, Name: s.name, CPUs: s.cpus, MemoryMB: s.memoryMB, IdleMinutes: s.idleMinutes,
		ImageID: imageID, ImageRevision: imageRev, AllowedArches: allowedArches,
	})
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.AppCreate, ag.Name, nil)
	out, err := a.reloadApp(ctx, ag.ID, authz.Owner)
	if err != nil {
		return nil, err
	}
	return gen.CreateApp201JSONResponse(out), nil
}

// GetApp implements gen.StrictServerInterface.
func (a *API) GetApp(ctx context.Context, req gen.GetAppRequestObject) (gen.GetAppResponseObject, error) {
	acc, err := a.appAccess(ctx, req.AppId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	out, err := a.reloadApp(ctx, acc.Row.Agent.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.GetApp200JSONResponse(out), nil
}

// UpdateApp implements gen.StrictServerInterface.
func (a *API) UpdateApp(ctx context.Context, req gen.UpdateAppRequestObject) (gen.UpdateAppResponseObject, error) {
	acc, err := a.appAccess(ctx, req.AppId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	s := appSettings{name: ag.Name, cpus: ag.Cpus, memoryMB: ag.MemoryMb, idleMinutes: ag.IdleMinutes}
	b := req.Body
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
	if err := a.validateApp(&s); err != nil {
		return nil, err
	}
	newImageID, newImageRev, imageChanged, err := a.resolveMachineImagePatch(ctx, ag, b.ImageId, b.ImageRevision)
	if err != nil {
		return nil, err
	}
	if s.name != ag.Name && !apps.ValidAppName(s.name) {
		return nil, badRequest("an app name is 1 to 40 lowercase letters, digits and hyphens, starting and ending with a letter or digit")
	}
	toTemplate := b.IsTemplate != nil && *b.IsTemplate && ag.IsTemplate == 0
	toApp := b.IsTemplate != nil && !*b.IsTemplate && ag.IsTemplate == 1
	if toTemplate || toApp {
		if err := a.checkTemplateChange(acc.Row, toTemplate); err != nil {
			return nil, err
		}
	}
	if toTemplate && sandbox.Phase(acc.Row.Sandbox.State) != sandbox.PhaseSleeping {
		if err := a.Sandboxes.Stop(ctx, ag.ID); err != nil {
			return nil, err
		}
	}
	if toTemplate || toApp {
		if err := a.Store.SetAgentTemplate(ctx, db.SetAgentTemplateParams{IsTemplate: b2i(toTemplate), UpdatedAt: a.Now().Unix(), ID: ag.ID}); err != nil {
			return nil, err
		}
	}
	if err := a.Store.UpdateAgent(ctx, db.UpdateAgentParams{
		Name: s.name, Image: ag.Image, Cpus: s.cpus, MemoryMb: s.memoryMB, IdleMinutes: s.idleMinutes, UpdatedAt: a.Now().Unix(), ID: ag.ID,
	}); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, apps.ErrNameTaken
		}
		return nil, err
	}
	if imageChanged {
		if err := a.Store.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: newImageID, ImageRevision: newImageRev, UpdatedAt: a.Now().Unix(), ID: ag.ID}); err != nil {
			return nil, err
		}
	}
	a.record(ctx, ag, audit.AppUpdate, ag.Name, map[string]any{"name": s.name, "cpus": s.cpus, "memoryMb": s.memoryMB, "idleMinutes": s.idleMinutes, "isTemplate": b.IsTemplate})
	out, err := a.reloadApp(ctx, ag.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.UpdateApp200JSONResponse(out), nil
}

// DeleteApp implements gen.StrictServerInterface.
func (a *API) DeleteApp(ctx context.Context, req gen.DeleteAppRequestObject) (gen.DeleteAppResponseObject, error) {
	acc, err := a.appAccess(ctx, req.AppId, authz.AgentDelete)
	if err != nil {
		return nil, err
	}
	if err := a.deleteMachine(ctx, acc.Row.Agent); err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.AppDelete, acc.Row.Agent.Name, nil)
	return gen.DeleteApp202Response{}, nil
}

// deleteMachine destroys a machine; its preview URLs stop now and their
// names stay reserved for the owner.
func (a *API) deleteMachine(ctx context.Context, ag db.Agent) error {
	if err := a.Sandboxes.Destroy(ctx, ag.ID); err != nil {
		return err
	}
	return a.Preview.DeleteAgent(ctx, ag)
}

func (a *API) appLifecycle(ctx context.Context, appID string, need authz.Action, action string, op func(context.Context, string) error) (gen.App, error) {
	acc, err := a.appAccess(ctx, appID, need)
	if err != nil {
		return gen.App{}, err
	}
	if err := op(ctx, appID); err != nil {
		return gen.App{}, err
	}
	a.record(ctx, acc.Row.Agent, action, "", nil)
	return a.reloadApp(ctx, appID, acc.Role)
}

// StartApp implements gen.StrictServerInterface.
func (a *API) StartApp(ctx context.Context, req gen.StartAppRequestObject) (gen.StartAppResponseObject, error) {
	out, err := a.appLifecycle(ctx, req.AppId, authz.AgentLifecycle, audit.AppStart, a.startChecked)
	if err != nil {
		return nil, err
	}
	return gen.StartApp202JSONResponse(out), nil
}

// StopApp implements gen.StrictServerInterface.
func (a *API) StopApp(ctx context.Context, req gen.StopAppRequestObject) (gen.StopAppResponseObject, error) {
	out, err := a.appLifecycle(ctx, req.AppId, authz.AgentLifecycle, audit.AppStop, a.Sandboxes.Stop)
	if err != nil {
		return nil, err
	}
	return gen.StopApp202JSONResponse(out), nil
}

// RebuildApp implements gen.StrictServerInterface.
func (a *API) RebuildApp(ctx context.Context, req gen.RebuildAppRequestObject) (gen.RebuildAppResponseObject, error) {
	out, err := a.appLifecycle(ctx, req.AppId, authz.AgentRebuild, audit.AppRebuild, a.Sandboxes.Rebuild)
	if err != nil {
		return nil, err
	}
	return gen.RebuildApp202JSONResponse(out), nil
}

// TakeOverApp implements gen.StrictServerInterface.
func (a *API) TakeOverApp(ctx context.Context, req gen.TakeOverAppRequestObject) (gen.TakeOverAppResponseObject, error) {
	acc, err := a.appAccess(ctx, req.AppId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	if err := a.Apps.TakeOver(ctx, acc.Row.Agent.ID); err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.AppTakeOver, acc.Row.Agent.Name, map[string]any{"fromAgentId": acc.Row.Agent.CreatedByAgentID})
	out, err := a.reloadApp(ctx, acc.Row.Agent.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.TakeOverApp200JSONResponse(out), nil
}

// DeployApp implements gen.StrictServerInterface.
func (a *API) DeployApp(ctx context.Context, req gen.DeployAppRequestObject) (gen.DeployAppResponseObject, error) {
	acc, err := a.appAccess(ctx, req.AppId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	b := req.Body
	// Copying a machine's files is reading all of them: as much as its terminal gives.
	src, err := a.authz.Agent(ctx, me(ctx).ID, b.SourceAgentId, authz.AgentTerminal)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return nil, badRequest("no source machine with that id that you may read")
		}
		return nil, err
	}
	p := apps.DeployParams{
		App: acc.Row.Agent, SourceID: src.Row.Agent.ID, SourceDir: b.SourceDir, Command: b.Command, Port: b.Port,
		Expose: true, ExposedBy: me(ctx).ID, ActorUserID: me(ctx).ID,
	}
	if b.Env != nil {
		p.Env = *b.Env
	}
	if b.Exclude != nil {
		p.Exclude = *b.Exclude
	}
	if b.Access != nil {
		p.Access = preview.Access(*b.Access)
	}
	if err := a.checkMemory(ctx, acc.Row.Agent.ID); err != nil {
		return nil, err
	}
	res, err := a.Apps.Deploy(ctx, p)
	if err != nil {
		return nil, exposureError(err)
	}
	return gen.DeployApp200JSONResponse(a.apiDeployResult(res)), nil
}

func (a *API) apiDeployResult(res apps.DeployResult) gen.DeployResult {
	out := gen.DeployResult{Release: res.Release, Ready: res.Ready, RolledBack: res.RolledBack, Logs: res.Logs}
	if res.Exposure != nil {
		e := a.apiExposure(*res.Exposure)
		out.Exposure = &e
		out.Url = &e.Url
	}
	if res.Service != nil {
		out.Service = apiServiceStatus(*res.Service)
	}
	return out
}
