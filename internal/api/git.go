package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

var errGitUnavailable = errStatus(http.StatusServiceUnavailable, "git settings are not available on this server")

// gitPushTimeout bounds pushing a changed commit identity to one machine.
var gitPushTimeout = 3 * time.Second

func gitError(err error) error {
	switch {
	case errors.Is(err, git.ErrInvalid):
		return badRequest(err.Error())
	case errors.Is(err, git.ErrNotFound):
		return errNotFound
	case errors.Is(err, git.ErrFlowNotFound):
		return errStatus(http.StatusNotFound, err.Error())
	case errors.Is(err, git.ErrGitHubDisabled):
		return errStatus(http.StatusServiceUnavailable, err.Error())
	}
	return err
}

// githubError turns a failure talking to GitHub into a 502 the user can read.
func githubError(err error) error {
	if errors.Is(err, git.ErrFlowNotFound) || errors.Is(err, git.ErrGitHubDisabled) || errors.Is(err, git.ErrInvalid) {
		return gitError(err)
	}
	return errStatus(http.StatusBadGateway, err.Error())
}

func apiGitCredential(c git.Credential) gen.GitCredential {
	out := gen.GitCredential{
		Id: c.ID, Host: c.Host, Username: c.Username, Label: c.Label, Source: gen.GitCredentialSource(c.Source),
		CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt),
	}
	if c.Account != "" {
		out.Account = &c.Account
	}
	if c.Scopes != "" {
		out.Scopes = &c.Scopes
	}
	if c.ExpiresAt > 0 {
		e := ts(c.ExpiresAt)
		out.ExpiresAt = &e
	}
	return out
}

func apiGitRepo(r tunnel.GitRepo) gen.GitRepo {
	out := gen.GitRepo{
		Path: r.Path, Project: r.Project, Branch: r.Branch, Detached: r.Detached, Dirty: r.Dirty,
		Ahead: r.Ahead, Behind: r.Behind, IsWorktree: r.IsWorktree,
	}
	if r.RemoteURL != "" {
		u := r.RemoteURL
		out.RemoteUrl = &u
	}
	if r.MainPath != "" {
		p := r.MainPath
		out.MainPath = &p
	}
	return out
}

// cleanRelPath validates a path relative to the home: no NUL, not absolute,
// not leaving the home. "" and "." are the home itself ("").
func cleanRelPath(field, p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsRune(p, 0) {
		return "", badRequest(field + " cannot contain NUL bytes")
	}
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", badRequest(field + " must be relative to the home directory")
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", badRequest(field + " must stay inside the home directory")
	}
	if p == "." {
		return "", nil
	}
	return p, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// GetGitSettings implements gen.StrictServerInterface.
func (a *API) GetGitSettings(ctx context.Context, _ gen.GetGitSettingsRequestObject) (gen.GetGitSettingsResponseObject, error) {
	g, err := a.gitSettings(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetGitSettings200JSONResponse(g), nil
}

func (a *API) gitSettings(ctx context.Context) (gen.GitSettings, error) {
	if a.Git == nil {
		return gen.GitSettings{}, errGitUnavailable
	}
	s, err := a.Git.GetSettings(ctx, *me(ctx))
	if err != nil {
		return gen.GitSettings{}, err
	}
	out := gen.GitSettings{
		AuthorName: s.AuthorName, AuthorEmail: s.AuthorEmail,
		DefaultAuthorName: s.DefaultAuthorName, DefaultAuthorEmail: s.DefaultAuthorEmail,
		Credentials: []gen.GitCredential{},
		Github:      gen.GitHubConnectInfo{},
	}
	if gh := a.Git.GitHub; gh != nil && gh.Config.Enabled() {
		out.Github = gen.GitHubConnectInfo{Enabled: true, Host: gh.Config.Host()}
	}
	for _, c := range s.Credentials {
		out.Credentials = append(out.Credentials, apiGitCredential(c))
	}
	return out, nil
}

// UpdateGitSettings implements gen.StrictServerInterface.
func (a *API) UpdateGitSettings(ctx context.Context, req gen.UpdateGitSettingsRequestObject) (gen.UpdateGitSettingsResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	u := me(ctx)
	if err := a.Git.UpdateConfig(ctx, u.ID, req.Body.AuthorName, req.Body.AuthorEmail); err != nil {
		return nil, gitError(err)
	}
	a.pushGitIdentity(ctx, u.ID)
	g, err := a.gitSettings(ctx)
	if err != nil {
		return nil, err
	}
	return gen.UpdateGitSettings200JSONResponse(g), nil
}

// pushGitIdentity writes the user's commit identity into their running
// machines (booting ones get it with their boot files). Best effort.
func (a *API) pushGitIdentity(ctx context.Context, userID string) {
	rows, err := a.Store.ListUserAgents(ctx, userID)
	if err != nil {
		return
	}
	for _, r := range rows {
		ag := r.Agent
		if ag.OwnerID != userID {
			continue
		}
		if ok, err := a.Node.Connected(ctx, ag.ID); err != nil || !ok {
			continue
		}
		name, email, err := a.Git.Identity(ctx, ag.ID)
		if err != nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, gitPushTimeout)
		_, err = node.Client(a.Node, ag.ID).Git(pctx, tunnel.GitRequest{Action: tunnel.GitConfig, AuthorName: name, AuthorEmail: email})
		cancel()
		if err != nil && !errors.Is(err, tunnel.ErrTooOld) {
			slog.WarnContext(ctx, "push the git identity", "agent", ag.ID, "err", err)
		}
	}
}

