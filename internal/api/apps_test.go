package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
)

// freePort returns a loopback port nothing listens on (fake machines share
// the host's loopback).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *env) serviceState(c *client, machineID, name string) string {
	var s gen.Service
	if c.do("GET", "/agents/"+machineID+"/services/"+name, nil, &s) != 200 || s.Status == nil {
		return ""
	}
	return string(s.Status.State)
}

func TestServicesCRUDAndLogs(t *testing.T) {
	tm := newTeam(t)
	e, alice, bob, dave := tm.e, tm.alice, tm.bob, tm.dave
	ag := tm.agent.Id
	tm.share("bob")
	base := "/agents/" + ag + "/services"

	var list gen.ServiceList
	expect(t, "list empty", alice.do("GET", base, nil, &list), 200)
	if !list.Running || len(list.Services) != 0 {
		t.Fatalf("list = %+v", list)
	}
	in := gen.ServiceInput{Command: "echo hello $WHO; exec sleep 300", Env: &map[string]string{"WHO": "svc"}}
	expect(t, "shared user cannot write", bob.do("PUT", base+"/web", in, nil), 403)
	expect(t, "stranger", dave.do("PUT", base+"/web", in, nil), 404)
	expect(t, "bad name", alice.do("PUT", base+"/Bad_Name", in, nil), 400)
	expect(t, "bad env", alice.do("PUT", base+"/web", gen.ServiceInput{Command: "x", Env: &map[string]string{"1X": "y"}}, nil), 400)
	var s gen.Service
	expect(t, "put", alice.do("PUT", base+"/web", in, &s), 200)
	if !s.Enabled || s.Env["WHO"] != "svc" {
		t.Fatalf("service = %+v", s)
	}
	waitUntil(t, "running", 5*time.Second, func() bool { return e.serviceState(bob, ag, "web") == "running" })

	waitUntil(t, "log line", 5*time.Second, func() bool {
		res := bob.raw("GET", "/agents/"+ag+"/services/web/logs?lines=5", nil)
		body := readAll(t, res.Body)
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK && strings.Contains(body, "hello svc")
	})
	if _, err := os.Stat(filepath.Join(e.home(ag), ".agenthold/services/web/log")); err != nil {
		t.Fatal("no log file on the home disk")
	}

	// Follow over a WebSocket.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/agents/" + ag + "/services/web/logs/stream?lines=10"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: bob.http})
	if err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.Read(ctx)
	if err != nil || !strings.Contains(string(msg), "hello svc") {
		t.Fatalf("stream: %q %v", msg, err)
	}
	// A restart is followed live.
	expect(t, "restart", alice.do("POST", base+"/web/restart", nil, &s), 200)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "restarting") {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("follow: %v (got %q)", err, seen.String())
		}
		seen.Write(msg)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")

	expect(t, "stop", alice.do("POST", base+"/web/stop", nil, &s), 200)
	if s.Enabled || s.Status == nil || s.Status.State != gen.ServiceStatusStateStopped {
		t.Fatalf("stopped = %+v", s)
	}
	expect(t, "start", alice.do("POST", base+"/web/start", nil, &s), 200)
	waitUntil(t, "running again", 5*time.Second, func() bool { return e.serviceState(alice, ag, "web") == "running" })

	// Sleep: services are down; waking brings enabled ones back from the boot files.
	expect(t, "stop agent", alice.do("POST", "/agents/"+ag+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "list asleep", alice.do("GET", base, nil, &list), 200)
	if list.Running || len(list.Services) != 1 || list.Services[0].Status != nil {
		t.Fatalf("asleep list = %+v", list)
	}
	expect(t, "logs asleep", alice.do("GET", base+"/web/logs", nil, nil), 409)
	expect(t, "start agent", alice.do("POST", "/agents/"+ag+"/start", nil, nil), 202)
	e.sandboxes.Wait()
	waitUntil(t, "running after boot", 10*time.Second, func() bool { return e.serviceState(alice, ag, "web") == "running" })

	expect(t, "delete", alice.do("DELETE", base+"/web", nil, nil), 204)
	expect(t, "deleted", alice.do("GET", base+"/web", nil, nil), 404)
	expect(t, "list", alice.do("GET", base, nil, &list), 200)
	if len(list.Services) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}

func readAll(t *testing.T, res io.Reader) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

