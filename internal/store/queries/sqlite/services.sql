-- name: ListServices :many
SELECT * FROM services WHERE agent_id = ? ORDER BY name;

-- name: GetService :one
SELECT * FROM services WHERE agent_id = ? AND name = ?;

-- name: UpsertService :exec
INSERT INTO services (agent_id, name, command, workdir, env, enabled, created_at, updated_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(name), sqlc.arg(command), sqlc.arg(workdir), sqlc.arg(env), sqlc.arg(enabled), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (agent_id, name) DO UPDATE SET
    command = excluded.command, workdir = excluded.workdir, env = excluded.env,
    enabled = excluded.enabled, updated_at = excluded.updated_at;

-- name: SetServiceEnabled :execrows
UPDATE services SET enabled = ?, updated_at = ? WHERE agent_id = ? AND name = ?;

-- name: DeleteService :execrows
DELETE FROM services WHERE agent_id = ? AND name = ?;

-- name: CreateApp :exec
-- An app is a machine without a harness.
INSERT INTO agents (id, owner_id, name, image, kind, harness, created_by_agent_id, cpus, memory_mb, idle_minutes, login_user_id, created_at, updated_at)
VALUES (?, ?, ?, ?, 'app', '', ?, ?, ?, ?, '', ?, ?);

-- name: ListAgentApps :many
-- Apps created by an agent.
SELECT * FROM agents WHERE kind = 'app' AND created_by_agent_id = ? ORDER BY name, id;
