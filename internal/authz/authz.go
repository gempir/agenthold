// Package authz is the only place that decides who may do what.
//
// The model is small: a user owns agents and may share each with other
// users. Everyone else has no access. The decision functions are pure and
// table-tested. Checker adds the lookups handlers need, so a handler only
// ever asks one question: "may this user do this action on this agent?".
package authz

import "errors"

// Role is a user's relation to an agent.
type Role string

// Roles.
const (
	// None: a stranger. The agent is invisible to them.
	None Role = ""
	// Shared: the owner shared the agent with this user. They can use it.
	Shared Role = "shared"
	// Owner: full control.
	Owner Role = "owner"
)

// Valid reports whether r gives any access.
func (r Role) Valid() bool { return r == Shared || r == Owner }

// RoleOf derives a user's role on an agent owned by ownerID. shared says
// whether the agent is shared with the user.
func RoleOf(userID, ownerID string, shared bool) Role {
	switch {
	case userID == "":
		return None
	case userID == ownerID:
		return Owner
	case shared:
		return Shared
	}
	return None
}

// Action is something a user wants to do with an agent.
type Action string

// Agent actions.
const (
	AgentRead       Action = "agent.read"        // the agent, sessions, files, attached tools, access list
	AgentPrompt     Action = "agent.prompt"      // create sessions, prompt, cancel, answer permissions, run schedules
	AgentTerminal   Action = "agent.terminal"    // open a terminal
	AgentFilesWrite Action = "agent.files.write" // upload, mkdir, delete files
	AgentLifecycle  Action = "agent.lifecycle"   // start, stop
	AgentDelegateTo Action = "agent.delegate"    // the user's other agents may message this agent
	AgentConfigure  Action = "agent.configure"   // settings, MCP servers, skills, schedules, webhooks, account logins
	AgentSecrets    Action = "agent.secrets"     // attach and detach secrets
	AgentShare      Action = "agent.share"       // share with and unshare from other users
	AgentRebuild    Action = "agent.rebuild"     // reset the system (rebuild the sandbox)
	AgentClone      Action = "agent.clone"       // copy the machine: its home disk can hold harness credentials
	AgentDelete     Action = "agent.delete"      // destroy
	AgentAuthLogin  Action = "agent.auth.login"  // harness login inside the sandbox
	AgentReadAudit  Action = "agent.audit.read"
	AgentPreview    Action = "agent.preview" // open its private exposures (preview URLs)
	AgentExpose     Action = "agent.expose"  // expose ports, change and remove exposures
)

// sharedActions are what a shared user may do. The owner may do everything.
var sharedActions = map[Action]bool{
	AgentRead:       true,
	AgentPrompt:     true,
	AgentTerminal:   true,
	AgentFilesWrite: true,
	AgentLifecycle:  true,
	AgentDelegateTo: true,
	AgentPreview:    true,
}

var ownerActions = map[Action]bool{
	AgentConfigure: true,
	AgentSecrets:   true,
	AgentShare:     true,
	AgentRebuild:   true,
	AgentClone:     true,
	AgentDelete:    true,
	AgentAuthLogin: true,
	AgentReadAudit: true,
	AgentExpose:    true,
}

// Errors returned by checks. Handlers map ErrNotFound to 404 and ErrForbidden to 403.
var (
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound is returned when the user cannot even see the resource, so
	// its existence is not revealed.
	ErrNotFound = errors.New("not found")
)

// Can decides an agent action for a role.
func Can(role Role, action Action) bool {
	switch role {
	case Owner:
		return sharedActions[action] || ownerActions[action]
	case Shared:
		return sharedActions[action]
	}
	return false
}

// CanUnshare decides whether actor (with role on the agent) may remove
// target's share: the owner removes anyone, a shared user only themselves.
func CanUnshare(role Role, actorID, targetID string) bool {
	return Can(role, AgentShare) || (role == Shared && actorID != "" && actorID == targetID)
}

// CanBindLogin decides whether actor may make an agent use loginUser's
// account logins (Claude Code, Codex). Only for themselves: nobody can make
// an agent use someone else's login. And only on agents they own, because
// the choice changes the agent's configuration.
func CanBindLogin(actorID, loginUserID string, role Role) bool {
	return actorID != "" && actorID == loginUserID && Can(role, AgentConfigure)
}

// CanUnbindLogin decides whether actor may make an agent stop using
// loginUser's account logins: the agent's owner, and the login's owner, who
// can always withdraw it from any agent they can see.
func CanUnbindLogin(actorID, loginUserID string, role Role) bool {
	if actorID != "" && actorID == loginUserID && Can(role, AgentRead) {
		return true
	}
	return Can(role, AgentConfigure)
}

// SessionRole is a user's effective relation to a single session (thread).
type SessionRole string

// Session roles.
const (
	SessionNone         SessionRole = ""
	SessionViewer       SessionRole = "viewer"
	SessionCollaborator SessionRole = "collaborator"
	SessionShared       SessionRole = "shared"
	SessionOwner        SessionRole = "owner"
)

// Valid reports whether r gives any access to the session.
func (r SessionRole) Valid() bool {
	switch r {
	case SessionViewer, SessionCollaborator, SessionShared, SessionOwner:
		return true
	}
	return false
}

// ValidSessionShareRole reports whether role is a valid thread-level share role.
func ValidSessionShareRole(role string) bool {
	return role == string(SessionViewer) || role == string(SessionCollaborator)
}

// SessionAction is something a user wants to do on a session (thread).
type SessionAction string

// Session actions.
const (
	SessionRead         SessionAction = "session.read"
	SessionComment      SessionAction = "session.comment"
	SessionPrompt       SessionAction = "session.prompt"
	SessionApproveGuest SessionAction = "session.guest.approve"
	SessionManageShares SessionAction = "session.shares.manage"
)

// CanSession decides a session action for a session role.
func CanSession(role SessionRole, action SessionAction) bool {
	switch role {
	case SessionOwner:
		return true
	case SessionShared:
		return action != SessionManageShares
	case SessionCollaborator:
		return action == SessionRead || action == SessionComment || action == SessionPrompt || action == SessionApproveGuest
	case SessionViewer:
		return action == SessionRead || action == SessionComment
	}
	return false
}

// CanUnshareSession decides whether actor (with role on the session) may remove
// target's thread-level share: the agent owner removes anyone, and a thread-shared
// user may remove themselves ("Leave").
func CanUnshareSession(role SessionRole, actorID, targetID string) bool {
	if CanSession(role, SessionManageShares) {
		return true
	}
	return (role == SessionViewer || role == SessionCollaborator) && actorID != "" && actorID == targetID
}
