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

-- name: InsertSessionEvent :exec
INSERT INTO agent_session_events (session_id, seq, at, type, user_id, data) VALUES (?, ?, ?, ?, ?, ?);

-- name: MaxSessionSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM agent_session_events WHERE session_id = ?;

-- name: ListSessionEvents :many
SELECT * FROM agent_session_events WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?;

-- name: SetPermissionPolicy :exec
UPDATE agents SET permission_policy = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentHarness :exec
UPDATE agents SET harness = ?, model = ?, updated_at = ? WHERE id = ?;

-- name: MarkCredentialsInSandbox :exec
UPDATE agents SET credentials_in_sandbox = 1 WHERE id = ?;

-- name: SetSessionOrigin :exec
UPDATE agent_sessions SET origin_agent_id = ?, origin_session_id = ? WHERE id = ?;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = ? AND origin_agent_id = ? ORDER BY updated_at DESC;
