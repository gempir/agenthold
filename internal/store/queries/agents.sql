-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, owner_id, name, image, harness, model, cpus, memory_mb, idle_minutes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: GetAgentWithSandbox :one
SELECT sqlc.embed(agents), sqlc.embed(sandboxes)
FROM agents JOIN sandboxes ON sandboxes.agent_id = agents.id
WHERE agents.id = ?;

-- name: ListWorkspaceAgents :many
SELECT sqlc.embed(agents), sqlc.embed(sandboxes)
FROM agents JOIN sandboxes ON sandboxes.agent_id = agents.id
WHERE agents.workspace_id = ?
ORDER BY agents.name;

-- name: UpdateAgent :exec
UPDATE agents SET name = ?, image = ?, cpus = ?, memory_mb = ?, idle_minutes = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;

-- name: CreateSandbox :exec
INSERT INTO sandboxes (agent_id, driver, state, state_changed_at) VALUES (?, ?, ?, ?);

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE agent_id = ?;

-- name: ListSandboxes :many
SELECT * FROM sandboxes ORDER BY agent_id;

-- name: TransitionSandbox :execrows
-- Moves a sandbox to a new state only if it is currently in one of the allowed states.
UPDATE sandboxes SET state = sqlc.arg(to_state), error = sqlc.arg(error), state_changed_at = sqlc.arg(now)
WHERE agent_id = sqlc.arg(agent_id) AND state IN (sqlc.slice(from_states));

-- name: SetSandboxState :exec
UPDATE sandboxes SET state = ?, error = ?, state_changed_at = ? WHERE agent_id = ?;

-- name: SetSandboxBuilt :exec
UPDATE sandboxes SET driver_id = ?, applied_spec = ? WHERE agent_id = ?;

-- name: SetBootToken :exec
UPDATE sandboxes SET boot_token_hash = ? WHERE agent_id = ?;
