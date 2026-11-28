package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/pr/prtest"
)

// githubToken gives c's user the fake GitHub's token.
func (e *env) githubToken(c *client) {
	e.t.Helper()
	if _, err := e.git.UpsertCredential(e.t.Context(), c.me().Id, "github.com", "", e.prGitHub.Token, ""); err != nil {
		e.t.Fatal(err)
	}
}

func TestPullRequestHub(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	e.githubToken(alice)
	gh := e.prGitHub
	gh.Add(prtest.PR{Repo: "o/demo", Number: 1, Title: "Add login", HeadRef: "login", HeadSHA: "sha1", Body: "Adds a login page"})
	gh.Add(prtest.PR{Repo: "o/demo", Number: 2, Title: "Fix typo", HeadRef: "typo", HeadSHA: "sha2"})
	gh.Add(prtest.PR{Repo: "o/demo", Number: 3, Title: "Old", Merged: true})
	gh.SetChecks("sha1", prtest.CheckRun{Name: "build", Status: "completed", Conclusion: "success"})
	gh.AddComment("o/demo", 1, prtest.Comment{Author: "rev", Body: "LGTM?"})
	gh.SetFiles("o/demo", 1, map[string]any{"filename": "login.go", "status": "added", "additions": 10, "patch": "@@ -0,0 +1 @@\n+package login"})

	var list gen.PullRequestList
	expect(t, "bob lists", bob.do("GET", "/pull-requests", nil, &list), 200)
	if len(list.Hosts) != 0 || len(list.PullRequests) != 0 {
		t.Fatalf("without a credential: %+v", list)
	}
	expect(t, "alice lists", alice.do("GET", "/pull-requests", nil, &list), 200)
	if len(list.Hosts) != 1 || len(list.PullRequests) != 2 || list.PullRequests[0].Number != 2 || len(list.Errors) != 0 {
		t.Fatalf("open: %+v", list)
	}
	expect(t, "merged", alice.do("GET", "/pull-requests?state=merged&filter=authored", nil, &list), 200)
	if len(list.PullRequests) != 1 || list.PullRequests[0].State != gen.PullRequestStateMerged {
		t.Fatalf("merged: %+v", list)
	}
	expect(t, "bad state", alice.do("GET", "/pull-requests?state=nope", nil, nil), 400)

	var d gen.PullRequestDetail
	expect(t, "detail", alice.do("GET", "/pull-requests/o/demo/1", nil, &d), 200)
	if d.Body != "Adds a login page" || d.ChecksState != gen.PullRequestChecksStateSuccess || len(d.Comments) != 1 ||
		len(d.Files) != 1 || d.Files[0].Status != gen.PullRequestFileStatusA || d.Files[0].Patch == nil || deref(d.PullRequest.HeadSha) != "sha1" {
		t.Fatalf("detail: %+v", d)
	}
	expect(t, "missing", alice.do("GET", "/pull-requests/o/demo/99", nil, nil), 404)
	expect(t, "bob has no credential", bob.do("GET", "/pull-requests/o/demo/1", nil, nil), 409)
	expect(t, "unsupported host", alice.do("GET", "/pull-requests/o/demo/1?host=gitlab.com", nil, nil), 400)
	expect(t, "bad repo", alice.do("GET", "/pull-requests/o/..%2Fx/1", nil, nil), 400)

	expect(t, "comment", alice.do("POST", "/pull-requests/o/demo/1/comments", map[string]any{"body": "Thanks!"}, nil), 204)
	expect(t, "approve", alice.do("POST", "/pull-requests/o/demo/1/comments", map[string]any{"event": "approve"}, nil), 204)
	expect(t, "empty comment", alice.do("POST", "/pull-requests/o/demo/1/comments", map[string]any{"body": ""}, nil), 400)

	expect(t, "merge a moved head", alice.do("POST", "/pull-requests/o/demo/1/merge", map[string]any{"method": "squash", "headSha": "stale"}, nil), 409)
	expect(t, "merge without head", alice.do("POST", "/pull-requests/o/demo/1/merge", map[string]any{"method": "squash"}, nil), 400)
	if len(gh.MergesSoFar()) != 0 {
		t.Fatal("merged a moved head")
	}
	var res gen.PullRequestMergeResult
	expect(t, "merge", alice.do("POST", "/pull-requests/o/demo/1/merge", map[string]any{"method": "squash", "headSha": "sha1"}, &res), 200)
	if !res.Merged || gh.MergesSoFar()[0].Method != "squash" {
		t.Fatalf("merge: %+v %+v", res, gh.MergesSoFar())
	}
}

