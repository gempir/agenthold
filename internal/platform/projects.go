package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gempir/agenthold/internal/project"
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
		out.Threads = append(out.Threads, info)
	}
	for _, o := range overlaps {
		out.Overlaps = append(out.Overlaps, overlapInfo{File: o.Source + "/" + o.Path, Threads: o.Threads})
	}
	return toJSON(out), nil
}
