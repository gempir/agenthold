package pr_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/pr/prtest"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

func TestParseRef(t *testing.T) {
	for _, c := range []struct {
		in     string
		number int
		want   pr.Ref
	}{
		{"https://github.com/o/r/pull/12", 0, pr.Ref{Host: "github.com", Repo: "o/r", Number: 12}},
		{"https://github.com/o/r/pull/12/files", 0, pr.Ref{Host: "github.com", Repo: "o/r", Number: 12}},
		{"https://ghe.example.com/team/app/pull/3", 0, pr.Ref{Host: "ghe.example.com", Repo: "team/app", Number: 3}},
		{"https://gitlab.com/group/proj/-/merge_requests/9", 0, pr.Ref{Host: "gitlab.com", Repo: "group/proj", Number: 9}},
		{"o/r#5", 0, pr.Ref{Host: "github.com", Repo: "o/r", Number: 5}},
		{"ghe.example.com/o/r#5", 0, pr.Ref{Host: "ghe.example.com", Repo: "o/r", Number: 5}},
		{"o/r", 7, pr.Ref{Host: "github.com", Repo: "o/r", Number: 7}},
		{"#8", 0, pr.Ref{Host: "github.com", Number: 8}},
		{"", 9, pr.Ref{Host: "github.com", Number: 9}},
	} {
		got, err := pr.ParseRef(c.in, c.number, "github.com")
		if err != nil || got != c.want {
			t.Errorf("ParseRef(%q, %d) = %+v, %v; want %+v", c.in, c.number, got, err, c.want)
		}
	}
	for _, bad := range []string{"o/r", "https://github.com/o/r/issues/1", "../x#1", "a b#1", "o/r/s/t#1"} {
		if _, err := pr.ParseRef(bad, 0, "github.com"); !errors.Is(err, pr.ErrInvalid) {
			t.Errorf("ParseRef(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/o/r.git":            "github.com o/r",
		"https://x-access-token@github.com/o/r": "github.com o/r",
		"git@github.com:o/r.git":                "github.com o/r",
		"ssh://git@ghe.example.com/team/app":    "ghe.example.com team/app",
	} {
		host, repo, err := pr.ParseRemote(in)
		if err != nil || host+" "+repo != want {
			t.Errorf("ParseRemote(%q) = %q %q %v", in, host, repo, err)
		}
	}
	for _, bad := range []string{"", "/srv/git/demo.git", "file:///tmp/demo.git"} {
		if _, _, err := pr.ParseRemote(bad); err == nil {
			t.Errorf("ParseRemote(%q) succeeded", bad)
		}
	}
}

func TestChecksVerdict(t *testing.T) {
	done := func(c string) pr.Check { return pr.Check{Name: c, Status: "completed", Conclusion: c} }
	for want, checks := range map[string][]pr.Check{
		pr.ChecksNone:    nil,
		pr.ChecksSuccess: {done("success"), done("skipped"), done("neutral")},
		pr.ChecksPending: {done("success"), {Name: "x", Status: "in_progress"}},
		pr.ChecksFailure: {{Name: "x", Status: "in_progress"}, done("failure")},
	} {
		if got := pr.ChecksVerdict(checks); got != want {
			t.Errorf("verdict(%v) = %q, want %q", checks, got, want)
		}
	}
}

// fakeSessions records the watcher's wakes.
type fakeSessions struct {
	mu      sync.Mutex
	running map[string]bool
	busy    bool
	prompts []string
	notices []string
	created []session.CreateOpts
	st      *store.Store
}

func (f *fakeSessions) Running(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[id]
}

func (f *fakeSessions) PromptWith(_ context.Context, sessionID, _, userName, text string, _ func(string), _ session.PromptOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return session.ErrBusy
	}
	if userName != pr.WatcherName {
		return errors.New("unexpected prompter " + userName)
	}
	f.prompts = append(f.prompts, sessionID+": "+text)
	return nil
}

func (f *fakeSessions) CreateWith(ctx context.Context, agentID, userID, title string, o session.CreateOpts) (db.AgentSession, error) {
	f.mu.Lock()
	f.created = append(f.created, o)
	f.mu.Unlock()
	id := "ses_work"
	if err := f.st.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: id, AgentID: agentID, Title: title, CreatedBy: userID, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		return db.AgentSession{}, err
	}
	return f.st.GetAgentSession(ctx, id)
}

