package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/sandbox"
)

// cloneOf clones an agent and waits until the copy has finished.
func (e *env) cloneOf(c *client, src gen.Agent, name string, asTemplate bool) gen.Agent {
	e.t.Helper()
	var dst gen.Agent
	expect(e.t, "clone", c.do("POST", "/agents/"+src.Id+"/clone", gen.CloneInput{Name: name, AsTemplate: &asTemplate}, &dst), 201)
	if dst.State != gen.SandboxStateCreating {
		e.t.Fatalf("a fresh clone is %s, want creating", dst.State)
	}
	e.sandboxes.Wait()
	return dst
}

func (e *env) writeHome(agentID, name, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.home(agentID), name), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) readHome(agentID, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(e.home(agentID), name))
	return string(b), err
}

func TestCloneAgent(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	src := tm.agent
	tm.share("bob")
	ag := src.Id

	// What a clone copies: settings, attachments, disks. What it does not:
	// shares, sessions, schedules, webhooks.
	var sec gen.Secret
	expect(t, "secret", alice.do("POST", "/secrets", gen.SecretInput{Name: "Claude key", Value: "sk-ant-real", Preset: ptr("anthropic")}, &sec), 201)
	expect(t, "attach secret", alice.do("PUT", "/agents/"+ag+"/secrets/"+sec.Id, map[string]string{"envVar": "ANTHROPIC_API_KEY"}, nil), 200)
	var lib gen.MCPServer
	expect(t, "library mcp", alice.do("POST", "/mcp-servers", gen.MCPServerInput{Name: "docs", Transport: "http", Url: ptr("https://docs.example.com/mcp")}, &lib), 201)
	expect(t, "attach mcp", alice.do("PUT", "/agents/"+ag+"/mcp-servers/"+lib.Id, nil, nil), 204)
	expect(t, "own mcp", alice.do("POST", "/agents/"+ag+"/mcp-servers", gen.MCPServerInput{Name: "local", Transport: "stdio", Command: ptr("true")}, nil), 201)
	var skill gen.Skill
	expect(t, "skill", alice.do("POST", "/skills", gen.SkillInput{Name: "notes", Description: "Write notes", Content: "# Notes"}, &skill), 201)
	expect(t, "attach skill", alice.do("PUT", "/agents/"+ag+"/skills/"+skill.Id, nil, nil), 204)
	expect(t, "patch", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{IdleMinutes: ptr(42), MemoryMb: ptr(int64(2048))}, nil), 200)
	expect(t, "session", alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{}, nil), 201)
	expect(t, "schedule", alice.do("POST", "/agents/"+ag+"/schedules", gen.ScheduleInput{Name: "nightly", Cron: "@daily", Prompt: "go"}, nil), 201)
	expect(t, "webhook", alice.do("POST", "/agents/"+ag+"/webhooks", gen.WebhookInput{Name: "hook", PromptTemplate: "{{payload}}"}, nil), 201)
	e.writeHome(ag, "note.txt", "from the source")
	if err := e.store.MarkCredentialsInSandbox(t.Context(), ag); err != nil {
		t.Fatal(err)
	}
	// Changes that are not applied yet (the attached secret's variable, the
	// new memory) are inherited by a clone: it is refused until the system is reset.
	res := alice.raw("POST", "/agents/"+ag+"/clone", jsonBody(t, gen.CloneInput{Name: "too early"}))
	msg, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(msg), "reset the system first") {
		t.Fatalf("clone with pending changes: %d %s", res.StatusCode, msg)
	}
	expect(t, "reset", alice.do("POST", "/agents/"+ag+"/rebuild", nil, nil), 202)
	e.sandboxes.Wait()
	startsBefore := mustSandbox(t, e, ag).Starts

	dst := e.cloneOf(alice, src, "the copy", false)
	if dst.Id == src.Id || dst.Name != "the copy" || dst.Owner.Id != tm.ids["alice"] || dst.Role != gen.Owner {
		t.Fatalf("clone = %+v", dst)
	}
	if dst.ClonedFrom == nil || *dst.ClonedFrom != ag || dst.IsTemplate {
		t.Fatalf("lineage = %+v", dst)
	}
	got := alice.agent(dst.Id)
	if got.State != gen.SandboxStateSleeping {
		t.Fatalf("clone is %s (%s), want sleeping", got.State, got.StateError)
	}
	if got.Harness != src.Harness || got.IdleMinutes != 42 || got.MemoryMb != 2048 || got.Cpus != src.Cpus || got.PermissionPolicy != src.PermissionPolicy {
		t.Fatalf("settings differ: %+v", got)
	}
	// The home disk holds whatever login happened inside the sandbox: the flag goes with it.
	if !got.CredentialsInSandbox {
		t.Fatal("the clone lost credentialsInSandbox although its disk carries the credentials")
	}
	// Same node as the source.
	if got.NodeId == nil || src.NodeId == nil || *got.NodeId != *src.NodeId {
		t.Fatalf("clone on node %v, source on %v", got.NodeId, src.NodeId)
	}

	// The source was running and runs again.
	if s := alice.agent(ag); s.State != gen.SandboxStateRunning {
		t.Fatalf("source is %s (%s), want running again", s.State, s.StateError)
	}
	if sb := mustSandbox(t, e, ag); sb.Starts != startsBefore+1 {
		t.Fatalf("source was started %d times, want %d", sb.Starts, startsBefore+1)
	}

	// Disks: copied, then independent.
	if b, err := e.readHome(dst.Id, "note.txt"); err != nil || b != "from the source" {
		t.Fatalf("clone home: %q %v", b, err)
	}
	e.writeHome(ag, "later.txt", "only in the source")
	e.writeHome(dst.Id, "other.txt", "only in the clone")
	if _, err := e.readHome(dst.Id, "later.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a later write in the source shows in the clone: %v", err)
	}
	if _, err := e.readHome(ag, "other.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write in the clone shows in the source: %v", err)
	}
	if sb := mustSandbox(t, e, dst.Id); sb.ClonedFrom != sandbox.ID("fake-"+ag) {
		t.Fatalf("driver clone of %q", sb.ClonedFrom)
	}

	// Attachments are copied, shares, sessions, schedules and webhooks are not.
	var secrets []gen.AttachedSecret
	alice.do("GET", "/agents/"+dst.Id+"/secrets", nil, &secrets)
	if len(secrets) != 1 || secrets[0].Secret.Id != sec.Id || secrets[0].EnvVar != "ANTHROPIC_API_KEY" {
		t.Fatalf("secrets = %+v", secrets)
	}
	var servers []gen.MCPServer
	alice.do("GET", "/agents/"+dst.Id+"/mcp-servers", nil, &servers)
	names := map[string]gen.MCPServer{}
	for _, s := range servers {
		names[s.Name] = s
	}
	if len(servers) != 2 || names["docs"].Id != lib.Id || names["local"].AgentId == nil || *names["local"].AgentId != dst.Id {
		t.Fatalf("mcp servers = %+v", servers)
	}
	var skills []gen.Skill
	alice.do("GET", "/agents/"+dst.Id+"/skills", nil, &skills)
	if len(skills) != 1 || skills[0].Id != skill.Id {
		t.Fatalf("skills = %+v", skills)
	}
	var sessions []gen.Session
	alice.do("GET", "/agents/"+dst.Id+"/sessions", nil, &sessions)
	var schedules []gen.Schedule
	alice.do("GET", "/agents/"+dst.Id+"/schedules", nil, &schedules)
	var hooks []gen.Webhook
	alice.do("GET", "/agents/"+dst.Id+"/webhooks", nil, &hooks)
	var shares struct{ Shares []gen.Share }
	alice.do("GET", "/agents/"+dst.Id+"/shares", nil, &shares)
	if len(sessions)+len(schedules)+len(hooks)+len(shares.Shares) != 0 {
		t.Fatalf("clone has sessions %d, schedules %d, webhooks %d, shares %d", len(sessions), len(schedules), len(hooks), len(shares.Shares))
	}
	expect(t, "bob sees the clone", tm.bob.do("GET", "/agents/"+dst.Id, nil, nil), 404)

	// Audit: the owner sees the clone on the source and on the new agent.
	var log []gen.AuditEntry
	alice.do("GET", "/agents/"+ag+"/audit", nil, &log)
	found := false
	for _, a := range log {
		found = found || (a.Action == "agent.clone" && a.Details["cloneId"] == dst.Id)
	}
	if !found {
		t.Fatalf("no agent.clone entry in %+v", log)
	}

	// It is a machine of its own: it starts and runs next to its source.
	expect(t, "start clone", alice.do("POST", "/agents/"+dst.Id+"/start", nil, nil), 202)
	e.sandboxes.Wait()
	if s := alice.agent(dst.Id); s.State != gen.SandboxStateRunning {
		t.Fatalf("clone is %s (%s)", s.State, s.StateError)
	}
	if s := alice.agent(ag); s.State != gen.SandboxStateRunning {
		t.Fatalf("source is %s", s.State)
	}
}

