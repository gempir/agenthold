package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/store/db"
)

// Pull request hub: pull requests on the users' git hosts, read and acted on
// with their own credentials (on the server, never in a sandbox), and the
// pull requests linked to threads.

var errPullRequestsUnavailable = errStatus(http.StatusServiceUnavailable, "pull requests are not available on this server")

// linkRefreshAge is how old a linked pull request's state may be before a
// thread's list re-reads it.
const linkRefreshAge = time.Minute

func prError(err error) error {
	var ae *pr.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pr.ErrInvalid), errors.Is(err, pr.ErrUnsupportedHost), errors.Is(err, git.ErrInvalid):
		return badRequest(err.Error())
	case errors.Is(err, pr.ErrNotFound):
		return errStatus(http.StatusNotFound, "pull request not found")
	case errors.Is(err, pr.ErrNoCredential), errors.Is(err, pr.ErrHeadMoved), errors.Is(err, pr.ErrNotMergeable),
		errors.Is(err, pr.ErrSubthread):
		return errStatus(http.StatusConflict, err.Error())
	case errors.As(err, &ae):
		return errStatus(http.StatusBadGateway, err.Error())
	}
	if strings.HasPrefix(err.Error(), "github: ") {
		return errStatus(http.StatusBadGateway, err.Error())
	}
	return err
}

func (a *API) prs() (*pr.Service, error) {
	if a.PullRequests == nil {
		return nil, errPullRequestsUnavailable
	}
	return a.PullRequests, nil
}

func checksState(s string) gen.PullRequestChecksState {
	if s == "" {
		return gen.PullRequestChecksStateNone
	}
	return gen.PullRequestChecksState(s)
}

func apiPullRequest(p pr.PullRequest) gen.PullRequestSummary {
	return gen.PullRequestSummary{
		Host: p.Host, Repo: p.Repo, Number: p.Number, Url: p.URL, Title: p.Title, State: gen.PullRequestState(p.State),
		Draft: p.Draft, Author: p.Author, HeadRef: optString(p.HeadRef), HeadSha: optString(p.HeadSHA), BaseRef: optString(p.BaseRef),
		Comments: p.Comments, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt),
	}
}

func apiPullRequestDetail(d pr.Detail) gen.PullRequestDetail {
	out := gen.PullRequestDetail{
		PullRequest: apiPullRequest(d.PullRequest), Body: d.Body, Mergeable: d.Mergeable, MergeableState: optString(d.MergeableState),
		Conflicted: d.Conflicted(), BaseSha: optString(d.BaseSHA), HeadRepo: optString(d.HeadRepo),
		Additions: d.Additions, Deletions: d.Deletions, ChangedFiles: d.ChangedFiles,
		Checks: []gen.PullRequestCheck{}, ChecksState: checksState(d.ChecksState),
		Comments: []gen.PullRequestComment{}, Files: []gen.PullRequestFile{}, Viewer: optString(d.Viewer),
	}
	for _, c := range d.Checks {
		out.Checks = append(out.Checks, gen.PullRequestCheck{Name: c.Name, Status: c.Status, Conclusion: optString(c.Conclusion), Url: optString(c.URL)})
	}
	for _, c := range d.Comments {
		gc := gen.PullRequestComment{
			Id: c.ID, Kind: gen.PullRequestCommentKind(c.Kind), Author: c.Author, Body: c.Body, State: optString(c.State),
			Path: optString(c.Path), Url: optString(c.URL), CreatedAt: ts(c.CreatedAt),
		}
		if c.Line > 0 {
			line := c.Line
			gc.Line = &line
		}
		out.Comments = append(out.Comments, gc)
	}
	for _, f := range d.Files {
		out.Files = append(out.Files, gen.PullRequestFile{
			Path: f.Path, PreviousPath: optString(f.PreviousPath), Status: gen.PullRequestFileStatus(f.Status),
			Additions: f.Additions, Deletions: f.Deletions, Patch: optString(f.Patch),
		})
	}
	return out
}

func apiSessionPullRequest(r db.SessionPullRequest) gen.SessionPullRequest {
	out := gen.SessionPullRequest{
		Id: r.ID, SessionId: r.SessionID, Host: r.Host, Repo: r.Repo, Number: int(r.Number), Url: r.Url, Title: r.Title,
		State: gen.PullRequestState(r.State), HeadSha: r.HeadSha, ChecksState: checksState(r.ChecksState),
		Watching: r.Watching == 1, WakeCount: int(r.WakeCount), FailCount: int(r.FailCount),
		CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt),
	}
	if r.LastCheckedAt.Valid {
		t := ts(r.LastCheckedAt.Int64)
		out.LastCheckedAt = &t
	}
	return out
}

