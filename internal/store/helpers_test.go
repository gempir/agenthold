package store

import "github.com/gempir/agenthold/internal/store/db"

func dbCreateWorkspace(id string) db.CreateWorkspaceParams {
	return db.CreateWorkspaceParams{ID: id, Name: "test", CreatedAt: 1, UpdatedAt: 1}
}
