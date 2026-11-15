package authz

import (
	"errors"
	"testing"
)

func TestCanWorkspace(t *testing.T) {
	type row struct {
		action                     Action
		none, member, admin, owner bool
	}
	rows := []row{
		{WorkspaceRead, false, true, true, true},
		{WorkspaceListAll, false, true, true, true},
		{AgentCreate, false, true, true, true},
		{WorkspaceUpdate, false, false, true, true},
		{MembersManage, false, false, true, true},
		{InvitesManage, false, false, true, true},
		{WorkspaceConfig, false, false, true, true},
		{WorkspaceDelete, false, false, false, true},
		{Action("unknown"), false, false, false, false},
		{AgentRead, false, false, false, false}, // agent actions are not workspace actions
	}
	for _, r := range rows {
		for role, want := range map[WorkspaceRole]bool{
			WorkspaceNone: r.none, WorkspaceMember: r.member, WorkspaceAdmin: r.admin, WorkspaceOwner: r.owner,
			WorkspaceRole("superuser"): false,
		} {
			if got := CanWorkspace(role, r.action); got != want {
				t.Errorf("CanWorkspace(%q, %q) = %v, want %v", role, r.action, got, want)
			}
		}
	}
}

func TestCanAgent(t *testing.T) {
	type row struct {
		action                        Action
		none, viewer, operator, admin bool
	}
	rows := []row{
		{AgentRead, false, true, true, true},
		{AgentPrompt, false, false, true, true},
		{AgentTerminal, false, false, true, true},
		{AgentLifecycle, false, false, true, true},
		{AgentDelegateTo, false, false, true, true},
		{AgentConfigure, false, false, false, true},
		{AgentSecrets, false, false, false, true},
		{AgentGrants, false, false, false, true},
		{AgentDelete, false, false, false, true},
		{AgentAuthLogin, false, false, false, true},
		{AgentReadAudit, false, false, false, true},
		{WorkspaceRead, false, false, false, false},
	}
	for _, r := range rows {
		for role, want := range map[AgentRole]bool{
			AgentNone: r.none, AgentViewer: r.viewer, AgentOperator: r.operator, AgentAdmin: r.admin,
			AgentRole("root"): false,
		} {
			if got := CanAgent(role, r.action); got != want {
				t.Errorf("CanAgent(%q, %q) = %v, want %v", role, r.action, got, want)
			}
		}
	}
}

func TestEffectiveAgentRole(t *testing.T) {
	tests := []struct {
		name   string
		ws     WorkspaceRole
		owner  bool
		grants []AgentRole
		want   AgentRole
	}{
		{"non-member sees nothing even with grant", WorkspaceNone, false, []AgentRole{AgentAdmin}, AgentNone},
		{"non-member agent owner sees nothing", WorkspaceNone, true, nil, AgentNone},
		{"member without grant", WorkspaceMember, false, nil, AgentNone},
		{"member with viewer", WorkspaceMember, false, []AgentRole{AgentViewer}, AgentViewer},
		{"highest grant wins", WorkspaceMember, false, []AgentRole{AgentViewer, AgentOperator}, AgentOperator},
		{"invalid grant ignored", WorkspaceMember, false, []AgentRole{"bogus"}, AgentNone},
		{"agent owner is admin", WorkspaceMember, true, nil, AgentAdmin},
		{"workspace admin is admin", WorkspaceAdmin, false, nil, AgentAdmin},
		{"workspace owner is admin", WorkspaceOwner, false, []AgentRole{AgentViewer}, AgentAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveAgentRole(tt.ws, tt.owner, tt.grants...); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanSetMemberRole(t *testing.T) {
	O, A, M, N := WorkspaceOwner, WorkspaceAdmin, WorkspaceMember, WorkspaceNone
	tests := []struct {
		actor, current, next WorkspaceRole
		owners               int
		want                 error
	}{
		{O, M, A, 1, nil},
		{O, M, O, 1, nil},
		{O, O, A, 2, nil},
		{O, O, M, 1, ErrLastOwner},
		{A, M, A, 1, nil},
		{A, A, M, 1, nil},
		{A, M, O, 1, ErrForbidden},
		{A, O, M, 2, ErrForbidden},
		{M, M, A, 1, ErrForbidden},
		{N, M, A, 1, ErrForbidden},
		{O, M, "god", 1, ErrForbidden},
	}
	for _, tt := range tests {
		if got := CanSetMemberRole(tt.actor, tt.current, tt.next, tt.owners); !errors.Is(got, tt.want) {
			t.Errorf("CanSetMemberRole(%s, %s->%s, owners=%d) = %v, want %v", tt.actor, tt.current, tt.next, tt.owners, got, tt.want)
		}
	}
}

func TestCanRemoveMember(t *testing.T) {
	O, A, M, N := WorkspaceOwner, WorkspaceAdmin, WorkspaceMember, WorkspaceNone
	tests := []struct {
		actor, target WorkspaceRole
		self          bool
		owners        int
		want          error
	}{
		{O, M, false, 1, nil},
		{O, A, false, 1, nil},
		{O, O, false, 2, nil},
		{O, O, true, 1, ErrLastOwner},
		{O, O, true, 2, nil},
		{A, M, false, 1, nil},
		{A, A, false, 1, nil},
		{A, O, false, 2, ErrForbidden},
		{A, A, true, 1, nil},
		{M, M, true, 1, nil},
		{M, A, false, 1, ErrForbidden},
		{N, M, false, 1, ErrForbidden},
	}
	for _, tt := range tests {
		if got := CanRemoveMember(tt.actor, tt.target, tt.self, tt.owners); !errors.Is(got, tt.want) {
			t.Errorf("CanRemoveMember(%s removes %s, self=%v, owners=%d) = %v, want %v", tt.actor, tt.target, tt.self, tt.owners, got, tt.want)
		}
	}
}

func TestCanInvite(t *testing.T) {
	tests := []struct {
		actor, role WorkspaceRole
		want        error
	}{
		{WorkspaceOwner, WorkspaceAdmin, nil},
		{WorkspaceOwner, WorkspaceMember, nil},
		{WorkspaceOwner, WorkspaceOwner, ErrForbidden},
		{WorkspaceAdmin, WorkspaceAdmin, nil},
		{WorkspaceAdmin, WorkspaceMember, nil},
		{WorkspaceMember, WorkspaceMember, ErrForbidden},
		{WorkspaceNone, WorkspaceMember, ErrForbidden},
	}
	for _, tt := range tests {
		if got := CanInvite(tt.actor, tt.role); !errors.Is(got, tt.want) {
			t.Errorf("CanInvite(%s, %s) = %v, want %v", tt.actor, tt.role, got, tt.want)
		}
	}
}

func TestLoginBinding(t *testing.T) {
	tests := []struct {
		actor, loginUser string
		role             AgentRole
		bind, unbind     bool
	}{
		{"alice", "alice", AgentAdmin, true, true},
		{"alice", "alice", AgentOperator, false, true}, // may withdraw, not bind
		{"alice", "alice", AgentViewer, false, true},
		{"alice", "alice", AgentNone, false, false},
		{"alice", "bob", AgentAdmin, false, true}, // admins detach others' logins, never attach them
		{"alice", "bob", AgentOperator, false, false},
		{"alice", "bob", AgentViewer, false, false},
		{"", "", AgentAdmin, false, true},
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
