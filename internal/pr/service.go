package pr

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/project"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Tokens gives the server a user's token for a host (git.Service).
type Tokens interface {
	Token(ctx context.Context, userID, host string) (string, error)
}

// Sessions is what the hub needs of the session manager.
type Sessions interface {
	Running(sessionID string) bool
	PromptWith(ctx context.Context, sessionID, userID, userName, text string, status func(string), opts session.PromptOpts) error
	CreateWith(ctx context.Context, agentID, userID, title string, opts session.CreateOpts) (db.AgentSession, error)
}

// noticer records notices in a session's log (session.Manager).
type noticer interface {
	Notice(ctx context.Context, sessionID, message string) error
}

// Workspace runs git in an agent's running machine.
type Workspace interface {
	Git(ctx context.Context, agentID string, req tunnel.GitRequest) (tunnel.GitReply, error)
}

// MachineWorkspace reaches machines through fn.
func MachineWorkspace(fn project.MachineFunc) Workspace { return machineWorkspace(fn) }

type machineWorkspace project.MachineFunc

func (m machineWorkspace) Git(ctx context.Context, agentID string, req tunnel.GitRequest) (tunnel.GitReply, error) {
	ts, err := m(ctx, agentID)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return ts.Git(ctx, req)
}

// Service is the pull request hub.
type Service struct {
	Store  *store.Store
	Tokens Tokens
	// Providers returns the provider for a host (nil: unsupported). Default
	// DefaultProviders with no GitHub Enterprise configuration.
	Providers func(host string) Provider
	// Sessions wakes and creates threads (Watcher, Work).
	Sessions Sessions
	// Workspace runs git in agents' machines (CreateForSession, Work).
	Workspace Workspace
	Audit     *audit.Log
	Log       *slog.Logger
	Now       func() time.Time

	mu      sync.Mutex
	viewers map[string]string // user+host+token hash → login
}

// DefaultProviders speaks GitHub to github.com and the GitHub Enterprise
// Server of the GitHub connect configuration. Other hosts are not called:
// the server must not make requests to whatever host a user names.
func DefaultProviders(gh config.GitHubConnect, client *http.Client) func(host string) Provider {
	enterprise := ""
	if gh.URL != "" {
		enterprise = gh.Host()
	}
	return func(host string) Provider {
		switch {
		case host == "github.com":
			return &GitHub{Host: host, API: GitHubAPI(host), HTTP: client}
		case enterprise != "" && host == enterprise && enterprise != "github.com":
			api := gh.APIURL
			if api == "" {
				api = GitHubAPI(host)
			}
			return &GitHub{Host: host, API: api, HTTP: client}
		}
		return nil
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) provider(host string) Provider {
	if s.Providers == nil {
		return DefaultProviders(config.GitHubConnect{}, nil)(host)
	}
	return s.Providers(host)
}

// client is the provider and userID's token for host.
func (s *Service) client(ctx context.Context, userID, host string) (Provider, string, error) {
	p := s.provider(host)
	if p == nil {
		return nil, "", ErrUnsupportedHost
	}
	token, err := s.Tokens.Token(ctx, userID, host)
	if errors.Is(err, git.ErrNotFound) {
		return nil, "", ErrNoCredential
	}
	if err != nil {
		return nil, "", err
	}
	return p, token, nil
}

// Hosts lists the hosts userID stored a credential for that pull requests
// can be read on, github.com first.
func (s *Service) Hosts(ctx context.Context, userID string) ([]string, error) {
	creds, err := s.Store.ListGitCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if s.provider(c.Host) != nil {
			out = append(out, c.Host)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] == "github.com" && out[j] != "github.com" })
	return out, nil
}

// DefaultHost is the host of references that name none: github.com, or the
// user's only supported host.
func (s *Service) DefaultHost(ctx context.Context, userID string) string {
	hosts, err := s.Hosts(ctx, userID)
	if err != nil || len(hosts) == 0 {
		return "github.com"
	}
	return hosts[0]
}