// UpsertGitCredential implements gen.StrictServerInterface.
func (a *API) UpsertGitCredential(ctx context.Context, req gen.UpsertGitCredentialRequestObject) (gen.UpsertGitCredentialResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	b := req.Body
	c, err := a.Git.UpsertCredential(ctx, me(ctx).ID, b.Host, deref(b.Username), b.Token, deref(b.Label))
	if err != nil {
		return nil, gitError(err)
	}
	return gen.UpsertGitCredential200JSONResponse(apiGitCredential(c)), nil
}

// DeleteGitCredential implements gen.StrictServerInterface.
func (a *API) DeleteGitCredential(ctx context.Context, req gen.DeleteGitCredentialRequestObject) (gen.DeleteGitCredentialResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	if err := a.Git.DeleteCredential(ctx, me(ctx).ID, req.CredentialId); err != nil {
		return nil, gitError(err)
	}
	return gen.DeleteGitCredential204Response{}, nil
}

// StartGitHubConnect implements gen.StrictServerInterface.
func (a *API) StartGitHubConnect(ctx context.Context, _ gen.StartGitHubConnectRequestObject) (gen.StartGitHubConnectResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	f, err := a.Git.StartGitHub(ctx, me(ctx).ID)
	if err != nil {
		return nil, githubError(err)
	}
	return gen.StartGitHubConnect201JSONResponse{
		Id: f.ID, UserCode: f.UserCode, VerificationUri: f.VerificationURI, ExpiresAt: ts(f.ExpiresAt), Interval: f.Interval,
	}, nil
}

// PollGitHubConnect implements gen.StrictServerInterface.
func (a *API) PollGitHubConnect(ctx context.Context, req gen.PollGitHubConnectRequestObject) (gen.PollGitHubConnectResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	r, err := a.Git.PollGitHub(ctx, me(ctx).ID, req.FlowId)
	if err != nil {
		return nil, githubError(err)
	}
	out := gen.PollGitHubConnect200JSONResponse{Status: gen.GitHubDevicePollStatus(r.Status)}
	if r.Credential != nil {
		c := apiGitCredential(*r.Credential)
		out.Credential = &c
	}
	return out, nil
}

// CancelGitHubConnect implements gen.StrictServerInterface.
func (a *API) CancelGitHubConnect(ctx context.Context, req gen.CancelGitHubConnectRequestObject) (gen.CancelGitHubConnectResponseObject, error) {
	if a.Git == nil {
		return nil, errGitUnavailable
	}
	a.Git.CancelGitHub(me(ctx).ID, req.FlowId)
	return gen.CancelGitHubConnect204Response{}, nil
}

// ListAgentGitRepos implements gen.StrictServerInterface.
func (a *API) ListAgentGitRepos(ctx context.Context, req gen.ListAgentGitReposRequestObject) (gen.ListAgentGitReposResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	out := gen.ListAgentGitRepos200JSONResponse{Repos: []gen.GitRepo{}}
	s, err := a.session(ctx, req.AgentId)
	if errors.Is(err, tunnel.ErrNotConnected) || errors.Is(err, sandbox.ErrUnreachable) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	reply, err := s.Git(ctx, tunnel.GitRequest{Action: tunnel.GitList})
	if err != nil {
		return nil, err
	}
	out.Connected = true
	for _, r := range reply.Repos {
		out.Repos = append(out.Repos, apiGitRepo(r))
	}
	return out, nil
}

