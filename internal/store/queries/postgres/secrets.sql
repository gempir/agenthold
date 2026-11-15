-- name: CreateSecret :exec
INSERT INTO secrets (id, owner_id, name, ciphertext, rule, placeholder, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = $1;

-- name: GetSecretByName :one
SELECT * FROM secrets WHERE owner_id = $1 AND name = $2;

-- name: ListUserSecrets :many
SELECT * FROM secrets WHERE owner_id = $1 ORDER BY name;

-- name: UpdateSecret :exec
UPDATE secrets SET name = $1, ciphertext = $2, rule = $3, updated_at = $4 WHERE id = $5;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = $1;

-- name: AttachSecret :exec
INSERT INTO agent_secrets (agent_id, secret_id, env_var, created_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id, secret_id) DO UPDATE SET env_var = excluded.env_var;

-- name: DetachSecret :execrows
DELETE FROM agent_secrets WHERE agent_id = $1 AND secret_id = $2;

-- name: ListAgentSecrets :many
SELECT sqlc.embed(secrets), agent_secrets.env_var
FROM agent_secrets JOIN secrets ON secrets.id = agent_secrets.secret_id
WHERE agent_secrets.agent_id = $1
ORDER BY secrets.name;
