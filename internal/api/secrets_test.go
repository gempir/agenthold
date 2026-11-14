package api_test

import (
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestSecretsAndAttachments(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	var team gen.Workspace
	alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "Team"}, &team)
	ws := "/workspaces/" + team.Id
	var inv gen.CreatedInvite
	alice.do("POST", ws+"/invites", gen.InviteInput{Role: "member"}, &inv)
	bob.do("POST", "/invites/"+inv.Token+"/accept", nil, nil)

	var presets []gen.SecretPreset
	expect(t, "presets", alice.do("GET", "/secrets/presets", nil, &presets), 200)
	if len(presets) == 0 || presets[0].Id != "anthropic" {
		t.Fatalf("presets %+v", presets)
	}

	var sec gen.Secret
	expect(t, "create", alice.do("POST", ws+"/secrets", gen.SecretInput{Name: "Claude key", Value: "sk-ant-real", Preset: ptr("anthropic")}, &sec), 201)
	if sec.Rule.Header != "x-api-key" || !strings.HasPrefix(sec.Placeholder, "agenthold-placeholder-") || !sec.CanManage {
		t.Fatalf("secret %+v", sec)
	}
	expect(t, "duplicate", alice.do("POST", ws+"/secrets", gen.SecretInput{Name: "Claude key", Value: "x", Preset: ptr("anthropic")}, nil), 409)
	expect(t, "bad rule", alice.do("POST", ws+"/secrets", gen.SecretInput{Name: "x", Value: "x", Rule: &gen.SecretRule{Hosts: []string{"*"}, Header: "A"}}, nil), 400)
	expect(t, "newline value", alice.do("POST", ws+"/secrets", gen.SecretInput{Name: "y", Value: "a\nb", Preset: ptr("openai")}, nil), 400)

	// The value is never returned in any form.
	res := alice.raw("GET", ws+"/secrets", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "sk-ant-real") || !strings.Contains(string(body), "Claude key") {
		t.Fatalf("list body %s", body)
	}
	// Bob (member) cannot see or manage Alice's secret.
	var bobList []gen.Secret
	bob.do("GET", ws+"/secrets", nil, &bobList)
	if len(bobList) != 0 {
		t.Fatalf("bob sees %d secrets", len(bobList))
	}
	expect(t, "bob patch", bob.do("PATCH", "/secrets/"+sec.Id, gen.SecretPatch{Name: ptr("mine")}, nil), 403)

	// Bob's agent: he cannot attach Alice's secret; Alice (workspace owner) can.
	var ag gen.Agent
	expect(t, "agent", bob.do("POST", ws+"/agents", gen.AgentInput{Name: "bob-agent"}, &ag), 201)
	e.sandboxes.Wait()
	expect(t, "bob attaches", bob.do("PUT", "/agents/"+ag.Id+"/secrets/"+sec.Id, map[string]string{"envVar": "ANTHROPIC_API_KEY"}, nil), 403)
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

	// Policy.
	var pol gen.NetworkPolicy
	expect(t, "default policy", bob.do("GET", "/agents/"+ag.Id+"/network-policy", nil, &pol), 200)
	if pol.Mode != "open" {
		t.Fatalf("default %+v", pol)
	}
	expect(t, "bad policy", bob.do("PUT", "/agents/"+ag.Id+"/network-policy", gen.NetworkPolicy{Mode: "allowlist", Allow: []string{"a.com:0"}}, nil), 400)
	expect(t, "set policy", bob.do("PUT", "/agents/"+ag.Id+"/network-policy", gen.NetworkPolicy{Mode: "allowlist", Allow: []string{" github.com:22 ", "api.anthropic.com", ""}}, &pol), 200)
	if len(pol.Allow) != 2 {
		t.Fatalf("policy %+v", pol)
	}
	pa, _ = e.directory.AgentForSource(t.Context(), netip.MustParseAddr("10.213.0.9"))
	if pa.Policy.Mode != "allowlist" || !pa.Policy.AllowsName("github.com") {
		t.Fatalf("proxy policy %+v", pa.Policy)
	}

	// Request log.
	rec := &egress.Recorder{Store: e.store}
	for i := range 3 {
		rec.Record(egress.Event{AgentID: ag.Id, At: time.Now(), Kind: "https", Host: "api.anthropic.com", Port: 443, Allowed: i != 1, Reason: map[bool]string{true: "not in allowlist"}[i == 1]})
	}
	rec.Flush()
	var log []gen.EgressEvent
	expect(t, "log", bob.do("GET", "/agents/"+ag.Id+"/egress-log?limit=2", nil, &log), 200)
	if len(log) != 2 || log[0].Id < log[1].Id || !log[0].Allowed || log[1].Allowed || log[1].Reason != "not in allowlist" {
		t.Fatalf("log %+v", log)
	}
	expect(t, "outsider log", e.login("eve", "Eve", "e@example.com").do("GET", "/agents/"+ag.Id+"/egress-log", nil, nil), 404)

	expect(t, "detach", alice.do("DELETE", "/agents/"+ag.Id+"/secrets/"+sec.Id, nil, nil), 204)
	expect(t, "delete secret", alice.do("DELETE", "/secrets/"+sec.Id, nil, nil), 204)

	res = alice.raw("GET", "/egress/ca.pem", nil)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Fatalf("ca %q", b)
	}
}