func TestAppsCRUDAndAgentOnlyOperations(t *testing.T) {
	tm := newTeam(t)
	e, alice, bob, dave := tm.e, tm.alice, tm.bob, tm.dave
	var app gen.App
	expect(t, "bad idle", alice.do("POST", "/apps", gen.AppInput{Name: "x", IdleMinutes: ptr(-1)}, nil), 400)
	expect(t, "create", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, &app), 201)
	e.sandboxes.Wait()
	if app.IdleMinutes != 0 || app.Cpus != 1 || app.MemoryMb != 1024 || app.Role != gen.Owner || app.Links.Services != "/api/v1/agents/"+app.Id+"/services" {
		t.Fatalf("app = %+v", app)
	}
	expect(t, "get", alice.do("GET", "/apps/"+app.Id, nil, &app), 200)
	if app.State != gen.SandboxStateRunning {
		t.Fatalf("state = %s", app.State)
	}

	var agents []gen.Agent
	expect(t, "agents", alice.do("GET", "/agents", nil, &agents), 200)
	if len(agents) != 1 || agents[0].Id != tm.agent.Id {
		t.Fatalf("/agents lists apps: %+v", agents)
	}
	var list []gen.App
	expect(t, "apps", alice.do("GET", "/apps", nil, &list), 200)
	if len(list) != 1 || list[0].Id != app.Id {
		t.Fatalf("/apps = %+v", list)
	}
	expect(t, "an agent is not an app", alice.do("GET", "/apps/"+tm.agent.Id, nil, nil), 404)
	for _, p := range []string{"", "/start", "/stop", "/rebuild"} {
		m := "POST"
		if p == "" {
			m = "GET"
		}
		expect(t, "agent op on app "+p, alice.do(m, "/agents/"+app.Id+p, nil, nil), 404)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/sessions"},
		{"POST", "/sessions"},
		{"GET", "/harness"},
		{"GET", "/schedules"},
		{"POST", "/schedules"},
		{"GET", "/webhooks"},
		{"GET", "/mcp-servers"},
		{"GET", "/skills"},
	} {
		body := any(nil)
		if c.method == "POST" {
			body = map[string]any{"title": "x", "name": "x", "cron": "* * * * *", "prompt": "x"}
		}
		expect(t, "harness op "+c.path, alice.do(c.method, "/agents/"+app.Id+c.path, body, nil), 409)
	}
	// Machine sub-resources work.
	var ls gen.DirListing
	expect(t, "files", alice.do("GET", "/agents/"+app.Id+"/files", nil, &ls), 200)
	expect(t, "services", alice.do("GET", "/agents/"+app.Id+"/services", nil, nil), 200)
	expect(t, "share", alice.do("POST", "/agents/"+app.Id+"/shares", gen.ShareInput{Email: tm.emails["bob"]}, nil), 200)
	expect(t, "shared user reads", bob.do("GET", "/apps/"+app.Id, nil, &app), 200)
	if app.Role != gen.Shared {
		t.Fatalf("role = %s", app.Role)
	}
	expect(t, "shared user cannot change", bob.do("PATCH", "/apps/"+app.Id, gen.AppPatch{Name: ptr("x")}, nil), 403)
	expect(t, "stranger", dave.do("GET", "/apps/"+app.Id, nil, nil), 404)
	expect(t, "patch", alice.do("PATCH", "/apps/"+app.Id, gen.AppPatch{IdleMinutes: ptr(30)}, &app), 200)
	if app.IdleMinutes != 30 {
		t.Fatalf("patched = %+v", app)
	}
	expect(t, "stop", alice.do("POST", "/apps/"+app.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "start", alice.do("POST", "/apps/"+app.Id+"/start", nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "delete", alice.do("DELETE", "/apps/"+app.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "deleted", alice.do("GET", "/apps/"+app.Id, nil, nil), 404)
}

func TestAppNeverSleepsByDefault(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	var app gen.App
	expect(t, "create", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, &app), 201)
	e.sandboxes.Wait()
	e.clock.Advance(48 * time.Hour)
	if err := e.sleeper.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(48 * time.Hour)
	_ = e.sleeper.Tick(t.Context())
	e.sandboxes.Wait()
	expect(t, "get", alice.do("GET", "/apps/"+app.Id, nil, &app), 200)
	if app.State != gen.SandboxStateRunning || app.SleepsAt != nil {
		t.Fatalf("an always-on app slept: %+v", app)
	}
}

// writeSite puts a tiny static site into the agent's home.
func writeSite(t *testing.T, home, dir, body string) {
	t.Helper()
	p := filepath.Join(home, dir)
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(p, ".git", "HEAD"), []byte("ref"), 0o644)
}

