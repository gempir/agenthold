-- name: GetUserGitConfig :one
SELECT * FROM user_git_configs WHERE user_id = ?;

-- name: UpsertUserGitConfig :exec
INSERT INTO user_git_configs (user_id, author_name, author_email, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id) DO UPDATE SET
    author_name = excluded.author_name,
    author_email = excluded.author_email,
    updated_at = excluded.updated_at;

-- name: ListGitCredentials :many
SELECT * FROM git_credentials WHERE user_id = ? ORDER BY host;

-- name: GetGitCredential :one
SELECT * FROM git_credentials WHERE id = ?;

-- name: GetGitCredentialByHost :one
SELECT * FROM git_credentials WHERE user_id = ? AND host = ?;

-- name: CreateGitCredential :exec
INSERT INTO git_credentials (id, user_id, host, username, ciphertext, label, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateGitCredential :exec
UPDATE git_credentials SET username = ?, ciphertext = ?, label = ?, updated_at = ? WHERE id = ?;

-- name: DeleteGitCredential :execrows
DELETE FROM git_credentials WHERE id = ? AND user_id = ?;