func mustSandbox(t *testing.T, e *env, agentID string) (sb struct {
	Starts     int
	ClonedFrom sandbox.ID
},
) {
	t.Helper()
	got, ok := e.driver.Get(sandbox.ID("fake-" + agentID))
	if !ok {
		t.Fatalf("driver does not know %s", agentID)
	}
	sb.Starts, sb.ClonedFrom = got.Starts, got.ClonedFrom
	return sb
}

func TestCloneOfSleepingAgentStaysAsleep(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	expect(t, "stop", alice.do("POST", "/agents/"+tm.agent.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	e.cloneOf(alice, tm.agent, "copy", false)
	if s := alice.agent(tm.agent.Id); s.State != gen.SandboxStateSleeping {
		t.Fatalf("source is %s, want sleeping", s.State)
	}
	if got := mustSandbox(t, e, tm.agent.Id).Starts; got != 1 {
		t.Fatalf("a sleeping source was started again (%d starts)", got)
	}
}

func TestOnlyTheOwnerClones(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	path := "/agents/" + tm.agent.Id + "/clone"
	body := gen.CloneInput{Name: "stolen"}
	expect(t, "shared user", tm.bob.do("POST", path, body, nil), 403)
	expect(t, "stranger", tm.dave.do("POST", path, body, nil), 404)
	expect(t, "anonymous", tm.e.anon().do("POST", path, body, nil), 401)
	expect(t, "unknown agent", tm.alice.do("POST", "/agents/nope/clone", body, nil), 404)
	expect(t, "empty name", tm.alice.do("POST", path, gen.CloneInput{Name: "  "}, nil), 400)
	tm.e.sandboxes.Wait()
	var list []gen.Agent
	tm.alice.do("GET", "/agents", nil, &list)
	if len(list) != 1 {
		t.Fatalf("a refused clone left %d agents", len(list))
	}
	if s := tm.alice.agent(tm.agent.Id); s.State != gen.SandboxStateRunning {
		t.Fatalf("a refused clone disturbed the source: %s", s.State)
	}
}

func TestFailedCloneRestoresTheSource(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	e.driver.FailNext("clone", errors.New("disk full"))
	dst := e.cloneOf(alice, tm.agent, "doomed", false)
	got := alice.agent(dst.Id)
	if got.State != gen.SandboxStateError || !strings.Contains(got.StateError, "disk full") {
		t.Fatalf("failed clone is %s (%s)", got.State, got.StateError)
	}
	if s := alice.agent(tm.agent.Id); s.State != gen.SandboxStateRunning {
		t.Fatalf("source is %s (%s), want running again", s.State, s.StateError)
	}
	// Starting a clone that never got disks must not boot a blank machine.
	res := alice.raw("POST", "/agents/"+dst.Id+"/start", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(body), "clone did not finish") {
		t.Fatalf("start of an unfinished clone: %d %s", res.StatusCode, body)
	}
	expect(t, "delete", alice.do("DELETE", "/agents/"+dst.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "deleted", alice.do("GET", "/agents/"+dst.Id, nil, nil), 404)
}

func TestCloneRefusesAnAgentThatIsBusyOrBeingCloned(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	release := make(chan struct{})
	entered := make(chan struct{})
	e.driver.OnClone = func(ctx context.Context, _ sandbox.ID) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	var dst gen.Agent
	expect(t, "clone", alice.do("POST", "/agents/"+tm.agent.Id+"/clone", gen.CloneInput{Name: "slow"}, &dst), 201)
	<-entered
	// While its disks are copied the source is held: nothing else may touch it.
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateStopping {
		t.Fatalf("source during the copy: %s", s)
	}
	expect(t, "second clone", alice.do("POST", "/agents/"+tm.agent.Id+"/clone", gen.CloneInput{Name: "second"}, nil), 409)
	expect(t, "start", alice.do("POST", "/agents/"+tm.agent.Id+"/start", nil, nil), 409)
	expect(t, "stop", alice.do("POST", "/agents/"+tm.agent.Id+"/stop", nil, nil), 409)
	expect(t, "rebuild", alice.do("POST", "/agents/"+tm.agent.Id+"/rebuild", nil, nil), 409)
	expect(t, "make it a template", alice.do("PATCH", "/agents/"+tm.agent.Id, gen.AgentPatch{IsTemplate: ptr(true)}, nil), 409)
	close(release)
	e.sandboxes.Wait()
	if s := alice.agent(dst.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("clone is %s", s)
	}
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("source is %s", s)
	}
}

func TestTemplates(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	tm.share("bob")
	ag := tm.agent.Id
	e.writeHome(ag, "toolchain.txt", "installed")

	// Marking a template stops the machine first.
	var tpl gen.Agent
	expect(t, "mark", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{IsTemplate: ptr(true)}, &tpl), 200)
	e.sandboxes.Wait()
	if tpl = alice.agent(ag); !tpl.IsTemplate || tpl.State != gen.SandboxStateSleeping {
		t.Fatalf("template = %+v", tpl)
	}
	if sb, _ := e.driver.Get(sandbox.ID("fake-" + ag)); sb.State == sandbox.StateRunning {
		t.Fatal("the template is still running")
	}

	// Listing: templates have their own filter.
	var all, only, normal, dflt []gen.Agent
	alice.do("GET", "/agents?template=all", nil, &all)
	alice.do("GET", "/agents?template=true", nil, &only)
	alice.do("GET", "/agents?template=false", nil, &normal)
	alice.do("GET", "/agents", nil, &dflt)
	if len(all) != 1 || len(only) != 1 || only[0].Id != ag || len(normal) != 0 || len(dflt) != 0 {
		t.Fatalf("all=%d templates=%d normal=%d default=%d", len(all), len(only), len(normal), len(dflt))
	}
	expect(t, "bad filter", alice.do("GET", "/agents?template=maybe", nil, nil), 400)

	// It cannot be started, chatted with or scheduled.
	for _, tt := range []struct{ name, method, path string }{
		{"start", "POST", "/agents/" + ag + "/start"},
		{"session", "POST", "/agents/" + ag + "/sessions"},
		{"schedule", "POST", "/agents/" + ag + "/schedules"},
		{"webhook", "POST", "/agents/" + ag + "/webhooks"},
	} {
		var body any = map[string]string{}
		switch tt.name {
		case "schedule":
			body = gen.ScheduleInput{Name: "n", Cron: "@daily", Prompt: "p"}
		case "webhook":
			body = gen.WebhookInput{Name: "h", PromptTemplate: "{{payload}}"}
		}
		res := alice.raw(tt.method, tt.path, jsonBody(t, body))
		msg, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusConflict || !strings.Contains(string(msg), "template") {
			t.Errorf("%s: %d %s", tt.name, res.StatusCode, msg)
		}
	}
	e.sandboxes.Wait()
	if s := alice.agent(ag).State; s != gen.SandboxStateSleeping {
		t.Fatalf("template is %s after refused starts", s)
	}

	// Shared users and strangers cannot clone it either.
	expect(t, "shared user clones template", tm.bob.do("POST", "/agents/"+ag+"/clone", gen.CloneInput{Name: "x"}, nil), 403)
	expect(t, "shared user converts", tm.bob.do("PATCH", "/agents/"+ag, gen.AgentPatch{IsTemplate: ptr(false)}, nil), 403)

	// New agent from template = clone; the template stays what it is.
	dst := e.cloneOf(alice, tpl, "from template", false)
	got := alice.agent(dst.Id)
	if got.IsTemplate || got.State != gen.SandboxStateSleeping {
		t.Fatalf("agent from template = %+v", got)
	}
	if b, err := e.readHome(dst.Id, "toolchain.txt"); err != nil || b != "installed" {
		t.Fatalf("home of the new agent: %q %v", b, err)
	}
	if s := alice.agent(ag); !s.IsTemplate || s.State != gen.SandboxStateSleeping {
		t.Fatalf("template after cloning = %+v", s)
	}
	expect(t, "start the new agent", alice.do("POST", "/agents/"+dst.Id+"/start", nil, nil), 202)
	e.sandboxes.Wait()
	if s := alice.agent(dst.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("agent from template is %s", s)
	}

	// A clone can be made a template from the start (and then never boots).
	second := e.cloneOf(alice, got, "template of a template", true)
	if s := alice.agent(second.Id); !s.IsTemplate || s.State != gen.SandboxStateSleeping {
		t.Fatalf("clone as template = %+v", s)
	}
	expect(t, "start template clone", alice.do("POST", "/agents/"+second.Id+"/start", nil, nil), 409)

	// Converting back makes it a normal machine again.
	expect(t, "unmark", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{IsTemplate: ptr(false)}, nil), 200)
	if s := alice.agent(ag); s.IsTemplate {
		t.Fatal("still a template")
	}
	expect(t, "start after unmark", alice.do("POST", "/agents/"+ag+"/start", nil, nil), 202)
	e.sandboxes.Wait()
	if s := alice.agent(ag).State; s != gen.SandboxStateRunning {
		t.Fatalf("converted template is %s (%s)", s, alice.agent(ag).StateError)
	}
}