const pyServer = "exec python3 -m http.server $PORT --bind 127.0.0.1"

func TestDeployAppEndToEnd(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	writeSite(t, e.home(ag), "site", "<h1>v1</h1>")
	var app gen.App
	expect(t, "create", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, &app), 201)
	e.sandboxes.Wait()
	port := freePort(t)

	var res gen.DeployResult
	in := gen.DeployInput{SourceAgentId: ag, SourceDir: "site", Command: pyServer, Port: port}
	expect(t, "stranger source", tm.dave.do("POST", "/apps/"+app.Id+"/deploy", in, nil), 404)
	expect(t, "deploy", alice.do("POST", "/apps/"+app.Id+"/deploy", in, &res), 200)
	if !res.Ready || res.Release != 1 || res.Url == nil || res.Exposure == nil || res.Exposure.Access != gen.Private {
		t.Fatalf("deploy = %+v", res)
	}
	home := e.home(app.Id)
	if l, err := os.Readlink(filepath.Join(home, "app/current")); err != nil || l != "releases/1" {
		t.Fatalf("current -> %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(home, "app/releases/1/.git")); err == nil {
		t.Fatal(".git was deployed")
	}
	// Served through the preview router behind the gate.
	ab := e.browser(alice, true)
	if r := get(t, ab, *res.Url+"index.html", nil); r.status != 200 || r.body != "<h1>v1</h1>" {
		t.Fatalf("v1: %d %q", r.status, r.body)
	}
	if r := get(t, e.browser(e.anon(), false), *res.Url, nil); r.status != 302 {
		t.Fatalf("anonymous: %d", r.status)
	}
	expect(t, "get", alice.do("GET", "/apps/"+app.Id, nil, &app), 200)
	if app.Url == nil || *app.Url != *res.Url {
		t.Fatalf("app url = %v", app.Url)
	}

	// Redeploy: new release, same URL.
	writeSite(t, e.home(ag), "site", "<h1>v2</h1>")
	expect(t, "redeploy", alice.do("POST", "/apps/"+app.Id+"/deploy", in, &res), 200)
	if !res.Ready || res.Release != 2 {
		t.Fatalf("redeploy = %+v", res)
	}
	waitUntil(t, "v2 served", 5*time.Second, func() bool {
		r := get(t, ab, *res.Url+"index.html", nil)
		return r.body == "<h1>v2</h1>"
	})

	// A release that does not come up is rolled back; v2 keeps serving.
	bad := in
	bad.Command = "echo boom >&2; exit 1"
	waitedFrom := time.Now()
	e.apps.Limits.DeployMaxBytes = 1 << 30
	expect(t, "broken deploy", alice.do("POST", "/apps/"+app.Id+"/deploy", bad, &res), 200)
	if res.Ready || !res.RolledBack || !strings.Contains(res.Logs, "boom") {
		t.Fatalf("broken = %+v (after %s)", res, time.Since(waitedFrom))
	}
	waitUntil(t, "v2 after rollback", 10*time.Second, func() bool {
		r := get(t, ab, *res.Url+"index.html", nil)
		return r.body == "<h1>v2</h1>"
	})
	if l, _ := os.Readlink(filepath.Join(home, "app/current")); l != "releases/2" {
		t.Fatalf("current after rollback -> %q", l)
	}
	var svc gen.Service
	expect(t, "service", alice.do("GET", "/agents/"+app.Id+"/services/app", nil, &svc), 200)
	if svc.Command != pyServer {
		t.Fatalf("service definition not restored: %+v", svc)
	}

	// A copy that fails (missing directory) changes nothing.
	missing := in
	missing.SourceDir = "nope"
	if st := alice.do("POST", "/apps/"+app.Id+"/deploy", missing, nil); st < 400 {
		t.Fatalf("missing source: %d", st)
	}
	if r := get(t, ab, *res.Url+"index.html", nil); r.body != "<h1>v2</h1>" {
		t.Fatalf("after failed copy: %q", r.body)
	}

	// Releases are pruned to the last 3.
	for range 2 {
		expect(t, "more deploys", alice.do("POST", "/apps/"+app.Id+"/deploy", in, &res), 200)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "app/releases"))
	if len(entries) != 3 {
		var names []string
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Fatalf("releases = %v", names)
	}
}

