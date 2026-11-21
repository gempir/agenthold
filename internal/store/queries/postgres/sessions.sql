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

-- name: ResetSessionModels :exec
-- A new harness knows nothing of the models chosen on the old one.
UPDATE agent_sessions SET model = '', model_options = '{}' WHERE agent_id = $1;

-- name: SetSessionModel :exec
UPDATE agent_sessions SET model = $1, model_options = $2 WHERE id = $3;

-- name: GetModelCatalog :one
SELECT * FROM agent_model_catalogs WHERE agent_id = $1;

-- name: UpsertModelCatalog :exec
INSERT INTO agent_model_catalogs (agent_id, harness, data, seen_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id) DO UPDATE SET harness = excluded.harness, data = excluded.data, seen_at = excluded.seen_at;

-- name: MarkSessionRunning :exec
-- A new turn is activity: it also unsettles the session.
UPDATE agent_sessions SET status = 'running', updated_at = $1, settled_at = NULL, settled_by = '' WHERE id = $2;

-- name: SettleSession :execrows
-- Settles an active session; no row when it is settled already.
UPDATE agent_sessions SET settled_at = $1, settled_by = $2 WHERE id = $3 AND settled_at IS NULL;

-- name: UnsettleSession :execrows
-- Unsettles a settled session; no row when it is active already.
UPDATE agent_sessions SET settled_at = NULL, settled_by = '', unsettled_at = $1 WHERE id = $2 AND settled_at IS NOT NULL;

-- name: ListAutoSettleCandidates :many
-- Active, idle sessions whose agent owner's auto-settle period has passed
-- since their last activity or their last unsettle by hand.
SELECT s.id, s.agent_id, s.title, a.owner_id, u.thread_auto_settle_days
FROM agent_sessions s JOIN agents a ON a.id = s.agent_id JOIN users u ON u.id = a.owner_id
WHERE s.settled_at IS NULL AND s.status = 'idle' AND u.thread_auto_settle_days > 0
  AND GREATEST(s.updated_at, COALESCE(s.unsettled_at, 0)) < CAST(sqlc.arg(now) AS BIGINT) - u.thread_auto_settle_days * 86400
ORDER BY s.id;

-- name: AutoSettleSession :execrows
-- Settles a candidate unless it saw activity since it was listed.
UPDATE agent_sessions SET settled_at = sqlc.arg(settled_at), settled_by = 'auto'
WHERE id = sqlc.arg(id) AND settled_at IS NULL AND status = 'idle'
  AND GREATEST(updated_at, COALESCE(unsettled_at, 0)) < CAST(sqlc.arg(cutoff) AS BIGINT);
