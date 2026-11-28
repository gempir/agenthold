package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/project"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

var errProjectsUnavailable = errStatus(http.StatusServiceUnavailable, "projects are not available on this server")

func apiProject(p db.Project, sources []db.ProjectSource) gen.Project {
	out := gen.Project{
		Id: p.ID, Name: p.Name, Slug: p.Slug, Description: p.Description, Instructions: p.Instructions,
		Sources: []gen.ProjectSource{}, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt),
	}
	for _, s := range sources {
		out.Sources = append(out.Sources, gen.ProjectSource{
			Id: s.ID, Name: s.Name, Kind: gen.ProjectSourceKind(s.Kind),
			Url: opt(s.Url), Path: opt(s.Path), DefaultBranch: opt(s.DefaultBranch),
		})
	}
	return out
}

// ownProject loads a project of the signed-in user; others' are not found.
func (a *API) ownProject(ctx context.Context, projectID string) (db.Project, error) {
	if a.Projects == nil {
		return db.Project{}, errProjectsUnavailable
	}
	p, err := a.Store.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && p.OwnerID != me(ctx).ID {
		return db.Project{}, errNotFound
	}
	return p, err
}

func (a *API) projectOut(ctx context.Context, p db.Project) (gen.Project, error) {
	src, err := a.Store.ListProjectSources(ctx, p.ID)
	if err != nil {
		return gen.Project{}, err
	}
	return apiProject(p, src), nil
}

// projectFields validates the editable fields of a project.
func projectFields(in gen.ProjectInput) (name, desc, instr string, sources []project.SourceInput, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return "", "", "", nil, badRequest("name must be 1 to 100 characters")
	}
	desc, instr = strings.TrimSpace(deref(in.Description)), strings.TrimSpace(deref(in.Instructions))
	if len(desc) > 2000 || len(instr) > 20000 {
		return "", "", "", nil, badRequest("description or instructions too long")
	}
	var raw []project.SourceInput
	if in.Sources != nil {
		if len(*in.Sources) > 20 {
			return "", "", "", nil, badRequest("a project has up to 20 sources")
		}
		for _, s := range *in.Sources {
			kind := ""
			if s.Kind != nil {
				kind = string(*s.Kind)
			}
			raw = append(raw, project.SourceInput{Name: deref(s.Name), Kind: kind, URL: deref(s.Url), Path: deref(s.Path), DefaultBranch: deref(s.DefaultBranch)})
		}
	}
	sources, err = project.CleanSources(raw)
	return name, desc, instr, sources, err
}

