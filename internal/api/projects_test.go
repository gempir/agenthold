package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
)

// writeCommit writes name in the repository dir and commits it.
func writeCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "change "+name)
}

func (e *env) projectTool(agentID, sessionID, tool string, args any) (string, error) {
	e.t.Helper()
	b, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(e.t.Context(), 2*time.Minute)
	defer cancel()
	out, err := e.platform.Call(ctx, agentID, sessionID, tool, b)
	if err != nil {
		return "", err
	}
	s, _ := out.(string)
	return s, nil
}

func TestProjects(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	home := e.home(ag.Id)
	url, _ := bareRepo(t)

	// Create: bad sources are refused.
	for name, body := range map[string]map[string]any{
		"no name":       {"name": ""},
		"repo, no url":  {"name": "P", "sources": []map[string]any{{"name": "x", "kind": "repo"}}},
		"twice":         {"name": "P", "sources": []map[string]any{{"name": "x"}, {"name": "X"}}},
		"path outside":  {"name": "P", "sources": []map[string]any{{"name": "x", "path": "../x"}}},
		"reserved name": {"name": "P", "sources": []map[string]any{{"name": ".threads"}}},
	} {
		expect(t, name, alice.do("POST", "/projects", body, nil), 400)
	}
	var p gen.Project
	expect(t, "create", alice.do("POST", "/projects", map[string]any{
		"name": "Web Shop", "description": "The shop.", "instructions": "Run the tests before you commit.",
		"sources": []map[string]any{{"url": url}, {"name": "notes", "kind": "folder"}},
	}, &p), 201)
	if p.Slug != "web-shop" || len(p.Sources) != 2 || p.Sources[0].Name != "demo" || p.Sources[0].Kind != "repo" || p.Sources[1].Kind != "folder" {
		t.Fatalf("project %+v", p)
	}
	var again gen.Project
	expect(t, "same name", alice.do("POST", "/projects", map[string]any{"name": "Web Shop"}, &again), 201)
	if again.Slug != "web-shop-2" {
		t.Fatalf("second slug %q", again.Slug)
	}
	expect(t, "bob get", bob.do("GET", "/projects/"+p.Id, nil, nil), 404)
	expect(t, "bob thread", bob.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id}, nil), 404)
	var list []gen.Project
	expect(t, "bob list", bob.do("GET", "/projects", nil, &list), 200)
	if len(list) != 0 {
		t.Fatalf("bob sees %+v", list)
	}
	// Alice may not use Bob's agent.
	bobAgent := e.fakeAgent(bob, "")
	expect(t, "other's agent", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": bobAgent.Id}, nil), 404)

	// Two threads, each in a workspace of its own.
	var one, two gen.Session
	expect(t, "thread one", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id, "title": "One"}, &one), 201)
	expect(t, "thread two", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id, "title": "Two"}, &two), 201)
	if one.Project == nil || one.Project.Direct || one.Project.Branch == nil || !strings.HasPrefix(*one.Project.Branch, "agenthold/") {
		t.Fatalf("thread one %+v", one.Project)
	}
	key := strings.TrimPrefix(*one.Project.Branch, "agenthold/")
	ws1 := filepath.Join(home, "projects/web-shop/.threads", key)
	if one.Cwd == nil || *one.Cwd != "projects/web-shop/.threads/"+key {
		t.Fatalf("cwd %v", one.Cwd)
	}
	for _, src := range []string{"demo", "notes"} {
		if got := runGit(t, filepath.Join(ws1, src), "branch", "--show-current"); got != *one.Project.Branch {
			t.Fatalf("%s worktree on %q", src, got)
		}
		if got := runGit(t, filepath.Join(home, "projects/web-shop", src), "branch", "--show-current"); got != "main" {
			t.Fatalf("%s main checkout on %q", src, got)
		}
	}
	guide, err := os.ReadFile(filepath.Join(ws1, "AGENTS.md"))
	if err != nil || !strings.Contains(string(guide), "Run the tests before you commit.") || !strings.Contains(string(guide), *one.Project.Branch) {
		t.Fatalf("AGENTS.md: %v\n%s", err, guide)
	}
	if got := text(e.prompt(alice, one.Id, "cwd")); !strings.Contains(got, "cwd="+ws1) {
		t.Fatalf("harness cwd: %q", got)
	}
	// The default branch found on the first checkout is recorded.
	expect(t, "get", alice.do("GET", "/projects/"+p.Id, nil, &p), 200)
	if p.Sources[0].DefaultBranch == nil || *p.Sources[0].DefaultBranch != "main" {
		t.Fatalf("default branch %+v", p.Sources[0])
	}

	// Both change README.md: an overlap.
	ws2 := filepath.Join(home, "projects/web-shop/.threads", strings.TrimPrefix(*two.Project.Branch, "agenthold/"))
	writeCommit(t, filepath.Join(ws1, "demo"), "README.md", "one\n")
	writeCommit(t, filepath.Join(ws1, "notes"), "plan.md", "plan\n")
	if err := os.WriteFile(filepath.Join(ws2, "demo", "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var st gen.ProjectStatus
	expect(t, "status", alice.do("GET", "/projects/"+p.Id+"/status", nil, &st), 200)
	if len(st.Threads) != 2 || len(st.Overlaps) != 1 || st.Overlaps[0].Source != "demo" || st.Overlaps[0].Path != "README.md" || len(st.Overlaps[0].SessionIds) != 2 {
		t.Fatalf("status %+v", st)
	}
	var changes []gen.ProjectSourceChanges
	expect(t, "changes", alice.do("GET", "/sessions/"+two.Id+"/project/changes", nil, &changes), 200)
	if len(changes) != 2 || !changes[0].Dirty || changes[0].Commits != 0 {
		t.Fatalf("changes %+v", changes)
	}

	// Merge: uncommitted work is refused; committed work lands in the main checkouts.
	expect(t, "merge dirty", alice.do("POST", "/sessions/"+two.Id+"/project/merge", nil, nil), 400)
	var res []gen.ProjectSourceResult
	expect(t, "merge one", alice.do("POST", "/sessions/"+one.Id+"/project/merge", nil, &res), 200)
	if len(res) != 2 || !res[0].Done || !res[1].Done || res[0].Commits != 1 {
		t.Fatalf("merge %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "projects/web-shop/demo/README.md")); string(b) != "one\n" {
		t.Fatalf("main README %q", b)
	}
	// The second thread conflicts now: the merge aborts and names the file.
	runGit(t, filepath.Join(ws2, "demo"), "commit", "-qam", "two")
	expect(t, "merge two", alice.do("POST", "/sessions/"+two.Id+"/project/merge", nil, &res), 200)
	if len(res) == 0 || res[0].Done || res[0].Error == nil || !strings.Contains(*res[0].Error, "README.md") {
		t.Fatalf("conflict %+v", res)
	}
	// Close: unmerged commits need force.
	expect(t, "close two", alice.do("POST", "/sessions/"+two.Id+"/project/close", map[string]any{}, nil), 400)
	var closed gen.Session
	expect(t, "discard two", alice.do("POST", "/sessions/"+two.Id+"/project/close", map[string]any{"force": true}, &closed), 200)
	if closed.SettledAt == nil {
		t.Fatalf("closed thread not settled: %+v", closed)
	}
	if _, err := os.Stat(ws2); !os.IsNotExist(err) {
		t.Fatalf("workspace still there: %v", err)
	}
	if out := runGit(t, filepath.Join(home, "projects/web-shop/demo"), "branch", "--list", *two.Project.Branch); out != "" {
		t.Fatalf("branch kept: %q", out)
	}
	expect(t, "close one", alice.do("POST", "/sessions/"+one.Id+"/project/close", map[string]any{}, nil), 200)

	// A direct thread works in the main checkouts.
	var direct gen.Session
	expect(t, "direct", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id, "direct": true}, &direct), 201)
	if direct.Project == nil || !direct.Project.Direct || direct.Cwd == nil || *direct.Cwd != "projects/web-shop" {
		t.Fatalf("direct %+v %v", direct.Project, direct.Cwd)
	}
	expect(t, "merge direct", alice.do("POST", "/sessions/"+direct.Id+"/project/merge", nil, nil), 400)

	// Editing keeps sources by name; deleting unlinks the threads.
	expect(t, "update", alice.do("PATCH", "/projects/"+p.Id, map[string]any{
		"name": "Shop", "sources": []map[string]any{{"url": url}},
	}, &p), 200)
	if p.Name != "Shop" || p.Slug != "web-shop" || len(p.Sources) != 1 || p.Sources[0].DefaultBranch == nil {
		t.Fatalf("updated %+v", p)
	}
	expect(t, "delete", alice.do("DELETE", "/projects/"+p.Id, nil, nil), 204)
	if s := e.getSession(alice, direct.Id); s.Project != nil {
		t.Fatalf("thread still on the project: %+v", s.Project)
	}
	expect(t, "deleted", alice.do("GET", "/projects/"+p.Id, nil, nil), 404)
}