// Agents cannot message a template: it is not running and cannot be woken.
func TestTemplatesAreNotMessageableByAgents(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	worker := tm.newAgent(alice, "worker")
	if got := newSessionText(t, e, alice, tm.agent.Id, "agents"); !strings.Contains(got, `"name": "worker"`) {
		t.Fatalf("list_agents before: %s", got)
	}
	expect(t, "template", alice.do("PATCH", "/agents/"+worker.Id, gen.AgentPatch{IsTemplate: ptr(true)}, nil), 200)
	e.sandboxes.Wait()
	if got := newSessionText(t, e, alice, tm.agent.Id, "agents"); strings.Contains(got, `"name": "worker"`) {
		t.Fatalf("list_agents lists a template: %s", got)
	}
	if got := newSessionText(t, e, alice, tm.agent.Id, "ask worker are you there"); strings.Contains(got, "echo: are you there") {
		t.Fatalf("a template answered: %s", got)
	}
	if s := alice.agent(worker.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("template is %s", s)
	}
}

// The clone of an agent on an edge node whose connection is down fails fast
// with the node's unreachable error, and leaves nothing behind.
func TestCloneOnAnOfflineNode(t *testing.T) {
	tm := newTeam(t)
	if tm.e.edge == nil {
		t.Skip("needs AGENTHOLD_TEST_NODE=remote")
	}
	tm.e.edge.down()
	res := tm.alice.raw("POST", "/agents/"+tm.agent.Id+"/clone", jsonBody(t, gen.CloneInput{Name: "x"}))
	msg, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("clone on an offline node: %d %s", res.StatusCode, msg)
	}
	tm.e.edge.up()
	var list []gen.Agent
	tm.alice.do("GET", "/agents", nil, &list)
	if len(list) != 1 || list[0].State != gen.SandboxStateRunning {
		t.Fatalf("after the node returned: %+v", list)
	}
}