func (e *env) tool(agentID, tool string, args any) (string, error) {
	e.t.Helper()
	b, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(e.t.Context(), 2*time.Minute)
	defer cancel()
	out, err := e.platform.Call(ctx, agentID, "", tool, b)
	if err != nil {
		return "", err
	}
	s, _ := out.(string)
	return s, nil
}

func TestServiceTools(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	ag := tm.agent.Id
	out, err := e.tool(ag, "start_service", map[string]any{"name": "dev", "command": "echo up; exec sleep 300"})
	if err != nil || !strings.Contains(out, "running") {
		t.Fatalf("start_service: %q %v", out, err)
	}
	out, err = e.tool(ag, "list_services", nil)
	if err != nil || !strings.Contains(out, `"name": "dev"`) {
		t.Fatalf("list_services: %q %v", out, err)
	}
	waitUntil(t, "log", 5*time.Second, func() bool {
		out, _ = e.tool(ag, "service_logs", map[string]any{"name": "dev"})
		return strings.Contains(out, "up")
	})
	if out, err = e.tool(ag, "stop_service", map[string]any{"name": "dev"}); err != nil {
		t.Fatal(err)
	}
	if st := e.serviceState(tm.alice, ag, "dev"); st != "stopped" {
		t.Fatalf("after stop_service: %s", st)
	}
	var tools []map[string]any
	res, _ := e.platform.Call(t.Context(), ag, "", "_list_tools", nil)
	b, _ := json.Marshal(res)
	_ = json.Unmarshal(b, &tools)
	var desc string
	for _, tl := range tools {
		if tl["name"] == "start_service" {
			desc, _ = tl["description"].(string)
		}
	}
	if !strings.Contains(desc, "stopped when your harness ends") || !strings.Contains(desc, "expose_port") {
		t.Fatalf("start_service description: %q", desc)
	}
}

