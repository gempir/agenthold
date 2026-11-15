-- name: ShareAgent :exec
INSERT INTO agent_shares (agent_id, user_id, created_at) VALUES ($1, $2, $3)
ON CONFLICT (agent_id, user_id) DO NOTHING;

-- name: GetShare :one
SELECT * FROM agent_shares WHERE agent_id = $1 AND user_id = $2;

-- name: IsSharedWith :one
SELECT COUNT(*) FROM agent_shares WHERE agent_id = $1 AND user_id = $2;

-- name: ListAgentShares :many
SELECT sqlc.embed(users), agent_shares.created_at AS shared_at
FROM agent_shares JOIN users ON users.id = agent_shares.user_id
WHERE agent_shares.agent_id = $1
ORDER BY users.name, users.email, users.id;

-- name: UnshareAgent :execrows
DELETE FROM agent_shares WHERE agent_id = $1 AND user_id = $2;

-- name: InsertAudit :exec
INSERT INTO audit_log (at, agent_id, user_id, action, target, details) VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAgentAudit :many
SELECT * FROM audit_log WHERE agent_id = $1 AND id < $2 ORDER BY id DESC LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: ListUserAudit :many
SELECT * FROM audit_log WHERE user_id = $1 AND agent_id = '' ORDER BY id DESC;
