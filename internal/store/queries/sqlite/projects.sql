-- name: CreateProject :exec
INSERT INTO projects (id, owner_id, name, slug, description, instructions, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects WHERE owner_id = ? ORDER BY name, id;

-- name: UpdateProject :exec
UPDATE projects SET name = ?, description = ?, instructions = ?, updated_at = ? WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- name: ListProjectSources :many
SELECT * FROM project_sources WHERE project_id = ? ORDER BY position, name;

-- name: CreateProjectSource :exec
INSERT INTO project_sources (id, project_id, name, kind, url, path, default_branch, position, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateProjectSource :exec
UPDATE project_sources SET kind = ?, url = ?, path = ?, default_branch = ?, position = ? WHERE id = ?;

-- name: SetProjectSourceDefaultBranch :exec
-- Records the branch a first checkout found, unless one is set.
UPDATE project_sources SET default_branch = ? WHERE id = ? AND default_branch = '';

-- name: DeleteProjectSource :exec
DELETE FROM project_sources WHERE id = ?;

-- name: ListProjectSessions :many
SELECT * FROM agent_sessions WHERE project_id = ? ORDER BY updated_at DESC;

-- name: SetSessionProject :exec
UPDATE agent_sessions SET project_id = ?, project_branch = ? WHERE id = ?;

-- name: ClearProjectSessions :exec
UPDATE agent_sessions SET project_id = '', project_branch = '' WHERE project_id = ?;