func (f *fakeSessions) Notice(_ context.Context, sessionID, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, sessionID+": "+msg)
	return nil
}

func (f *fakeSessions) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.prompts
	f.prompts = nil
	return p
}

// fakeWorkspace answers git requests like a machine with one checkout.
type fakeWorkspace struct {
	mu     sync.Mutex
	repos  []tunnel.GitRepo
	status tunnel.GitRepo
	calls  []tunnel.GitRequest
	// branchExists makes creating the worktree's branch fail.
	branchExists bool
}

func (w *fakeWorkspace) Git(_ context.Context, _ string, req tunnel.GitRequest) (tunnel.GitReply, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, req)
	switch req.Action {
	case tunnel.GitStatus, tunnel.GitPush:
		r := w.status
		return tunnel.GitReply{Repo: &r}, nil
	case tunnel.GitList:
		return tunnel.GitReply{Repos: w.repos}, nil
	case tunnel.GitClone:
		r := tunnel.GitRepo{Path: "projects/r", Project: "r", RemoteURL: req.URL, Branch: "main"}
		return tunnel.GitReply{Repo: &r}, nil
	case tunnel.GitPull:
		return tunnel.GitReply{Repo: &w.repos[0]}, nil
	case tunnel.GitWorktreeAdd:
		if req.Create && w.branchExists {
			return tunnel.GitReply{}, errors.New("fatal: a branch named '" + req.Branch + "' already exists")
		}
		r := tunnel.GitRepo{Path: req.WorktreePath, Project: "r", Branch: req.Branch, IsWorktree: true}
		return tunnel.GitReply{Repo: &r}, nil
	}
	return tunnel.GitReply{}, errors.New("unexpected " + req.Action)
}

