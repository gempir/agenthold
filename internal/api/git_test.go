package api_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
)

// runGit runs git in dir with a fixed identity and no user or system config.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareRepo makes a bare repository with one commit on main and returns its
// file:// URL and a work tree that pushes to it.
func bareRepo(t *testing.T) (url, work string) {
	t.Helper()
	base := t.TempDir()
	work = filepath.Join(base, "work")
	bare := filepath.Join(base, "demo.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-q", "-m", "initial")
	runGit(t, base, "clone", "-q", "--bare", work, bare)
	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "fetch", "-q", "origin")
	return "file://" + bare, work
}

// cloneDemo clones a fresh bare repository into projects/demo of the agent.
func (e *env) cloneDemo(c *client, agentID string) (gen.GitRepo, string) {
	e.t.Helper()
	url, work := bareRepo(e.t)
	var repo gen.GitRepo
	expect(e.t, "clone", c.do("POST", "/agents/"+agentID+"/git/clone", map[string]string{"url": url}, &repo), 201)
	return repo, work
}

func (e *env) prompt(c *client, sessionID, text string) []gen.SessionEvent {
	e.t.Helper()
	// Only this turn's events: the stream starts after the log's end.
	var log []gen.SessionEvent
	expect(e.t, "events", c.do("GET", "/sessions/"+sessionID+"/events?limit=5000", nil, &log), 200)
	var last int64
	if len(log) > 0 {
		last = log[len(log)-1].Seq
	}
	st := e.stream(c, sessionID, last)
	expect(e.t, "prompt "+text, c.do("POST", "/sessions/"+sessionID+"/prompt", map[string]string{"text": text}, nil), 202)
	evs := st.until(isType("turn_end"))
	if evs[0].Type != "prompt" || evs[0].Data["text"] != text {
		e.t.Fatalf("turn of %q starts with %+v", text, evs[0])
	}
	if end := evs[len(evs)-1]; end.Data["stopReason"] != "end_turn" {
		e.t.Fatalf("turn of %q ended with %v: %+v", text, end.Data, evs)
	}
	return evs
}

func (e *env) getSession(c *client, id string) gen.Session {
	e.t.Helper()
	var s gen.Session
	expect(e.t, "get session", c.do("GET", "/sessions/"+id, nil, &s), 200)
	return s
}

func sessionGit(s gen.Session) (project, branch, repoPath string) {
	if s.Git == nil {
		return "", "", ""
	}
	if s.Git.RepoPath != nil {
		repoPath = *s.Git.RepoPath
	}
	return s.Git.Project, s.Git.Branch, repoPath
}

