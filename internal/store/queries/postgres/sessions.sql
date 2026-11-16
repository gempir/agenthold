-- name: CreateAgentSession :exec
INSERT INTO agent_sessions (id, agent_id, title, created_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetAgentSession :one
SELECT * FROM agent_sessions WHERE id = $1;

-- name: ListAgentSessions :many
SELECT * FROM agent_sessions WHERE agent_id = $1 ORDER BY updated_at DESC;

-- name: SetSessionACPID :exec
UPDATE agent_sessions SET acp_session_id = $1 WHERE id = $2;

-- name: SetSessionStatus :exec
UPDATE agent_sessions SET status = $1, updated_at = $2 WHERE id = $3;

-- name: RenameSession :exec
UPDATE agent_sessions SET title = $1, updated_at = $2 WHERE id = $3;

-- name: DeleteAgentSession :exec
DELETE FROM agent_sessions WHERE id = $1;

-- name: ResetRunningSessions :many
-- After a restart no turn is running anymore. Returns the sessions whose turn was cut off.
UPDATE agent_sessions SET status = 'idle' WHERE status = 'running' RETURNING id;

-- name: InsertSessionEvent :exec
INSERT INTO agent_session_events (session_id, seq, at, type, user_id, data) VALUES ($1, $2, $3, $4, $5, $6);

-- name: MaxSessionSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS BIGINT) FROM agent_session_events WHERE session_id = $1;

-- name: ListSessionEvents :many
SELECT * FROM agent_session_events WHERE session_id = $1 AND seq > $2 ORDER BY seq LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: SetPermissionPolicy :exec
UPDATE agents SET permission_policy = $1, updated_at = $2 WHERE id = $3;

-- name: SetAgentHarness :exec
UPDATE agents SET harness = $1, model = $2, updated_at = $3 WHERE id = $4;

-- name: MarkCredentialsInSandbox :exec
UPDATE agents SET credentials_in_sandbox = 1 WHERE id = $1;

-- name: SetSessionOrigin :exec
UPDATE agent_sessions SET origin_agent_id = $1, origin_session_id = $2 WHERE id = $3;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = $1 AND origin_agent_id = $2 ORDER BY updated_at DESC;
