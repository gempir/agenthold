package api_test

import (
	"io"
	"net/netip"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestSecretsAndAttachments(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")

	var presets []gen.SecretPreset
	expect(t, "presets", alice.do("GET", "/secrets/presets", nil, &presets), 200)
	if len(presets) == 0 || presets[0].Id != "anthropic" {
		t.Fatalf("presets %+v", presets)
	}

	var sec gen.Secret
	expect(t, "create", alice.do("POST", "/secrets", gen.SecretInput{Name: "Claude key", Value: "sk-ant-real", Preset: ptr("anthropic")}, &sec), 201)
	if sec.Rule.Header != "x-api-key" || !strings.HasPrefix(sec.Placeholder, "agenthold-placeholder-") || !sec.CanManage {
		t.Fatalf("secret %+v", sec)
	}
	expect(t, "duplicate", alice.do("POST", "/secrets", gen.SecretInput{Name: "Claude key", Value: "x", Preset: ptr("anthropic")}, nil), 409)
	expect(t, "bad rule", alice.do("POST", "/secrets", gen.SecretInput{Name: "x", Value: "x", Rule: &gen.SecretRule{Hosts: []string{"*"}, Header: "A"}}, nil), 400)
	expect(t, "newline value", alice.do("POST", "/secrets", gen.SecretInput{Name: "y", Value: "a\nb", Preset: ptr("openai")}, nil), 400)

	// The value is never returned in any form.
	res := alice.raw("GET", "/secrets", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "sk-ant-real") || !strings.Contains(string(body), "Claude key") {
		t.Fatalf("list body %s", body)
	}
	// Bob cannot see or manage Alice's secret.
	var bobList []gen.Secret
	bob.do("GET", "/secrets", nil, &bobList)
	if len(bobList) != 0 {
		t.Fatalf("bob sees %d secrets", len(bobList))
	}
	expect(t, "bob patch", bob.do("PATCH", "/secrets/"+sec.Id, gen.SecretPatch{Name: ptr("mine")}, nil), 404)
	expect(t, "bob deletes", bob.do("DELETE", "/secrets/"+sec.Id, nil, nil), 404)
	// Names are per user: bob can have a secret with the same name.
	expect(t, "bob same name", bob.do("POST", "/secrets", gen.SecretInput{Name: "Claude key", Value: "bobs", Preset: ptr("anthropic")}, nil), 201)

	// Bob's own agent: he cannot attach Alice's secret.
	var bobs gen.Agent
	expect(t, "bob's agent", bob.do("POST", "/agents", gen.AgentInput{Name: "bob-agent"}, &bobs), 201)
	e.sandboxes.Wait()
	expect(t, "bob attaches alice's secret", bob.do("PUT", "/agents/"+bobs.Id+"/secrets/"+sec.Id, map[string]string{"envVar": "ANTHROPIC_API_KEY"}, nil), 404)

	// Alice's agent, shared with bob: only alice attaches.
	var ag gen.Agent
	expect(t, "agent", alice.do("POST", "/agents", gen.AgentInput{Name: "alice-agent"}, &ag), 201)
	e.sandboxes.Wait()
	expect(t, "share", alice.do("POST", "/agents/"+ag.Id+"/shares", gen.ShareInput{Email: "b@example.com"}, nil), 200)
	expect(t, "shared user attaches", bob.do("PUT", "/agents/"+ag.Id+"/secrets/"+sec.Id, map[string]string{"envVar": "ANTHROPIC_API_KEY"}, nil), 403)
	expect(t, "reserved env", alice.do("PUT", "/agents/"+ag.Id+"/secrets/"+sec.Id, map[string]string{"envVar": "HTTPS_PROXY"}, nil), 400)
	expect(t, "alice attaches", alice.do("PUT", "/agents/"+ag.Id+"/secrets/"+sec.Id, map[string]string{"envVar": "ANTHROPIC_API_KEY"}, nil), 200)
	var attached []gen.AttachedSecret
	expect(t, "list attached", bob.do("GET", "/agents/"+ag.Id+"/secrets", nil, &attached), 200)
	if len(attached) != 1 || attached[0].EnvVar != "ANTHROPIC_API_KEY" || attached[0].Secret.CanManage {
		t.Fatalf("attached %+v", attached)
	}
	// A new environment variable needs a rebuild; afterwards the sandbox sees only the placeholder.
	if got := bob.agent(ag.Id); !got.NeedsRebuild {
		t.Fatal("rebuild not pending after attaching an env var")
	}

	// The proxy's view: the agent's traffic gets the real value injected.
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.store.SetSandboxSubnet(t.Context(), db.SetSandboxSubnetParams{Subnet: "10.213.0.0/28", AgentID: ag.Id}))
	e.directory.Invalidate()
	pa, err := e.directory.AgentForSource(t.Context(), netip.MustParseAddr("10.213.0.9"))
	if err != nil || pa == nil || len(pa.Injections) != 1 || pa.Injections[0].Value != "sk-ant-real" || pa.Injections[0].Placeholder != sec.Placeholder {
		t.Fatalf("directory agent %+v %v", pa, err)
	}
	if other, _ := e.directory.AgentForSource(t.Context(), netip.MustParseAddr("10.213.0.200")); other != nil {
		t.Fatal("unknown source mapped to an agent")
	}

	expect(t, "shared user detaches", bob.do("DELETE", "/agents/"+ag.Id+"/secrets/"+sec.Id, nil, nil), 403)
	expect(t, "detach", alice.do("DELETE", "/agents/"+ag.Id+"/secrets/"+sec.Id, nil, nil), 204)
	expect(t, "delete secret", alice.do("DELETE", "/secrets/"+sec.Id, nil, nil), 204)

	res = alice.raw("GET", "/egress/ca.pem", nil)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Fatalf("ca %q", b)
	}
}