func TestAppTools(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	var other gen.Agent
	expect(t, "second agent", alice.do("POST", "/agents", gen.AgentInput{Name: "other", Harness: ptr("fake")}, &other), 201)
	e.sandboxes.Wait()
	writeSite(t, e.home(ag), "site", "<h1>tool</h1>")
	port := freePort(t)
	args := map[string]any{"name": "web", "source_dir": "site", "command": pyServer, "port": port}

	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "web", "source_dir": "site", "command": pyServer, "port": port, "access": "public"}); err == nil {
		t.Fatal("an agent made an app public")
	}
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "web", "source_dir": "site", "command": pyServer, "port": port, "memory_mb": 99999}); err == nil {
		t.Fatal("over the size limit")
	}
	out, err := e.tool(ag, "deploy_app", args)
	if err != nil || !strings.Contains(out, "Created app web") || !strings.Contains(out, "serving at http://") {
		t.Fatalf("deploy_app: %q %v", out, err)
	}
	var apps []gen.App
	expect(t, "apps", alice.do("GET", "/apps", nil, &apps), 200)
	if len(apps) != 1 || apps[0].CreatedByAgent == nil || apps[0].CreatedByAgent.Id != ag || apps[0].IdleMinutes != 0 {
		t.Fatalf("apps = %+v", apps)
	}
	url := *apps[0].Url
	if r := get(t, e.browser(alice, true), url+"index.html", nil); r.body != "<h1>tool</h1>" {
		t.Fatalf("served: %d %q", r.status, r.body)
	}
	// Apps are not agents to message.
	// Match the name field: agent ids are random and can contain "web" by chance.
	if out, _ := e.tool(ag, "list_agents", nil); strings.Contains(out, `"name": "web"`) {
		t.Fatalf("list_agents shows the app: %s", out)
	}
	if out, err = e.tool(ag, "list_apps", nil); err != nil || !strings.Contains(out, `"createdByYou": "yes"`) || strings.Contains(out, `"id"`) {
		t.Fatalf("list_apps: %q %v", out, err)
	}
	if out, err = e.tool(ag, "app_logs", map[string]any{"name": "web"}); err != nil || !strings.Contains(out, "agenthold: started") {
		t.Fatalf("app_logs: %q %v", out, err)
	}
	if out, err = e.tool(ag, "app_exec", map[string]any{"name": "web", "command": "cat index.html; exit 4"}); err != nil ||
		!strings.Contains(out, "Exit code 4") || !strings.Contains(out, "<h1>tool</h1>") {
		t.Fatalf("app_exec: %q %v", out, err)
	}
	if out, err = e.tool(ag, "restart_app", map[string]any{"name": "web"}); err != nil || !strings.Contains(out, "restarted") {
		t.Fatalf("restart_app: %q %v", out, err)
	}

	// Another agent of the same owner sees it in list_apps but cannot use it at all.
	if out, _ := e.tool(other.Id, "list_apps", nil); !strings.Contains(out, `"createdByYou": "no"`) {
		t.Fatalf("other agent list_apps: %s", out)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"deploy_app", args},
		{"delete_app", map[string]any{"name": "web"}},
		{"app_logs", map[string]any{"name": "web"}},
		{"app_exec", map[string]any{"name": "web", "command": "true"}},
		{"restart_app", map[string]any{"name": "web"}},
		{"stop_app", map[string]any{"name": "web"}},
	} {
		if _, err := e.tool(other.Id, c.tool, c.args); err == nil || !strings.Contains(err.Error(), "not managed by you") {
			t.Fatalf("other agent %s: %v", c.tool, err)
		}
	}
	// An agent of another owner does not see it at all.
	var dagent gen.Agent
	expect(t, "dave's agent", tm.dave.do("POST", "/agents", gen.AgentInput{Name: "d", Harness: ptr("fake")}, &dagent), 201)
	e.sandboxes.Wait()
	if _, err := e.tool(dagent.Id, "app_logs", map[string]any{"name": "web"}); err == nil {
		t.Fatal("a stranger's agent read the app")
	}

	// The per-agent limit.
	e.apps.Limits.MaxPerAgent = ptr(1)
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "web2", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err == nil ||
		!strings.Contains(err.Error(), "most an agent may have") {
		t.Fatalf("limit: %v", err)
	}
	e.apps.Limits.MaxPerAgent = ptr(0)
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "web2", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err == nil ||
		!strings.Contains(err.Error(), "agents cannot create apps") {
		t.Fatalf("max_per_agent 0: %v", err)
	}
	// Updating its own app still works with no room for new ones.
	if _, err := e.tool(ag, "deploy_app", args); err != nil {
		t.Fatalf("redeploy at the limit: %v", err)
	}
	// A name the owner already uses for an app this agent did not create.
	e.apps.Limits.MaxPerAgent = ptr(3)
	var mine gen.App
	expect(t, "owner's app", alice.do("POST", "/apps", gen.AppInput{Name: "owned"}, &mine), 201)
	e.sandboxes.Wait()
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "owned", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err == nil ||
		!strings.Contains(err.Error(), "not managed by you") {
		t.Fatalf("owner's name: %v", err)
	}

	if out, err = e.tool(ag, "stop_app", map[string]any{"name": "web"}); err != nil {
		t.Fatalf("stop_app: %q %v", out, err)
	}
	e.sandboxes.Wait()
	if out, err = e.tool(ag, "delete_app", map[string]any{"name": "web"}); err != nil {
		t.Fatalf("delete_app: %q %v", out, err)
	}
	e.sandboxes.Wait()
	expect(t, "apps after delete", alice.do("GET", "/apps", nil, &apps), 200)
	if len(apps) != 1 || apps[0].Name != "owned" {
		t.Fatalf("apps = %+v", apps)
	}
}

func TestAppSurvivesCreatorDeletion(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	writeSite(t, e.home(ag), "site", "x")
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "keep", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err != nil {
		t.Fatal(err)
	}
	expect(t, "delete agent", alice.do("DELETE", "/agents/"+ag, nil, nil), 202)
	e.sandboxes.Wait()
	var apps []gen.App
	expect(t, "apps", alice.do("GET", "/apps", nil, &apps), 200)
	if len(apps) != 1 || apps[0].CreatedByAgent == nil || apps[0].CreatedByAgent.Name != "" {
		t.Fatalf("apps = %+v", apps)
	}
}

