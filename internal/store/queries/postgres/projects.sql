-- name: CreateProject :exec
INSERT INTO projects (id, owner_id, name, slug, description, instructions, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT * FROM projects WHERE owner_id = $1 ORDER BY name, id;

-- name: UpdateProject :exec
UPDATE projects SET name = $1, description = $2, instructions = $3, updated_at = $4 WHERE id = $5;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = $1;

-- name: ListProjectSources :many
SELECT * FROM project_sources WHERE project_id = $1 ORDER BY position, name;

-- name: CreateProjectSource :exec
INSERT INTO project_sources (id, project_id, name, kind, url, path, default_branch, position, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdateProjectSource :exec
UPDATE project_sources SET kind = $1, url = $2, path = $3, default_branch = $4, position = $5 WHERE id = $6;

-- name: SetProjectSourceDefaultBranch :exec
-- Records the branch a first checkout found, unless one is set.
UPDATE project_sources SET default_branch = $1 WHERE id = $2 AND default_branch = '';

-- name: DeleteProjectSource :exec
DELETE FROM project_sources WHERE id = $1;

-- name: ListProjectSessions :many
SELECT * FROM agent_sessions WHERE project_id = $1 ORDER BY updated_at DESC;

-- name: SetSessionProject :exec
UPDATE agent_sessions SET project_id = $1, project_branch = $2 WHERE id = $3;

-- name: ClearProjectSessions :exec
UPDATE agent_sessions SET project_id = '', project_branch = '' WHERE project_id = $1;
