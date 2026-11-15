// Package authz is the only place that decides who may do what.
//
// The decision functions are pure and table-tested. Checker adds the lookups
// handlers need, so a handler only ever asks one question: "may this user do
// this action on this resource?".
package authz

import (
	"errors"
)

// WorkspaceRole is a user's role in a workspace.
type WorkspaceRole string

// Workspace roles, in increasing order of privilege.
const (
	WorkspaceNone   WorkspaceRole = ""
	WorkspaceMember WorkspaceRole = "member"
	WorkspaceAdmin  WorkspaceRole = "admin"
	WorkspaceOwner  WorkspaceRole = "owner"
)

// Valid reports whether r is a real role.
func (r WorkspaceRole) Valid() bool {
	return r == WorkspaceMember || r == WorkspaceAdmin || r == WorkspaceOwner
}

func (r WorkspaceRole) rank() int {
	switch r {
	case WorkspaceMember:
		return 1
	case WorkspaceAdmin:
		return 2
	case WorkspaceOwner:
		return 3
	}
	return 0
}

// AtLeast reports whether r is at least min.
func (r WorkspaceRole) AtLeast(minRole WorkspaceRole) bool { return r.rank() >= minRole.rank() }

// AgentRole is a user's effective role on an agent.
type AgentRole string

// Agent roles, in increasing order of privilege.
const (
	AgentNone     AgentRole = ""
	AgentViewer   AgentRole = "viewer"
	AgentOperator AgentRole = "operator"
	AgentAdmin    AgentRole = "admin"
)

// Valid reports whether r is a real role.
func (r AgentRole) Valid() bool {
	return r == AgentViewer || r == AgentOperator || r == AgentAdmin
}

func (r AgentRole) rank() int {
	switch r {
	case AgentViewer:
		return 1
	case AgentOperator:
		return 2
	case AgentAdmin:
		return 3
	}
	return 0
}

// AtLeast reports whether r is at least min.
func (r AgentRole) AtLeast(minRole AgentRole) bool { return r.rank() >= minRole.rank() }

// Action is something a user wants to do.
type Action string

// Workspace actions.
const (
	WorkspaceRead    Action = "workspace.read"
	WorkspaceUpdate  Action = "workspace.update"
	WorkspaceDelete  Action = "workspace.delete"
	MembersManage    Action = "workspace.members.manage"
	InvitesManage    Action = "workspace.invites.manage"
	AgentCreate      Action = "workspace.agents.create"
	WorkspaceConfig  Action = "workspace.config" // MCP servers, skills, secrets at workspace level
	WorkspaceListAll Action = "workspace.agents.list"
)

// Agent actions.
const (
	AgentRead       Action = "agent.read"       // sessions, files
	AgentPrompt     Action = "agent.prompt"     // prompt, cancel, answer permissions
	AgentTerminal   Action = "agent.terminal"   // open a terminal
	AgentConfigure  Action = "agent.configure"  // settings, MCP, skills, schedules, webhooks
	AgentSecrets    Action = "agent.secrets"    // attach and detach secrets
	AgentGrants     Action = "agent.grants"     // share
	AgentDelete     Action = "agent.delete"     // destroy
	AgentLifecycle  Action = "agent.lifecycle"  // start, stop, rebuild
	AgentAuthLogin  Action = "agent.auth.login" // harness subscription login
	AgentReadAudit  Action = "agent.audit.read"
	AgentDelegateTo Action = "agent.delegate" // another agent sends messages to this agent
)

var workspaceRequirements = map[Action]WorkspaceRole{
	WorkspaceRead:    WorkspaceMember,
	WorkspaceListAll: WorkspaceMember,
	AgentCreate:      WorkspaceMember,
	WorkspaceUpdate:  WorkspaceAdmin,
	MembersManage:    WorkspaceAdmin,
	InvitesManage:    WorkspaceAdmin,
	WorkspaceConfig:  WorkspaceAdmin,
	WorkspaceDelete:  WorkspaceOwner,
}

var agentRequirements = map[Action]AgentRole{
	AgentRead:       AgentViewer,
	AgentPrompt:     AgentOperator,
	AgentTerminal:   AgentOperator,
	AgentDelegateTo: AgentOperator,
	AgentLifecycle:  AgentOperator,
	AgentConfigure:  AgentAdmin,
	AgentSecrets:    AgentAdmin,
	AgentGrants:     AgentAdmin,
	AgentDelete:     AgentAdmin,
	AgentAuthLogin:  AgentAdmin,
	AgentReadAudit:  AgentAdmin,
}