func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}

func TestClonedFromIsForTheOwner(t *testing.T) {
	tm := newTeam(t)
	dst := tm.e.cloneOf(tm.alice, tm.agent, "copy", false)
	if got := tm.alice.agent(dst.Id); got.ClonedFrom == nil || *got.ClonedFrom != tm.agent.Id {
		t.Fatalf("owner sees %v", got.ClonedFrom)
	}
	expect(t, "share", tm.alice.do("POST", "/agents/"+dst.Id+"/shares", gen.ShareInput{Email: tm.emails["bob"]}, nil), 200)
	if got := tm.bob.agent(dst.Id); got.ClonedFrom != nil {
		t.Fatalf("a shared user learns the source id %q", *got.ClonedFrom)
	}
	var list []gen.Agent
	tm.bob.do("GET", "/agents", nil, &list)
	if len(list) != 1 || list[0].ClonedFrom != nil {
		t.Fatalf("list for the shared user: %+v", list)
	}
}

// While the source is stopped for the copy, nothing may start work on it: a
// prompt accepted now would be killed.
func TestNoPromptsWhileTheSourceIsCopied(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+tm.agent.Id+"/sessions", map[string]string{}, &sess), 201)
	release, entered := make(chan struct{}), make(chan struct{})
	e.driver.OnClone = func(ctx context.Context, _ sandbox.ID) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	expect(t, "clone", alice.do("POST", "/agents/"+tm.agent.Id+"/clone", gen.CloneInput{Name: "slow"}, nil), 201)
	<-entered
	// The prompt is accepted (the turn runs in the background) but the wake is
	// refused, so the turn fails visibly instead of running on a doomed VM.
	expect(t, "prompt while copied", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hi"}, nil), 202)
	e.sessions.Wait()
	var evs []gen.SessionEvent
	alice.do("GET", "/sessions/"+sess.Id+"/events", nil, &evs)
	if !strings.Contains(fmt.Sprint(evs), "being stopped or copied") {
		t.Fatalf("the turn was not refused: %v", evs)
	}
	if got := mustSandbox(t, e, tm.agent.Id).Starts; got != 1 {
		t.Fatalf("a prompt woke the source during the copy (%d starts)", got)
	}
	close(release)
	e.sandboxes.Wait()
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateRunning {
		t.Fatalf("source is %s", s)
	}
}