// viewer is the login of userID's account on host (cached per token).
func (s *Service) viewer(ctx context.Context, p Provider, userID, host, token string) (string, error) {
	sum := sha256.Sum256([]byte(token))
	key := userID + "\x00" + host + "\x00" + hex.EncodeToString(sum[:8])
	s.mu.Lock()
	login, ok := s.viewers[key]
	s.mu.Unlock()
	if ok {
		return login, nil
	}
	login, err := p.Viewer(ctx, token)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.viewers == nil || len(s.viewers) > 1000 {
		s.viewers = map[string]string{}
	}
	s.viewers[key] = login
	s.mu.Unlock()
	return login, nil
}

// HostError is a failure to list one host's pull requests.
type HostError struct {
	Host string
	Err  error
}

// List lists userID's pull requests on every host they can read them on
// (or only host), newest activity first. Hosts that fail are reported, the
// others listed.
func (s *Service) List(ctx context.Context, userID, host string, opts ListOptions) ([]PullRequest, []string, []HostError, error) {
	hosts := []string{host}
	if host == "" {
		var err error
		if hosts, err = s.Hosts(ctx, userID); err != nil {
			return nil, nil, nil, err
		}
	}
	out := []PullRequest{}
	var failed []HostError
	for _, h := range hosts {
		p, token, err := s.client(ctx, userID, h)
		if err == nil {
			var prs []PullRequest
			if prs, err = p.List(ctx, token, opts); err == nil {
				out = append(out, prs...)
				continue
			}
		}
		if errors.Is(err, ErrInvalid) {
			return nil, nil, nil, err
		}
		failed = append(failed, HostError{Host: h, Err: err})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, hosts, failed, nil
}

// Get reads one pull request with its checks and comments (and files).
func (s *Service) Get(ctx context.Context, userID string, ref Ref, files bool) (Detail, error) {
	p, token, err := s.client(ctx, userID, ref.Host)
	if err != nil {
		return Detail{}, err
	}
	d, err := p.Get(ctx, token, ref, GetOptions{Files: files, Comments: true, Checks: true})
	if err != nil {
		return Detail{}, err
	}
	if login, err := s.viewer(ctx, p, userID, ref.Host, token); err == nil {
		d.Viewer = login
	}
	return d, nil
}

// Review comments on or reviews a pull request as userID.
func (s *Service) Review(ctx context.Context, userID string, ref Ref, in ReviewInput) error {
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" && in.Event != ReviewApprove {
		return invalid("the comment is empty")
	}
	if len(in.Body) > 65536 {
		return invalid("the comment is too long (at most 65536 bytes)")
	}
	p, token, err := s.client(ctx, userID, ref.Host)
	if err != nil {
		return err
	}
	if err := p.Review(ctx, token, ref, in); err != nil {
		return err
	}
	s.Audit.Record(ctx, audit.Entry{
		UserID: userID, Action: audit.PullRequestReview, Target: ref.String(),
		Details: map[string]any{"event": in.Event},
	})
	return nil
}

// Merge merges a pull request as userID, refusing when its head is no
// longer expectSHA (what the caller reviewed).
func (s *Service) Merge(ctx context.Context, userID string, ref Ref, method, expectSHA string) (MergeResult, error) {
	switch method {
	case "":
		method = MergeMerge
	case MergeMerge, MergeSquash, MergeRebase:
	default:
		return MergeResult{}, invalid("method must be merge, squash or rebase")
	}
	expectSHA = strings.TrimSpace(expectSHA)
	if expectSHA == "" {
		return MergeResult{}, invalid("headSha is required: the head commit you reviewed")
	}
	p, token, err := s.client(ctx, userID, ref.Host)
	if err != nil {
		return MergeResult{}, err
	}
	cur, err := p.Get(ctx, token, ref, GetOptions{})
	if err != nil {
		return MergeResult{}, err
	}
	if cur.HeadSHA != expectSHA {
		return MergeResult{}, ErrHeadMoved
	}
	if cur.State != StateOpen {
		return MergeResult{}, fmt.Errorf("%w: it is %s", ErrNotMergeable, cur.State)
	}
	res, err := p.Merge(ctx, token, ref, MergeInput{Method: method, SHA: expectSHA})
	if err != nil {
		return MergeResult{}, err
	}
	s.Audit.Record(ctx, audit.Entry{
		UserID: userID, Action: audit.PullRequestMerge, Target: ref.String(),
		Details: map[string]any{"method": method, "sha": expectSHA},
	})
	if res.Merged {
		s.refreshLinks(ctx, ref, func(r *db.SessionPullRequest) { r.State = StateMerged })
	}
	return res, nil
}

