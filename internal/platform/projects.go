package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/project"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// projectArg is the optional project argument of the project tools.
var projectArg = map[string]any{"type": "string", "description": "The project's name or id (see list_projects); default: the project of the current thread."}

// projectTools coordinate work on the owner's projects.
var projectTools = []map[string]any{
	{
		"name":        "list_projects",
		"description": "List your owner's projects with their repositories and folders.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "project_status",
		"description": "Show who works on a project: its active threads (yours and other agents'), the branch each works on, " +
			"the files each changed, and the files more than one thread changed (likely merge conflicts). Check it " +
			"before larger changes and coordinate with the other threads (send_message) when you overlap.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"project": projectArg}},
	},
	{
		"name":        "list_tasks",
		"description": "List the tasks of a project, newest first: open ones, or all with status \"all\".",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"project": projectArg,
			"status":  map[string]any{"type": "string", "enum": []string{"open", "in_progress", "review", "done", "cancelled", "all"}},
		}},
	},
	{
		"name":        "create_task",
		"description": "Add a task to a project, e.g. to split work up. Assign it with assign_task, or claim it with claim_task.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"title"},
			"properties": map[string]any{
				"project": projectArg,
				"title":   map[string]any{"type": "string"},
				"body":    map[string]any{"type": "string", "description": "What to do, in enough detail for another agent."},
			},
		},
	},
	{
		"name":        "claim_task",
		"description": "Take an open task of the current thread's project: it becomes yours, worked on in the current thread (in_progress).",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"task_id"},
			"properties": map[string]any{"task_id": map[string]any{"type": "string"}},
		},
	},
	{
		"name": "update_task",
		"description": "Change a task's status and leave a note: \"review\" when your work is committed and ready for your owner, " +
			"\"open\" to hand it back, \"done\" or \"cancelled\".",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"task_id"},
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string", "enum": project.TaskStatuses},
				"note":    map[string]any{"type": "string"},
			},
		},
	},
	{
		"name": "assign_task",
		"description": "Hand a task to an agent: it starts a new thread with a workspace of its own (its own branch of every " +
			"repository of the project) and works on the task there in parallel. The agent may be you (a parallel thread " +
			"of yours) or another agent (see list_agents). Returns at once.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"task_id", "agent"},
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"agent":   map[string]any{"type": "string", "description": "The agent's name or id, or \"me\"."},
			},
		},
	},
}

// projectToolArgs are the arguments of the project tools.
type projectToolArgs struct {
	Project string `json:"project"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	TaskID  string `json:"task_id"`
	Note    string `json:"note"`
	Agent   string `json:"agent"`
}

func isProjectTool(tool string) bool {
	for _, t := range projectTools {
		if t["name"] == tool {
			return true
		}
	}
	return false
}

func (p *Service) projectCall(ctx context.Context, caller db.Agent, sessionID, tool string, raw json.RawMessage) (any, error) {
	var in projectToolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	switch tool {
	case "list_projects":
		return p.listProjects(ctx, caller)
	case "project_status":
		return p.projectStatus(ctx, caller, sessionID, in.Project)
	case "list_tasks":
		return p.listTasks(ctx, caller, sessionID, in)
	case "create_task":
		return p.createTask(ctx, caller, sessionID, in)
	case "claim_task":
		return p.claimTask(ctx, caller, sessionID, in.TaskID)
	case "update_task":
		return p.updateTask(ctx, caller, in)
	case "assign_task":
		return p.assignTask(ctx, caller, sessionID, in)
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

// ErrNoProject is returned for projects that are not the caller's owner's.
var ErrNoProject = errors.New("no project with that name or id; see list_projects")

// findProject resolves a project of the caller's owner by id or name, or the
// project of the current thread.
func (p *Service) findProject(ctx context.Context, caller db.Agent, sessionID, ref string) (db.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if sess, err := p.Store.GetAgentSession(ctx, sessionID); err == nil && sess.AgentID == caller.ID && sess.ProjectID != "" {
			if pr, err := p.Store.GetProject(ctx, sess.ProjectID); err == nil && pr.OwnerID == caller.OwnerID {
				return pr, nil
			}
		}
		return db.Project{}, errors.New("the current thread works on no project; name one (see list_projects)")
	}
	rows, err := p.Store.ListProjects(ctx, caller.OwnerID)
	if err != nil {
		return db.Project{}, err
	}
	for _, pr := range rows {
		if pr.ID == ref || strings.EqualFold(pr.Name, ref) || pr.Slug == ref {
			return pr, nil
		}
	}
	return db.Project{}, ErrNoProject
}

// findTask loads a task of a project of the caller's owner.
func (p *Service) findTask(ctx context.Context, caller db.Agent, taskID string) (db.ProjectTask, db.Project, error) {
	t, err := p.Store.GetProjectTask(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return t, db.Project{}, errors.New("no task with that id; see list_tasks")
	}
	pr, err := p.Store.GetProject(ctx, t.ProjectID)
	if err != nil || pr.OwnerID != caller.OwnerID {
		return t, db.Project{}, errors.New("no task with that id; see list_tasks")
	}
	return t, pr, nil
}

func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

type sourceInfo struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	URL           string `json:"url,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

type projectInfo struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description,omitempty"`
	Directory    string       `json:"directory"`
	Sources      []sourceInfo `json:"sources"`
	Instructions string       `json:"instructions,omitempty"`
}

