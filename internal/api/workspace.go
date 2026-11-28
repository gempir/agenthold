package api

import (
	"context"
	"errors"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/scripts"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Workspace endpoints: structured git diffs, commits, the file viewer and
// editor, file search and project scripts.

func apiGitCommit(c tunnel.GitCommit) gen.GitCommit {
	return gen.GitCommit{
		Sha: c.SHA, ShortSha: c.ShortSHA, Subject: c.Subject, Body: c.Body, Author: c.Author, Email: c.Email,
		Date: ts(c.Date), FilesChanged: c.FilesChanged, Additions: c.Additions, Deletions: c.Deletions,
	}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetAgentGitDiff implements gen.StrictServerInterface.
func (a *API) GetAgentGitDiff(ctx context.Context, req gen.GetAgentGitDiffRequestObject) (gen.GetAgentGitDiffResponseObject, error) {
	p, err := cleanRelPath("path", deref(req.Params.Path))
	if err != nil {
		return nil, err
	}
	mode := tunnel.DiffUncommitted
	if req.Params.Mode != nil {
		mode = string(*req.Params.Mode)
	}
	ref := strings.TrimSpace(deref(req.Params.Ref))
	if mode == tunnel.DiffCommit && ref == "" {
		return nil, badRequest("ref is required for a commit diff")
	}
	if c := req.Params.Context; c != nil && (*c < 0 || *c > 1000) {
		return nil, badRequest("context must be between 0 and 1000")
	}
	s, err := a.connected(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	reply, err := s.GitData(ctx, tunnel.GitRequest{
		Action: tunnel.GitDiff, Path: p, Mode: mode, Ref: ref, Base: strings.TrimSpace(deref(req.Params.Base)), Context: req.Params.Context,
	})
	if err != nil {
		return nil, err
	}
	if reply.Repo == nil || reply.Diff == nil {
		return nil, errors.New("the sandbox did not describe the diff")
	}
	d := reply.Diff
	out := gen.GetAgentGitDiff200JSONResponse{
		Mode: gen.GitDiffMode(d.Mode), Repo: apiGitRepo(*reply.Repo), Files: make([]gen.GitDiffFile, 0, len(d.Files)),
		Additions: d.Additions, Deletions: d.Deletions, Truncated: d.Truncated,
		Base: optString(d.Base), MergeBase: optString(d.MergeBase), Ref: optString(d.Ref),
	}
	for _, f := range d.Files {
		out.Files = append(out.Files, gen.GitDiffFile{
			Path: f.Path, OldPath: optString(f.OldPath), Status: gen.GitDiffFileStatus(f.Status),
			Additions: f.Additions, Deletions: f.Deletions, Binary: f.Binary, Truncated: f.Truncated, Patch: optString(f.Patch),
		})
	}
	return out, nil
}

// ListAgentGitCommits implements gen.StrictServerInterface.
func (a *API) ListAgentGitCommits(ctx context.Context, req gen.ListAgentGitCommitsRequestObject) (gen.ListAgentGitCommitsResponseObject, error) {
	p, err := cleanRelPath("path", deref(req.Params.Path))
	if err != nil {
		return nil, err
	}
	limit := 50
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if limit < 1 || limit > 500 {
		return nil, badRequest("limit must be between 1 and 500")
	}
	s, err := a.connected(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	reply, err := s.GitData(ctx, tunnel.GitRequest{Action: tunnel.GitLog, Path: p, Limit: limit})
	if err != nil {
		return nil, err
	}
	if reply.Repo == nil {
		return nil, errors.New("the sandbox did not describe the repository")
	}
	out := gen.ListAgentGitCommits200JSONResponse{Repo: apiGitRepo(*reply.Repo), Commits: make([]gen.GitCommit, 0, len(reply.Commits))}
	for _, c := range reply.Commits {
		out.Commits = append(out.Commits, apiGitCommit(c))
	}
	return out, nil
}

// CommitAgentGitRepo implements gen.StrictServerInterface.
func (a *API) CommitAgentGitRepo(ctx context.Context, req gen.CommitAgentGitRepoRequestObject) (gen.CommitAgentGitRepoResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Message) == "" {
		return nil, badRequest("message is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	s, ag, err := a.connectedAgent(ctx, req.AgentId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	all := deref(req.Body.All)
	reply, err := s.Git(ctx, tunnel.GitRequest{Action: tunnel.GitCreateCommit, Path: p, Message: req.Body.Message, All: all})
	if err != nil {
		return nil, err
	}
	if reply.Repo == nil || reply.Commit == nil {
		return nil, errors.New("the sandbox did not describe the commit")
	}
	a.record(ctx, ag, audit.GitCommit, reply.Repo.Path, map[string]any{
		"path": reply.Repo.Path, "branch": reply.Repo.Branch, "sha": reply.Commit.SHA, "subject": reply.Commit.Subject, "all": all,
	})
	return gen.CommitAgentGitRepo201JSONResponse{Repo: apiGitRepo(*reply.Repo), Commit: apiGitCommit(*reply.Commit)}, nil
}

func apiTextFile(f tunnel.TextFile) gen.TextFile {
	out := gen.TextFile{
		Path: f.Path, Size: f.Size, ModTime: ts(f.ModTime), MimeType: f.MimeType, IsBinary: f.IsBinary,
		Truncated: f.Truncated, Content: f.Content,
	}
	if f.StartLine > 0 {
		out.StartLine, out.EndLine = &f.StartLine, &f.EndLine
	}
	if f.TotalLines > 0 {
		out.TotalLines = &f.TotalLines
	}
	return out
}

// filePath validates a required path relative to the home.
func filePath(p string) (string, error) {
	p, err := cleanRelPath("path", p)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", badRequest("path must name a file inside the home directory")
	}
	return p, nil
}

// ReadFileText implements gen.StrictServerInterface.
func (a *API) ReadFileText(ctx context.Context, req gen.ReadFileTextRequestObject) (gen.ReadFileTextResponseObject, error) {
	p, err := filePath(req.Params.Path)
	if err != nil {
		return nil, err
	}
	maxBytes := int64(512 << 10)
	if m := req.Params.MaxBytes; m != nil {
		if *m < 1 || *m > 8<<20 {
			return nil, badRequest("maxBytes must be between 1 and 8388608")
		}
		maxBytes = int64(*m)
	}
	start, end := deref(req.Params.StartLine), deref(req.Params.EndLine)
	if start < 0 || end < 0 || (end > 0 && start > end) {
		return nil, badRequest("invalid line range")
	}
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	s, err := a.session(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	f, err := s.ReadText(ctx, p, maxBytes, start, end, a.fileDeny(ctx, acc)...)
	if err != nil {
		return nil, err
	}
	return gen.ReadFileText200JSONResponse(apiTextFile(f)), nil
}

// WriteFileText implements gen.StrictServerInterface.
func (a *API) WriteFileText(ctx context.Context, req gen.WriteFileTextRequestObject) (gen.WriteFileTextResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	p, err := filePath(req.Body.Path)
	if err != nil {
		return nil, err
	}
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentFilesWrite)
	if err != nil {
		return nil, err
	}
	s, err := a.session(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	f, err := s.WriteText(ctx, p, []byte(req.Body.Content), a.fileDeny(ctx, acc)...)
	if err != nil {
		return nil, err
	}
	a.record(ctx, acc.Row.Agent, audit.FileWrite, f.Path, map[string]any{"editor": true, "size": f.Size})
	return gen.WriteFileText200JSONResponse(apiTextFile(f)), nil
}

// SearchFiles implements gen.StrictServerInterface.
func (a *API) SearchFiles(ctx context.Context, req gen.SearchFilesRequestObject) (gen.SearchFilesResponseObject, error) {
	p, err := cleanRelPath("path", deref(req.Params.Path))
	if err != nil {
		return nil, err
	}
	limit := 50
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if limit < 1 || limit > 500 {
		return nil, badRequest("limit must be between 1 and 500")
	}
	q := deref(req.Params.Q)
	if len(q) > 256 {
		return nil, badRequest("q is too long")
	}
	s, err := a.connected(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	r, err := s.SearchFiles(ctx, p, q, limit)
	if err != nil {
		return nil, err
	}
	out := gen.SearchFiles200JSONResponse{Base: r.Base, Truncated: r.Truncated, Results: make([]gen.FileMatch, 0, len(r.Results))}
	for _, m := range r.Results {
		out.Results = append(out.Results, gen.FileMatch{Path: m.Path, Name: m.Name, IsDir: m.IsDir})
	}
	return out, nil
}

func (a *API) scriptRunner() *scripts.Runner {
	if a.Projects != nil && a.Projects.Scripts != nil {
		return a.Projects.Scripts
	}
	return &scripts.Runner{Apps: a.Apps}
}

func apiScript(dir string, s scripts.Script, live map[string]tunnel.ServiceState) gen.ProjectScript {
	out := gen.ProjectScript{
		Id: s.ID, Name: s.Name, Command: s.Command, Icon: gen.ProjectScriptIcon(s.Icon),
		RunOnWorktreeCreate: s.RunOnWorktreeCreate, Async: s.IsAsync(), StopOnSettle: s.StopOnSettle, RunOnSettle: s.RunOnSettle,
		PreviewUrl: optString(s.PreviewURL), AutoOpenPreview: s.AutoOpenPreview, LongRunning: s.LongRunning(),
	}
	if s.LongRunning() {
		name := scripts.ServiceName(dir, s)
		out.Service = &name
		if st, ok := live[name]; ok {
			out.Status = apiServiceStatus(st)
		}
	}
	return out
}

// GetProjectScripts implements gen.StrictServerInterface.
func (a *API) GetProjectScripts(ctx context.Context, req gen.GetProjectScriptsRequestObject) (gen.GetProjectScriptsResponseObject, error) {
	dir, err := cleanRelPath("path", deref(req.Params.Path))
	if err != nil {
		return nil, err
	}
	s, err := a.connected(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.GetProjectScripts200JSONResponse{Path: dir, Scripts: []gen.ProjectScript{}}
	cfg, err := scripts.Load(ctx, s, dir)
	if errors.Is(err, tunnel.ErrTooOld) || errors.Is(err, tunnel.ErrNotConnected) {
		return nil, err
	}
	if cfg.Source != "" {
		src := gen.ProjectScriptsSource(cfg.Source)
		out.Source = &src
	}
	if err != nil {
		msg := err.Error()
		out.Error = &msg
		return out, nil
	}
	out.IconPath = optString(cfg.IconPath)
	if cfg.DefaultThreadEnvMode != "" {
		m := gen.ProjectScriptsDefaultThreadEnvMode(cfg.DefaultThreadEnvMode)
		out.DefaultThreadEnvMode = &m
	}
	var live map[string]tunnel.ServiceState
	if a.Apps != nil {
		live, _, _ = a.Apps.Status(ctx, req.AgentId)
	}
	for _, sc := range cfg.Scripts {
		out.Scripts = append(out.Scripts, apiScript(dir, sc, live))
	}
	return out, nil
}

// RunProjectScript implements gen.StrictServerInterface.
func (a *API) RunProjectScript(ctx context.Context, req gen.RunProjectScriptRequestObject) (gen.RunProjectScriptResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Name) == "" {
		return nil, badRequest("name is required")
	}
	dir, err := cleanRelPath("path", deref(req.Body.Path))
	if err != nil {
		return nil, err
	}
	// A script runs any command: the same power as a terminal.
	s, ag, err := a.connectedAgent(ctx, req.AgentId, authz.AgentTerminal)
	if err != nil {
		return nil, err
	}
	cfg, err := scripts.Load(ctx, s, dir)
	if err != nil {
		if errors.Is(err, tunnel.ErrTooOld) {
			return nil, err
		}
		return nil, badRequest(err.Error())
	}
	sc, ok := cfg.Find(req.Body.Name)
	if !ok {
		return nil, errStatus(404, "no script "+req.Body.Name+" in "+dirName(dir))
	}
	res, err := a.scriptRunner().Run(ctx, s, req.AgentId, dir, sc)
	details := map[string]any{"path": dir, "script": sc.Name, "command": sc.Command}
	if err != nil {
		details["error"] = err.Error()
		a.record(ctx, ag, audit.ScriptRun, sc.Name, details)
		return nil, err
	}
	out := gen.RunProjectScript200JSONResponse{Mode: gen.ProjectScriptRunModeExec}
	if res.Service != "" {
		out.Mode = gen.ProjectScriptRunModeService
		out.Service = &res.Service
		details["service"] = res.Service
	} else {
		out.ExitCode, out.Output, out.Truncated, out.TimedOut = &res.ExitCode, &res.Output, &res.Truncated, &res.TimedOut
		details["exitCode"] = res.ExitCode
	}
	a.record(ctx, ag, audit.ScriptRun, sc.Name, details)
	var live map[string]tunnel.ServiceState
	if res.Service != "" && a.Apps != nil {
		live, _, _ = a.Apps.Status(ctx, req.AgentId)
	}
	out.Script = apiScript(dir, sc, live)
	return out, nil
}

func dirName(dir string) string {
	if dir == "" {
		return "the home directory"
	}
	return dir
}
