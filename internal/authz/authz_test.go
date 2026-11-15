package authz

import "testing"

func TestRoleOf(t *testing.T) {
	tests := []struct {
		name        string
		user, owner string
		shared      bool
		want        Role
	}{
		{"owner", "alice", "alice", false, Owner},
		{"owner with a stray share row", "alice", "alice", true, Owner},
		{"shared user", "bob", "alice", true, Shared},
		{"stranger", "eve", "alice", false, None},
		{"anonymous", "", "alice", true, None},
		{"anonymous on ownerless agent", "", "", false, None},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RoleOf(tt.user, tt.owner, tt.shared); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCan(t *testing.T) {
	type row struct {
		action                  Action
		stranger, shared, owner bool
	}
	rows := []row{
		{AgentRead, false, true, true},
		{AgentPrompt, false, true, true},
		{AgentTerminal, false, true, true},
		{AgentFilesWrite, false, true, true},
		{AgentLifecycle, false, true, true},
		{AgentDelegateTo, false, true, true},
		{AgentConfigure, false, false, true},
		{AgentSecrets, false, false, true},
		{AgentShare, false, false, true},
		{AgentRebuild, false, false, true},
		{AgentDelete, false, false, true},
		{AgentAuthLogin, false, false, true},
		{AgentReadAudit, false, false, true},
		{Action("unknown"), false, false, false},
	}
	for _, r := range rows {
		for role, want := range map[Role]bool{
			None: r.stranger, Shared: r.shared, Owner: r.owner, Role("admin"): false,
		} {
			if got := Can(role, r.action); got != want {
				t.Errorf("Can(%q, %q) = %v, want %v", role, r.action, got, want)
			}
		}
	}
}

func TestCanUnshare(t *testing.T) {
	tests := []struct {
		role          Role
		actor, target string
		want          bool
	}{
		{Owner, "alice", "bob", true},
		{Owner, "alice", "alice", true},
		{Shared, "bob", "bob", true}, // leave
		{Shared, "bob", "carol", false},
		{None, "eve", "eve", false},
		{Shared, "", "", false},
	}
	for _, tt := range tests {
		if got := CanUnshare(tt.role, tt.actor, tt.target); got != tt.want {
			t.Errorf("CanUnshare(%s, %s removes %s) = %v", tt.role, tt.actor, tt.target, got)
		}
	}
}

func TestLoginBinding(t *testing.T) {
	tests := []struct {
		actor, loginUser string
		role             Role
		bind, unbind     bool
	}{
		{"alice", "alice", Owner, true, true},
		{"alice", "alice", Shared, false, true}, // may withdraw, not bind
		{"alice", "alice", None, false, false},
		{"alice", "bob", Owner, false, true}, // owners detach others' logins, never attach them
		{"alice", "bob", Shared, false, false},
		{"", "", Owner, false, true},
	}
	for _, tt := range tests {
		if got := CanBindLogin(tt.actor, tt.loginUser, tt.role); got != tt.bind {
			t.Errorf("CanBindLogin(%s, %s, %s) = %v", tt.actor, tt.loginUser, tt.role, got)
		}
		if got := CanUnbindLogin(tt.actor, tt.loginUser, tt.role); got != tt.unbind {
			t.Errorf("CanUnbindLogin(%s, %s, %s) = %v", tt.actor, tt.loginUser, tt.role, got)
		}
	}
}