type fixture struct {
	st   *store.Store
	gh   *prtest.GitHub
	svc  *pr.Service
	ses  *fakeSessions
	ws   *fakeWorkspace
	now  time.Time
	sess db.AgentSession
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _ := secrets.NewBox(bytes.Repeat([]byte{7}, secrets.KeySize))
	gitSvc := &git.Service{Store: st, Box: box, Audit: &audit.Log{Store: st}}
	for _, u := range []string{"u1", "u2"} {
		if err := st.CreateUser(ctx, db.CreateUserParams{ID: u, Name: u, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitSvc.UpsertCredential(ctx, "u1", "github.com", "", "ghp_test", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: "ag1", OwnerID: "u1", Name: "a", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: "ses1", AgentID: "ag1", Title: "Fix login", CreatedBy: "u1", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionGit(ctx, db.SetSessionGitParams{Cwd: "projects/r", GitProject: "r", GitBranch: "feat", GitRepoPath: "projects/r", ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, gh: prtest.NewGitHub(t), now: time.Unix(2_000_000_000, 0)}
	f.ses = &fakeSessions{running: map[string]bool{}, st: st}
	f.ws = &fakeWorkspace{
		status: tunnel.GitRepo{Path: "projects/r", Project: "r", Branch: "feat", RemoteURL: "https://github.com/o/r.git"},
		repos:  []tunnel.GitRepo{{Path: "projects/r", Project: "r", Branch: "main", RemoteURL: "https://github.com/o/r.git"}},
	}
	f.svc = &pr.Service{
		Store: st, Tokens: gitSvc, Sessions: f.ses, Workspace: f.ws, Audit: &audit.Log{Store: st},
		Providers: func(host string) pr.Provider {
			if host != "github.com" {
				return nil
			}
			return &pr.GitHub{Host: host, API: f.gh.URL}
		},
		Now: func() time.Time { return f.now },
	}
	f.sess, _ = st.GetAgentSession(ctx, "ses1")
	return f
}

func TestListAndDetail(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 1, Title: "Older", HeadRef: "a", HeadSHA: "s1"})
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 2, Title: "Newer", HeadRef: "b", HeadSHA: "s2", Body: "Fixes it"})
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 3, Title: "Done", Merged: true})
	f.gh.SetChecks("s2", prtest.CheckRun{Name: "lint", Status: "completed", Conclusion: "success"},
		prtest.CheckRun{Name: "test", Status: "completed", Conclusion: "failure"})
	f.gh.AddComment("o/r", 2, prtest.Comment{Author: "bob", Body: "Looks off"})
	f.gh.AddComment("o/r", 2, prtest.Comment{Kind: pr.KindReview, Author: "carol", Body: "Please fix", State: "CHANGES_REQUESTED"})
	f.gh.AddComment("o/r", 2, prtest.Comment{Kind: pr.KindReviewComment, Author: "carol", Body: "Typo", Path: "a.go", Line: 4})
	f.gh.SetFiles("o/r", 2, map[string]any{"filename": "a.go", "status": "modified", "additions": 3, "deletions": 1, "patch": "@@ -1 +1 @@\n-a\n+b"})

	list, hosts, failed, err := f.svc.List(ctx, "u1", "", pr.ListOptions{})
	if err != nil || len(failed) != 0 || len(hosts) != 1 || hosts[0] != "github.com" {
		t.Fatalf("list: %v %v %v", hosts, failed, err)
	}
	if len(list) != 2 || list[0].Number != 2 || list[1].Number != 1 || list[0].Repo != "o/r" {
		t.Fatalf("open list = %+v", list)
	}
	if merged, _, _, _ := f.svc.List(ctx, "u1", "", pr.ListOptions{State: pr.StateMerged}); len(merged) != 1 || merged[0].State != pr.StateMerged {
		t.Fatalf("merged list = %+v", merged)
	}
	if _, _, _, err := f.svc.List(ctx, "u1", "", pr.ListOptions{State: "weird"}); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("bad state: %v", err)
	}
	// No credential: nothing to list on no host.
	if list, hosts, _, err := f.svc.List(ctx, "u2", "", pr.ListOptions{}); err != nil || len(list) != 0 || len(hosts) != 0 {
		t.Fatalf("u2 list = %v %v %v", list, hosts, err)
	}

	d, err := f.svc.Get(ctx, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != "Fixes it" || d.ChecksState != pr.ChecksFailure || len(d.Checks) != 2 || d.Viewer != "me" ||
		len(d.Comments) != 3 || len(d.Files) != 1 || d.Files[0].Status != "M" || d.HeadRef != "b" {
		t.Fatalf("detail = %+v", d)
	}
	if _, err := f.svc.Get(ctx, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 99}, false); !errors.Is(err, pr.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := f.svc.Get(ctx, "u2", pr.Ref{Host: "github.com", Repo: "o/r", Number: 2}, false); !errors.Is(err, pr.ErrNoCredential) {
		t.Fatalf("no credential: %v", err)
	}
	if _, err := f.svc.Get(ctx, "u1", pr.Ref{Host: "gitlab.com", Repo: "o/r", Number: 2}, false); !errors.Is(err, pr.ErrUnsupportedHost) {
		t.Fatalf("unsupported: %v", err)
	}

	if err := f.svc.Review(ctx, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 2}, pr.ReviewInput{Body: "Thanks"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Review(ctx, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 2}, pr.ReviewInput{Event: pr.ReviewApprove}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Review(ctx, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 2}, pr.ReviewInput{Body: " "}); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("empty comment: %v", err)
	}
	if len(f.gh.Reviews) != 2 || f.gh.Reviews[0]["kind"] != "comment" || f.gh.Reviews[1]["event"] != "APPROVE" {
		t.Fatalf("reviews = %+v", f.gh.Reviews)
	}
}

