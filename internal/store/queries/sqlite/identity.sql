-- name: CreateUser :exec
INSERT INTO users (id, email, email_verified, name, avatar_url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: UpdateUserProfile :exec
UPDATE users SET email = ?, email_verified = ?, name = ?, avatar_url = ?, updated_at = ? WHERE id = ?;

-- name: ListUsersByVerifiedEmail :many
-- Users with this verified email, for sharing. Emails are compared case-insensitively.
SELECT * FROM users WHERE LOWER(email) = LOWER(?) AND email_verified = 1 ORDER BY created_at;

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
SELECT sqlc.embed(users), sessions.expires_at, sessions.created_at
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

-- name: SetIdentityProviderRole :exec
UPDATE identities SET provider_role = ?, role_config = ? WHERE provider = ? AND subject = ?;

-- name: SetUserRole :exec
UPDATE users SET role = ? WHERE id = ?;

-- name: SetUserRoleOverride :exec
UPDATE users SET role_override = ?, updated_at = ? WHERE id = ?;

-- name: ListIdentities :many
-- Every identity with its user, for re-evaluating roles at start.
SELECT identities.*, users.email AS user_email, users.email_verified AS user_email_verified, users.role AS user_role
FROM identities JOIN users ON users.id = identities.user_id ORDER BY identities.user_id;

-- name: ListUsersWithIdentity :many
-- The admin users list: every user, the provider and username they signed
-- in with (the oldest identity) and how many agents they own.
SELECT users.*,
    CAST(COALESCE((SELECT i.provider FROM identities i WHERE i.user_id = users.id ORDER BY i.created_at LIMIT 1), '') AS TEXT) AS provider,
    CAST(COALESCE((SELECT i.username FROM identities i WHERE i.user_id = users.id ORDER BY i.created_at LIMIT 1), '') AS TEXT) AS username,
    CAST(COALESCE((SELECT MAX(i.last_login_at) FROM identities i WHERE i.user_id = users.id), 0) AS INTEGER) AS last_login_at,
    CAST((SELECT COUNT(*) FROM agents a WHERE a.owner_id = users.id AND a.kind = 'agent') AS INTEGER) AS agents
FROM users ORDER BY users.created_at;

-- name: CountOwnedAgents :one
-- Machines of kind agent (templates and clones included) a user owns, for auth.user_max_agents.
SELECT COUNT(*) FROM agents WHERE owner_id = ? AND kind = 'agent';

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: LockUser :exec
-- Serializes quota checks of one owner across API processes (PostgreSQL
-- row lock until the transaction ends; SQLite transactions are already
-- serialized).
SELECT id FROM users WHERE id = ? ;

-- name: SetUserThreadAutoSettleDays :exec
UPDATE users SET thread_auto_settle_days = ?, updated_at = ? WHERE id = ?;
