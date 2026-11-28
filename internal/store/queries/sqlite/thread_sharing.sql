-- name: UpsertSessionShare :exec
INSERT INTO session_shares (session_id, user_id, role, created_by, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, user_id) DO UPDATE SET role = excluded.role;

-- name: GetSessionShare :one
SELECT * FROM session_shares WHERE session_id = ? AND user_id = ?;

-- name: ListSessionShares :many
SELECT sqlc.embed(users), session_shares.role, session_shares.created_by, session_shares.created_at AS shared_at
FROM session_shares
JOIN users ON users.id = session_shares.user_id
WHERE session_shares.session_id = ?
ORDER BY users.name, users.id;

-- name: DeleteSessionShare :execrows
DELETE FROM session_shares WHERE session_id = ? AND user_id = ?;

-- name: ListUserSharedSessions :many
SELECT sqlc.embed(agent_sessions), session_shares.role AS share_role, session_shares.created_at AS shared_at, agents.name AS agent_name, agents.owner_id AS agent_owner_id, agents.harness AS agent_harness
FROM session_shares
JOIN agent_sessions ON agent_sessions.id = session_shares.session_id
JOIN agents ON agents.id = agent_sessions.agent_id
WHERE session_shares.user_id = ?
ORDER BY agent_sessions.updated_at DESC;

-- name: ListUserSharedSessionsForAgent :many
SELECT sqlc.embed(agent_sessions), session_shares.role AS share_role
FROM session_shares
JOIN agent_sessions ON agent_sessions.id = session_shares.session_id
WHERE agent_sessions.agent_id = ? AND session_shares.user_id = ?
ORDER BY agent_sessions.updated_at DESC;

-- name: HasAnySessionShareOnAgent :one
SELECT COUNT(*)
FROM session_shares
JOIN agent_sessions ON agent_sessions.id = session_shares.session_id
WHERE agent_sessions.agent_id = ? AND session_shares.user_id = ?;

-- name: CreateSessionGuestLink :exec
INSERT INTO session_guest_links (id, session_id, token_hash, created_by, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetSessionGuestLink :one
SELECT * FROM session_guest_links WHERE id = ? AND session_id = ?;

-- name: GetSessionGuestLinkByTokenHash :one
SELECT * FROM session_guest_links WHERE token_hash = ?;

-- name: ListSessionGuestLinks :many
SELECT * FROM session_guest_links WHERE session_id = ? ORDER BY created_at DESC;

-- name: RevokeSessionGuestLink :execrows
UPDATE session_guest_links SET revoked_at = ? WHERE id = ? AND session_id = ? AND revoked_at IS NULL;

-- name: CreateSessionGuestRequest :exec
INSERT INTO session_guest_requests (id, session_id, guest_link_id, guest_name, text, status, created_at)
VALUES (?, ?, ?, ?, ?, 'pending', ?);

-- name: GetSessionGuestRequest :one
SELECT * FROM session_guest_requests WHERE id = ? AND session_id = ?;

-- name: ListSessionGuestRequests :many
SELECT * FROM session_guest_requests WHERE session_id = ? ORDER BY created_at ASC, id ASC;

-- name: ListPendingSessionGuestRequests :many
SELECT * FROM session_guest_requests WHERE session_id = ? AND status = 'pending' ORDER BY created_at ASC, id ASC;

-- name: ResolveSessionGuestRequest :execrows
UPDATE session_guest_requests
SET status = ?, resolved_by = ?, resolved_at = ?
WHERE id = ? AND session_id = ? AND status = 'pending';

-- name: CreateSessionReviewComment :exec
INSERT INTO session_review_comments (id, session_id, user_id, user_name, file_path, start_line, end_line, side, body, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSessionReviewComment :one
SELECT * FROM session_review_comments WHERE id = ? AND session_id = ?;

-- name: ListSessionReviewComments :many
SELECT * FROM session_review_comments WHERE session_id = ? ORDER BY created_at ASC, id ASC;

-- name: ResolveSessionReviewComment :execrows
UPDATE session_review_comments SET resolved_at = ? WHERE id = ? AND session_id = ? AND resolved_at IS NULL;

-- name: DeleteSessionReviewComment :execrows
DELETE FROM session_review_comments WHERE id = ? AND session_id = ?;