func TestMergeRefusesAMovedHead(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	ref := pr.Ref{Host: "github.com", Repo: "o/r", Number: 4}
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 4, Title: "Ship", HeadRef: "ship", HeadSHA: "new"})
	if _, err := f.svc.Merge(ctx, "u1", ref, pr.MergeSquash, "old"); !errors.Is(err, pr.ErrHeadMoved) {
		t.Fatalf("moved head: %v", err)
	}
	if _, err := f.svc.Merge(ctx, "u1", ref, pr.MergeSquash, ""); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("no head: %v", err)
	}
	if _, err := f.svc.Merge(ctx, "u1", ref, "octopus", "new"); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("bad method: %v", err)
	}
	if len(f.gh.MergesSoFar()) != 0 {
		t.Fatal("merged despite refusals")
	}
	link, err := f.svc.Link(ctx, f.sess, "u1", ref)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Merge(ctx, "u1", ref, pr.MergeSquash, "new")
	if err != nil || !res.Merged {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if m := f.gh.MergesSoFar(); len(m) != 1 || m[0].Method != "squash" || m[0].SHA != "new" {
		t.Fatalf("merges = %+v", m)
	}
	if r, _ := f.st.GetSessionPullRequest(ctx, link.ID); r.State != pr.StateMerged {
		t.Fatalf("link after merge: %+v", r)
	}
	if _, err := f.svc.Merge(ctx, "u1", ref, pr.MergeMerge, "new"); !errors.Is(err, pr.ErrNotMergeable) {
		t.Fatalf("merge twice: %v", err)
	}
}

func TestLinkWatchAndUnlink(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 5, Title: "Five", HeadRef: "feat", HeadSHA: "h5"})
	// A bare number resolves against the thread's repository.
	ref, _ := pr.ParseRef("5", 0, "github.com")
	link, err := f.svc.Link(ctx, f.sess, "u1", ref)
	if err != nil || link.Repo != "o/r" || link.Number != 5 || link.Title != "Five" || link.Watching != 0 {
		t.Fatalf("link = %+v %v", link, err)
	}
	again, err := f.svc.Link(ctx, f.sess, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 5})
	if err != nil || again.ID != link.ID {
		t.Fatalf("relink = %+v %v", again, err)
	}
	w, err := f.svc.SetWatching(ctx, f.sess, "u1", "u1", link.ID, true)
	if err != nil || w.Watching != 1 || w.LastFingerprint == "" || !w.LastCheckedAt.Valid {
		t.Fatalf("watch = %+v %v", w, err)
	}
	// Sub-threads do not watch.
	sub := f.sess
	sub.ID, sub.ParentKind, sub.OriginSessionID = "ses_sub", session.ParentKindSubthread, "ses1"
	if err := f.st.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: sub.ID, AgentID: "ag1", Title: "sub", CreatedBy: "u1", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	subLink, err := f.svc.Link(ctx, sub, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetWatching(ctx, sub, "u1", "u1", subLink.ID, true); !errors.Is(err, pr.ErrSubthread) || !strings.Contains(err.Error(), "ses1") {
		t.Fatalf("sub-thread watch: %v", err)
	}
	// Another session's link is not found through this one.
	if _, err := f.svc.SetWatching(ctx, f.sess, "u1", "u1", subLink.ID, false); !errors.Is(err, pr.ErrNotFound) {
		t.Fatalf("foreign link: %v", err)
	}
	if err := f.svc.Unlink(ctx, f.sess, "u1", link.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.svc.Linked(ctx, f.sess.ID); len(rows) != 0 {
		t.Fatalf("after unlink: %+v", rows)
	}
}

func TestCreateForSession(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	link, existing, err := f.svc.CreateForSession(ctx, f.sess, "u1", pr.CreateOptions{Body: "Why"})
	if err != nil || existing || link.Repo != "o/r" || link.Title != "Fix login" || link.State != pr.StateOpen {
		t.Fatalf("create = %+v %v %v", link, existing, err)
	}
	if len(f.gh.Created) != 1 || f.gh.Created[0]["head"] != "feat" || f.gh.Created[0]["base"] != "main" {
		t.Fatalf("created = %+v", f.gh.Created)
	}
	if f.ws.calls[1].Action != tunnel.GitPush {
		t.Fatalf("calls = %+v", f.ws.calls)
	}
	// One already open for the branch is linked instead.
	again, existing, err := f.svc.CreateForSession(ctx, f.sess, "u1", pr.CreateOptions{Title: "Again", NoPush: true})
	if err != nil || !existing || again.ID != link.ID {
		t.Fatalf("create again = %+v %v %v", again, existing, err)
	}
	f.ws.status.Detached = true
	if _, _, err := f.svc.CreateForSession(ctx, f.sess, "u1", pr.CreateOptions{}); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("detached: %v", err)
	}
}