func TestGitSettings(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")

	var g gen.GitSettings
	expect(t, "get", alice.do("GET", "/me/git", nil, &g), 200)
	if g.AuthorName != "" || g.AuthorEmail != "" || g.DefaultAuthorName != "Alice" || g.DefaultAuthorEmail != "a@example.com" || g.Credentials == nil || len(g.Credentials) != 0 {
		t.Fatalf("defaults %+v", g)
	}

	// The identity is validated and pushed into the running machine.
	expect(t, "bad email", alice.do("PUT", "/me/git", map[string]string{"authorName": "A", "authorEmail": "not an <email>"}, nil), 400)
	expect(t, "line break", alice.do("PUT", "/me/git", map[string]string{"authorName": "A\nB", "authorEmail": ""}, nil), 400)
	expect(t, "put", alice.do("PUT", "/me/git", map[string]string{"authorName": " Alice Coder ", "authorEmail": "alice@code.example"}, &g), 200)
	if g.AuthorName != "Alice Coder" || g.AuthorEmail != "alice@code.example" {
		t.Fatalf("after put %+v", g)
	}
	cfg, err := os.ReadFile(filepath.Join(e.home(ag.Id), ".config/agenthold/gitconfig"))
	if err != nil || !strings.Contains(string(cfg), `name = "Alice Coder"`) || !strings.Contains(string(cfg), `email = "alice@code.example"`) {
		t.Fatalf("pushed gitconfig %q %v", cfg, err)
	}

	// Credentials: the token is never returned, the egress proxy adds it.
	expect(t, "bad host", alice.do("POST", "/me/git/credentials", map[string]string{"host": "10.0.0.1", "token": "x"}, nil), 400)
	expect(t, "no token", alice.do("POST", "/me/git/credentials", map[string]string{"host": "github.com", "token": " "}, nil), 400)
	var cred gen.GitCredential
	if code, body := alice.rawJSON("POST", "/me/git/credentials", map[string]string{"host": "https://GitHub.com/", "token": "ghp_secret", "label": "work"}); code != 200 || strings.Contains(body, "ghp_secret") {
		t.Fatalf("upsert: %d %s", code, body)
	}
	expect(t, "upsert again", alice.do("POST", "/me/git/credentials", map[string]string{"host": "github.com", "token": "ghp_rotated"}, &cred), 200)
	if cred.Host != "github.com" || cred.Username != "x-access-token" || cred.Id == "" || cred.CreatedAt.IsZero() || time.Since(cred.UpdatedAt) > time.Hour {
		t.Fatalf("credential %+v", cred)
	}
	if code, body := alice.rawJSON("GET", "/me/git", nil); code != 200 || strings.Contains(body, "ghp_") {
		t.Fatalf("settings leak the token: %d %s", code, body)
	}
	expect(t, "get", alice.do("GET", "/me/git", nil, &g), 200)
	if len(g.Credentials) != 1 || g.Credentials[0].Id != cred.Id {
		t.Fatalf("credentials %+v", g.Credentials)
	}

	inj := e.egressInjection(ag.Id, "github.com")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_rotated"))
	if inj == "" || inj != want {
		t.Fatalf("egress injection %q, want %q", inj, want)
	}
	if e.egressInjection(ag.Id, "api.github.com") != want {
		t.Fatal("the GitHub API does not get the token")
	}

	// Others cannot see or delete it.
	expect(t, "bob get", bob.do("GET", "/me/git", nil, &g), 200)
	if len(g.Credentials) != 0 {
		t.Fatalf("bob sees %+v", g.Credentials)
	}
	expect(t, "bob delete", bob.do("DELETE", "/me/git/credentials/"+cred.Id, nil, nil), 404)
	expect(t, "delete", alice.do("DELETE", "/me/git/credentials/"+cred.Id, nil, nil), 204)
	expect(t, "delete again", alice.do("DELETE", "/me/git/credentials/"+cred.Id, nil, nil), 404)
	if e.egressInjection(ag.Id, "github.com") != "" {
		t.Fatal("deleted credential still injected")
	}
	expect(t, "anon", e.anon().do("GET", "/me/git", nil, nil), 401)
}