// Errors returned by checks. Handlers map ErrNotFound to 404 and ErrForbidden to 403.
var (
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound is returned when the user cannot even see the resource, so
	// its existence is not revealed.
	ErrNotFound = errors.New("not found")
	// ErrLastOwner protects workspaces from losing their last owner.
	ErrLastOwner = errors.New("a workspace needs at least one owner")
	// ErrPersonal protects personal workspaces.
	ErrPersonal = errors.New("not allowed on a personal workspace")
)

// CanWorkspace decides a workspace action for a workspace role.
func CanWorkspace(role WorkspaceRole, action Action) bool {
	req, ok := workspaceRequirements[action]
	return ok && role.Valid() && role.AtLeast(req)
}

// CanAgent decides an agent action for an effective agent role.
func CanAgent(role AgentRole, action Action) bool {
	req, ok := agentRequirements[action]
	return ok && role.Valid() && role.AtLeast(req)
}

// EffectiveAgentRole combines everything that gives a user access to an agent:
// workspace owners and admins and the agent's owner are implicit admins;
// otherwise the highest grant wins (direct user grants and the workspace-wide
// grant). Non-members of the workspace never have access.
func EffectiveAgentRole(ws WorkspaceRole, isAgentOwner bool, grants ...AgentRole) AgentRole {
	if !ws.Valid() {
		return AgentNone
	}
	if ws.AtLeast(WorkspaceAdmin) || isAgentOwner {
		return AgentAdmin
	}
	best := AgentNone
	for _, g := range grants {
		if g.Valid() && g.rank() > best.rank() {
			best = g
		}
	}
	return best
}

// CanSetMemberRole decides whether actor may change target's role from current to next.
// owners counts the owners currently in the workspace.
func CanSetMemberRole(actor, current, next WorkspaceRole, owners int) error {
	if !next.Valid() || !current.Valid() {
		return ErrForbidden
	}
	switch actor {
	case WorkspaceOwner:
	case WorkspaceAdmin:
		// Admins manage members and admins, never owners.
		if current == WorkspaceOwner || next == WorkspaceOwner {
			return ErrForbidden
		}
	default:
		return ErrForbidden
	}
	if current == WorkspaceOwner && next != WorkspaceOwner && owners <= 1 {
		return ErrLastOwner
	}
	return nil
}

// CanRemoveMember decides whether actor may remove target. self is true when
// the actor removes themselves (leaving the workspace).
func CanRemoveMember(actor, target WorkspaceRole, self bool, owners int) error {
	if !target.Valid() {
		return ErrForbidden
	}
	if target == WorkspaceOwner && owners <= 1 {
		return ErrLastOwner
	}
	if self && actor.Valid() {
		return nil
	}
	switch actor {
	case WorkspaceOwner:
		return nil
	case WorkspaceAdmin:
		if target == WorkspaceOwner {
			return ErrForbidden
		}
		return nil
	}
	return ErrForbidden
}

// CanInvite decides whether actor may create an invite for role.
func CanInvite(actor, role WorkspaceRole) error {
	if !CanWorkspace(actor, InvitesManage) {
		return ErrForbidden
	}
	if role != WorkspaceMember && role != WorkspaceAdmin {
		return ErrForbidden
	}
	return nil
}

// CanBindLogin decides whether actor may make an agent use loginUser's
// account logins (Claude Code, Codex). Only for themselves: nobody can make
// an agent use someone else's login. And only on agents they administer,
// because the choice changes the agent's configuration.
func CanBindLogin(actorID, loginUserID string, role AgentRole) bool {
	return actorID != "" && actorID == loginUserID && CanAgent(role, AgentConfigure)
}

// CanUnbindLogin decides whether actor may make an agent stop using
// loginUser's account logins: the agent's admins, and the login's owner, who
// can always withdraw it from any agent they can see.
func CanUnbindLogin(actorID, loginUserID string, role AgentRole) bool {
	if actorID != "" && actorID == loginUserID && CanAgent(role, AgentRead) {
		return true
	}
	return CanAgent(role, AgentConfigure)
}
