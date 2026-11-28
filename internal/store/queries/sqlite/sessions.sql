-- name: CreateAgentSession :exec
INSERT INTO agent_sessions (id, agent_id, title, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetAgentSession :one
SELECT * FROM agent_sessions WHERE id = ?;

-- name: ListAgentSessions :many
SELECT * FROM agent_sessions WHERE agent_id = ? ORDER BY updated_at DESC;

-- name: SetSessionACPID :exec
UPDATE agent_sessions SET acp_session_id = ? WHERE id = ?;

-- name: SetSessionNative :exec
UPDATE agent_sessions SET acp_session_id = ?, acp_harness = ? WHERE id = ?;

-- name: SetSessionHandoffSeq :exec
UPDATE agent_sessions SET handoff_seq = ? WHERE id = ?;

-- name: GetSessionEvent :one
SELECT * FROM agent_session_events WHERE session_id = ? AND seq = ?;

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

-- name: SetSessionHarness :exec
UPDATE agent_sessions SET harness = ? WHERE id = ?;

-- name: ListChildSessions :many
-- Sub-threads started from a parent session, oldest first.
SELECT * FROM agent_sessions WHERE origin_session_id = ? ORDER BY created_at ASC;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = ? AND origin_agent_id = ? ORDER BY updated_at DESC;

-- name: SetAgentModel :exec
UPDATE agents SET model = ?, model_options = ?, updated_at = ? WHERE id = ?;

-- name: ResetSessionModels :exec
-- A new harness knows nothing of the models chosen on the old one.
-- Sub-threads pinned to their own harness keep theirs.
UPDATE agent_sessions SET model = '', model_options = '{}' WHERE agent_id = ? AND harness = '';

-- name: SetSessionModel :exec
UPDATE agent_sessions SET model = ?, model_options = ? WHERE id = ?;

-- name: GetModelCatalog :one
SELECT * FROM agent_model_catalogs WHERE agent_id = ?;

-- name: UpsertModelCatalog :exec
INSERT INTO agent_model_catalogs (agent_id, harness, data, seen_at) VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id) DO UPDATE SET harness = excluded.harness, data = excluded.data, seen_at = excluded.seen_at;

-- name: MarkSessionRunning :exec
-- A new turn is activity: it also unsettles and wakes a snoozed session.
UPDATE agent_sessions SET status = 'running', updated_at = ?, settled_at = NULL, settled_by = '', snoozed_until = NULL WHERE id = ?;

-- name: SettleSession :execrows
-- Settles an active session; no row when it is settled already.
UPDATE agent_sessions SET settled_at = ?, settled_by = ? WHERE id = ? AND settled_at IS NULL;

-- name: UnsettleSession :execrows
-- Unsettles a settled session; no row when it is active already.
UPDATE agent_sessions SET settled_at = NULL, settled_by = '', unsettled_at = ? WHERE id = ? AND settled_at IS NOT NULL;

-- name: ListAutoSettleCandidates :many
-- Active, idle sessions whose agent owner's auto-settle period has passed
-- since their last activity or their last unsettle by hand.
SELECT s.id, s.agent_id, s.title, a.owner_id, u.thread_auto_settle_days
FROM agent_sessions s JOIN agents a ON a.id = s.agent_id JOIN users u ON u.id = a.owner_id
WHERE s.settled_at IS NULL AND s.status = 'idle' AND u.thread_auto_settle_days > 0
  AND MAX(s.updated_at, COALESCE(s.unsettled_at, 0)) < CAST(sqlc.arg(now) AS INTEGER) - u.thread_auto_settle_days * 86400
ORDER BY s.id;

-- name: AutoSettleSession :execrows
-- Settles a candidate unless it saw activity since it was listed.
UPDATE agent_sessions SET settled_at = sqlc.arg(settled_at), settled_by = 'auto'
WHERE id = sqlc.arg(id) AND settled_at IS NULL AND status = 'idle'
  AND MAX(updated_at, COALESCE(unsettled_at, 0)) < CAST(sqlc.arg(cutoff) AS INTEGER);

-- name: SetSessionGit :exec
UPDATE agent_sessions SET cwd = ?, git_project = ?, git_branch = ?, git_repo_path = ? WHERE id = ?;

-- name: UpdateSessionGitStatus :exec
UPDATE agent_sessions SET git_project = ?, git_branch = ?, git_repo_path = ? WHERE id = ?;

