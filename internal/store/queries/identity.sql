-- name: CreateUser :exec
INSERT INTO users (id, email, email_verified, name, avatar_url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: UpdateUserProfile :exec
UPDATE users SET email = ?, email_verified = ?, name = ?, avatar_url = ?, updated_at = ? WHERE id = ?;

-- name: GetIdentity :one
SELECT * FROM identities WHERE provider = ? AND subject = ?;

-- name: CreateIdentity :exec
INSERT INTO identities (provider, subject, user_id, email, username, created_at, last_login_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: TouchIdentity :exec
UPDATE identities SET email = ?, username = ?, last_login_at = ? WHERE provider = ? AND subject = ?;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, created_at, expires_at, user_agent)
VALUES (?, ?, ?, ?, ?);

-- name: GetSessionUser :one
SELECT sqlc.embed(users), sessions.expires_at
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = ? AND sessions.expires_at > ?;

-- name: ExtendSession :exec
UPDATE sessions SET expires_at = ? WHERE token_hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: CreateLoginState :exec
INSERT INTO login_states (state, provider, verifier, nonce, redirect_to, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: TakeLoginState :one
DELETE FROM login_states WHERE state = ? AND expires_at > ? RETURNING *;

-- name: DeleteExpiredLoginStates :exec
DELETE FROM login_states WHERE expires_at <= ?;
