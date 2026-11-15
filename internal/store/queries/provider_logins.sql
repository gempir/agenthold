-- name: CreateProviderLogin :exec
INSERT INTO provider_logins (id, user_id, provider, kind, ciphertext, seed, label, expires_at, refreshed_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ReplaceProviderLogin :exec
-- Reconnect: new credential, same id and seed.
UPDATE provider_logins SET kind = ?, ciphertext = ?, label = ?, expires_at = ?, refreshed_at = ?,
    status = 'ok', status_message = '', updated_at = ?
WHERE id = ?;

-- name: GetProviderLogin :one
SELECT * FROM provider_logins WHERE user_id = ? AND provider = ?;

-- name: GetProviderLoginByID :one
SELECT * FROM provider_logins WHERE id = ?;

-- name: ListUserProviderLogins :many
SELECT * FROM provider_logins WHERE user_id = ? ORDER BY provider;

-- name: ListProviderLogins :many
SELECT * FROM provider_logins ORDER BY id;

-- name: StoreRefreshedLogin :execrows
-- Only if nobody replaced the credential meanwhile.
UPDATE provider_logins SET ciphertext = sqlc.arg(ciphertext), label = sqlc.arg(label), expires_at = sqlc.arg(expires_at),
    refreshed_at = sqlc.arg(refreshed_at), status = 'ok', status_message = ''
WHERE id = sqlc.arg(id) AND ciphertext = sqlc.arg(old_ciphertext);

-- name: SetProviderLoginStatus :exec
UPDATE provider_logins SET status = ?, status_message = ? WHERE id = ?;

-- name: TouchProviderLogin :exec
UPDATE provider_logins SET last_used_at = ? WHERE id = ?;

-- name: DeleteProviderLogin :execrows
DELETE FROM provider_logins WHERE user_id = ? AND provider = ?;

-- name: SetAgentLoginUser :exec
UPDATE agents SET login_user_id = ? WHERE id = ?;

-- name: ListAgentsByLoginUser :many
SELECT * FROM agents WHERE login_user_id = ? ORDER BY name;