// egressInjection is the Authorization value the egress proxy adds for host.
func (e *env) egressInjection(agentID, host string) string {
	e.t.Helper()
	a, err := e.directory.Agent(e.t.Context(), agentID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, inj := range a.Injections {
		if strings.HasPrefix(inj.SecretID, "git:") && slices.Contains(inj.Rule.Hosts, host) {
			return strings.ReplaceAll(inj.Rule.Template, "{{.Value}}", inj.Value)
		}
	}
	return ""
}

func TestGitRepos(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")

	var list gen.GitRepoList
	expect(t, "list", alice.do("GET", "/agents/"+ag.Id+"/git/repos", nil, &list), 200)
	if !list.Connected || len(list.Repos) != 0 {
		t.Fatalf("empty home %+v", list)
	}
	expect(t, "bob list", bob.do("GET", "/agents/"+ag.Id+"/git/repos", nil, nil), 404)

	// Clone.
	expect(t, "clone without url", alice.do("POST", "/agents/"+ag.Id+"/git/clone", map[string]string{}, nil), 400)
	expect(t, "clone outside", alice.do("POST", "/agents/"+ag.Id+"/git/clone", map[string]string{"url": "file:///nope", "path": "../x"}, nil), 400)
	repo, work := e.cloneDemo(alice, ag.Id)
	if repo.Path != "projects/demo" || repo.Project != "demo" || repo.Branch != "main" || repo.Dirty || repo.RemoteUrl == nil {
		t.Fatalf("cloned %+v", repo)
	}
	if _, err := os.Stat(filepath.Join(e.home(ag.Id), "projects/demo/README.md")); err != nil {
		t.Fatal(err)
	}
	expect(t, "clone again", alice.do("POST", "/agents/"+ag.Id+"/git/clone", map[string]string{"url": "file:///nope", "path": "projects/demo"}, nil), 400)

	expect(t, "list", alice.do("GET", "/agents/"+ag.Id+"/git/repos", nil, &list), 200)
	if !list.Connected || len(list.Repos) != 1 || list.Repos[0].Path != "projects/demo" {
		t.Fatalf("repos %+v", list)
	}

	// Status.
	var st gen.GitRepoStatus
	expect(t, "status", alice.do("GET", "/agents/"+ag.Id+"/git/status?path=projects/demo", nil, &st), 200)
	if st.Repo == nil || st.Repo.Branch != "main" || !slices.Equal(st.Branches, []string{"main"}) {
		t.Fatalf("status %+v", st)
	}
	var homeSt gen.GitRepoStatus
	expect(t, "status home", alice.do("GET", "/agents/"+ag.Id+"/git/status", nil, &homeSt), 200)
	if homeSt.Repo != nil || homeSt.Branches == nil {
		t.Fatalf("home is no repository: %+v", homeSt)
	}
	expect(t, "status outside", alice.do("GET", "/agents/"+ag.Id+"/git/status?path=/etc", nil, nil), 400)

	// Checkout: create and switch.
	expect(t, "checkout unknown", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "nope"}, nil), 400)
	expect(t, "checkout without branch", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo"}, nil), 400)
	expect(t, "checkout -c", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "feature/x", "create": true}, &repo), 200)
	if repo.Branch != "feature/x" {
		t.Fatalf("after checkout %+v", repo)
	}
	expect(t, "checkout main", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "main"}, &repo), 200)
	if repo.Branch != "main" {
		t.Fatalf("after checkout %+v", repo)
	}

	// Worktree.
	var wt gen.GitRepo
	expect(t, "worktree", alice.do("POST", "/agents/"+ag.Id+"/git/worktrees", map[string]any{"path": "projects/demo", "branch": "wt", "create": true}, &wt), 201)
	if wt.Path != ".worktrees/demo/wt" || wt.Branch != "wt" || !wt.IsWorktree || wt.MainPath == nil || *wt.MainPath != "projects/demo" {
		t.Fatalf("worktree %+v", wt)
	}
	if _, err := os.Stat(filepath.Join(e.home(ag.Id), ".worktrees/demo/wt/README.md")); err != nil {
		t.Fatal(err)
	}

	// Pull: a new commit upstream arrives.
	if err := os.WriteFile(filepath.Join(work, "NEW.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-q", "-m", "second")
	runGit(t, work, "push", "-q", "origin", "main")
	expect(t, "fetch", alice.do("POST", "/agents/"+ag.Id+"/git/pull", map[string]any{"path": "projects/demo", "fetchOnly": true}, &repo), 200)
	if repo.Behind != 1 {
		t.Fatalf("after fetch %+v", repo)
	}
	expect(t, "pull", alice.do("POST", "/agents/"+ag.Id+"/git/pull", map[string]any{"path": "projects/demo"}, &repo), 200)
	if repo.Behind != 0 {
		t.Fatalf("after pull %+v", repo)
	}
	if _, err := os.Stat(filepath.Join(e.home(ag.Id), "projects/demo/NEW.md")); err != nil {
		t.Fatal(err)
	}

	// Changing actions need prompt access; they are audited.
	expect(t, "bob checkout", bob.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "main"}, nil), 404)
	var audit []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &audit), 200)
	var actions []string
	for _, a := range audit {
		actions = append(actions, a.Action)
	}
	for _, want := range []string{"git.clone", "git.checkout", "git.worktree_add", "git.pull"} {
		if !slices.Contains(actions, want) {
			t.Fatalf("audit %v misses %s", actions, want)
		}
	}

	// Asleep: no repositories to show, and changing actions conflict.
	e.clock.Advance(16 * time.Minute)
	e.tick()
	if s := alice.agent(ag.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("not asleep: %s", s)
	}
	expect(t, "list asleep", alice.do("GET", "/agents/"+ag.Id+"/git/repos", nil, &list), 200)
	if list.Connected || list.Repos == nil || len(list.Repos) != 0 {
		t.Fatalf("asleep %+v", list)
	}
	expect(t, "status asleep", alice.do("GET", "/agents/"+ag.Id+"/git/status?path=projects/demo", nil, nil), 409)
	expect(t, "checkout asleep", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "main"}, nil), 409)
}

