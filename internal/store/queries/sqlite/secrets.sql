-- name: CreateSecret :exec
INSERT INTO secrets (id, owner_id, name, ciphertext, rule, placeholder, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = ?;

-- name: GetSecretByName :one
SELECT * FROM secrets WHERE owner_id = ? AND name = ?;

-- name: ListUserSecrets :many
SELECT * FROM secrets WHERE owner_id = ? ORDER BY name;

-- name: UpdateSecret :exec
UPDATE secrets SET name = ?, ciphertext = ?, rule = ?, updated_at = ? WHERE id = ?;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = ?;

-- name: AttachSecret :exec
INSERT INTO agent_secrets (agent_id, secret_id, env_var, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, secret_id) DO UPDATE SET env_var = excluded.env_var;

-- name: DetachSecret :execrows
DELETE FROM agent_secrets WHERE agent_id = ? AND secret_id = ?;

-- name: ListAgentSecrets :many
SELECT sqlc.embed(secrets), agent_secrets.env_var
FROM agent_secrets JOIN secrets ON secrets.id = agent_secrets.secret_id
WHERE agent_secrets.agent_id = ?
ORDER BY secrets.name;