-- name: UpdateAgentRepoBranch :exec
UPDATE agent_sessions SET git_branch = ? WHERE agent_id = ? AND git_repo_path = ?;


-- name: SetSessionPermissionMode :exec
UPDATE agent_sessions SET permission_mode = ?, updated_at = ? WHERE id = ?;

-- name: SetSessionParentKind :exec
UPDATE agent_sessions SET parent_kind = ? WHERE id = ?;

-- name: PinSession :execrows
UPDATE agent_sessions SET pinned_at = ? WHERE id = ? AND pinned_at IS NULL;

-- name: UnpinSession :execrows
UPDATE agent_sessions SET pinned_at = NULL WHERE id = ? AND pinned_at IS NOT NULL;

-- name: SnoozeSession :exec
UPDATE agent_sessions SET snoozed_until = ? WHERE id = ?;

-- name: UnsnoozeSession :execrows
UPDATE agent_sessions SET snoozed_until = NULL WHERE id = ? AND snoozed_until IS NOT NULL;

-- name: DeleteSessionEventsFrom :execrows
DELETE FROM agent_session_events WHERE session_id = ? AND seq >= ?;

-- name: EnqueueSessionMessage :exec
INSERT INTO session_queued_messages (id, session_id, user_id, user_name, text, images_json, steer, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListSessionQueuedMessages :many
SELECT * FROM session_queued_messages WHERE session_id = ? ORDER BY steer DESC, created_at ASC, id ASC;

-- name: GetSessionQueuedMessage :one
SELECT * FROM session_queued_messages WHERE id = ? AND session_id = ?;

-- name: UpdateSessionQueuedMessage :execrows
UPDATE session_queued_messages SET text = ? WHERE id = ? AND session_id = ?;

-- name: SetSessionQueuedMessageSteer :execrows
UPDATE session_queued_messages SET steer = ? WHERE id = ? AND session_id = ?;

-- name: DeleteSessionQueuedMessage :execrows
DELETE FROM session_queued_messages WHERE id = ? AND session_id = ?;

-- name: CountSessionQueuedMessages :one
SELECT CAST(COUNT(*) AS INTEGER) FROM session_queued_messages WHERE session_id = ?;

-- name: CountAgentQueuedMessages :many
SELECT q.session_id, CAST(COUNT(*) AS INTEGER) AS count
FROM session_queued_messages q
JOIN agent_sessions s ON s.id = q.session_id
WHERE s.agent_id = ?
GROUP BY q.session_id;

-- name: SearchUserSessionsByMetadata :many
SELECT sqlc.embed(agent_sessions), agents.name AS agent_name, CAST(COALESCE(projects.name, '') AS TEXT) AS project_name
FROM agent_sessions
JOIN agents ON agents.id = agent_sessions.agent_id
LEFT JOIN projects ON projects.id = agent_sessions.project_id
WHERE (agents.owner_id = sqlc.arg(user_id)
       OR agents.id IN (SELECT agent_shares.agent_id FROM agent_shares WHERE agent_shares.user_id = sqlc.arg(user_id)))
  AND (INSTR(LOWER(agent_sessions.title), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR INSTR(LOWER(agent_sessions.git_branch), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR INSTR(LOWER(agent_sessions.git_project), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR INSTR(LOWER(agent_sessions.project_branch), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR INSTR(LOWER(COALESCE(projects.name, '')), CAST(sqlc.arg(needle) AS TEXT)) > 0)
ORDER BY agent_sessions.updated_at DESC
LIMIT CAST(sqlc.arg(max_rows) AS INTEGER);

-- name: SearchUserSessionEvents :many
SELECT sqlc.embed(agent_sessions), agents.name AS agent_name, agent_session_events.seq AS event_seq, agent_session_events.type AS event_type, agent_session_events.data AS event_data
FROM agent_session_events
JOIN agent_sessions ON agent_sessions.id = agent_session_events.session_id
JOIN agents ON agents.id = agent_sessions.agent_id
WHERE (agents.owner_id = sqlc.arg(user_id)
       OR agents.id IN (SELECT agent_shares.agent_id FROM agent_shares WHERE agent_shares.user_id = sqlc.arg(user_id)))
  AND agent_session_events.type IN ('prompt', 'update', 'error')
  AND INSTR(LOWER(agent_session_events.data), CAST(sqlc.arg(needle) AS TEXT)) > 0
ORDER BY agent_sessions.updated_at DESC, agent_session_events.seq DESC
LIMIT CAST(sqlc.arg(max_rows) AS INTEGER);
