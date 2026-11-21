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
INSERT INTO git_credentials (id, user_id, host, username, ciphertext, label, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: UpdateGitCredential :exec
UPDATE git_credentials SET username = $1, ciphertext = $2, label = $3, updated_at = $4 WHERE id = $5;

-- name: DeleteGitCredential :execrows
DELETE FROM git_credentials WHERE id = $1 AND user_id = $2;