func TestGitSessionInRepository(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	e.cloneDemo(alice, ag.Id)
	home := e.home(ag.Id)

	// Invalid starts.
	for name, body := range map[string]map[string]any{
		"absolute":            {"cwd": "/etc"},
		"traversal":           {"cwd": "projects/../../x"},
		"branch without cwd":  {"gitBranch": "main"},
		"worktree no branch":  {"cwd": "projects/demo", "createWorktree": true},
		"create no branch":    {"cwd": "projects/demo", "createBranch": true},
		"unknown branch":      {"cwd": "projects/demo", "gitBranch": "nope"},
		"worktree in no repo": {"cwd": "nothing", "gitBranch": "x", "createBranch": true, "createWorktree": true},
	} {
		expect(t, name, alice.do("POST", "/agents/"+ag.Id+"/sessions", body, nil), 400)
	}

	// A session in the repository.
	var sess gen.Session
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"cwd": "projects/demo/"}, &sess), 201)
	if sess.Cwd == nil || *sess.Cwd != "projects/demo" {
		t.Fatalf("cwd %v", sess.Cwd)
	}
	if p, b, r := sessionGit(sess); p != "demo" || b != "main" || r != "projects/demo" {
		t.Fatalf("git %q %q %q", p, b, r)
	}
	if got := text(e.prompt(alice, sess.Id, "cwd")); !strings.Contains(got, "cwd="+filepath.Join(home, "projects/demo")) {
		t.Fatalf("harness cwd: %q", got)
	}

	// The branch changes outside the session (a terminal): the next turn
	// notices.
	runGit(t, filepath.Join(home, "projects/demo"), "switch", "-q", "-c", "outside")
	if _, b, _ := sessionGit(e.getSession(alice, sess.Id)); b != "main" {
		t.Fatalf("branch changed before a turn: %q", b)
	}
	e.prompt(alice, sess.Id, "hello")
	if p, b, _ := sessionGit(e.getSession(alice, sess.Id)); p != "demo" || b != "outside" {
		t.Fatalf("after the turn: %q %q", p, b)
	}

	// A checkout through the API updates sessions in the repository at once.
	expect(t, "checkout", alice.do("POST", "/agents/"+ag.Id+"/git/checkout", map[string]any{"path": "projects/demo", "branch": "main"}, nil), 200)
	if _, b, _ := sessionGit(e.getSession(alice, sess.Id)); b != "main" {
		t.Fatalf("after checkout: %q", b)
	}

	// A session on a new branch, checked out in the repository.
	var onBranch gen.Session
	expect(t, "create on branch", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"cwd": "projects/demo", "gitBranch": "topic", "createBranch": true}, &onBranch), 201)
	if p, b, r := sessionGit(onBranch); p != "demo" || b != "topic" || r != "projects/demo" || onBranch.Cwd == nil || *onBranch.Cwd != "projects/demo" {
		t.Fatalf("on branch: %q %q %q %v", p, b, r, onBranch.Cwd)
	}
	if got := runGit(t, filepath.Join(home, "projects/demo"), "branch", "--show-current"); got != "topic" {
		t.Fatalf("repository on %q", got)
	}
	// The other session in the repository follows.
	if _, b, _ := sessionGit(e.getSession(alice, sess.Id)); b != "topic" {
		t.Fatalf("first session on %q", b)
	}
}

func TestGitSessionInNewWorktree(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	e.cloneDemo(alice, ag.Id)
	home := e.home(ag.Id)

	var sess gen.Session
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{
		"cwd": "projects/demo", "gitBranch": "feature/login", "createBranch": true, "createWorktree": true, "title": "Login",
	}, &sess), 201)
	wt := ".worktrees/demo/feature-login"
	if sess.Cwd == nil || *sess.Cwd != wt || sess.Title != "Login" {
		t.Fatalf("session %+v", sess)
	}
	if p, b, r := sessionGit(sess); p != "demo" || b != "feature/login" || r != wt {
		t.Fatalf("git %q %q %q", p, b, r)
	}
	if got := runGit(t, filepath.Join(home, wt), "branch", "--show-current"); got != "feature/login" {
		t.Fatalf("worktree on %q", got)
	}
	// The main checkout stays on main.
	if got := runGit(t, filepath.Join(home, "projects/demo"), "branch", "--show-current"); got != "main" {
		t.Fatalf("main checkout on %q", got)
	}
	if got := text(e.prompt(alice, sess.Id, "cwd")); !strings.Contains(got, "cwd="+filepath.Join(home, wt)) {
		t.Fatalf("harness cwd: %q", got)
	}
	if p, b, _ := sessionGit(e.getSession(alice, sess.Id)); p != "demo" || b != "feature/login" {
		t.Fatalf("after the turn: %q %q", p, b)
	}
	var audit []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &audit), 200)
	if !slices.ContainsFunc(audit, func(a gen.AuditEntry) bool { return a.Action == "git.worktree_add" }) {
		t.Fatalf("audit %+v", audit)
	}
}