func TestSessionPullRequestsAndWatcher(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	e.githubToken(alice)
	gh := e.prGitHub
	gh.Add(prtest.PR{Repo: "o/demo", Number: 2, Title: "Watch me", HeadRef: "w", HeadSHA: "w1"})
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	base := "/sessions/" + sess.Id + "/pull-requests"

	expect(t, "link nothing", alice.do("POST", base, map[string]any{}, nil), 400)
	expect(t, "link unknown", alice.do("POST", base, map[string]any{"url": "o/demo#404"}, nil), 404)
	expect(t, "stranger links", bob.do("POST", base, map[string]any{"url": "o/demo#2"}, nil), 404)
	var link gen.SessionPullRequest
	expect(t, "link", alice.do("POST", base, map[string]any{"url": "https://github.com/o/demo/pull/2"}, &link), 201)
	if link.Repo != "o/demo" || link.Number != 2 || link.Title != "Watch me" || link.Watching || link.ChecksState != gen.PullRequestChecksStateNone {
		t.Fatalf("link: %+v", link)
	}
	var linked struct{ PullRequests []gen.SessionPullRequest }
	expect(t, "list", alice.do("GET", base, nil, &linked), 200)
	if len(linked.PullRequests) != 1 || linked.PullRequests[0].Id != link.Id {
		t.Fatalf("linked: %+v", linked)
	}
	expect(t, "stranger lists", bob.do("GET", base, nil, nil), 404)
	expect(t, "watch", alice.do("PATCH", base+"/"+link.Id, map[string]any{"watching": true}, &link), 200)
	if !link.Watching || link.LastCheckedAt == nil {
		t.Fatalf("watching: %+v", link)
	}
	expect(t, "watch unknown", alice.do("PATCH", base+"/spr_nope", map[string]any{"watching": true}, nil), 404)

	// Someone reviews: the watcher wakes the thread with a prompt.
	w := &pr.Watcher{Service: e.prs}
	if res, err := w.Once(t.Context()); err != nil || res.Woken != 0 {
		t.Fatalf("quiet poll: %+v %v", res, err)
	}
	gh.AddComment("o/demo", 2, prtest.Comment{Kind: pr.KindReview, Author: "rev", Body: "Please add tests", State: "CHANGES_REQUESTED"})
	st := e.stream(alice, sess.Id, 0)
	if res, err := w.Once(t.Context()); err != nil || res.Woken != 1 {
		t.Fatalf("review poll: %+v %v", res, err)
	}
	evs := st.until(isType("turn_end"))
	var prompt string
	for _, ev := range evs {
		if ev.Type == "prompt" {
			prompt, _ = ev.Data["text"].(string)
			if u, _ := ev.Data["user"].(map[string]any); u["name"] != pr.WatcherName {
				t.Fatalf("prompted by %v", u)
			}
		}
	}
	if !strings.Contains(prompt, "@rev (review, changes requested): Please add tests") {
		t.Fatalf("wake prompt: %q", prompt)
	}
	e.sessions.Wait()

	// Merged: watching stops and the thread settles.
	gh.Update("o/demo", 2, func(p *prtest.PR) { p.Merged = true })
	if res, err := w.Once(t.Context()); err != nil || res.Settled != 1 {
		t.Fatalf("merge poll: %+v %v", res, err)
	}
	var got gen.Session
	expect(t, "session", alice.do("GET", "/sessions/"+sess.Id, nil, &got), 200)
	if got.SettledAt == nil {
		t.Fatalf("merged pull request's thread not settled: %+v", got)
	}
	expect(t, "list", alice.do("GET", base, nil, &linked), 200)
	if l := linked.PullRequests[0]; l.Watching || l.State != gen.PullRequestStateMerged || l.WakeCount != 1 {
		t.Fatalf("after merge: %+v", l)
	}
	expect(t, "watch a merged one", alice.do("PATCH", base+"/"+link.Id, map[string]any{"watching": true}, nil), 400)

	expect(t, "stranger unlinks", bob.do("DELETE", base+"/"+link.Id, nil, nil), 404)
	expect(t, "unlink", alice.do("DELETE", base+"/"+link.Id, nil, nil), 204)
	expect(t, "unlink again", alice.do("DELETE", base+"/"+link.Id, nil, nil), 404)
}