func (p *Service) listProjects(ctx context.Context, caller db.Agent) (string, error) {
	rows, err := p.Store.ListProjects(ctx, caller.OwnerID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "Your owner has no projects.", nil
	}
	out := []projectInfo{}
	for _, pr := range rows {
		src, err := p.Store.ListProjectSources(ctx, pr.ID)
		if err != nil {
			return "", err
		}
		info := projectInfo{ID: pr.ID, Name: pr.Name, Description: pr.Description, Directory: project.Root(pr), Sources: []sourceInfo{}, Instructions: pr.Instructions}
		for _, s := range src {
			info.Sources = append(info.Sources, sourceInfo{Name: s.Name, Kind: s.Kind, URL: s.Url, DefaultBranch: s.DefaultBranch})
		}
		out = append(out, info)
	}
	return toJSON(out), nil
}

type changeInfo struct {
	Source  string   `json:"source"`
	Commits int      `json:"commits"`
	Dirty   bool     `json:"uncommitted,omitempty"`
	Files   []string `json:"files,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type projectThreadInfo struct {
	ThreadID string       `json:"threadId"`
	Title    string       `json:"title"`
	Agent    string       `json:"agent"`
	Branch   string       `json:"branch,omitempty"`
	Direct   bool         `json:"direct,omitempty"`
	Running  bool         `json:"running,omitempty"`
	Current  bool         `json:"current,omitempty"`
	Offline  bool         `json:"machineOff,omitempty"`
	Changes  []changeInfo `json:"changes,omitempty"`
	Tasks    []string     `json:"tasks,omitempty"`
}

type overlapInfo struct {
	File    string   `json:"file"`
	Threads []string `json:"threads"`
}

func (p *Service) projectStatus(ctx context.Context, caller db.Agent, sessionID, ref string) (string, error) {
	pr, err := p.findProject(ctx, caller, sessionID, ref)
	if err != nil {
		return "", err
	}
	sources, err := p.Store.ListProjectSources(ctx, pr.ID)
	if err != nil {
		return "", err
	}
	threads, overlaps, err := p.Projects.Status(ctx, pr, sources, false)
	if err != nil {
		return "", err
	}
	reachable := map[string]bool{caller.ID: true}
	if rows, err := p.reachable(ctx, caller); err == nil {
		for _, r := range rows {
			reachable[r.Agent.ID] = true
		}
	}
	out := struct {
		Project  string              `json:"project"`
		Threads  []projectThreadInfo `json:"threads"`
		Overlaps []overlapInfo       `json:"overlaps,omitempty"`
	}{Project: pr.Name, Threads: []projectThreadInfo{}}
	for _, t := range threads {
		if !reachable[t.Agent.ID] {
			continue
		}
		info := projectThreadInfo{
			ThreadID: t.Session.ID, Title: t.Session.Title, Agent: t.Agent.Name, Branch: t.Session.ProjectBranch,
			Direct: t.Session.ProjectBranch == "", Current: t.Session.ID == sessionID, Offline: !t.Connected,
		}
		if p.Sessions != nil {
			info.Running = p.Sessions.Running(t.Session.ID)
		}
		for _, c := range t.Changes {
			ci := changeInfo{Source: c.Source, Commits: c.Commits, Dirty: c.Dirty, Error: c.Error}
			for _, f := range c.Files {
				ci.Files = append(ci.Files, f.Status+" "+f.Path)
			}
			if ci.Commits > 0 || ci.Dirty || len(ci.Files) > 0 || ci.Error != "" {
				info.Changes = append(info.Changes, ci)
			}
		}
		if tasks, err := p.Store.ListSessionTasks(ctx, t.Session.ID); err == nil {
			for _, task := range tasks {
				info.Tasks = append(info.Tasks, task.ID+" ("+task.Status+"): "+task.Title)
			}
		}
		out.Threads = append(out.Threads, info)
	}
	for _, o := range overlaps {
		out.Overlaps = append(out.Overlaps, overlapInfo{File: o.Source + "/" + o.Path, Threads: o.Threads})
	}
	return toJSON(out), nil
}

type taskInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Status   string `json:"status"`
	Agent    string `json:"agent,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
	Note     string `json:"note,omitempty"`
}

func (p *Service) taskInfo(ctx context.Context, t db.ProjectTask) taskInfo {
	info := taskInfo{ID: t.ID, Title: t.Title, Body: t.Body, Status: t.Status, ThreadID: t.SessionID, Note: t.Note}
	if t.AgentID != "" {
		if ag, err := p.Store.GetAgent(ctx, t.AgentID); err == nil {
			info.Agent = ag.Name
		}
	}
	return info
}

