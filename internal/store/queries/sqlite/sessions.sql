-- name: CreateAgentSession :exec
INSERT INTO agent_sessions (id, agent_id, title, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetAgentSession :one
SELECT * FROM agent_sessions WHERE id = ?;

-- name: ListAgentSessions :many
SELECT * FROM agent_sessions WHERE agent_id = ? ORDER BY updated_at DESC;

-- name: SetSessionACPID :exec
UPDATE agent_sessions SET acp_session_id = ? WHERE id = ?;

-- name: SetSessionStatus :exec
UPDATE agent_sessions SET status = ?, updated_at = ? WHERE id = ?;

-- name: RenameSession :exec
UPDATE agent_sessions SET title = ?, updated_at = ? WHERE id = ?;

-- name: DeleteAgentSession :exec
DELETE FROM agent_sessions WHERE id = ?;

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
INSERT INTO agent_session_events (session_id, seq, at, type, user_id, data) VALUES (?, ?, ?, ?, ?, ?);

-- name: MaxSessionSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM agent_session_events WHERE session_id = ?;

-- name: ListSessionEvents :many
SELECT * FROM agent_session_events WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?;

-- name: SetPermissionPolicy :exec
UPDATE agents SET permission_policy = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentHarness :exec
UPDATE agents SET harness = ?, model = ?, model_options = ?, updated_at = ? WHERE id = ?;

-- name: MarkCredentialsInSandbox :exec
UPDATE agents SET credentials_in_sandbox = 1 WHERE id = ?;

-- name: SetSessionOrigin :exec
UPDATE agent_sessions SET origin_agent_id = ?, origin_session_id = ? WHERE id = ?;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = ? AND origin_agent_id = ? ORDER BY updated_at DESC;

-- name: SetAgentModel :exec
UPDATE agents SET model = ?, model_options = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionModel :exec
UPDATE agent_sessions SET model = ?, model_options = ? WHERE id = ?;

-- name: GetModelCatalog :one
SELECT * FROM agent_model_catalogs WHERE agent_id = ?;

-- name: UpsertModelCatalog :exec
INSERT INTO agent_model_catalogs (agent_id, harness, data, seen_at) VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id) DO UPDATE SET harness = excluded.harness, data = excluded.data, seen_at = excluded.seen_at;