func TestWorkOnAndCreatePullRequest(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	e.githubToken(alice)
	e.githubToken(bob)
	ag := e.fakeAgent(alice, "")
	url, work := bareRepo(t)
	bare := strings.TrimPrefix(url, "file://")
	runGit(t, work, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-q", "-m", "feature")
	runGit(t, work, "push", "-q", "origin", "feature")
	e.prGitHub.Add(prtest.PR{Repo: "o/demo", Number: 1, Title: "Feature", HeadRef: "feature", HeadSHA: "f1", CloneURL: url})

	expect(t, "bob works on alice's agent", bob.do("POST", "/pull-requests/o/demo/1/work", map[string]any{"agentId": ag.Id}, nil), 404)
	var wk gen.PullRequestWork
	expect(t, "work", alice.do("POST", "/pull-requests/o/demo/1/work", map[string]any{"agentId": ag.Id}, &wk), 201)
	if wk.Worktree != ".worktrees/demo/pr-1" || wk.Session.Git == nil || wk.Session.Git.Branch != "feature" ||
		wk.Session.Cwd == nil || *wk.Session.Cwd != wk.Worktree || wk.PullRequest.Number != 1 || wk.Session.Title != "PR #1: Feature" {
		t.Fatalf("work: %+v", wk)
	}
	if b, err := os.ReadFile(filepath.Join(e.home(ag.Id), wk.Worktree, "feature.txt")); err != nil || string(b) != "feature\n" {
		t.Fatalf("worktree content: %q %v", b, err)
	}
	expect(t, "work twice", alice.do("POST", "/pull-requests/o/demo/1/work", map[string]any{"agentId": ag.Id}, nil), 400)

	// Create: the thread's branch is pushed and a pull request opened.
	home := e.home(ag.Id)
	repo := filepath.Join(home, "projects/demo")
	runGit(t, repo, "checkout", "-q", "-b", "fix")
	if err := os.WriteFile(filepath.Join(repo, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "fix")
	// The checkout's remote is on GitHub; pushes go to the bare repository.
	runGit(t, repo, "remote", "set-url", "origin", "https://github.com/o/demo.git")
	runGit(t, repo, "remote", "set-url", "--push", "origin", bare)
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	expect(t, "cwd", alice.do("PATCH", "/sessions/"+sess.Id+"/git", map[string]any{"cwd": "projects/demo"}, &sess), 200)
	var link gen.SessionPullRequest
	expect(t, "create", alice.do("POST", "/sessions/"+sess.Id+"/pull-requests", map[string]any{
		"create": map[string]any{"title": "Fix it", "body": "Because"}, "watch": true,
	}, &link), 201)
	if link.Repo != "o/demo" || link.Number != 2 || link.Title != "Fix it" || !link.Watching {
		t.Fatalf("created: %+v", link)
	}
	if c := e.prGitHub.Created; len(c) != 1 || c[0]["head"] != "fix" || c[0]["base"] != "main" || c[0]["body"] != "Because" {
		t.Fatalf("created at GitHub: %+v", c)
	}
	if out := runGit(t, bare, "branch", "--list", "fix"); !strings.Contains(out, "fix") {
		t.Fatalf("branch not pushed: %q", out)
	}
	expect(t, "create and url", alice.do("POST", "/sessions/"+sess.Id+"/pull-requests", map[string]any{
		"create": map[string]any{}, "url": "o/demo#1",
	}, nil), 400)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
