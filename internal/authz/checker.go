package authz

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gempir/agenthold/internal/store/db"
)

// Checker answers authorization questions that need the database.
type Checker struct {
	Q *db.Queries
}

// AgentAccess is an agent together with the caller's role on it.
type AgentAccess struct {
	Row  db.GetAgentWithSandboxRow
	Role Role
}

// RoleOf computes userID's role on an agent.
func (c Checker) RoleOf(ctx context.Context, userID string, a db.Agent) (Role, error) {
	if userID == "" {
		return None, nil
	}
	if userID == a.OwnerID {
		return Owner, nil
	}
	n, err := c.Q.IsSharedWith(ctx, db.IsSharedWithParams{AgentID: a.ID, UserID: userID})
	if err != nil {
		return None, err
	}
	return RoleOf(userID, a.OwnerID, n > 0), nil
}

// Agent loads an agent if userID may perform action on it. Users who cannot
// even read the agent get ErrNotFound, others lacking the role ErrForbidden.
func (c Checker) Agent(ctx context.Context, userID, agentID string, action Action) (AgentAccess, error) {
	row, err := c.Q.GetAgentWithSandbox(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentAccess{}, ErrNotFound
	}
	if err != nil {
		return AgentAccess{}, err
	}
	role, err := c.RoleOf(ctx, userID, row.Agent)
	if err != nil {
		return AgentAccess{}, err
	}
	if !Can(role, AgentRead) {
		return AgentAccess{}, ErrNotFound
	}
	acc := AgentAccess{Row: row, Role: role}
	if !Can(role, action) {
		return acc, ErrForbidden
	}
	return acc, nil
}

// CanDelegate reports whether agents owned by userID may message target:
// the user owns target or it is shared with them.
func (c Checker) CanDelegate(ctx context.Context, userID string, target db.Agent) (bool, error) {
	role, err := c.RoleOf(ctx, userID, target)
	if err != nil {
		return false, err
	}
	return Can(role, AgentDelegateTo), nil
}

// SessionAccess is a session together with its parent agent and the caller's
// effective session role (derived from agent access or a session_shares row).
type SessionAccess struct {
	Session   db.AgentSession
	AgentRow  db.GetAgentWithSandboxRow
	AgentRole Role
	Role      SessionRole
}

// SessionRoleOf computes userID's effective SessionRole on sess/agent.
func (c Checker) SessionRoleOf(ctx context.Context, userID string, sess db.AgentSession, agent db.Agent) (SessionRole, Role, error) {
	if userID == "" {
		return SessionNone, None, nil
	}
	agRole, err := c.RoleOf(ctx, userID, agent)
	if err != nil {
		return SessionNone, None, err
	}
	switch agRole {
	case Owner:
		return SessionOwner, Owner, nil
	case Shared:
		return SessionShared, Shared, nil
	}
	sh, err := c.Q.GetSessionShare(ctx, db.GetSessionShareParams{SessionID: sess.ID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return SessionNone, None, nil
	}
	if err != nil {
		return SessionNone, None, err
	}
	switch sh.Role {
	case string(SessionCollaborator):
		return SessionCollaborator, None, nil
	case string(SessionViewer):
		return SessionViewer, None, nil
	}
	return SessionNone, None, nil
}

// Session loads a session if userID may perform action on it. Users who cannot
// even read the session get ErrNotFound; users with read access lacking action
// get ErrForbidden.
func (c Checker) Session(ctx context.Context, userID, sessionID string, action SessionAction) (SessionAccess, error) {
	sess, err := c.Q.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionAccess{}, ErrNotFound
	}
	if err != nil {
		return SessionAccess{}, err
	}
	row, err := c.Q.GetAgentWithSandbox(ctx, sess.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionAccess{}, ErrNotFound
	}
	if err != nil {
		return SessionAccess{}, err
	}
	sRole, agRole, err := c.SessionRoleOf(ctx, userID, sess, row.Agent)
	if err != nil {
		return SessionAccess{}, err
	}
	if !CanSession(sRole, SessionRead) {
		return SessionAccess{}, ErrNotFound
	}
	acc := SessionAccess{Session: sess, AgentRow: row, AgentRole: agRole, Role: sRole}
	if !CanSession(sRole, action) {
		return acc, ErrForbidden
	}
	return acc, nil
}

// HasSessionShareOnAgent reports whether userID has a thread-level share on any
// session of agentID (used to grant read-only diff/file access without terminal
// or write privileges).
func (c Checker) HasSessionShareOnAgent(ctx context.Context, userID, agentID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	n, err := c.Q.HasAnySessionShareOnAgent(ctx, db.HasAnySessionShareOnAgentParams{
		AgentID: agentID,
		UserID:  userID,
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
