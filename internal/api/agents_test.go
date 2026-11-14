package api_test

import (
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/sandbox"
)

func ptr[T any](v T) *T { return &v }

func (c *client) agent(id string) gen.Agent {
	c.t.Helper()
	var a gen.Agent
	expect(c.t, "get agent", c.do("GET", "/agents/"+id, nil, &a), 200)
	return a
}

func TestAgentLifecycleAndAccess(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "alice@example.com")
	bob := e.login("bob", "Bob", "bob@example.com")
	carol := e.login("carol", "Carol", "carol@example.com")

	var team gen.Workspace
	alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "Team"}, &team)
	ws := "/workspaces/" + team.Id
	for _, who := range []*client{bob, carol} {
		var inv gen.CreatedInvite
		alice.do("POST", ws+"/invites", gen.InviteInput{Role: "member"}, &inv)
		expect(t, "accept", who.do("POST", "/invites/"+inv.Token+"/accept", nil, nil), 200)
	}

	var limits gen.SandboxLimits
	expect(t, "limits", alice.do("GET", "/sandbox/limits", nil, &limits), 200)
	if limits.Driver != "fake" || limits.DefaultImage == "" {
		t.Fatalf("limits = %+v", limits)
	}

	// Validation.
	expect(t, "too many cpus", alice.do("POST", ws+"/agents", gen.AgentInput{Name: "x", Cpus: ptr(float32(99))}, nil), 400)
	expect(t, "bad image", alice.do("POST", ws+"/agents", gen.AgentInput{Name: "x", Image: ptr("a b")}, nil), 400)
	expect(t, "outsider creates", e.login("dave", "Dave", "d@example.com").do("POST", ws+"/agents", gen.AgentInput{Name: "x"}, nil), 404)

	var ag gen.Agent
	expect(t, "create", bob.do("POST", ws+"/agents", gen.AgentInput{Name: "Bob's agent", MemoryMb: ptr(int64(1024))}, &ag), 201)
	if ag.Image != limits.DefaultImage || ag.Role != gen.AgentRoleAdmin || ag.Owner.Name != "Bob" {
		t.Fatalf("created = %+v", ag)
	}
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.State != gen.SandboxStateRunning {
		t.Fatalf("state = %s (%s)", got.State, got.StateError)
	}

	// Visibility: owner and workspace owner see it; another member does not.
	var list []gen.Agent
	bob.do("GET", ws+"/agents", nil, &list)
	if len(list) != 1 {
		t.Fatalf("bob sees %d agents", len(list))
	}
	carol.do("GET", ws+"/agents", nil, &list)
	if len(list) != 0 {
		t.Fatalf("carol sees %d agents", len(list))
	}
	expect(t, "carol gets", carol.do("GET", "/agents/"+ag.Id, nil, nil), 404)
	expect(t, "carol stops", carol.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 404)
	if got := alice.agent(ag.Id); got.Role != gen.AgentRoleAdmin {
		t.Fatalf("workspace owner role = %s", got.Role)
	}

	// Lifecycle.
	expect(t, "stop", bob.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.State != gen.SandboxStateSleeping {
		t.Fatalf("after stop: %s", got.State)
	}
	expect(t, "stop again", bob.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 409)
	expect(t, "start", bob.do("POST", "/agents/"+ag.Id+"/start", nil, nil), 202)
	e.sandboxes.Wait()

	// Changing the image marks a rebuild as pending.
	var upd gen.Agent
	expect(t, "patch", bob.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Image: ptr("ubuntu:24.04")}, &upd), 200)
	if !upd.NeedsRebuild || upd.Image != "ubuntu:24.04" {
		t.Fatalf("after patch: %+v", upd)
	}
	expect(t, "rebuild", bob.do("POST", "/agents/"+ag.Id+"/rebuild", nil, nil), 202)
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.NeedsRebuild || got.State != gen.SandboxStateRunning {
		t.Fatalf("after rebuild: %+v", got)
	}
	if sb, _ := e.driver.Get(sandbox.ID("fake-" + ag.Id)); sb.Spec.Image != "ubuntu:24.04" {
		t.Fatalf("driver image = %s", sb.Spec.Image)
	}

	expect(t, "delete", bob.do("DELETE", "/agents/"+ag.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "deleted", bob.do("GET", "/agents/"+ag.Id, nil, nil), 404)
}