func (p *Service) listTasks(ctx context.Context, caller db.Agent, sessionID string, in projectToolArgs) (string, error) {
	pr, err := p.findProject(ctx, caller, sessionID, in.Project)
	if err != nil {
		return "", err
	}
	rows, err := p.Store.ListProjectTasks(ctx, pr.ID)
	if err != nil {
		return "", err
	}
	status := in.Status
	if status == "" {
		status = project.TaskOpen
	}
	out := []taskInfo{}
	for _, t := range rows {
		if status == "all" || t.Status == status {
			out = append(out, p.taskInfo(ctx, t))
		}
	}
	if len(out) == 0 {
		return fmt.Sprintf("Project %s has no tasks with status %s.", pr.Name, status), nil
	}
	return toJSON(out), nil
}

func (p *Service) createTask(ctx context.Context, caller db.Agent, sessionID string, in projectToolArgs) (string, error) {
	pr, err := p.findProject(ctx, caller, sessionID, in.Project)
	if err != nil {
		return "", err
	}
	t, err := p.Projects.CreateTask(ctx, pr.ID, project.TaskInput{Title: in.Title, Body: in.Body, CreatedBy: "agent:" + caller.ID})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created task %s in project %s.", t.ID, pr.Name), nil
}

func (p *Service) claimTask(ctx context.Context, caller db.Agent, sessionID, taskID string) (string, error) {
	t, pr, err := p.findTask(ctx, caller, taskID)
	if err != nil {
		return "", err
	}
	sess, err := p.Store.GetAgentSession(ctx, sessionID)
	if err != nil || sess.AgentID != caller.ID {
		return "", errors.New("claim_task works from a thread of yours")
	}
	if sess.ProjectID != pr.ID {
		return "", fmt.Errorf("the current thread does not work on project %s; ask your owner to start a thread of it, or use assign_task", pr.Name)
	}
	if t.Status != project.TaskOpen {
		return "", fmt.Errorf("the task is %s, not open", t.Status)
	}
	t.AgentID, t.SessionID, t.Status = caller.ID, sess.ID, project.TaskInProgress
	if err := p.Projects.SaveTask(ctx, t); err != nil {
		return "", err
	}
	return fmt.Sprintf("Task %s is yours now, worked on in this thread.", t.ID), nil
}

func (p *Service) updateTask(ctx context.Context, caller db.Agent, in projectToolArgs) (string, error) {
	t, _, err := p.findTask(ctx, caller, in.TaskID)
	if err != nil {
		return "", err
	}
	if in.Status == "" && in.Note == "" {
		return "", errors.New("give a status or a note")
	}
	if in.Status != "" {
		t.Status = in.Status
		if in.Status == project.TaskOpen {
			t.AgentID, t.SessionID = "", ""
		}
	}
	if in.Note != "" {
		t.Note = in.Note
	}
	if err := p.Projects.SaveTask(ctx, t); err != nil {
		return "", err
	}
	return fmt.Sprintf("Task %s is %s.", t.ID, t.Status), nil
}

func (p *Service) assignTask(ctx context.Context, caller db.Agent, sessionID string, in projectToolArgs) (string, error) {
	t, pr, err := p.findTask(ctx, caller, in.TaskID)
	if err != nil {
		return "", err
	}
	ref := in.Agent
	if strings.EqualFold(strings.TrimSpace(ref), "me") {
		ref = caller.ID
	}
	target, err := p.resolve(ctx, caller, ref)
	if err != nil {
		return "", err
	}
	depth := p.Sessions.AgentDepth(caller.ID) + 1
	if depth > MaxDepth {
		return "", ErrDepth
	}
	owner, err := p.Store.GetUser(ctx, caller.OwnerID)
	if err != nil {
		return "", err
	}
	t, sess, err := p.Projects.AssignTask(ctx, t, project.AssignOpts{
		AgentID: target.ID, UserID: caller.OwnerID, UserName: "agent " + caller.Name + " (for " + owner.Name + ")",
		Prompt: session.PromptOpts{Depth: depth, Origin: map[string]string{"agentId": caller.ID, "agentName": caller.Name, "sessionId": sessionID}},
	})
	if err != nil {
		return "", err
	}
	_ = p.Store.SetSessionOrigin(ctx, db.SetSessionOriginParams{OriginAgentID: caller.ID, OriginSessionID: sessionID, ID: sess.ID})
	p.agentAudit(ctx, caller, target.ID, audit.TaskAssign, sess.ID, map[string]any{"task": t.ID, "title": t.Title, "project": pr.Name})
	return fmt.Sprintf("Task %s is assigned to %s, working on it in thread %s on branch %s. Follow it with project_status.", t.ID, target.Name, sess.ID, sess.ProjectBranch), nil
}
