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

-- name: ResetRunningSessions :exec
-- After a restart no turn is running anymore.
UPDATE agent_sessions SET status = 'idle' WHERE status = 'running';

-- name: ListUnfinishedSessions :many
-- Sessions whose last prompt has no turn_end after it: their turn was cut off.
SELECT p.session_id FROM agent_session_events p
WHERE p.type = 'prompt'
  AND p.seq = (SELECT MAX(l.seq) FROM agent_session_events l WHERE l.session_id = p.session_id AND l.type = 'prompt')
  AND NOT EXISTS (SELECT 1 FROM agent_session_events e WHERE e.session_id = p.session_id AND e.type = 'turn_end' AND e.seq > p.seq);

-- name: InsertSessionEvent :exec
INSERT INTO agent_session_events (session_id, seq, at, type, user_id, data) VALUES ($1, $2, $3, $4, $5, $6);

-- name: MaxSessionSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS BIGINT) FROM agent_session_events WHERE session_id = $1;

-- name: ListSessionEvents :many
SELECT * FROM agent_session_events WHERE session_id = $1 AND seq > $2 ORDER BY seq LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: SetPermissionPolicy :exec
UPDATE agents SET permission_policy = $1, updated_at = $2 WHERE id = $3;

-- name: SetAgentHarness :exec
UPDATE agents SET harness = $1, model = $2, model_options = $3, updated_at = $4 WHERE id = $5;

-- name: MarkCredentialsInSandbox :exec
UPDATE agents SET credentials_in_sandbox = 1 WHERE id = $1;

-- name: SetSessionOrigin :exec
UPDATE agent_sessions SET origin_agent_id = $1, origin_session_id = $2 WHERE id = $3;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = $1 AND origin_agent_id = $2 ORDER BY updated_at DESC;

-- name: SetAgentModel :exec
UPDATE agents SET model = $1, model_options = $2, updated_at = $3 WHERE id = $4;

-- name: SetSessionModel :exec
UPDATE agent_sessions SET model = $1, model_options = $2 WHERE id = $3;

-- name: GetModelCatalog :one
SELECT * FROM agent_model_catalogs WHERE agent_id = $1;

-- name: UpsertModelCatalog :exec
INSERT INTO agent_model_catalogs (agent_id, harness, data, seen_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id) DO UPDATE SET harness = excluded.harness, data = excluded.data, seen_at = excluded.seen_at;
