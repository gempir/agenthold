-- name: GetUserGitConfig :one
SELECT * FROM user_git_configs WHERE user_id = $1;

-- name: UpsertUserGitConfig :exec
INSERT INTO user_git_configs (user_id, author_name, author_email, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET
    author_name = excluded.author_name,
    author_email = excluded.author_email,
    updated_at = excluded.updated_at;

-- name: ListGitCredentials :many
SELECT * FROM git_credentials WHERE user_id = $1 ORDER BY host;

-- name: GetGitCredential :one
SELECT * FROM git_credentials WHERE id = $1;

-- name: GetGitCredentialByHost :one
SELECT * FROM git_credentials WHERE user_id = $1 AND host = $2;

-- name: CreateGitCredential :exec
INSERT INTO git_credentials (id, user_id, host, username, ciphertext, label, source, account, scopes,
    refresh_ciphertext, expires_at, refresh_expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: UpdateGitCredential :exec
UPDATE git_credentials SET username = $1, ciphertext = $2, label = $3, source = $4, account = $5, scopes = $6,
    refresh_ciphertext = $7, expires_at = $8, refresh_expires_at = $9, updated_at = $10
WHERE id = $11;

-- name: RefreshGitCredential :execrows
UPDATE git_credentials SET ciphertext = sqlc.arg(ciphertext), refresh_ciphertext = sqlc.arg(refresh_ciphertext),
    expires_at = sqlc.arg(expires_at), refresh_expires_at = sqlc.arg(refresh_expires_at), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(prev_updated_at);

-- name: ListRefreshableGitCredentials :many
SELECT * FROM git_credentials
WHERE source = 'github' AND expires_at > 0 AND expires_at < $1 AND octet_length(refresh_ciphertext) > 0
ORDER BY expires_at;

-- name: DeleteGitCredential :execrows
DELETE FROM git_credentials WHERE id = $1 AND user_id = $2;
