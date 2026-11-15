-- name: CreateProviderLogin :exec
INSERT INTO provider_logins (id, user_id, provider, kind, ciphertext, seed, label, expires_at, refreshed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ReplaceProviderLogin :exec
-- Reconnect: new credential, same id and seed.
UPDATE provider_logins SET kind = $1, ciphertext = $2, label = $3, expires_at = $4, refreshed_at = $5,
    status = 'ok', status_message = '', updated_at = $6
WHERE id = $7;

-- name: GetProviderLogin :one
SELECT * FROM provider_logins WHERE user_id = $1 AND provider = $2;

-- name: GetProviderLoginByID :one
SELECT * FROM provider_logins WHERE id = $1;

-- name: ListUserProviderLogins :many
SELECT * FROM provider_logins WHERE user_id = $1 ORDER BY provider;

-- name: ListProviderLogins :many
SELECT * FROM provider_logins ORDER BY id;

-- name: StoreRefreshedLogin :execrows
-- Only if nobody replaced the credential meanwhile.
UPDATE provider_logins SET ciphertext = sqlc.arg(ciphertext), label = sqlc.arg(label), expires_at = sqlc.arg(expires_at),
    refreshed_at = sqlc.arg(refreshed_at), status = 'ok', status_message = ''
WHERE id = sqlc.arg(id) AND ciphertext = sqlc.arg(old_ciphertext);

-- name: SetProviderLoginStatus :exec
UPDATE provider_logins SET status = $1, status_message = $2 WHERE id = $3;

-- name: TouchProviderLogin :exec
UPDATE provider_logins SET last_used_at = $1 WHERE id = $2;

-- name: DeleteProviderLogin :execrows
DELETE FROM provider_logins WHERE user_id = $1 AND provider = $2;

-- name: SetAgentLoginUser :exec
UPDATE agents SET login_user_id = $1 WHERE id = $2;

-- name: ListAgentsByLoginUser :many
SELECT * FROM agents WHERE login_user_id = $1 ORDER BY name;
