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
