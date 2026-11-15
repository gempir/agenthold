package api_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

func newSessionText(t *testing.T, e *env, c *client, agentID, prompt string) string {
	t.Helper()
	var sess gen.Session
	expect(t, "session", c.do("POST", "/agents/"+agentID+"/sessions", map[string]string{}, &sess), 201)
	st := e.stream(c, sess.Id, 0)
	expect(t, "prompt", c.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": prompt}, nil), 202)
	return text(st.until(isType("turn_end")))
}

func TestMCPServersAndSkills(t *testing.T) {
	tm := newTeam(t)
	e, alice, bob := tm.e, tm.alice, tm.bob
	ag := tm.agent.Id

	var sec gen.Secret
	expect(t, "secret", alice.do("POST", "/secrets", gen.SecretInput{
		Name: "mcp-token", Value: "real-mcp-token",
		Rule: &gen.SecretRule{Hosts: []string{"mcp.example.com"}, Header: "X-Unused", Template: "{{.Value}}"},
	}, &sec), 201)

	// A remote server in alice's library, with an authenticated header.
	var remote gen.MCPServer
	expect(t, "bob's own library", bob.do("POST", "/mcp-servers", gen.MCPServerInput{Name: "x", Transport: "http", Url: ptr("https://a.example.com")}, nil), 201)
	expect(t, "unknown secret", alice.do("POST", "/mcp-servers", gen.MCPServerInput{
		Name: "docs", Transport: "http", Url: ptr("https://mcp.example.com/mcp"),
		Headers: &map[string]string{"Authorization": "Bearer {{secret:nope}}"},
	}, nil), 400)
	expect(t, "bad url", alice.do("POST", "/mcp-servers", gen.MCPServerInput{Name: "docs", Transport: "http", Url: ptr("ftp://x")}, nil), 400)
	expect(t, "create remote", alice.do("POST", "/mcp-servers", gen.MCPServerInput{
		Name: "docs", Transport: "http", Url: ptr("https://mcp.example.com/mcp"),
		Headers: &map[string]string{"Authorization": "Bearer {{secret:mcp-token}}"},
	}, &remote), 201)
	if len(remote.SecretRefs) != 1 || remote.SecretRefs[0] != "mcp-token" || remote.Scope != gen.MCPServerScopeLibrary {
		t.Fatalf("remote %+v", remote)
	}

	// Agent-level stdio server.
	var local gen.MCPServer
	expect(t, "create local", alice.do("POST", "/agents/"+ag+"/mcp-servers", gen.MCPServerInput{
		Name: "everything", Transport: "stdio", Command: ptr("npx"),
		Args: &[]string{"-y", "@modelcontextprotocol/server-everything"}, Env: &map[string]string{"TOKEN": "{{secret:mcp-token}}"},
	}, &local), 201)

	// Skills library.
	var skill gen.Skill
	expect(t, "bob's own skill", bob.do("POST", "/skills", gen.SkillInput{Name: "x", Description: "x", Content: "x"}, nil), 201)
	var lib []gen.MCPServer
	alice.do("GET", "/mcp-servers", nil, &lib)
	if len(lib) != 1 || lib[0].Name != "docs" {
		t.Fatalf("alice's library %+v", lib)
	}
	var skills []gen.Skill
	alice.do("GET", "/skills", nil, &skills)
	if len(skills) != 0 {
		t.Fatalf("alice sees bob's skills: %+v", skills)
	}
	expect(t, "bad skill name", alice.do("POST", "/skills", gen.SkillInput{Name: "Bad Name", Description: "x", Content: "x"}, nil), 400)
	expect(t, "create skill", alice.do("POST", "/skills", gen.SkillInput{Name: "release-notes", Description: "Write release notes from git log", Content: "# Release notes\n\nRun git log and summarize."}, &skill), 201)

	// Before attaching: the session sees only the agent's own server, no skills.
	got := newSessionText(t, e, alice, ag, "tools")
	if !strings.Contains(got, "stdio:everything:npx:TOKEN=agenthold-placeholder-") || strings.Contains(got, "http:docs") || strings.Contains(got, "release-notes") {
		t.Fatalf("before attach: %s", got)
	}

	expect(t, "attach remote", alice.do("PUT", "/agents/"+ag+"/mcp-servers/"+remote.Id, nil, nil), 204)
	expect(t, "attach skill", alice.do("PUT", "/agents/"+ag+"/skills/"+skill.Id, nil, nil), 204)
	var servers []gen.MCPServer
	alice.do("GET", "/agents/"+ag+"/mcp-servers", nil, &servers)
	if len(servers) != 2 {
		t.Fatalf("agent servers %+v", servers)
	}

	// The next session gets both; the secret appears only as its placeholder.
	got = newSessionText(t, e, alice, ag, "tools")
	if !strings.Contains(got, "http:docs:https://mcp.example.com/mcp:Authorization=Bearer "+sec.Placeholder) || !strings.Contains(got, "release-notes(") {
		t.Fatalf("after attach: %s", got)
	}
	if strings.Contains(got, "real-mcp-token") {
		t.Fatal("secret value reached the sandbox")
	}
	md, err := os.ReadFile(filepath.Join(e.home(ag), ".fakeagent/skills/release-notes/SKILL.md"))
	if err != nil || !strings.HasPrefix(string(md), "---\nname: release-notes\ndescription: \"Write release notes from git log\"\n---") {
		t.Fatalf("SKILL.md: %q %v", md, err)
	}

	// The proxy injects the referenced secret although it is not attached to the agent.
	_ = e.store.SetSandboxSubnet(t.Context(), db.SetSandboxSubnetParams{Subnet: "10.213.0.0/28", AgentID: ag})
	e.directory.Invalidate()
	pa, err := e.directory.AgentForSource(t.Context(), netip.MustParseAddr("10.213.0.9"))
	if err != nil || pa == nil || len(pa.Injections) != 1 || pa.Injections[0].Value != "real-mcp-token" || pa.Injections[0].Placeholder != sec.Placeholder {
		t.Fatalf("injections %+v %v", pa, err)
	}

	// Nobody can route someone else's secret through their own agent's server:
	// names resolve among the server owner's secrets only.
	var bobAgent gen.Agent
	bob.do("POST", "/agents", gen.AgentInput{Name: "bobs", Harness: ptr("fake")}, &bobAgent)
	e.sandboxes.Wait()
	expect(t, "steal secret", bob.do("POST", "/agents/"+bobAgent.Id+"/mcp-servers", gen.MCPServerInput{
		Name: "leak", Transport: "http", Url: ptr("https://evil.example.com"),
		Headers: &map[string]string{"X": "{{secret:mcp-token}}"},
	}, nil), 400)
	expect(t, "stranger attaches to alice's agent", bob.do("PUT", "/agents/"+ag+"/skills/"+skill.Id, nil, nil), 404)
	tm.share("bob")
	expect(t, "shared user attaches", bob.do("PUT", "/agents/"+ag+"/skills/"+skill.Id, nil, nil), 403)
	expect(t, "shared user edits alice's server", bob.do("DELETE", "/mcp-servers/"+remote.Id, nil, nil), 404)

	// Detach: removed from the sandbox in the next session; skills the agent wrote itself stay.
	own := filepath.Join(e.home(ag), ".fakeagent/skills/my-own/SKILL.md")
	_ = os.MkdirAll(filepath.Dir(own), 0o755)
	_ = os.WriteFile(own, []byte("mine"), 0o644)
	expect(t, "detach skill", alice.do("DELETE", "/agents/"+ag+"/skills/"+skill.Id, nil, nil), 204)
	expect(t, "detach remote", alice.do("DELETE", "/agents/"+ag+"/mcp-servers/"+remote.Id, nil, nil), 204)
	got = newSessionText(t, e, alice, ag, "tools")
	if strings.Contains(got, "release-notes") || strings.Contains(got, "http:docs") || !strings.Contains(got, "my-own") {
		t.Fatalf("after detach: %s", got)
	}
	// Deleting the agent's own server removes it.
	expect(t, "delete own server", alice.do("DELETE", "/agents/"+ag+"/mcp-servers/"+local.Id, nil, nil), 204)
	alice.do("GET", "/agents/"+ag+"/mcp-servers", nil, &servers)
	if len(servers) != 0 {
		t.Fatalf("servers left: %+v", servers)
	}
}