// GetAgentGitStatus implements gen.StrictServerInterface.
func (a *API) GetAgentGitStatus(ctx context.Context, req gen.GetAgentGitStatusRequestObject) (gen.GetAgentGitStatusResponseObject, error) {
	p, err := cleanRelPath("path", deref(req.Params.Path))
	if err != nil {
		return nil, err
	}
	s, err := a.connected(ctx, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	reply, err := s.Git(ctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: p})
	if err != nil {
		return nil, err
	}
	out := gen.GetAgentGitStatus200JSONResponse{Branches: []string{}}
	if reply.Repo != nil {
		r := apiGitRepo(*reply.Repo)
		out.Repo = &r
	}
	out.Branches = append(out.Branches, reply.Branches...)
	return out, nil
}

// gitAction runs a changing git action in the agent's running machine and
// records it.
func (a *API) gitAction(ctx context.Context, agentID, action string, req tunnel.GitRequest, details map[string]any) (tunnel.GitRepo, error) {
	s, ag, err := a.connectedAgent(ctx, agentID, authz.AgentPrompt)
	if err != nil {
		return tunnel.GitRepo{}, err
	}
	reply, err := s.Git(ctx, req)
	if err != nil {
		return tunnel.GitRepo{}, err
	}
	if reply.Repo == nil {
		return tunnel.GitRepo{}, errStatus(http.StatusBadGateway, "the sandbox did not describe the repository")
	}
	if details == nil {
		details = map[string]any{}
	}
	details["path"] = reply.Repo.Path
	details["branch"] = reply.Repo.Branch
	a.record(ctx, ag, action, reply.Repo.Path, details)
	return *reply.Repo, nil
}

// CloneAgentGitRepo implements gen.StrictServerInterface.
func (a *API) CloneAgentGitRepo(ctx context.Context, req gen.CloneAgentGitRepoRequestObject) (gen.CloneAgentGitRepoResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Url) == "" {
		return nil, badRequest("url is required")
	}
	p, err := cleanRelPath("path", deref(req.Body.Path))
	if err != nil {
		return nil, err
	}
	remote := strings.TrimSpace(req.Body.Url)
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitClone, tunnel.GitRequest{
		Action: tunnel.GitClone, URL: remote, Path: p, Branch: strings.TrimSpace(deref(req.Body.Branch)),
	}, map[string]any{"url": redactURL(remote)})
	if err != nil {
		return nil, err
	}
	return gen.CloneAgentGitRepo201JSONResponse(apiGitRepo(repo)), nil
}

// redactURL drops credentials from a URL for the audit log.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// CheckoutAgentGitBranch implements gen.StrictServerInterface.
func (a *API) CheckoutAgentGitBranch(ctx context.Context, req gen.CheckoutAgentGitBranchRequestObject) (gen.CheckoutAgentGitBranchResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Branch) == "" {
		return nil, badRequest("branch is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitCheckout, tunnel.GitRequest{
		Action: tunnel.GitCheckout, Path: p, Branch: strings.TrimSpace(req.Body.Branch),
		Create: deref(req.Body.Create), FromRef: strings.TrimSpace(deref(req.Body.FromRef)),
	}, map[string]any{"create": deref(req.Body.Create)})
	if err != nil {
		return nil, err
	}
	a.noteBranch(ctx, req.AgentId, repo)
	return gen.CheckoutAgentGitBranch200JSONResponse(apiGitRepo(repo)), nil
}

// noteBranch records the branch a repository is on in the agent's sessions
// that work in it.
func (a *API) noteBranch(ctx context.Context, agentID string, repo tunnel.GitRepo) {
	if err := a.Store.UpdateAgentRepoBranch(ctx, db.UpdateAgentRepoBranchParams{
		GitBranch: repo.Branch, AgentID: agentID, GitRepoPath: repo.Path,
	}); err != nil {
		slog.WarnContext(ctx, "record the branch in sessions", "agent", agentID, "err", err)
	}
}

// CreateAgentGitWorktree implements gen.StrictServerInterface.
func (a *API) CreateAgentGitWorktree(ctx context.Context, req gen.CreateAgentGitWorktreeRequestObject) (gen.CreateAgentGitWorktreeResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Branch) == "" {
		return nil, badRequest("branch is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	wt, err := cleanRelPath("worktreePath", deref(req.Body.WorktreePath))
	if err != nil {
		return nil, err
	}
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitWorktreeAdd, tunnel.GitRequest{
		Action: tunnel.GitWorktreeAdd, Path: p, Branch: strings.TrimSpace(req.Body.Branch),
		Create: deref(req.Body.Create), FromRef: strings.TrimSpace(deref(req.Body.FromRef)), WorktreePath: wt,
	}, map[string]any{"repo": p})
	if err != nil {
		return nil, err
	}
	return gen.CreateAgentGitWorktree201JSONResponse(apiGitRepo(repo)), nil
}

