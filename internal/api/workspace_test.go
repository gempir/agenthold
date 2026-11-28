package api_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/scripts"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestGitDiffCommitsAndCommit(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	e.cloneDemo(alice, ag.Id)
	repo := filepath.Join(e.home(ag.Id), "projects/demo")
	base := "/agents/" + ag.Id + "/git/"

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("demo\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var d gen.GitDiff
	expect(t, "diff", alice.do("GET", base+"diff?path=projects/demo", nil, &d), 200)
	if d.Mode != gen.GitDiffModeUncommitted || d.Repo.Path != "projects/demo" || len(d.Files) != 2 || d.Additions != 2 {
		t.Fatalf("diff %+v", d)
	}
	for _, f := range d.Files {
		switch f.Path {
		case "README.md":
			if f.Status != "M" || f.Patch == nil || !strings.Contains(*f.Patch, "+more") {
				t.Fatalf("README %+v", f)
			}
		case "new.txt":
			if f.Status != "?" || f.Additions != 1 {
				t.Fatalf("new %+v", f)
			}
		default:
			t.Fatalf("file %+v", f)
		}
	}
	expect(t, "bob diff", bob.do("GET", base+"diff?path=projects/demo", nil, nil), 404)
	expect(t, "commit diff without ref", alice.do("GET", base+"diff?path=projects/demo&mode=commit", nil, nil), 400)
	expect(t, "bad mode", alice.do("GET", base+"diff?path=projects/demo&mode=turn", nil, nil), 400)
	expect(t, "outside", alice.do("GET", base+"diff?path=../x", nil, nil), 400)
	expect(t, "not a repo", alice.do("GET", base+"diff?path=projects", nil, nil), 400)

	// Commit everything; it is audited.
	expect(t, "no message", alice.do("POST", base+"commit", map[string]any{"path": "projects/demo", "message": " "}, nil), 400)
	expect(t, "bob commit", bob.do("POST", base+"commit", map[string]any{"path": "projects/demo", "message": "x"}, nil), 404)
	var c gen.GitCommitResult
	expect(t, "commit", alice.do("POST", base+"commit", map[string]any{"path": "projects/demo", "message": "Add more", "all": true}, &c), 201)
	if c.Commit.Subject != "Add more" || c.Commit.FilesChanged != 2 || c.Repo.Dirty || c.Repo.Ahead != 1 || c.Commit.Date.IsZero() {
		t.Fatalf("commit %+v", c)
	}
	expect(t, "nothing to commit", alice.do("POST", base+"commit", map[string]any{"path": "projects/demo", "message": "x", "all": true}, nil), 400)
	var audit []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &audit), 200)
	found := false
	for _, a := range audit {
		if a.Action == "git.commit" && a.Details["sha"] == c.Commit.Sha {
			found = true
		}
	}
	if !found {
		t.Fatalf("no git.commit audit entry in %+v", audit)
	}

	var log gen.GitCommitList
	expect(t, "commits", alice.do("GET", base+"commits?path=projects/demo&limit=1", nil, &log), 200)
	if len(log.Commits) != 1 || log.Commits[0].Sha != c.Commit.Sha || log.Repo.Path != "projects/demo" {
		t.Fatalf("commits %+v", log)
	}
	expect(t, "limit", alice.do("GET", base+"commits?path=projects/demo&limit=0", nil, nil), 400)

	expect(t, "commit diff", alice.do("GET", base+"diff?path=projects/demo&mode=commit&ref="+c.Commit.ShortSha, nil, &d), 200)
	if d.Ref == nil || *d.Ref != c.Commit.Sha || len(d.Files) != 2 {
		t.Fatalf("commit diff %+v", d)
	}
	expect(t, "branch diff", alice.do("GET", base+"diff?path=projects/demo&mode=branch", nil, &d), 200)
	if d.Base == nil || !strings.HasPrefix(*d.Base, "origin/") || d.MergeBase == nil || len(d.Files) != 2 {
		t.Fatalf("branch diff %+v", d)
	}
	expect(t, "diff after commit", alice.do("GET", base+"diff?path=projects/demo", nil, &d), 200)
	if len(d.Files) != 0 {
		t.Fatalf("clean diff %+v", d.Files)
	}
}

