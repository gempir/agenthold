package api_test

import (
	"encoding/json"
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
	bob := e.login("bob", "Bob", "bob@example.com")
	carol := e.login("carol", "Carol", "carol@example.com")

	var limits gen.SandboxLimits
	expect(t, "limits", bob.do("GET", "/sandbox/limits", nil, &limits), 200)
	if limits.Driver != "fake" {
		t.Fatalf("limits = %+v", limits)
	}

	// Validation.
	expect(t, "too many cpus", bob.do("POST", "/agents", gen.AgentInput{Name: "x", Cpus: ptr(float32(99))}, nil), 400)
	expect(t, "image rejected on create", bob.do("POST", "/agents", map[string]any{"name": "x", "image": "ubuntu:24.04"}, nil), 400)
	expect(t, "anonymous creates", e.anon().do("POST", "/agents", gen.AgentInput{Name: "x"}, nil), 401)

	var ag gen.Agent
	expect(t, "create", bob.do("POST", "/agents", gen.AgentInput{Name: "Bob's agent", MemoryMb: ptr(int64(1024))}, &ag), 201)
	if ag.Role != gen.Owner || ag.Owner.Name != "Bob" {
		t.Fatalf("created = %+v", ag)
	}
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.State != gen.SandboxStateRunning {
		t.Fatalf("state = %s (%s)", got.State, got.StateError)
	}

	// Visibility: the owner sees it; another user does not.
	var list []gen.Agent
	bob.do("GET", "/agents", nil, &list)
	if len(list) != 1 {
		t.Fatalf("bob sees %d agents", len(list))
	}
	carol.do("GET", "/agents", nil, &list)
	if len(list) != 0 {
		t.Fatalf("carol sees %d agents", len(list))
	}
	expect(t, "carol gets", carol.do("GET", "/agents/"+ag.Id, nil, nil), 404)
	expect(t, "carol stops", carol.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 404)

	// Lifecycle.
	expect(t, "stop", bob.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.State != gen.SandboxStateSleeping {
		t.Fatalf("after stop: %s", got.State)
	}
	expect(t, "stop again", bob.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 409)
	expect(t, "start", bob.do("POST", "/agents/"+ag.Id+"/start", nil, nil), 202)
	e.sandboxes.Wait()

	expect(t, "image rejected on patch", bob.do("PATCH", "/agents/"+ag.Id, map[string]any{"image": "ubuntu:24.04"}, nil), 400)

	// Resource changes mark a reset as pending.
	var upd gen.Agent
	expect(t, "patch", bob.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{MemoryMb: ptr(int64(2048))}, &upd), 200)
	if !upd.NeedsRebuild || upd.MemoryMb != 2048 {
		t.Fatalf("after patch: %+v", upd)
	}
	expect(t, "rebuild", bob.do("POST", "/agents/"+ag.Id+"/rebuild", nil, nil), 202)
	e.sandboxes.Wait()
	if got := bob.agent(ag.Id); got.NeedsRebuild || got.State != gen.SandboxStateRunning {
		t.Fatalf("after rebuild: %+v", got)
	}
	if sb, _ := e.driver.Get(sandbox.ID("fake-" + ag.Id)); sb.Spec.Image != "base" || sb.Spec.Resources.MemoryMB != 2048 {
		t.Fatalf("driver spec = %+v", sb.Spec)
	}

	expect(t, "delete", bob.do("DELETE", "/agents/"+ag.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "deleted", bob.do("GET", "/agents/"+ag.Id, nil, nil), 404)
}

func jsonUnmarshalTest(b []byte, v any) error { return json.Unmarshal(b, v) }