func TestAppTakeOver(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	tm.share("bob")
	writeSite(t, e.home(ag), "site", "x")
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "mine", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err != nil {
		t.Fatal(err)
	}
	var apps []gen.App
	expect(t, "apps", alice.do("GET", "/apps", nil, &apps), 200)
	app := apps[0]
	if app.CreatedByAgent == nil || app.CreatedByAgent.Id != ag {
		t.Fatalf("app = %+v", app)
	}
	expect(t, "take over by a stranger", tm.dave.do("POST", "/apps/"+app.Id+"/take-over", nil, nil), 404)
	var taken gen.App
	expect(t, "take over", alice.do("POST", "/apps/"+app.Id+"/take-over", nil, &taken), 200)
	if taken.CreatedByAgent != nil {
		t.Fatalf("still managed: %+v", taken.CreatedByAgent)
	}
	for _, tool := range []string{"app_logs", "restart_app", "stop_app", "delete_app"} {
		if _, err := e.tool(ag, tool, map[string]any{"name": "mine"}); err == nil {
			t.Fatalf("%s after take-over", tool)
		}
	}
	if _, err := e.tool(ag, "app_exec", map[string]any{"name": "mine", "command": "true"}); err == nil {
		t.Fatal("app_exec after take-over")
	}
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "mine", "source_dir": "site", "command": pyServer, "port": freePort(t)}); err == nil {
		t.Fatal("deploy after take-over")
	}
}

func TestAppNamesAndOwnerLimit(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	expect(t, "not a DNS label", alice.do("POST", "/apps", gen.AppInput{Name: "My Site"}, nil), 400)
	expect(t, "create", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, nil), 201)
	expect(t, "same name", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, nil), 409)
	var other gen.App
	expect(t, "create other", alice.do("POST", "/apps", gen.AppInput{Name: "other"}, &other), 201)
	expect(t, "rename onto a taken name", alice.do("PATCH", "/apps/"+other.Id, gen.AppPatch{Name: ptr("site")}, nil), 409)
	// Another user may use the same name.
	bob := e.login("bob", "Bob", "b@example.com")
	expect(t, "bob's site", bob.do("POST", "/apps", gen.AppInput{Name: "site"}, nil), 201)
	e.apps.Limits.MaxPerOwner = ptr(2)
	code, body := alice.rawJSON("POST", "/apps", gen.AppInput{Name: "third"})
	if code != 409 || !strings.Contains(body, "max_per_owner") {
		t.Fatalf("owner limit: %d %s", code, body)
	}
	e.sandboxes.Wait()
}

// Concurrent creates cannot overshoot the per-owner limit.
func TestAppOwnerLimitConcurrent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	e.apps.Limits.MaxPerOwner = ptr(3)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := range 8 {
		wg.Go(func() { codes <- alice.do("POST", "/apps", gen.AppInput{Name: "a" + strconv.Itoa(i)}, nil) })
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		if c == 201 {
			created++
		} else if c != 409 {
			t.Fatalf("status %d", c)
		}
	}
	if created != 3 {
		t.Fatalf("created %d apps, limit 3", created)
	}
	e.sandboxes.Wait()
}