// pathRef is the pull request a path names.
func (a *API) pathRef(ctx context.Context, svc *pr.Service, owner, repo string, number int, host *string) (pr.Ref, error) {
	h := strings.TrimSpace(deref(host))
	if h == "" {
		h = svc.DefaultHost(ctx, me(ctx).ID)
	} else {
		var err error
		if h, err = git.NormalizeHost(h); err != nil {
			return pr.Ref{}, badRequest(err.Error())
		}
	}
	r := owner + "/" + repo
	if !pr.ValidRepo(r) {
		return pr.Ref{}, badRequest("not a repository: " + r)
	}
	if number <= 0 {
		return pr.Ref{}, badRequest("number must be positive")
	}
	return pr.Ref{Host: h, Repo: r, Number: number}, nil
}

// ListPullRequests implements gen.StrictServerInterface.
func (a *API) ListPullRequests(ctx context.Context, req gen.ListPullRequestsRequestObject) (gen.ListPullRequestsResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(deref(req.Params.Host))
	if host != "" {
		if host, err = git.NormalizeHost(host); err != nil {
			return nil, badRequest(err.Error())
		}
	}
	opts := pr.ListOptions{State: deref(req.Params.State), Repo: strings.TrimSpace(deref(req.Params.Repo)), Query: deref(req.Params.Q)}
	if req.Params.Filter != nil {
		opts.Filter = string(*req.Params.Filter)
	}
	list, hosts, failed, err := svc.List(ctx, me(ctx).ID, host, opts)
	if err != nil {
		return nil, prError(err)
	}
	out := gen.ListPullRequests200JSONResponse{PullRequests: []gen.PullRequestSummary{}, Hosts: hosts}
	if out.Hosts == nil {
		out.Hosts = []string{}
	}
	out.Errors = []struct {
		Error string `json:"error"`
		Host  string `json:"host"`
	}{}
	for _, p := range list {
		out.PullRequests = append(out.PullRequests, apiPullRequest(p))
	}
	for _, f := range failed {
		msg := f.Err.Error()
		var e *Error
		if errors.As(prError(f.Err), &e) {
			msg = e.Msg
		}
		out.Errors = append(out.Errors, struct {
			Error string `json:"error"`
			Host  string `json:"host"`
		}{Error: msg, Host: f.Host})
	}
	return out, nil
}

// GetPullRequest implements gen.StrictServerInterface.
func (a *API) GetPullRequest(ctx context.Context, req gen.GetPullRequestRequestObject) (gen.GetPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	ref, err := a.pathRef(ctx, svc, req.Owner, req.Repo, req.Number, req.Params.Host)
	if err != nil {
		return nil, err
	}
	d, err := svc.Get(ctx, me(ctx).ID, ref, true)
	if err != nil {
		return nil, prError(err)
	}
	return gen.GetPullRequest200JSONResponse(apiPullRequestDetail(d)), nil
}

// CommentOnPullRequest implements gen.StrictServerInterface.
func (a *API) CommentOnPullRequest(ctx context.Context, req gen.CommentOnPullRequestRequestObject) (gen.CommentOnPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	ref, err := a.pathRef(ctx, svc, req.Owner, req.Repo, req.Number, req.Params.Host)
	if err != nil {
		return nil, err
	}
	in := pr.ReviewInput{Body: deref(req.Body.Body)}
	if req.Body.Event != nil {
		in.Event = string(*req.Body.Event)
	}
	if err := svc.Review(ctx, me(ctx).ID, ref, in); err != nil {
		return nil, prError(err)
	}
	return gen.CommentOnPullRequest204Response{}, nil
}

// MergePullRequest implements gen.StrictServerInterface.
func (a *API) MergePullRequest(ctx context.Context, req gen.MergePullRequestRequestObject) (gen.MergePullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	ref, err := a.pathRef(ctx, svc, req.Owner, req.Repo, req.Number, req.Params.Host)
	if err != nil {
		return nil, err
	}
	method := ""
	if req.Body.Method != nil {
		method = string(*req.Body.Method)
	}
	res, err := svc.Merge(ctx, me(ctx).ID, ref, method, req.Body.HeadSha)
	if err != nil {
		return nil, prError(err)
	}
	return gen.MergePullRequest200JSONResponse{Merged: res.Merged, Sha: optString(res.SHA), Message: optString(res.Message)}, nil
}

