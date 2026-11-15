-- name: CreateUser :exec
INSERT INTO users (id, email, email_verified, name, avatar_url, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: UpdateUserProfile :exec
UPDATE users SET email = $1, email_verified = $2, name = $3, avatar_url = $4, updated_at = $5 WHERE id = $6;

-- name: ListUsersByVerifiedEmail :many
-- Users with this verified email, for sharing. Emails are compared case-insensitively.
SELECT * FROM users WHERE LOWER(email) = LOWER($1) AND email_verified = 1 ORDER BY created_at;

-- name: GetIdentity :one
SELECT * FROM identities WHERE provider = $1 AND subject = $2;

-- name: CreateIdentity :exec
INSERT INTO identities (provider, subject, user_id, email, username, created_at, last_login_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: TouchIdentity :exec
UPDATE identities SET email = $1, username = $2, last_login_at = $3 WHERE provider = $4 AND subject = $5;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, created_at, expires_at, user_agent)
VALUES ($1, $2, $3, $4, $5);

-- name: GetSessionUser :one
SELECT sqlc.embed(users), sessions.expires_at
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1 AND sessions.expires_at > $2;

-- name: ExtendSession :exec
UPDATE sessions SET expires_at = $1 WHERE token_hash = $2;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= $1;

-- name: CreateLoginState :exec
INSERT INTO login_states (state, provider, verifier, nonce, redirect_to, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: TakeLoginState :one
DELETE FROM login_states WHERE state = $1 AND expires_at > $2 RETURNING *;

-- name: DeleteExpiredLoginStates :exec
DELETE FROM login_states WHERE expires_at <= $1;