// refreshLinks updates the links of ref in every session.
func (s *Service) refreshLinks(ctx context.Context, ref Ref, change func(*db.SessionPullRequest)) {
	rows, err := s.Store.ListPullRequestSessions(ctx, db.ListPullRequestSessionsParams{Host: ref.Host, Repo: ref.Repo, Number: int64(ref.Number)})
	if err != nil {
		return
	}
	for _, r := range rows {
		change(&r)
		_ = s.Store.UpdatePullRequestInfo(ctx, db.UpdatePullRequestInfoParams{
			Title: r.Title, State: r.State, HeadSha: r.HeadSha, ChecksState: r.ChecksState, UpdatedAt: s.now().Unix(), ID: r.ID,
		})
	}
}

// Linked lists the pull requests linked to a session.
func (s *Service) Linked(ctx context.Context, sessionID string) ([]db.SessionPullRequest, error) {
	return s.Store.ListSessionPullRequests(ctx, sessionID)
}

// LinkedPR is one link of a session.
func (s *Service) LinkedPR(ctx context.Context, sessionID, prID string) (db.SessionPullRequest, error) {
	r, err := s.Store.GetSessionPullRequest(ctx, prID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.SessionID != sessionID) {
		return db.SessionPullRequest{}, ErrNotFound
	}
	return r, err
}

// ResolveRef completes a reference that names only a number with the
// repository the session works in.
func (s *Service) ResolveRef(ctx context.Context, sess db.AgentSession, ref Ref) (Ref, error) {
	if ref.Repo != "" {
		return ref, nil
	}
	repo, err := s.sessionRepo(ctx, sess)
	if err != nil {
		return Ref{}, err
	}
	host, name, err := ParseRemote(repo.RemoteURL)
	if err != nil {
		return Ref{}, err
	}
	ref.Host, ref.Repo = host, name
	return ref, nil
}

// sessionRepo is the repository the session works in.
func (s *Service) sessionRepo(ctx context.Context, sess db.AgentSession) (tunnel.GitRepo, error) {
	dir := sess.GitRepoPath
	if dir == "" {
		dir = sess.Cwd
	}
	if dir == "" {
		return tunnel.GitRepo{}, invalid("the thread works in no repository: name the pull request's repository")
	}
	if s.Workspace == nil {
		return tunnel.GitRepo{}, invalid("name the pull request's repository")
	}
	reply, err := s.Workspace.Git(ctx, sess.AgentID, tunnel.GitRequest{Action: tunnel.GitStatus, Path: dir})
	if err != nil {
		return tunnel.GitRepo{}, err
	}
	if reply.Repo == nil {
		return tunnel.GitRepo{}, invalid("the thread's directory %s is not in a git repository", dir)
	}
	return *reply.Repo, nil
}

// Link links a pull request to a session, reading it as userID.
func (s *Service) Link(ctx context.Context, sess db.AgentSession, userID string, ref Ref) (db.SessionPullRequest, error) {
	ref, err := s.ResolveRef(ctx, sess, ref)
	if err != nil {
		return db.SessionPullRequest{}, err
	}
	p, token, err := s.client(ctx, userID, ref.Host)
	if err != nil {
		return db.SessionPullRequest{}, err
	}
	d, err := p.Get(ctx, token, ref, GetOptions{Checks: true})
	if err != nil {
		return db.SessionPullRequest{}, err
	}
	return s.link(ctx, sess, userID, d.PullRequest, d.ChecksState)
}

