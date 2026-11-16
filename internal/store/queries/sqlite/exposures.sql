-- name: CreateExposure :exec
INSERT INTO exposures (id, agent_id, port, name, access, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetExposure :one
SELECT * FROM exposures WHERE id = ? AND agent_id = ?;

-- name: GetExposureByName :one
SELECT * FROM exposures WHERE name = ?;

-- name: GetExposureByPort :one
SELECT * FROM exposures WHERE agent_id = ? AND port = ?;

-- name: ListExposures :many
SELECT * FROM exposures WHERE agent_id = ? ORDER BY port;

-- name: UpdateExposure :exec
UPDATE exposures SET name = ?, access = ? WHERE id = ?;

-- name: DeleteExposure :execrows
DELETE FROM exposures WHERE id = ?;

-- name: CreatePreviewCode :exec
INSERT INTO preview_codes (code_hash, user_id, host, expires_at) VALUES (?, ?, ?, ?);

-- name: TakePreviewCode :one
DELETE FROM preview_codes WHERE code_hash = ? AND expires_at > ? RETURNING *;

-- name: DeleteExpiredPreviewCodes :exec
DELETE FROM preview_codes WHERE expires_at <= ?;

-- name: CreatePreviewSession :exec
INSERT INTO preview_sessions (token_hash, user_id, host, created_at, expires_at) VALUES (?, ?, ?, ?, ?);

-- name: GetPreviewSessionUser :one
SELECT sqlc.embed(users) FROM preview_sessions JOIN users ON users.id = preview_sessions.user_id
WHERE preview_sessions.token_hash = ? AND preview_sessions.host = ? AND preview_sessions.expires_at > ?;

-- name: DeletePreviewSession :exec
DELETE FROM preview_sessions WHERE token_hash = ?;

-- name: DeleteExpiredPreviewSessions :exec
DELETE FROM preview_sessions WHERE expires_at <= ?;