// saveSources makes the project's sources the given ones, matched by name.
func saveSources(ctx context.Context, q *db.Queries, projectID string, in []project.SourceInput, now int64) error {
	have, err := q.ListProjectSources(ctx, projectID)
	if err != nil {
		return err
	}
	byName := map[string]db.ProjectSource{}
	for _, s := range have {
		byName[strings.ToLower(s.Name)] = s
	}
	for i, s := range in {
		if old, ok := byName[strings.ToLower(s.Name)]; ok {
			delete(byName, strings.ToLower(s.Name))
			def := s.DefaultBranch
			if def == "" && old.Kind == s.Kind && old.Url == s.URL {
				def = old.DefaultBranch // found on a first checkout
			}
			if err := q.UpdateProjectSource(ctx, db.UpdateProjectSourceParams{
				Kind: s.Kind, Url: s.URL, Path: s.Path, DefaultBranch: def, Position: int64(i), ID: old.ID,
			}); err != nil {
				return err
			}
			continue
		}
		if err := q.CreateProjectSource(ctx, db.CreateProjectSourceParams{
			ID: id.New("src"), ProjectID: projectID, Name: s.Name, Kind: s.Kind, Url: s.URL, Path: s.Path,
			DefaultBranch: s.DefaultBranch, Position: int64(i), CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	for _, s := range byName {
		if err := q.DeleteProjectSource(ctx, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// ListProjects implements gen.StrictServerInterface.
func (a *API) ListProjects(ctx context.Context, _ gen.ListProjectsRequestObject) (gen.ListProjectsResponseObject, error) {
	if a.Projects == nil {
		return nil, errProjectsUnavailable
	}
	rows, err := a.Store.ListProjects(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListProjects200JSONResponse{}
	for _, p := range rows {
		g, err := a.projectOut(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// CreateProject implements gen.StrictServerInterface.
func (a *API) CreateProject(ctx context.Context, req gen.CreateProjectRequestObject) (gen.CreateProjectResponseObject, error) {
	if a.Projects == nil {
		return nil, errProjectsUnavailable
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	name, desc, instr, sources, err := projectFields(*req.Body)
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	existing, err := a.Store.ListProjects(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	// The slug names the directory on disks: unique per user, never changed.
	base := project.Slug(name)
	slug := base
	for n := 2; ; n++ {
		taken := false
		for _, p := range existing {
			if p.Slug == slug {
				taken = true
			}
		}
		if !taken {
			break
		}
		slug = base + "-" + itoa(n)
	}
	now := time.Now().Unix()
	p := db.Project{ID: id.New("prj"), OwnerID: u.ID, Name: name, Slug: slug, Description: desc, Instructions: instr, CreatedAt: now, UpdatedAt: now}
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateProject(ctx, db.CreateProjectParams{
			ID: p.ID, OwnerID: p.OwnerID, Name: p.Name, Slug: p.Slug, Description: p.Description,
			Instructions: p.Instructions, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return saveSources(ctx, q, p.ID, sources, now)
	})
	if err != nil {
		return nil, err
	}
	g, err := a.projectOut(ctx, p)
	if err != nil {
		return nil, err
	}
	return gen.CreateProject201JSONResponse(g), nil
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// GetProject implements gen.StrictServerInterface.
func (a *API) GetProject(ctx context.Context, req gen.GetProjectRequestObject) (gen.GetProjectResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	g, err := a.projectOut(ctx, p)
	if err != nil {
		return nil, err
	}
	return gen.GetProject200JSONResponse(g), nil
}

// UpdateProject implements gen.StrictServerInterface.
func (a *API) UpdateProject(ctx context.Context, req gen.UpdateProjectRequestObject) (gen.UpdateProjectResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	name, desc, instr, sources, err := projectFields(*req.Body)
	if err != nil {
		return nil, err
	}
	keep := req.Body.Sources == nil
	now := time.Now().Unix()
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateProject(ctx, db.UpdateProjectParams{Name: name, Description: desc, Instructions: instr, UpdatedAt: now, ID: p.ID}); err != nil {
			return err
		}
		if keep {
			return nil
		}
		return saveSources(ctx, q, p.ID, sources, now)
	})
	if err != nil {
		return nil, err
	}
	p.Name, p.Description, p.Instructions, p.UpdatedAt = name, desc, instr, now
	g, err := a.projectOut(ctx, p)
	if err != nil {
		return nil, err
	}
	return gen.UpdateProject200JSONResponse(g), nil
}

// DeleteProject implements gen.StrictServerInterface.
func (a *API) DeleteProject(ctx context.Context, req gen.DeleteProjectRequestObject) (gen.DeleteProjectResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	err = a.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.ClearProjectSessions(ctx, p.ID); err != nil {
			return err
		}
		return q.DeleteProject(ctx, p.ID)
	})
	if err != nil {
		return nil, err
	}
	return gen.DeleteProject204Response{}, nil
}

func apiChanges(cs []project.SourceChanges) []gen.ProjectSourceChanges {
	out := []gen.ProjectSourceChanges{}
	for _, c := range cs {
		g := gen.ProjectSourceChanges{Source: c.Source, Base: c.Base, Commits: c.Commits, Dirty: c.Dirty, Remote: c.Remote, Files: []gen.ProjectFileChange{}, Error: opt(c.Error)}
		for _, f := range c.Files {
			g.Files = append(g.Files, gen.ProjectFileChange{Path: f.Path, Status: f.Status})
		}
		out = append(out, g)
	}
	return out
}

// GetProjectStatus implements gen.StrictServerInterface.
func (a *API) GetProjectStatus(ctx context.Context, req gen.GetProjectStatusRequestObject) (gen.GetProjectStatusResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	sources, err := a.Store.ListProjectSources(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	threads, overlaps, err := a.Projects.Status(ctx, p, sources, deref(req.Params.IncludeSettled))
	if err != nil {
		return nil, err
	}
	out := gen.GetProjectStatus200JSONResponse{Threads: []gen.ProjectThread{}, Overlaps: []gen.ProjectOverlap{}}
	for _, t := range threads {
		// Only threads on agents the user may see.
		if _, err := a.authz.Agent(ctx, me(ctx).ID, t.Agent.ID, authz.AgentRead); err != nil {
			continue
		}
		s, err := a.apiSession(ctx, t.Session)
		if err != nil {
			return nil, err
		}
		out.Threads = append(out.Threads, gen.ProjectThread{
			Session: s, AgentId: t.Agent.ID, AgentName: t.Agent.Name, Connected: t.Connected,
			Sources: apiChanges(t.Changes), Error: opt(t.Error),
		})
	}
	for _, o := range overlaps {
		out.Overlaps = append(out.Overlaps, gen.ProjectOverlap{Source: o.Source, Path: o.Path, SessionIds: o.Threads})
	}
	return out, nil
}

// projectAgent checks that the user may start threads on the agent.
func (a *API) projectAgent(ctx context.Context, agentID string) (db.Agent, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, agentID, authz.AgentPrompt)
	if err != nil {
		return db.Agent{}, err
	}
	ag := acc.Row.Agent
	if ag.IsTemplate == 1 {
		return db.Agent{}, sandbox.ErrTemplate
	}
	if ag.Kind == apps.KindApp {
		return db.Agent{}, badRequest("apps run no threads")
	}
	return ag, nil
}

// StartProjectThread implements gen.StrictServerInterface.
func (a *API) StartProjectThread(ctx context.Context, req gen.StartProjectThreadRequestObject) (gen.StartProjectThreadResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	ag, err := a.projectAgent(ctx, req.Body.AgentId)
	if err != nil {
		return nil, err
	}
	s, err := a.Projects.StartThread(ctx, p, project.StartOpts{
		AgentID: ag.ID, UserID: me(ctx).ID, Title: strings.TrimSpace(deref(req.Body.Title)), Direct: deref(req.Body.Direct),
	})
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.ProjectThreadStart, s.ID, map[string]any{"project": p.Name, "branch": s.ProjectBranch, "cwd": s.Cwd})
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.StartProjectThread201JSONResponse(g), nil
}

func (a *API) apiTask(ctx context.Context, t db.ProjectTask) gen.ProjectTask {
	out := gen.ProjectTask{
		Id: t.ID, ProjectId: t.ProjectID, Title: t.Title, Body: t.Body, Status: gen.ProjectTaskStatus(t.Status),
		Note: t.Note, CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt), SessionId: opt(t.SessionID),
	}
	if t.AgentID != "" {
		out.AgentId = &t.AgentID
		name := "(deleted agent)"
		if ag, err := a.Store.GetAgent(ctx, t.AgentID); err == nil {
			name = ag.Name
		}
		out.AgentName = &name
	}
	out.CreatedBy = t.CreatedBy
	if agentID, ok := strings.CutPrefix(t.CreatedBy, "agent:"); ok {
		out.CreatedBy = "agent"
		if ag, err := a.Store.GetAgent(ctx, agentID); err == nil {
			out.CreatedBy = "agent " + ag.Name
		}
	} else if u, err := a.Store.GetUser(ctx, t.CreatedBy); err == nil {
		out.CreatedBy = u.Name
	}
	return out
}

// ListProjectTasks implements gen.StrictServerInterface.
func (a *API) ListProjectTasks(ctx context.Context, req gen.ListProjectTasksRequestObject) (gen.ListProjectTasksResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListProjectTasks(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListProjectTasks200JSONResponse{}
	for _, t := range rows {
		out = append(out, a.apiTask(ctx, t))
	}
	return out, nil
}

// CreateProjectTask implements gen.StrictServerInterface.
func (a *API) CreateProjectTask(ctx context.Context, req gen.CreateProjectTaskRequestObject) (gen.CreateProjectTaskResponseObject, error) {
	p, err := a.ownProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	t, err := a.Projects.CreateTask(ctx, p.ID, project.TaskInput{Title: req.Body.Title, Body: deref(req.Body.Body), CreatedBy: me(ctx).ID})
	if err != nil {
		return nil, err
	}
	return gen.CreateProjectTask201JSONResponse(a.apiTask(ctx, t)), nil
}

// ownTask loads a task of a project of the signed-in user.
func (a *API) ownTask(ctx context.Context, taskID string) (db.ProjectTask, error) {
	if a.Projects == nil {
		return db.ProjectTask{}, errProjectsUnavailable
	}
	t, err := a.Store.GetProjectTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errNotFound
	}
	if err != nil {
		return t, err
	}
	_, err = a.ownProject(ctx, t.ProjectID)
	return t, err
}

// UpdateProjectTask implements gen.StrictServerInterface.
func (a *API) UpdateProjectTask(ctx context.Context, req gen.UpdateProjectTaskRequestObject) (gen.UpdateProjectTaskResponseObject, error) {
	t, err := a.ownTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if b := req.Body; b != nil {
		if b.Title != nil {
			if title := strings.TrimSpace(*b.Title); title == "" || len(title) > 200 {
				return nil, badRequest("title must be 1 to 200 characters")
			}
			t.Title = strings.TrimSpace(*b.Title)
		}
		if b.Body != nil {
			t.Body = strings.TrimSpace(*b.Body)
		}
		if b.Status != nil {
			t.Status = string(*b.Status)
		}
		if b.Note != nil {
			t.Note = strings.TrimSpace(*b.Note)
		}
	}
	if err := a.Projects.SaveTask(ctx, t); err != nil {
		return nil, err
	}
	t, err = a.Store.GetProjectTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return gen.UpdateProjectTask200JSONResponse(a.apiTask(ctx, t)), nil
}

// DeleteProjectTask implements gen.StrictServerInterface.
func (a *API) DeleteProjectTask(ctx context.Context, req gen.DeleteProjectTaskRequestObject) (gen.DeleteProjectTaskResponseObject, error) {
	t, err := a.ownTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteProjectTask(ctx, t.ID); err != nil {
		return nil, err
	}
	return gen.DeleteProjectTask204Response{}, nil
}

// AssignProjectTask implements gen.StrictServerInterface.
func (a *API) AssignProjectTask(ctx context.Context, req gen.AssignProjectTaskRequestObject) (gen.AssignProjectTaskResponseObject, error) {
	t, err := a.ownTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	ag, err := a.projectAgent(ctx, req.Body.AgentId)
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	t, s, err := a.Projects.AssignTask(ctx, t, project.AssignOpts{AgentID: ag.ID, UserID: u.ID, UserName: u.Name})
	if err != nil {
		if errors.Is(err, session.ErrBusy) {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	a.record(ctx, ag, audit.TaskAssign, s.ID, map[string]any{"task": t.ID, "title": t.Title})
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.AssignProjectTask200JSONResponse{Task: a.apiTask(ctx, t), Session: g}, nil
}

// projectSession loads a project thread the user may act on (with action)
// and its running machine.
func (a *API) projectSession(ctx context.Context, sessionID string, action authz.Action) (db.AgentSession, *tunnel.Session, error) {
	if a.Projects == nil {
		return db.AgentSession{}, nil, errProjectsUnavailable
	}
	s, err := a.sessionAccess(ctx, sessionID, action)
	if err != nil {
		return s, nil, err
	}
	if s.ProjectID == "" {
		return s, nil, badRequest("the thread works on no project")
	}
	if s.ProjectBranch == "" {
		return s, nil, project.ErrNoWork
	}
	ts, err := a.session(ctx, s.AgentID)
	return s, ts, err
}

func apiResults(rs []project.MergeResult) []gen.ProjectSourceResult {
	out := []gen.ProjectSourceResult{}
	for _, r := range rs {
		out = append(out, gen.ProjectSourceResult{Source: r.Source, Done: r.Merged, Commits: r.Commits, Error: opt(r.Error)})
	}
	return out
}

// GetSessionProjectChanges implements gen.StrictServerInterface.
func (a *API) GetSessionProjectChanges(ctx context.Context, req gen.GetSessionProjectChangesRequestObject) (gen.GetSessionProjectChangesResponseObject, error) {
	s, ts, err := a.projectSession(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	p, sources, err := a.Projects.Load(ctx, s.ProjectID)
	if err != nil {
		return nil, err
	}
	cs, err := a.Projects.Changes(ctx, ts, p, sources, s)
	if err != nil {
		return nil, err
	}
	return gen.GetSessionProjectChanges200JSONResponse(apiChanges(cs)), nil
}

func (a *API) idleProjectSession(ctx context.Context, sessionID string) (db.AgentSession, *tunnel.Session, error) {
	s, ts, err := a.projectSession(ctx, sessionID, authz.AgentPrompt)
	if err != nil {
		return s, ts, err
	}
	if a.Sessions.Running(s.ID) {
		return s, ts, errStatus(http.StatusConflict, "the thread is running a turn; wait until it is done")
	}
	return s, ts, nil
}

// MergeSessionProject implements gen.StrictServerInterface.
func (a *API) MergeSessionProject(ctx context.Context, req gen.MergeSessionProjectRequestObject) (gen.MergeSessionProjectResponseObject, error) {
	s, ts, err := a.idleProjectSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	rs, err := a.Projects.Merge(ctx, ts, s)
	if err != nil {
		return nil, err
	}
	failed := false
	for _, r := range rs {
		failed = failed || r.Error != ""
	}
	if !failed {
		a.Projects.FinishThreadTasks(ctx, s.ID, project.TaskDone)
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.ProjectMerge, s.ID, map[string]any{"branch": s.ProjectBranch, "failed": failed})
	return gen.MergeSessionProject200JSONResponse(apiResults(rs)), nil
}

// PushSessionProject implements gen.StrictServerInterface.
func (a *API) PushSessionProject(ctx context.Context, req gen.PushSessionProjectRequestObject) (gen.PushSessionProjectResponseObject, error) {
	s, ts, err := a.idleProjectSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	rs, err := a.Projects.Push(ctx, ts, s)
	if err != nil {
		return nil, err
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.ProjectPush, s.ID, map[string]any{"branch": s.ProjectBranch})
	return gen.PushSessionProject200JSONResponse(apiResults(rs)), nil
}

// CloseSessionProject implements gen.StrictServerInterface.
func (a *API) CloseSessionProject(ctx context.Context, req gen.CloseSessionProjectRequestObject) (gen.CloseSessionProjectResponseObject, error) {
	s, ts, err := a.idleProjectSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	force := req.Body != nil && deref(req.Body.Force)
	if err := a.Projects.Close(ctx, ts, s, force); err != nil {
		return nil, err
	}
	if force {
		a.Projects.FinishThreadTasks(ctx, s.ID, project.TaskOpen)
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.ProjectClose, s.ID, map[string]any{"branch": s.ProjectBranch, "force": force})
	s, _, err = session.Settle(ctx, a.Store, s.ID, me(ctx).ID, time.Now())
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.CloseSessionProject200JSONResponse(g), nil
}