func (s *Service) link(ctx context.Context, sess db.AgentSession, userID string, p PullRequest, checks string) (db.SessionPullRequest, error) {
	now := s.now().Unix()
	if err := s.Store.UpsertSessionPullRequest(ctx, db.UpsertSessionPullRequestParams{
		ID: id.New("spr"), SessionID: sess.ID, Host: p.Host, Repo: p.Repo, Number: int64(p.Number), Url: p.URL,
		Title: p.Title, State: p.State, HeadSha: p.HeadSHA, ChecksState: checks, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return db.SessionPullRequest{}, err
	}
	r, err := s.Store.GetSessionPullRequestByKey(ctx, db.GetSessionPullRequestByKeyParams{
		SessionID: sess.ID, Host: p.Host, Repo: p.Repo, Number: int64(p.Number),
	})
	if err != nil {
		return db.SessionPullRequest{}, err
	}
	s.Audit.Record(ctx, audit.Entry{
		AgentID: sess.AgentID, UserID: userID, Action: audit.PullRequestLink, Target: sess.ID,
		Details: map[string]any{"pullRequest": p.Ref().String()},
	})
	return r, nil
}

// Unlink removes a link.
func (s *Service) Unlink(ctx context.Context, sess db.AgentSession, userID, prID string) error {
	r, err := s.LinkedPR(ctx, sess.ID, prID)
	if err != nil {
		return err
	}
	n, err := s.Store.DeleteSessionPullRequest(ctx, db.DeleteSessionPullRequestParams{ID: prID, SessionID: sess.ID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.Audit.Record(ctx, audit.Entry{
		AgentID: sess.AgentID, UserID: userID, Action: audit.PullRequestUnlink, Target: sess.ID,
		Details: map[string]any{"pullRequest": Ref{Host: r.Host, Repo: r.Repo, Number: int(r.Number)}.String()},
	})
	return nil
}

// CanWatch reports why a session cannot watch pull requests (nil: it can).
func CanWatch(sess db.AgentSession) error {
	if sess.ParentKind == session.ParentKindSubthread || sess.ParentKind == session.ParentKindSideChat {
		if sess.OriginSessionID != "" {
			return fmt.Errorf("%w (thread %s)", ErrSubthread, sess.OriginSessionID)
		}
		return ErrSubthread
	}
	return nil
}

// SetWatching starts or stops watching a linked pull request. Starting
// takes the pull request as it is now (read with ownerID's token) as the
// baseline: the agent is woken for what happens after.
func (s *Service) SetWatching(ctx context.Context, sess db.AgentSession, ownerID, userID, prID string, watch bool) (db.SessionPullRequest, error) {
	r, err := s.LinkedPR(ctx, sess.ID, prID)
	if err != nil {
		return db.SessionPullRequest{}, err
	}
	params := db.SetPullRequestWatchingParams{UpdatedAt: s.now().Unix(), ID: r.ID}
	if watch {
		if err := CanWatch(sess); err != nil {
			return db.SessionPullRequest{}, err
		}
		if r.State == StateMerged || r.State == StateClosed {
			return db.SessionPullRequest{}, invalid("pull request #%d is %s: there is nothing left to watch", r.Number, r.State)
		}
		params.Watching = 1
		ref := Ref{Host: r.Host, Repo: r.Repo, Number: int(r.Number)}
		// The baseline is best effort: without it the first poll takes it.
		if p, token, err := s.client(ctx, ownerID, ref.Host); err == nil {
			if d, err := p.Get(ctx, token, ref, GetOptions{Checks: true, Comments: true}); err == nil {
				viewer, _ := s.viewer(ctx, p, ownerID, ref.Host, token)
				params.LastFingerprint = Observe(d, viewer).Fingerprint()
				params.LastCheckedAt = sql.NullInt64{Int64: s.now().Unix(), Valid: true}
				_ = s.Store.UpdatePullRequestInfo(ctx, db.UpdatePullRequestInfoParams{
					Title: d.Title, State: d.State, HeadSha: d.HeadSHA, ChecksState: d.ChecksState, UpdatedAt: s.now().Unix(), ID: r.ID,
				})
			} else if errors.Is(err, ErrNotFound) {
				return db.SessionPullRequest{}, err
			}
		} else if errors.Is(err, ErrNoCredential) || errors.Is(err, ErrUnsupportedHost) {
			return db.SessionPullRequest{}, fmt.Errorf("the thread's owner cannot read %s: %w", ref, err)
		}
	}
	if _, err := s.Store.SetPullRequestWatching(ctx, params); err != nil {
		return db.SessionPullRequest{}, err
	}
	action := audit.PullRequestUnwatch
	if watch {
		action = audit.PullRequestWatch
	}
	s.Audit.Record(ctx, audit.Entry{
		AgentID: sess.AgentID, UserID: userID, Action: action, Target: sess.ID,
		Details: map[string]any{"pullRequest": Ref{Host: r.Host, Repo: r.Repo, Number: int(r.Number)}.String()},
	})
	return s.Store.GetSessionPullRequest(ctx, r.ID)
}

// Refresh re-reads linked pull requests (as userID) whose info is older than
// maxAge, best effort, and returns the links as they are then.
func (s *Service) Refresh(ctx context.Context, userID string, rows []db.SessionPullRequest, maxAge time.Duration) []db.SessionPullRequest {
	now := s.now()
	for i, r := range rows {
		if now.Sub(time.Unix(r.UpdatedAt, 0)) < maxAge || r.State == StateMerged || r.State == StateClosed {
			continue
		}
		p, token, err := s.client(ctx, userID, r.Host)
		if err != nil {
			continue
		}
		d, err := p.Get(ctx, token, Ref{Host: r.Host, Repo: r.Repo, Number: int(r.Number)}, GetOptions{Checks: true})
		if err != nil {
			continue
		}
		r.Title, r.State, r.HeadSha, r.ChecksState, r.UpdatedAt = d.Title, d.State, d.HeadSHA, d.ChecksState, now.Unix()
		if err := s.Store.UpdatePullRequestInfo(ctx, db.UpdatePullRequestInfoParams{
			Title: r.Title, State: r.State, HeadSha: r.HeadSha, ChecksState: r.ChecksState, UpdatedAt: r.UpdatedAt, ID: r.ID,
		}); err == nil {
			rows[i] = r
		}
	}
	return rows
}

// CreateOptions open a pull request for a session's branch.
type CreateOptions struct {
	Title, Body, Base string
	Draft             bool
	// NoPush skips pushing the branch first.
	NoPush bool
}

// CreateForSession pushes the branch the session works on and opens a pull
// request for it (as userID), linking it to the session. A pull request
// already open for the branch is linked instead.
func (s *Service) CreateForSession(ctx context.Context, sess db.AgentSession, userID string, o CreateOptions) (db.SessionPullRequest, bool, error) {
	repo, err := s.sessionRepo(ctx, sess)
	if err != nil {
		return db.SessionPullRequest{}, false, err
	}
	if repo.Detached || repo.Branch == "" {
		return db.SessionPullRequest{}, false, invalid("the repository is not on a branch")
	}
	host, name, err := ParseRemote(repo.RemoteURL)
	if err != nil {
		return db.SessionPullRequest{}, false, err
	}
	p, token, err := s.client(ctx, userID, host)
	if err != nil {
		return db.SessionPullRequest{}, false, err
	}
	if !o.NoPush {
		if _, err := s.Workspace.Git(ctx, sess.AgentID, tunnel.GitRequest{Action: tunnel.GitPush, Path: repo.Path}); err != nil {
			return db.SessionPullRequest{}, false, fmt.Errorf("push %s: %w", repo.Branch, err)
		}
	}
	title := strings.TrimSpace(o.Title)
	if title == "" {
		title = sess.Title
	}
	if title == "" || session.IsPlaceholderTitle(title) {
		title = repo.Branch
	}
	created, existing, err := p.Create(ctx, token, CreateInput{
		Repo: name, Head: repo.Branch, Base: strings.TrimSpace(o.Base), Title: title, Body: o.Body, Draft: o.Draft,
	})
	if err != nil {
		return db.SessionPullRequest{}, false, err
	}
	if !existing {
		s.Audit.Record(ctx, audit.Entry{
			AgentID: sess.AgentID, UserID: userID, Action: audit.PullRequestCreate, Target: created.Ref().String(),
			Details: map[string]any{"session": sess.ID, "branch": repo.Branch},
		})
	}
	r, err := s.link(ctx, sess, userID, created, ChecksPending)
	return r, existing, err
}

// WorkResult is the thread "Work on PR" started.
type WorkResult struct {
	Session db.AgentSession
	Link    db.SessionPullRequest
	// Worktree is the directory (relative to the home) the thread works in.
	Worktree string
}

// Work starts a thread of agentID (as userID) on a pull request's branch: it
// clones the repository when the agent has no checkout of it, fetches, adds
// a worktree on the branch and links the pull request to the new thread.
func (s *Service) Work(ctx context.Context, userID, agentID string, ref Ref) (WorkResult, error) {
	if s.Workspace == nil || s.Sessions == nil {
		return WorkResult{}, errors.New("working on pull requests is not available")
	}
	d, err := s.Get(ctx, userID, ref, false)
	if err != nil {
		return WorkResult{}, err
	}
	if d.State == StateMerged || d.State == StateClosed {
		return WorkResult{}, invalid("pull request #%d is %s", d.Number, d.State)
	}
	if d.HeadRepo != "" && !strings.EqualFold(d.HeadRepo, d.Repo) {
		return WorkResult{}, invalid("pull request #%d comes from the fork %s: forks are not supported yet", d.Number, d.HeadRepo)
	}
	list, err := s.Workspace.Git(ctx, agentID, tunnel.GitRequest{Action: tunnel.GitList})
	if err != nil {
		return WorkResult{}, err
	}
	var main *tunnel.GitRepo
	for i, r := range list.Repos {
		if r.IsWorktree {
			continue
		}
		if sameRepo(r.RemoteURL, d.BaseCloneURL, ref) {
			main = &list.Repos[i]
			break
		}
	}
	if main == nil {
		clone := d.BaseCloneURL
		if clone == "" {
			clone = "https://" + ref.Host + "/" + ref.Repo + ".git"
		}
		reply, err := s.Workspace.Git(ctx, agentID, tunnel.GitRequest{Action: tunnel.GitClone, URL: clone})
		if err != nil {
			return WorkResult{}, fmt.Errorf("clone %s: %w", ref.Repo, err)
		}
		if reply.Repo == nil {
			return WorkResult{}, errors.New("the machine did not describe the clone")
		}
		main = reply.Repo
	} else if _, err := s.Workspace.Git(ctx, agentID, tunnel.GitRequest{Action: tunnel.GitPull, Path: main.Path, FetchOnly: true}); err != nil {
		return WorkResult{}, fmt.Errorf("fetch %s: %w", ref.Repo, err)
	}
	wt := path.Join(".worktrees", path.Base(main.Path), fmt.Sprintf("pr-%d", d.Number))
	reply, err := s.Workspace.Git(ctx, agentID, tunnel.GitRequest{
		Action: tunnel.GitWorktreeAdd, Path: main.Path, Branch: d.HeadRef, Create: true, FromRef: "origin/" + d.HeadRef, WorktreePath: wt,
	})
	if err != nil && strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "worktree:") {
		// The branch exists in the checkout already: use it.
		reply, err = s.Workspace.Git(ctx, agentID, tunnel.GitRequest{
			Action: tunnel.GitWorktreeAdd, Path: main.Path, Branch: d.HeadRef, WorktreePath: wt,
		})
	}
	if err != nil {
		return WorkResult{}, fmt.Errorf("worktree for %s: %w", d.HeadRef, err)
	}
	if reply.Repo == nil {
		return WorkResult{}, errors.New("the machine did not describe the worktree")
	}
	w := reply.Repo
	sess, err := s.Sessions.CreateWith(ctx, agentID, userID, fmt.Sprintf("PR #%d: %s", d.Number, d.Title), session.CreateOpts{
		Cwd: w.Path, GitProject: w.Project, GitBranch: w.Branch, GitRepoPath: w.Path,
	})
	if err != nil {
		return WorkResult{}, err
	}
	link, err := s.link(ctx, sess, userID, d.PullRequest, d.ChecksState)
	if err != nil {
		return WorkResult{}, err
	}
	return WorkResult{Session: sess, Link: link, Worktree: w.Path}, nil
}

// sameRepo reports whether a checkout's remote is the pull request's repository.
func sameRepo(remote, cloneURL string, ref Ref) bool {
	if remote == "" {
		return false
	}
	if cloneURL != "" && strings.TrimSuffix(remote, ".git") == strings.TrimSuffix(cloneURL, ".git") {
		return true
	}
	host, repo, err := ParseRemote(remote)
	return err == nil && host == ref.Host && strings.EqualFold(repo, ref.Repo)
}