func TestProjectTools(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	url, _ := bareRepo(t)
	var p gen.Project
	expect(t, "create", alice.do("POST", "/projects", map[string]any{"name": "Shop", "sources": []map[string]any{{"url": url}}}, &p), 201)
	var th gen.Session
	expect(t, "thread", alice.do("POST", "/projects/"+p.Id+"/threads", map[string]any{"agentId": ag.Id}, &th), 201)

	out, err := e.projectTool(ag.Id, th.Id, "list_projects", nil)
	if err != nil || !strings.Contains(out, `"name": "Shop"`) || !strings.Contains(out, `"directory": "projects/shop"`) {
		t.Fatalf("list_projects %q %v", out, err)
	}
	for _, tool := range []string{"list_tasks", "create_task", "claim_task", "update_task", "assign_task"} {
		if _, err := e.projectTool(ag.Id, th.Id, tool, nil); err == nil {
			t.Fatalf("removed task tool %s is still available", tool)
		}
	}

	// project_status still reports the active project thread.
	home := e.home(ag.Id)
	for _, s := range []gen.Session{th} {
		if err := os.WriteFile(filepath.Join(home, *s.Cwd, "demo", "style.css"), []byte(s.Id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err = e.projectTool(ag.Id, th.Id, "project_status", nil)
	if err != nil || !strings.Contains(out, `"current": true`) {
		t.Fatalf("project_status %q %v", out, err)
	}
}