// WorkOnPullRequest implements gen.StrictServerInterface.
func (a *API) WorkOnPullRequest(ctx context.Context, req gen.WorkOnPullRequestRequestObject) (gen.WorkOnPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.AgentId) == "" {
		return nil, badRequest("agentId is required")
	}
	ref, err := a.pathRef(ctx, svc, req.Owner, req.Repo, req.Number, req.Params.Host)
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.Body.AgentId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	res, err := svc.Work(ctx, u.ID, acc.Row.Agent.ID, ref)
	if err != nil {
		return nil, prError(err)
	}
	if text := strings.TrimSpace(deref(req.Body.Prompt)); text != "" {
		if err := a.Sessions.Prompt(ctx, res.Session.ID, u.ID, u.Name, text, nil); err != nil {
			return nil, err
		}
		if s, err := a.Store.GetAgentSession(ctx, res.Session.ID); err == nil {
			res.Session = s
		}
	}
	g, err := a.apiSession(ctx, res.Session)
	if err != nil {
		return nil, err
	}
	return gen.WorkOnPullRequest201JSONResponse{Session: g, PullRequest: apiSessionPullRequest(res.Link), Worktree: res.Worktree}, nil
}

// ListSessionPullRequests implements gen.StrictServerInterface.
func (a *API) ListSessionPullRequests(ctx context.Context, req gen.ListSessionPullRequestsRequestObject) (gen.ListSessionPullRequestsResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionPullRequests200JSONResponse{PullRequests: []gen.SessionPullRequest{}}
	if a.PullRequests == nil {
		return out, nil
	}
	rows, err := a.PullRequests.Linked(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	rows = a.PullRequests.Refresh(ctx, me(ctx).ID, rows, linkRefreshAge)
	for _, r := range rows {
		out.PullRequests = append(out.PullRequests, apiSessionPullRequest(r))
	}
	return out, nil
}

// sessionOwner is the owner of the session's agent (whose credentials the
// watcher reads with).
func (a *API) sessionOwner(ctx context.Context, s db.AgentSession) (string, error) {
	ag, err := a.Store.GetAgent(ctx, s.AgentID)
	if err != nil {
		return "", err
	}
	return ag.OwnerID, nil
}

// LinkSessionPullRequest implements gen.StrictServerInterface.
func (a *API) LinkSessionPullRequest(ctx context.Context, req gen.LinkSessionPullRequestRequestObject) (gen.LinkSessionPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	u := me(ctx)
	var link db.SessionPullRequest
	switch b := req.Body; {
	case b.Create != nil:
		if b.Url != nil || b.Number != nil {
			return nil, badRequest("give either create or url/number")
		}
		c := b.Create
		link, _, err = svc.CreateForSession(ctx, s, u.ID, pr.CreateOptions{
			Title: deref(c.Title), Body: deref(c.Body), Base: deref(c.Base), Draft: deref(c.Draft),
			NoPush: c.Push != nil && !*c.Push,
		})
		if err != nil {
			return nil, prError(err)
		}
	default:
		if strings.TrimSpace(deref(b.Url)) == "" && b.Number == nil {
			return nil, badRequest("url or number is required (or create)")
		}
		ref, err := pr.ParseRef(deref(b.Url), deref(b.Number), svc.DefaultHost(ctx, u.ID))
		if err != nil {
			return nil, prError(err)
		}
		if link, err = svc.Link(ctx, s, u.ID, ref); err != nil {
			return nil, prError(err)
		}
	}
	if deref(req.Body.Watch) {
		owner, err := a.sessionOwner(ctx, s)
		if err != nil {
			return nil, err
		}
		if link, err = svc.SetWatching(ctx, s, owner, u.ID, link.ID, true); err != nil {
			return nil, prError(err)
		}
	}
	return gen.LinkSessionPullRequest201JSONResponse(apiSessionPullRequest(link)), nil
}

// UpdateSessionPullRequest implements gen.StrictServerInterface.
func (a *API) UpdateSessionPullRequest(ctx context.Context, req gen.UpdateSessionPullRequestRequestObject) (gen.UpdateSessionPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	owner, err := a.sessionOwner(ctx, s)
	if err != nil {
		return nil, err
	}
	link, err := svc.SetWatching(ctx, s, owner, me(ctx).ID, req.PrId, req.Body.Watching)
	if err != nil {
		return nil, prError(err)
	}
	return gen.UpdateSessionPullRequest200JSONResponse(apiSessionPullRequest(link)), nil
}

// UnlinkSessionPullRequest implements gen.StrictServerInterface.
func (a *API) UnlinkSessionPullRequest(ctx context.Context, req gen.UnlinkSessionPullRequestRequestObject) (gen.UnlinkSessionPullRequestResponseObject, error) {
	svc, err := a.prs()
	if err != nil {
		return nil, err
	}
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if err := svc.Unlink(ctx, s, me(ctx).ID, req.PrId); err != nil {
		return nil, prError(err)
	}
	return gen.UnlinkSessionPullRequest204Response{}, nil
}
