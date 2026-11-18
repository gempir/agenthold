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

-- name: CountOwnerApps :one
SELECT COUNT(*) FROM agents WHERE kind = 'app' AND owner_id = ?;

-- name: CountAgentApps :one
SELECT COUNT(*) FROM agents WHERE kind = 'app' AND created_by_agent_id = ?;

-- name: TakeOverApp :execrows
-- The owner takes an app away from the agent that created it.
UPDATE agents SET created_by_agent_id = '', updated_at = ? WHERE id = ? AND kind = 'app';

-- name: SetExposurePort :exec
UPDATE exposures SET port = ? WHERE id = ?;

-- name: MakeAppClone :exec
-- A clone of an app is an app of its cloner, managed by no agent.
UPDATE agents SET kind = 'app', harness = '', login_user_id = '', created_by_agent_id = '' WHERE id = ?;

-- name: CopyServices :exec
INSERT INTO services (agent_id, name, command, workdir, env, enabled, created_at, updated_at)
SELECT sqlc.arg(dst_id), src.name, src.command, src.workdir, src.env, src.enabled, sqlc.arg(now), sqlc.arg(now) FROM services AS src WHERE src.agent_id = sqlc.arg(src_id);
