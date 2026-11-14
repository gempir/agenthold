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
