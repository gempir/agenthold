-- name: ListServices :many
SELECT * FROM services WHERE agent_id = $1 ORDER BY name;

-- name: GetService :one
SELECT * FROM services WHERE agent_id = $1 AND name = $2;

-- name: UpsertService :exec
INSERT INTO services (agent_id, name, command, workdir, env, enabled, created_at, updated_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(name), sqlc.arg(command), sqlc.arg(workdir), sqlc.arg(env), sqlc.arg(enabled), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (agent_id, name) DO UPDATE SET
    command = excluded.command, workdir = excluded.workdir, env = excluded.env,
    enabled = excluded.enabled, updated_at = excluded.updated_at;

-- name: SetServiceEnabled :execrows
UPDATE services SET enabled = $1, updated_at = $2 WHERE agent_id = $3 AND name = $4;

-- name: DeleteService :execrows
DELETE FROM services WHERE agent_id = $1 AND name = $2;

-- name: CreateApp :exec
-- An app is a machine without a harness.
INSERT INTO agents (id, owner_id, name, image, kind, harness, created_by_agent_id, cpus, memory_mb, idle_minutes, login_user_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'app', '', $5, $6, $7, $8, '', $9, $10);

-- name: ListAgentApps :many
-- Apps created by an agent.
SELECT * FROM agents WHERE kind = 'app' AND created_by_agent_id = $1 ORDER BY name, id;

-- name: CountOwnerApps :one
SELECT COUNT(*) FROM agents WHERE kind = 'app' AND owner_id = $1;

-- name: CountAgentApps :one
SELECT COUNT(*) FROM agents WHERE kind = 'app' AND created_by_agent_id = $1;

-- name: TakeOverApp :execrows
-- The owner takes an app away from the agent that created it.
UPDATE agents SET created_by_agent_id = '', updated_at = $1 WHERE id = $2 AND kind = 'app';

-- name: SetExposurePort :exec
UPDATE exposures SET port = $1 WHERE id = $2;

-- name: MakeAppClone :exec
-- A clone of an app is an app of its cloner, managed by no agent.
UPDATE agents SET kind = 'app', harness = '', login_user_id = '', created_by_agent_id = '' WHERE id = $1;

-- name: CopyServices :exec
INSERT INTO services (agent_id, name, command, workdir, env, enabled, created_at, updated_at)
SELECT sqlc.arg(dst_id)::text, src.name, src.command, src.workdir, src.env, src.enabled, sqlc.arg(now)::bigint, sqlc.arg(now)::bigint FROM services AS src WHERE src.agent_id = sqlc.arg(src_id);