func TestTemplateConversionIsAllOrNothingAndSilencesTriggers(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	var sched gen.Schedule
	expect(t, "schedule", alice.do("POST", "/agents/"+ag+"/schedules", gen.ScheduleInput{Name: "nightly", Cron: "@daily", Prompt: "go"}, &sched), 201)
	var hook gen.Webhook
	expect(t, "webhook", alice.do("POST", "/agents/"+ag+"/webhooks", gen.WebhookInput{Name: "hook", PromptTemplate: "{{payload}}"}, &hook), 201)

	// A refusal changes nothing, not even the other fields of the same request:
	// here the machine is being copied.
	release, entered := make(chan struct{}), make(chan struct{})
	e.driver.OnClone = func(ctx context.Context, _ sandbox.ID) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	expect(t, "clone", alice.do("POST", "/agents/"+ag+"/clone", gen.CloneInput{Name: "copy"}, nil), 201)
	<-entered
	expect(t, "template while copied", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{Name: ptr("renamed"), IsTemplate: ptr(true)}, nil), 409)
	if got := alice.agent(ag); got.Name == "renamed" || got.IsTemplate {
		t.Fatalf("a refused update changed the agent: %+v", got)
	}
	close(release)
	e.sandboxes.Wait()

	expect(t, "template", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{Name: ptr("renamed"), IsTemplate: ptr(true)}, nil), 200)
	var schedules []gen.Schedule
	alice.do("GET", "/agents/"+ag+"/schedules", nil, &schedules)
	var hooks []gen.Webhook
	alice.do("GET", "/agents/"+ag+"/webhooks", nil, &hooks)
	if len(schedules) != 1 || schedules[0].Enabled || len(hooks) != 1 || hooks[0].Enabled {
		t.Fatalf("triggers of a template: %+v %+v", schedules, hooks)
	}
	// They cannot be switched on while it is a template, nor run by hand.
	in := gen.ScheduleInput{Name: "nightly", Cron: "@daily", Prompt: "go", Enabled: ptr(true)}
	expect(t, "enable schedule", alice.do("PUT", "/schedules/"+sched.Id, in, nil), 409)
	expect(t, "run schedule", alice.do("POST", "/schedules/"+sched.Id+"/run", nil, nil), 409)
	expect(t, "enable webhook", alice.do("PUT", "/webhooks/"+hook.Id, gen.WebhookInput{Name: "hook", PromptTemplate: "{{payload}}", Enabled: ptr(true)}, nil), 409)

	// Back to an agent: they stay off until the owner turns them on.
	expect(t, "agent", alice.do("PATCH", "/agents/"+ag, gen.AgentPatch{IsTemplate: ptr(false)}, nil), 200)
	alice.do("GET", "/agents/"+ag+"/schedules", nil, &schedules)
	if schedules[0].Enabled {
		t.Fatal("a schedule came back on by itself")
	}
	expect(t, "enable schedule again", alice.do("PUT", "/schedules/"+sched.Id, in, nil), 200)
}

