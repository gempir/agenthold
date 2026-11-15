-- name: ShareAgent :exec
INSERT INTO agent_shares (agent_id, user_id, created_at) VALUES (?, ?, ?)
ON CONFLICT (agent_id, user_id) DO NOTHING;

-- name: GetShare :one
SELECT * FROM agent_shares WHERE agent_id = ? AND user_id = ?;

-- name: IsSharedWith :one
SELECT COUNT(*) FROM agent_shares WHERE agent_id = ? AND user_id = ?;

-- name: ListAgentShares :many
SELECT sqlc.embed(users), agent_shares.created_at AS shared_at
FROM agent_shares JOIN users ON users.id = agent_shares.user_id
WHERE agent_shares.agent_id = ?
ORDER BY users.name, users.email, users.id;

-- name: UnshareAgent :execrows
DELETE FROM agent_shares WHERE agent_id = ? AND user_id = ?;

-- name: InsertAudit :exec
INSERT INTO audit_log (at, agent_id, user_id, action, target, details) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAgentAudit :many
SELECT * FROM audit_log WHERE agent_id = ? AND id < ? ORDER BY id DESC LIMIT ?;