func TestFileViewerEditorAndSearch(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	home := e.home(ag.Id)
	files := "/agents/" + ag.Id + "/files/"
	if err := os.MkdirAll(filepath.Join(home, "app/src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "app/src/main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "app/logo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}

	var f gen.TextFile
	expect(t, "read", alice.do("GET", files+"read?path=app/src/main.go", nil, &f), 200)
	if f.Path != "app/src/main.go" || f.IsBinary || f.Truncated || f.Content != "package main\n\nfunc main() {}\n" || f.Size != 29 {
		t.Fatalf("read %+v", f)
	}
	expect(t, "lines", alice.do("GET", files+"read?path=app/src/main.go&startLine=3&endLine=3", nil, &f), 200)
	if f.Content != "func main() {}\n" || f.StartLine == nil || *f.StartLine != 3 || f.TotalLines == nil || *f.TotalLines != 3 {
		t.Fatalf("lines %+v", f)
	}
	expect(t, "binary", alice.do("GET", files+"read?path=app/logo.png", nil, &f), 200)
	if !f.IsBinary || f.Content != "" || f.MimeType != "image/png" {
		t.Fatalf("binary %+v", f)
	}
	expect(t, "missing", alice.do("GET", files+"read?path=app/nope.go", nil, nil), 404)
	for _, p := range []string{"/etc/hosts", "../x", "", "."} {
		expect(t, "read "+p, alice.do("GET", files+"read?path="+url.QueryEscape(p), nil, nil), 400)
	}
	expect(t, "bad range", alice.do("GET", files+"read?path=app/src/main.go&startLine=3&endLine=2", nil, nil), 400)
	expect(t, "bob read", bob.do("GET", files+"read?path=app/src/main.go", nil, nil), 404)

	f = gen.TextFile{}
	expect(t, "write", alice.do("PUT", files+"write", map[string]string{"path": "app/src/main.go", "content": "package main\n// edited\n"}, &f), 200)
	if f.Size != 23 || f.Content != "" || f.Path != "app/src/main.go" {
		t.Fatalf("write %+v", f)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "app/src/main.go")); string(b) != "package main\n// edited\n" {
		t.Fatalf("written %q", b)
	}
	expect(t, "write new", alice.do("PUT", files+"write", map[string]string{"path": "app/docs/README.md", "content": "# App\n"}, nil), 200)
	expect(t, "write outside", alice.do("PUT", files+"write", map[string]string{"path": "/etc/passwd", "content": "x"}, nil), 400)
	expect(t, "bob write", bob.do("PUT", files+"write", map[string]string{"path": "app/x", "content": "x"}, nil), 404)

	var sr gen.FileSearchResult
	expect(t, "search", alice.do("GET", files+"search?path=app&q=main", nil, &sr), 200)
	if sr.Base != "app" || len(sr.Results) == 0 || sr.Results[0].Path != "src/main.go" || sr.Results[0].Name != "main.go" {
		t.Fatalf("search %+v", sr)
	}
	expect(t, "search home", alice.do("GET", files+"search?q=readme", nil, &sr), 200)
	if len(sr.Results) != 1 || sr.Results[0].Path != "app/docs/README.md" {
		t.Fatalf("search home %+v", sr)
	}
	expect(t, "search limit", alice.do("GET", files+"search?limit=501", nil, nil), 400)
	expect(t, "search outside", alice.do("GET", files+"search?path=/etc", nil, nil), 400)
}

const scriptsJSON = `{
	"defaultThreadEnvMode": "worktree",
	"scripts": [
		{"name": "Setup", "command": "echo setup-ran > .setup-done", "icon": "configure", "runOnWorktreeCreate": true, "async": false},
		{"name": "Test", "command": "echo testing; exit 3", "icon": "test"},
		{"name": "Dev", "command": "sleep 600", "previewUrl": "http://localhost:3000", "stopOnSettle": true}
	]
}`

func TestProjectScripts(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	home := e.home(ag.Id)
	for dir, files := range map[string]map[string]string{
		"app":   {"agenthold.json": scriptsJSON, "t3.json": `{"scripts": [{"name": "Ignored", "command": "true"}]}`},
		"t3app": {"t3.json": `{"scripts": [{"name": "Lint", "command": "echo lint", "icon": "lint"}]}`},
		"bad":   {"agenthold.json": `{"scripts": [`},
		"none":  {},
	} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(home, dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	base := "/agents/" + ag.Id + "/scripts"

	var ps gen.ProjectScripts
	expect(t, "scripts", alice.do("GET", base+"?path=app", nil, &ps), 200)
	if ps.Source == nil || *ps.Source != gen.AgentholdJson || len(ps.Scripts) != 3 || ps.DefaultThreadEnvMode == nil || *ps.DefaultThreadEnvMode != "worktree" || ps.Error != nil {
		t.Fatalf("scripts %+v", ps)
	}
	if dev := ps.Scripts[2]; dev.Id != "dev" || !dev.LongRunning || dev.Service == nil || dev.PreviewUrl == nil || dev.Status != nil {
		t.Fatalf("dev %+v", dev)
	}
	if setup := ps.Scripts[0]; setup.Async || !setup.RunOnWorktreeCreate || setup.Icon != "configure" || setup.LongRunning {
		t.Fatalf("setup %+v", setup)
	}
	ps = gen.ProjectScripts{}
	expect(t, "t3.json", alice.do("GET", base+"?path=t3app", nil, &ps), 200)
	if ps.Source == nil || *ps.Source != gen.T3Json || len(ps.Scripts) != 1 || ps.Scripts[0].Name != "Lint" {
		t.Fatalf("t3.json %+v", ps)
	}
	ps = gen.ProjectScripts{}
	expect(t, "none", alice.do("GET", base+"?path=none", nil, &ps), 200)
	if ps.Source != nil || len(ps.Scripts) != 0 || ps.Error != nil {
		t.Fatalf("none %+v", ps)
	}
	ps = gen.ProjectScripts{}
	expect(t, "missing dir", alice.do("GET", base+"?path=nowhere", nil, &ps), 200)
	if len(ps.Scripts) != 0 || ps.Error != nil {
		t.Fatalf("missing dir %+v", ps)
	}
	ps = gen.ProjectScripts{}
	expect(t, "bad", alice.do("GET", base+"?path=bad", nil, &ps), 200)
	if ps.Error == nil || !strings.Contains(*ps.Error, "agenthold.json") {
		t.Fatalf("bad %+v", ps)
	}
	expect(t, "outside", alice.do("GET", base+"?path=../x", nil, nil), 400)

	// Run one to completion, and a long-running one as a service.
	var run gen.ProjectScriptRun
	expect(t, "run test", alice.do("POST", base+"/run", map[string]string{"path": "app", "name": "test"}, &run), 200)
	if run.Mode != gen.ProjectScriptRunModeExec || run.ExitCode == nil || *run.ExitCode != 3 || run.Output == nil || !strings.Contains(*run.Output, "testing") {
		t.Fatalf("run test %+v", run)
	}
	expect(t, "run setup", alice.do("POST", base+"/run", map[string]string{"path": "app", "name": "Setup"}, &run), 200)
	if b, _ := os.ReadFile(filepath.Join(home, "app/.setup-done")); string(b) != "setup-ran\n" {
		t.Fatalf("setup wrote %q", b)
	}
	expect(t, "run missing", alice.do("POST", base+"/run", map[string]string{"path": "app", "name": "nope"}, nil), 404)
	expect(t, "run in bad", alice.do("POST", base+"/run", map[string]string{"path": "bad", "name": "x"}, nil), 400)
	expect(t, "run dev", alice.do("POST", base+"/run", map[string]string{"path": "app", "name": "Dev"}, &run), 200)
	if run.Mode != gen.ProjectScriptRunModeService || run.Service == nil || *run.Service != scripts.ServiceName("app", scripts.Script{ID: "dev"}) {
		t.Fatalf("run dev %+v", run)
	}
	svc := *run.Service
	var services gen.ServiceList
	expect(t, "services", alice.do("GET", "/agents/"+ag.Id+"/services", nil, &services), 200)
	if len(services.Services) != 1 || services.Services[0].Name != svc || services.Services[0].Workdir != "app" {
		t.Fatalf("services %+v", services)
	}
	// Running it again restarts it.
	expect(t, "run dev again", alice.do("POST", base+"/run", map[string]string{"path": "app", "name": "dev"}, &run), 200)

	// Settling a thread that works in app stops its stopOnSettle scripts.
	var s gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Work"}, &s), 201)
	if err := e.store.SetSessionGit(t.Context(), db.SetSessionGitParams{Cwd: "app", ID: s.Id}); err != nil {
		t.Fatal(err)
	}
	expect(t, "settle", alice.do("POST", "/sessions/"+s.Id+"/settle", nil, nil), 200)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		expect(t, "services", alice.do("GET", "/agents/"+ag.Id+"/services", nil, &services), 200)
		if len(services.Services) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev service not stopped on settle: %+v", services)
		}
	}

	// Only users who can open a terminal run scripts.
	bob := e.login("bob", "Bob", "b@example.com")
	expect(t, "bob run", bob.do("POST", base+"/run", map[string]string{"path": "app", "name": "test"}, nil), 404)
}

