-- name: CreateAgentSession :exec
INSERT INTO agent_sessions (id, agent_id, title, created_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetAgentSession :one
SELECT * FROM agent_sessions WHERE id = $1;

-- name: ListAgentSessions :many
SELECT * FROM agent_sessions WHERE agent_id = $1 ORDER BY updated_at DESC;

-- name: SetSessionACPID :exec
UPDATE agent_sessions SET acp_session_id = $1 WHERE id = $2;

-- name: SetSessionNative :exec
UPDATE agent_sessions SET acp_session_id = $1, acp_harness = $2 WHERE id = $3;

-- name: SetSessionHandoffSeq :exec
UPDATE agent_sessions SET handoff_seq = $1 WHERE id = $2;

-- name: GetSessionEvent :one
SELECT * FROM agent_session_events WHERE session_id = $1 AND seq = $2;

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

-- name: SetSessionHarness :exec
UPDATE agent_sessions SET harness = $1 WHERE id = $2;

-- name: ListChildSessions :many
-- Sub-threads started from a parent session, oldest first.
SELECT * FROM agent_sessions WHERE origin_session_id = $1 ORDER BY created_at ASC;

-- name: LinkedSessions :many
-- Sessions of an agent that another agent started, newest first.
SELECT * FROM agent_sessions WHERE agent_id = $1 AND origin_agent_id = $2 ORDER BY updated_at DESC;

-- name: SetAgentModel :exec
UPDATE agents SET model = $1, model_options = $2, updated_at = $3 WHERE id = $4;

-- name: ResetSessionModels :exec
-- A new harness knows nothing of the models chosen on the old one.
-- Sub-threads pinned to their own harness keep theirs.
UPDATE agent_sessions SET model = '', model_options = '{}' WHERE agent_id = $1 AND harness = '';

-- name: SetSessionModel :exec
UPDATE agent_sessions SET model = $1, model_options = $2 WHERE id = $3;

-- name: GetModelCatalog :one
SELECT * FROM agent_model_catalogs WHERE agent_id = $1;

-- name: UpsertModelCatalog :exec
INSERT INTO agent_model_catalogs (agent_id, harness, data, seen_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id) DO UPDATE SET harness = excluded.harness, data = excluded.data, seen_at = excluded.seen_at;

-- name: MarkSessionRunning :exec
-- A new turn is activity: it also unsettles and wakes a snoozed session.
UPDATE agent_sessions SET status = 'running', updated_at = $1, settled_at = NULL, settled_by = '', snoozed_until = NULL WHERE id = $2;

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

-- name: SetSessionGit :exec
UPDATE agent_sessions SET cwd = $1, git_project = $2, git_branch = $3, git_repo_path = $4 WHERE id = $5;

-- name: UpdateSessionGitStatus :exec
UPDATE agent_sessions SET git_project = $1, git_branch = $2, git_repo_path = $3 WHERE id = $4;

-- name: UpdateAgentRepoBranch :exec
UPDATE agent_sessions SET git_branch = $1 WHERE agent_id = $2 AND git_repo_path = $3;


-- name: SetSessionPermissionMode :exec
UPDATE agent_sessions SET permission_mode = $1, updated_at = $2 WHERE id = $3;

-- name: SetSessionParentKind :exec
UPDATE agent_sessions SET parent_kind = $1 WHERE id = $2;

-- name: PinSession :execrows
UPDATE agent_sessions SET pinned_at = $1 WHERE id = $2 AND pinned_at IS NULL;

-- name: UnpinSession :execrows
UPDATE agent_sessions SET pinned_at = NULL WHERE id = $1 AND pinned_at IS NOT NULL;

-- name: SnoozeSession :exec
UPDATE agent_sessions SET snoozed_until = $1 WHERE id = $2;

-- name: UnsnoozeSession :execrows
UPDATE agent_sessions SET snoozed_until = NULL WHERE id = $1 AND snoozed_until IS NOT NULL;

-- name: DeleteSessionEventsFrom :execrows
DELETE FROM agent_session_events WHERE session_id = $1 AND seq >= $2;

-- name: EnqueueSessionMessage :exec
INSERT INTO session_queued_messages (id, session_id, user_id, user_name, text, images_json, steer, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListSessionQueuedMessages :many
SELECT * FROM session_queued_messages WHERE session_id = $1 ORDER BY steer DESC, created_at ASC, id ASC;

-- name: GetSessionQueuedMessage :one
SELECT * FROM session_queued_messages WHERE id = $1 AND session_id = $2;

-- name: UpdateSessionQueuedMessage :execrows
UPDATE session_queued_messages SET text = $1 WHERE id = $2 AND session_id = $3;

-- name: SetSessionQueuedMessageSteer :execrows
UPDATE session_queued_messages SET steer = $1 WHERE id = $2 AND session_id = $3;

-- name: DeleteSessionQueuedMessage :execrows
DELETE FROM session_queued_messages WHERE id = $1 AND session_id = $2;

-- name: CountSessionQueuedMessages :one
SELECT CAST(COUNT(*) AS BIGINT) FROM session_queued_messages WHERE session_id = $1;

-- name: CountAgentQueuedMessages :many
SELECT q.session_id, CAST(COUNT(*) AS BIGINT) AS count
FROM session_queued_messages q
JOIN agent_sessions s ON s.id = q.session_id
WHERE s.agent_id = $1
GROUP BY q.session_id;

-- name: SearchUserSessionsByMetadata :many
SELECT sqlc.embed(agent_sessions), agents.name AS agent_name, CAST(COALESCE(projects.name, '') AS TEXT) AS project_name
FROM agent_sessions
JOIN agents ON agents.id = agent_sessions.agent_id
LEFT JOIN projects ON projects.id = agent_sessions.project_id
WHERE (agents.owner_id = sqlc.arg(user_id)
       OR agents.id IN (SELECT agent_shares.agent_id FROM agent_shares WHERE agent_shares.user_id = sqlc.arg(user_id)))
  AND (STRPOS(LOWER(agent_sessions.title), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR STRPOS(LOWER(agent_sessions.git_branch), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR STRPOS(LOWER(agent_sessions.git_project), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR STRPOS(LOWER(agent_sessions.project_branch), CAST(sqlc.arg(needle) AS TEXT)) > 0
       OR STRPOS(LOWER(COALESCE(projects.name, '')), CAST(sqlc.arg(needle) AS TEXT)) > 0)
ORDER BY agent_sessions.updated_at DESC
LIMIT CAST(sqlc.arg(max_rows) AS BIGINT);

-- name: SearchUserSessionEvents :many
SELECT sqlc.embed(agent_sessions), agents.name AS agent_name, agent_session_events.seq AS event_seq, agent_session_events.type AS event_type, agent_session_events.data AS event_data
FROM agent_session_events
JOIN agent_sessions ON agent_sessions.id = agent_session_events.session_id
JOIN agents ON agents.id = agent_sessions.agent_id
WHERE (agents.owner_id = sqlc.arg(user_id)
       OR agents.id IN (SELECT agent_shares.agent_id FROM agent_shares WHERE agent_shares.user_id = sqlc.arg(user_id)))
  AND agent_session_events.type IN ('prompt', 'update', 'error')
  AND STRPOS(LOWER(agent_session_events.data), CAST(sqlc.arg(needle) AS TEXT)) > 0
ORDER BY agent_sessions.updated_at DESC, agent_session_events.seq DESC
LIMIT CAST(sqlc.arg(max_rows) AS BIGINT);