func TestGitSessionDetectsTheRepositoryItWorksIn(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	e.cloneDemo(alice, ag.Id)

	// A session in the home: writing a file inside the repository tells
	// which repository it works in.
	var sess gen.Session
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	if sess.Cwd != nil || sess.Git != nil {
		t.Fatalf("plain session %+v", sess)
	}
	e.prompt(alice, sess.Id, "write projects/demo/notes.txt hi")
	if p, b, r := sessionGit(e.getSession(alice, sess.Id)); p != "demo" || b != "main" || r != "projects/demo" {
		t.Fatalf("detected %q %q %q", p, b, r)
	}
	if got := e.getSession(alice, sess.Id); got.Cwd != nil {
		t.Fatalf("cwd set by detection: %v", *got.Cwd)
	}
}

func TestGitUpdateSession(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	e.cloneDemo(alice, ag.Id)
	home := e.home(ag.Id)

	var sess gen.Session
	expect(t, "create", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	path := "/sessions/" + sess.Id + "/git"
	patch := func(what string, body map[string]any) gen.Session {
		t.Helper()
		var out gen.Session // fresh: omitted fields must not keep old values
		expect(t, what, alice.do("PATCH", path, body, &out), 200)
		return out
	}
	expect(t, "bob", bob.do("PATCH", path, map[string]any{"refresh": true}, nil), 404)
	expect(t, "absolute", alice.do("PATCH", path, map[string]any{"cwd": "/tmp"}, nil), 400)
	expect(t, "traversal", alice.do("PATCH", path, map[string]any{"cwd": ".."}, nil), 400)

	// Into the repository.
	sess = patch("cwd", map[string]any{"cwd": "projects/demo"})
	if p, b, r := sessionGit(sess); sess.Cwd == nil || *sess.Cwd != "projects/demo" || p != "demo" || b != "main" || r != "projects/demo" {
		t.Fatalf("after cwd: %v %q %q %q", sess.Cwd, p, b, r)
	}
	if got := text(e.prompt(alice, sess.Id, "cwd")); !strings.Contains(got, "cwd="+filepath.Join(home, "projects/demo")) {
		t.Fatalf("harness cwd: %q", got)
	}

	// Refresh picks up a branch change.
	runGit(t, filepath.Join(home, "projects/demo"), "switch", "-q", "-c", "elsewhere")
	sess = patch("refresh", map[string]any{"refresh": true})
	if _, b, _ := sessionGit(sess); b != "elsewhere" {
		t.Fatalf("after refresh: %q", b)
	}

	// Somewhere that is no repository: the git fields clear.
	if err := os.MkdirAll(filepath.Join(home, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	sess = patch("scratch", map[string]any{"cwd": "scratch"})
	if sess.Cwd == nil || *sess.Cwd != "scratch" || sess.Git != nil {
		t.Fatalf("in scratch %+v", sess)
	}

	// Back to the home.
	sess = patch("home", map[string]any{"cwd": "."})
	if sess.Cwd != nil || sess.Git != nil {
		t.Fatalf("in the home %+v", sess)
	}

	// Asleep: a refresh needs the machine.
	e.clock.Advance(16 * time.Minute)
	e.tick()
	expect(t, "refresh asleep", alice.do("PATCH", path, map[string]any{"refresh": true}, nil), 409)
	sess = patch("cwd asleep", map[string]any{"cwd": "projects/demo"})
	if sess.Cwd == nil || *sess.Cwd != "projects/demo" {
		t.Fatalf("cwd asleep %+v", sess)
	}
	// The next turn wakes the machine and works in the repository.
	if got := text(e.prompt(alice, sess.Id, "cwd")); !strings.Contains(got, "cwd="+filepath.Join(home, "projects/demo")) {
		t.Fatalf("harness cwd after wake: %q", got)
	}
	if p, b, _ := sessionGit(e.getSession(alice, sess.Id)); p != "demo" || b != "elsewhere" {
		t.Fatalf("after the turn: %q %q", p, b)
	}
}