func TestProjectThreadRunsWorktreeScripts(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	home := e.home(ag.Id)
	u, work := bareRepo(t)
	if err := os.WriteFile(filepath.Join(work, "agenthold.json"), []byte(scriptsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "agenthold.json")
	runGit(t, work, "commit", "-q", "-m", "scripts")
	runGit(t, work, "push", "-q", "origin", "main")

	var p gen.Project
	expect(t, "project", alice.do("POST", "/projects", map[string]any{"name": "Shop", "sources": []map[string]any{{"url": u}}}, &p), 201)
	var th gen.Session
	expect(t, "thread", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id}, &th), 201)
	key := strings.TrimPrefix(*th.Project.Branch, "agenthold/")
	wt := filepath.Join(home, "projects/shop/.threads", key, "demo")
	if b, err := os.ReadFile(filepath.Join(wt, ".setup-done")); err != nil || string(b) != "setup-ran\n" {
		t.Fatalf("setup did not run in the new worktree: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, "projects/shop/demo/.setup-done")); err == nil {
		t.Fatal("setup ran in the main checkout")
	}
}

func TestSubthreadWorktreeIsolation(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	home := e.home(ag.Id)
	e.cloneDemo(alice, ag.Id)
	if err := os.MkdirAll(filepath.Join(home, "projects/demo/pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommit(t, filepath.Join(home, "projects/demo"), "pkg/a.go", "package pkg\n")
	writeCommit(t, filepath.Join(home, "projects/demo"), "agenthold.json", scriptsJSON)

	var parent gen.Session
	expect(t, "parent", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Parent"}, &parent), 201)
	if err := e.store.SetSessionGit(t.Context(), db.SetSessionGitParams{
		Cwd: "projects/demo/pkg", GitProject: "demo", GitBranch: "main", GitRepoPath: "projects/demo", ID: parent.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.projectTool(ag.Id, parent.Id, "start_subthread", map[string]any{"text": "x", "isolation": "bogus"}); err == nil {
		t.Fatal("bad isolation accepted")
	}
	out, err := e.projectTool(ag.Id, parent.Id, "start_subthread", map[string]any{"text": "cwd", "isolation": "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ThreadID  string `json:"thread_id"`
		Cwd       string `json:"cwd"`
		Branch    string `json:"branch"`
		Isolation string `json:"isolation"`
		Status    string `json:"status"`
		Answer    string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Isolation != "worktree" || !strings.HasPrefix(res.Branch, "agenthold/sub-") || res.Status != "completed" {
		t.Fatalf("result %+v", res)
	}
	if !strings.HasPrefix(res.Cwd, ".worktrees/demo/") || !strings.HasSuffix(res.Cwd, "/pkg") {
		t.Fatalf("cwd %q", res.Cwd)
	}
	wt := filepath.Join(home, strings.TrimSuffix(res.Cwd, "/pkg"))
	if got := runGit(t, wt, "branch", "--show-current"); got != res.Branch {
		t.Fatalf("worktree on %q, want %q", got, res.Branch)
	}
	if !strings.Contains(res.Answer, "cwd="+filepath.Join(home, res.Cwd)) {
		t.Fatalf("the sub-thread did not run in its worktree: %q", res.Answer)
	}
	if b, err := os.ReadFile(filepath.Join(wt, ".setup-done")); err != nil || string(b) != "setup-ran\n" {
		t.Fatalf("setup did not run in the sub-thread's worktree: %q %v", b, err)
	}
	child := e.getSession(alice, res.ThreadID)
	if child.Git == nil || child.Git.Branch != res.Branch || child.Cwd == nil || *child.Cwd != res.Cwd {
		t.Fatalf("child %+v %v", child.Git, child.Cwd)
	}
	// The parent's checkout is untouched.
	if got := runGit(t, filepath.Join(home, "projects/demo"), "branch", "--show-current"); got != "main" {
		t.Fatalf("parent checkout on %q", got)
	}

	// Without a repository there is nothing to branch off.
	var plain gen.Session
	expect(t, "plain", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Plain"}, &plain), 201)
	if _, err := e.projectTool(ag.Id, plain.Id, "start_subthread", map[string]any{"text": "x", "isolation": "worktree"}); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("worktree isolation outside a repository: %v", err)
	}
}
