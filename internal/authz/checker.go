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

// WorkspaceRoleOf returns userID's role in workspaceID, or WorkspaceNone.
func (c Checker) WorkspaceRoleOf(ctx context.Context, userID, workspaceID string) (WorkspaceRole, error) {
	m, err := c.Q.GetMembership(ctx, db.GetMembershipParams{WorkspaceID: workspaceID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceNone, nil
	}
	if err != nil {
		return WorkspaceNone, err
	}
	return WorkspaceRole(m.Role), nil
}

// Workspace returns the user's role if they may perform action on the
// workspace. Non-members get ErrNotFound, members lacking the role ErrForbidden.
func (c Checker) Workspace(ctx context.Context, userID, workspaceID string, action Action) (WorkspaceRole, error) {
	role, err := c.WorkspaceRoleOf(ctx, userID, workspaceID)
	if err != nil {
		return WorkspaceNone, err
	}
	if !role.Valid() {
		return WorkspaceNone, ErrNotFound
	}
	if !CanWorkspace(role, action) {
		return role, ErrForbidden
	}
	return role, nil
}

// AgentAccess is an agent together with the caller's effective role.
type AgentAccess struct {
	Row           db.GetAgentWithSandboxRow
	WorkspaceRole WorkspaceRole
	Role          AgentRole
}

// AgentRoleOf computes userID's effective role on an agent.
func (c Checker) AgentRoleOf(ctx context.Context, userID string, a db.Agent) (WorkspaceRole, AgentRole, error) {
	ws, err := c.WorkspaceRoleOf(ctx, userID, a.WorkspaceID)
	if err != nil {
		return WorkspaceNone, AgentNone, err
	}
	if !ws.Valid() {
		return ws, AgentNone, nil
	}
	rows, err := c.Q.GrantRolesFor(ctx, db.GrantRolesForParams{AgentID: a.ID, SubjectID: userID})
	if err != nil {
		return ws, AgentNone, err
	}
	grants := make([]AgentRole, len(rows))
	for i, r := range rows {
		grants[i] = AgentRole(r)
	}
	return ws, EffectiveAgentRole(ws, a.OwnerID == userID, grants...), nil
}

// Agent loads an agent if userID may perform action on it. Users who cannot
// even read the agent get ErrNotFound.
func (c Checker) Agent(ctx context.Context, userID, agentID string, action Action) (AgentAccess, error) {
	row, err := c.Q.GetAgentWithSandbox(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentAccess{}, ErrNotFound
	}
	if err != nil {
		return AgentAccess{}, err
	}
	ws, role, err := c.AgentRoleOf(ctx, userID, row.Agent)
	if err != nil {
		return AgentAccess{}, err
	}
	acc := AgentAccess{Row: row, WorkspaceRole: ws, Role: role}
	if !CanAgent(role, AgentRead) {
		return AgentAccess{}, ErrNotFound
	}
	if !CanAgent(role, action) {
		return acc, ErrForbidden
	}
	return acc, nil
}