func TestWorkOnPullRequest(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 6, Title: "Six", HeadRef: "six", HeadSHA: "h6"})
	f.ws.branchExists = true
	res, err := f.svc.Work(ctx, "u1", "ag1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 6})
	if err != nil {
		t.Fatal(err)
	}
	if res.Worktree != ".worktrees/r/pr-6" || res.Link.Number != 6 || res.Session.Title != "PR #6: Six" {
		t.Fatalf("work = %+v", res)
	}
	if o := f.ses.created[0]; o.Cwd != ".worktrees/r/pr-6" || o.GitBranch != "six" {
		t.Fatalf("created with %+v", o)
	}
	var actions []string
	for _, c := range f.ws.calls {
		actions = append(actions, c.Action)
	}
	if got := strings.Join(actions, ","); got != "list,pull,worktree_add,worktree_add" {
		t.Fatalf("git calls = %s", got)
	}
	// Forks are refused before touching the machine.
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 7, Title: "Fork", HeadRef: "x", HeadSHA: "h7", HeadRepo: "someone/r"})
	if _, err := f.svc.Work(ctx, "u1", "ag1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 7}); !errors.Is(err, pr.ErrInvalid) {
		t.Fatalf("fork: %v", err)
	}
	// Without a checkout the repository is cloned.
	f.ws.repos = nil
	f.ws.branchExists = false
	f.ws.calls = nil
	_ = f.st.DeleteAgentSession(ctx, "ses_work")
	if _, err := f.svc.Work(ctx, "u1", "ag1", pr.Ref{Host: "github.com", Repo: "o/r", Number: 6}); err != nil {
		t.Fatal(err)
	}
	if f.ws.calls[1].Action != tunnel.GitClone || f.ws.calls[1].URL != "https://github.com/o/r.git" {
		t.Fatalf("calls = %+v", f.ws.calls)
	}
}

func TestDefaultProviders(t *testing.T) {
	providers := pr.DefaultProviders(config.GitHubConnect{URL: "https://ghe.example.com", APIURL: "https://ghe.example.com/api/v3"}, nil)
	if g, ok := providers("github.com").(*pr.GitHub); !ok || g.API != "https://api.github.com" {
		t.Fatalf("github.com: %+v", g)
	}
	if g, ok := providers("ghe.example.com").(*pr.GitHub); !ok || g.API != "https://ghe.example.com/api/v3" {
		t.Fatalf("enterprise: %+v", g)
	}
	// The server calls no host it was not configured for.
	for _, host := range []string{"github.internal.example", "gitlab.com", "example.com"} {
		if p := providers(host); p != nil {
			t.Fatalf("%s has a provider", host)
		}
	}
}