// Exposures belong to a machine: a clone starts without any, and a template
// keeps its own but they are inert (they answer like a machine that cannot be woken).
func TestExposuresAndCloneTemplates(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	app := startUpstream(t)
	ex := e.expose(alice, tm.agent.Id, app.port, "public")

	dst := e.cloneOf(alice, tm.agent, "copy", false)
	var list gen.ExposureList
	expect(t, "clone exposures", alice.do("GET", "/agents/"+dst.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 0 {
		t.Fatalf("a clone inherited exposures: %+v", list.Exposures)
	}
	expect(t, "source exposures", alice.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 1 || list.Exposures[0].Id != ex.Id {
		t.Fatalf("source exposures after the clone: %+v", list.Exposures)
	}
	anon := e.browser(e.anon(), false)
	if r := get(t, anon, ex.Url, nil); r.status != 200 {
		t.Fatalf("the source serves again after the clone: %d %s", r.status, r.body)
	}
	app.last(t)

	// Converting to a template leaves the exposure in place, inert.
	expect(t, "template", alice.do("PATCH", "/agents/"+tm.agent.Id, gen.AgentPatch{IsTemplate: ptr(true)}, nil), 200)
	e.sandboxes.Wait()
	expect(t, "exposures kept", alice.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 1 {
		t.Fatalf("exposures of a template: %+v", list.Exposures)
	}
	before := e.tracker.Last(tm.agent.Id)
	e.clock.Advance(time.Hour)
	var tok gen.CreatedAPIToken
	expect(t, "token", alice.do("POST", "/me/tokens", map[string]string{"name": "script"}, &tok), 201)
	for name, headers := range map[string]map[string]string{
		"visitor": nil,
		"owner":   {"Authorization": "Bearer " + tok.Token},
	} {
		r := get(t, anon, ex.Url, headers)
		if r.status != 503 || !strings.Contains(r.body, "template") {
			t.Fatalf("%s on a template: %d %s", name, r.status, r.body)
		}
	}
	if s := alice.agent(tm.agent.Id).State; s != gen.SandboxStateSleeping {
		t.Fatalf("a request woke the template: %s", s)
	}
	if !e.tracker.Last(tm.agent.Id).Equal(before) {
		t.Fatal("a request to a template counted as activity")
	}
	// Back to an agent: served again (the owner wakes it).
	expect(t, "agent", alice.do("PATCH", "/agents/"+tm.agent.Id, gen.AgentPatch{IsTemplate: ptr(false)}, nil), 200)
	if r := get(t, anon, ex.Url, map[string]string{"Authorization": "Bearer " + tok.Token}); r.status != 200 {
		t.Fatalf("after converting back: %d %s", r.status, r.body)
	}

	// Deleting the clone deletes its exposures only.
	expect(t, "exposure on the clone", alice.do("POST", "/agents/"+dst.Id+"/exposures", gen.ExposureInput{Port: app.port}, nil), 201)
	expect(t, "delete clone", alice.do("DELETE", "/agents/"+dst.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "source exposures", alice.do("GET", "/agents/"+tm.agent.Id+"/exposures", nil, &list), 200)
	if len(list.Exposures) != 1 {
		t.Fatalf("deleting the clone removed the source's exposures: %+v", list.Exposures)
	}
}