func TestDeployExposureRules(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	writeSite(t, e.home(ag), "site", "<h1>v1</h1>")
	port := freePort(t)
	// A first deploy that never serves gets no URL.
	if out, err := e.tool(ag, "deploy_app", map[string]any{"name": "rules", "source_dir": "site", "command": "exit 1", "port": port}); err != nil || strings.Contains(out, "serving at") {
		t.Fatalf("failing first deploy: %q %v", out, err)
	}
	var apps []gen.App
	expect(t, "apps", alice.do("GET", "/apps", nil, &apps), 200)
	app := apps[0]
	var list gen.ExposureList
	expect(t, "exposures", alice.do("GET", "/agents/"+app.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 0 {
		t.Fatalf("a release that never served got a URL: %+v", list.Exposures)
	}
	// It serves: exposed as authenticated (set at creation).
	if out, err := e.tool(ag, "deploy_app", map[string]any{"name": "rules", "source_dir": "site", "command": pyServer, "port": port, "access": "authenticated"}); err != nil || !strings.Contains(out, "access: authenticated") {
		t.Fatalf("deploy: %q %v", out, err)
	}
	expect(t, "exposures", alice.do("GET", "/agents/"+app.Id+"/exposures", nil, &list), 200)
	first := list.Exposures[0]
	// The owner makes it public; the agent's later access choice does not change that.
	expect(t, "owner sets public", alice.do("PATCH", "/agents/"+app.Id+"/exposures/"+first.Id, gen.ExposurePatch{Access: ptr(gen.Public)}, nil), 200)
	out, err := e.tool(ag, "deploy_app", map[string]any{"name": "rules", "source_dir": "site", "command": pyServer, "port": port, "access": "private"})
	if err != nil || !strings.Contains(out, "access stays public") {
		t.Fatalf("redeploy with access: %q %v", out, err)
	}
	// A new port moves the exposure in place: same id, name and access.
	port2 := freePort(t)
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "rules", "source_dir": "site", "command": pyServer, "port": port2}); err != nil {
		t.Fatal(err)
	}
	expect(t, "exposures", alice.do("GET", "/agents/"+app.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 1 || list.Exposures[0].Id != first.Id || list.Exposures[0].Port != port2 ||
		list.Exposures[0].Name != first.Name || list.Exposures[0].Access != gen.Public {
		t.Fatalf("after the port change: %+v (was %+v)", list.Exposures, first)
	}
	// A broken release on yet another port leaves the exposure exactly as it was.
	if _, err := e.tool(ag, "deploy_app", map[string]any{"name": "rules", "source_dir": "site", "command": "exit 1", "port": freePort(t)}); err != nil {
		t.Fatal(err)
	}
	expect(t, "exposures", alice.do("GET", "/agents/"+app.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 1 || list.Exposures[0].Port != port2 {
		t.Fatalf("after a failed deploy: %+v", list.Exposures)
	}
	waitUntil(t, "the rolled back release serves", 10*time.Second, func() bool {
		return get(t, e.browser(e.anon(), false), list.Exposures[0].Url+"index.html", nil).body == "<h1>v1</h1>"
	})
}

func TestCloneAppAndTemplate(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	var app gen.App
	expect(t, "create", alice.do("POST", "/apps", gen.AppInput{Name: "orig"}, &app), 201)
	e.sandboxes.Wait()
	expect(t, "service", alice.do("PUT", "/agents/"+app.Id+"/services/web", gen.ServiceInput{Command: "exec sleep 300"}, nil), 200)
	e.expose(alice, app.Id, freePort(t), "")
	expect(t, "clone: not a DNS label", alice.do("POST", "/agents/"+app.Id+"/clone", gen.CloneInput{Name: "My Copy"}, nil), 400)
	expect(t, "clone: taken name", alice.do("POST", "/agents/"+app.Id+"/clone", gen.CloneInput{Name: "orig"}, nil), 409)
	var clone gen.Agent
	expect(t, "clone", alice.do("POST", "/agents/"+app.Id+"/clone", gen.CloneInput{Name: "copy"}, &clone), 201)
	e.sandboxes.Wait()
	var got gen.App
	expect(t, "clone is an app", alice.do("GET", "/apps/"+clone.Id, nil, &got), 200)
	if got.CreatedByAgent != nil {
		t.Fatalf("clone = %+v", got)
	}
	var svcs gen.ServiceList
	expect(t, "clone services", alice.do("GET", "/agents/"+clone.Id+"/services", nil, &svcs), 200)
	if len(svcs.Services) != 1 || svcs.Services[0].Command != "exec sleep 300" {
		t.Fatalf("services not cloned: %+v", svcs)
	}
	var list gen.ExposureList
	expect(t, "clone exposures", alice.do("GET", "/agents/"+clone.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 0 {
		t.Fatalf("exposures were cloned: %+v", list.Exposures)
	}
	// An app can be a template: stopped, never started, cloned into apps.
	expect(t, "to template", alice.do("PATCH", "/apps/"+app.Id, gen.AppPatch{IsTemplate: ptr(true)}, &got), 200)
	e.sandboxes.Wait()
	if !got.IsTemplate {
		t.Fatalf("not a template: %+v", got)
	}
	expect(t, "start a template", alice.do("POST", "/apps/"+app.Id+"/start", nil, nil), 409)
	var fromTemplate gen.Agent
	expect(t, "from template", alice.do("POST", "/agents/"+app.Id+"/clone", gen.CloneInput{Name: "from-tpl"}, &fromTemplate), 201)
	e.sandboxes.Wait()
	expect(t, "an app", alice.do("GET", "/apps/"+fromTemplate.Id, nil, &got), 200)
}
