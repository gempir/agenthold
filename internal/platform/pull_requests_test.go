package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/pr/prtest"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

type repoWorkspace struct{ remote string }

func (w repoWorkspace) Git(_ context.Context, _ string, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if req.Action != tunnel.GitStatus {
		return tunnel.GitReply{}, errors.New("unexpected " + req.Action)
	}
	return tunnel.GitReply{Repo: &tunnel.GitRepo{Path: req.Path, Project: "r", Branch: "feat", RemoteURL: w.remote}}, nil
}

func TestPullRequestTools(t *testing.T) {
	p, ag := newExposureService(t, "")
	ctx := context.Background()
	gh := prtest.NewGitHub(t)
	gh.Add(prtest.PR{Repo: "o/r", Number: 3, Title: "Three", HeadRef: "feat", HeadSHA: "h3", Body: "The body"})
	gh.Add(prtest.PR{Repo: "o/r", Number: 4, Title: "Four", HeadRef: "four", HeadSHA: "h4"})
	gh.SetChecks("h3", prtest.CheckRun{Name: "ci", Status: "completed", Conclusion: "failure"})
	gh.AddComment("o/r", 3, prtest.Comment{Author: "bob", Body: "nit"})
	gh.SetFiles("o/r", 3, map[string]any{"filename": "a.go", "status": "added", "additions": 1, "patch": "@@ -0,0 +1 @@\n+x"})

	box, _ := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	gitSvc := &git.Service{Store: p.Store, Box: box, Audit: &audit.Log{Store: p.Store}}
	if _, err := gitSvc.UpsertCredential(ctx, "usr_1", "github.com", "", "ghp_test", ""); err != nil {
		t.Fatal(err)
	}
	p.PullRequests = &pr.Service{
		Store: p.Store, Tokens: gitSvc, Audit: &audit.Log{Store: p.Store},
		Workspace: repoWorkspace{remote: "git@github.com:o/r.git"},
		Providers: func(string) pr.Provider { return &pr.GitHub{Host: "github.com", API: gh.URL} },
	}
	for _, s := range []struct{ id, kind, origin string }{{"ses_1", "", ""}, {"ses_sub", session.ParentKindSubthread, "ses_1"}} {
		if err := p.Store.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: s.id, AgentID: ag, Title: s.id, CreatedBy: "usr_1", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		_ = p.Store.SetSessionGit(ctx, db.SetSessionGitParams{Cwd: "projects/r", GitRepoPath: "projects/r", ID: s.id})
		_ = p.Store.SetSessionParentKind(ctx, db.SetSessionParentKindParams{ParentKind: s.kind, ID: s.id})
		_ = p.Store.SetSessionOrigin(ctx, db.SetSessionOriginParams{OriginAgentID: "", OriginSessionID: s.origin, ID: s.id})
	}
	callIn := func(sess, tool string, args map[string]any) (string, error) {
		b, _ := json.Marshal(args)
		out, err := p.Call(ctx, ag, sess, tool, b)
		s, _ := out.(string)
		return s, err
	}

	tools, _ := p.Call(ctx, ag, "", "_list_tools", nil)
	b, _ := json.Marshal(tools)
	for _, name := range []string{"link_pull_request", "watch_pull_request", "list_pull_requests", "get_pull_request"} {
		if !strings.Contains(string(b), `"`+name+`"`) {
			t.Fatalf("%s not listed", name)
		}
	}

	// Link by number: the thread's repository is o/r (from its remote).
	out, err := callIn("ses_1", "link_pull_request", map[string]any{"number": 3})
	if err != nil || !strings.Contains(out, "Linked o/r#3") {
		t.Fatalf("link: %q %v", out, err)
	}
	// Watch without arguments: the only linked pull request.
	out, err = callIn("ses_1", "watch_pull_request", nil)
	if err != nil || !strings.Contains(out, "Watching o/r#3") {
		t.Fatalf("watch: %q %v", out, err)
	}
	// Watching another links it first; then there are two to choose from.
	if out, err = callIn("ses_1", "watch_pull_request", map[string]any{"url_or_repo": "https://github.com/o/r/pull/4"}); err != nil || !strings.Contains(out, "Watching o/r#4") {
		t.Fatalf("watch 4: %q %v", out, err)
	}
	if _, err = callIn("ses_1", "watch_pull_request", map[string]any{"watch": false}); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous: %v", err)
	}
	if out, err = callIn("ses_1", "watch_pull_request", map[string]any{"url_or_repo": "o/r#4", "watch": false}); err != nil || !strings.Contains(out, "Stopped watching o/r#4") {
		t.Fatalf("unwatch: %q %v", out, err)
	}
	rows, _ := p.Store.ListSessionPullRequests(ctx, "ses_1")
	watching := map[int64]int64{}
	for _, r := range rows {
		watching[r.Number] = r.Watching
	}
	if len(rows) != 2 || watching[3] != 1 || watching[4] != 0 {
		t.Fatalf("links: %+v", rows)
	}
	// Sub-threads cannot watch; their parent does.
	if _, err = callIn("ses_sub", "watch_pull_request", map[string]any{"url_or_repo": "o/r#3"}); !errors.Is(err, pr.ErrSubthread) || !strings.Contains(err.Error(), "ses_1") {
		t.Fatalf("sub-thread: %v", err)
	}
	if out, err = callIn("ses_sub", "link_pull_request", map[string]any{"url_or_repo": "o/r#3"}); err != nil {
		t.Fatalf("sub-thread link: %q %v", out, err)
	}

	out, err = callIn("ses_1", "list_pull_requests", map[string]any{"state": "open"})
	if err != nil || !strings.Contains(out, `"Three"`) || !strings.Contains(out, `"Four"`) || !strings.Contains(out, "linkedToThisThread") {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, err = callIn("ses_1", "list_pull_requests", map[string]any{"repo": "nope"}); err == nil {
		t.Fatal("bad repo listed")
	}

	out, err = callIn("ses_1", "get_pull_request", map[string]any{"repo": "o/r", "number": 3, "diff": true})
	if err != nil {
		t.Fatal(err)
	}
	var d pr.Detail
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if d.Body != "The body" || d.ChecksState != pr.ChecksFailure || len(d.Comments) != 1 || len(d.Files) != 1 || d.Files[0].Patch == "" {
		t.Fatalf("detail: %+v", d)
	}
	if out, _ = callIn("ses_1", "get_pull_request", map[string]any{"repo": "o/r", "number": 3}); strings.Contains(out, "@@ -0,0") {
		t.Fatalf("patch without diff: %s", out)
	}

	// Without a credential the owner is told where to add one.
	p.PullRequests.Tokens = noTokens{}
	if _, err = callIn("ses_1", "get_pull_request", map[string]any{"repo": "o/r", "number": 3}); err == nil || !strings.Contains(err.Error(), "Settings → Git") {
		t.Fatalf("no credential: %v", err)
	}
}

type noTokens struct{}

func (noTokens) Token(context.Context, string, string) (string, error) { return "", git.ErrNotFound }