// PullAgentGitRepo implements gen.StrictServerInterface.
func (a *API) PullAgentGitRepo(ctx context.Context, req gen.PullAgentGitRepoRequestObject) (gen.PullAgentGitRepoResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitPull, tunnel.GitRequest{
		Action: tunnel.GitPull, Path: p, FetchOnly: deref(req.Body.FetchOnly),
	}, map[string]any{"fetchOnly": deref(req.Body.FetchOnly)})
	if err != nil {
		return nil, err
	}
	return gen.PullAgentGitRepo200JSONResponse(apiGitRepo(repo)), nil
}

// PushAgentGitRepo implements gen.StrictServerInterface.
func (a *API) PushAgentGitRepo(ctx context.Context, req gen.PushAgentGitRepoRequestObject) (gen.PushAgentGitRepoResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitPush, tunnel.GitRequest{
		Action: tunnel.GitPush, Path: p, Force: deref(req.Body.Force),
	}, map[string]any{"force": deref(req.Body.Force)})
	if err != nil {
		return nil, err
	}
	return gen.PushAgentGitRepo200JSONResponse(apiGitRepo(repo)), nil
}

// RemoveAgentGitWorktree implements gen.StrictServerInterface.
func (a *API) RemoveAgentGitWorktree(ctx context.Context, req gen.RemoveAgentGitWorktreeRequestObject) (gen.RemoveAgentGitWorktreeResponseObject, error) {
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	p, err := cleanRelPath("path", req.Body.Path)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return nil, badRequest("path must name a worktree")
	}
	repo, err := a.gitAction(ctx, req.AgentId, audit.GitWorktreeRemove, tunnel.GitRequest{
		Action: tunnel.GitWorktreeRemove, Path: p, Force: deref(req.Body.Force),
	}, map[string]any{"worktree": p, "force": deref(req.Body.Force)})
	if err != nil {
		return nil, err
	}
	return gen.RemoveAgentGitWorktree200JSONResponse(apiGitRepo(repo)), nil
}

// UpdateSessionGit implements gen.StrictServerInterface.
func (a *API) UpdateSessionGit(ctx context.Context, req gen.UpdateSessionGitRequestObject) (gen.UpdateSessionGitResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	refresh := deref(req.Body.Refresh)
	setCwd := req.Body.Cwd != nil
	cwd := s.Cwd
	if setCwd {
		if cwd, err = cleanRelPath("cwd", *req.Body.Cwd); err != nil {
			return nil, err
		}
		if cwd != s.Cwd && a.Sessions.Running(s.ID) {
			return nil, errStatus(http.StatusConflict, "the session is running a turn; change its working directory once it is done")
		}
	}
	if refresh || setCwd {
		project, branch, repoPath := s.GitProject, s.GitBranch, s.GitRepoPath
		if setCwd && cwd == "" && !refresh {
			project, branch, repoPath = "", "", ""
		} else {
			target := cwd
			if target == "" {
				target = s.GitRepoPath
			}
			ts, err := a.session(ctx, s.AgentID)
			switch {
			case err == nil:
				reply, err := ts.Git(ctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: target})
				if err != nil {
					return nil, err
				}
				if r := reply.Repo; r != nil {
					project, branch, repoPath = r.Project, r.Branch, r.Path
				} else {
					project, branch, repoPath = "", "", ""
				}
			case refresh:
				return nil, err
			case errors.Is(err, tunnel.ErrNotConnected), errors.Is(err, sandbox.ErrUnreachable):
				// Not running: the working directory changes, and what
				// it holds is detected after the next turn.
				if cwd != s.Cwd {
					project, branch, repoPath = "", "", ""
				}
			default:
				return nil, err
			}
		}
		if err := a.Store.SetSessionGit(ctx, db.SetSessionGitParams{
			Cwd: cwd, GitProject: project, GitBranch: branch, GitRepoPath: repoPath, ID: s.ID,
		}); err != nil {
			return nil, err
		}
		s.Cwd, s.GitProject, s.GitBranch, s.GitRepoPath = cwd, project, branch, repoPath
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSessionGit200JSONResponse(g), nil
}
