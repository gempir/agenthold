-- name: CreateExposure :exec
INSERT INTO exposures (id, agent_id, port, name, access, created_by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetExposure :one
SELECT * FROM exposures WHERE id = $1 AND agent_id = $2;

-- name: GetExposureByName :one
SELECT * FROM exposures WHERE name = $1;

-- name: GetExposureByPort :one
SELECT * FROM exposures WHERE agent_id = $1 AND port = $2;

-- name: ListExposures :many
SELECT * FROM exposures WHERE agent_id = $1 ORDER BY port;

-- name: UpdateExposure :exec
UPDATE exposures SET name = $1, access = $2 WHERE id = $3;

-- name: DeleteExposure :execrows
DELETE FROM exposures WHERE id = $1;

-- name: CreatePreviewCode :exec
INSERT INTO preview_codes (code_hash, user_id, host, expires_at) VALUES ($1, $2, $3, $4);

-- name: TakePreviewCode :one
DELETE FROM preview_codes WHERE code_hash = $1 AND expires_at > $2 RETURNING *;

-- name: DeleteExpiredPreviewCodes :exec
DELETE FROM preview_codes WHERE expires_at <= $1;

-- name: CreatePreviewSession :exec
INSERT INTO preview_sessions (token_hash, user_id, host, created_at, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetPreviewSessionUser :one
SELECT sqlc.embed(users) FROM preview_sessions JOIN users ON users.id = preview_sessions.user_id
WHERE preview_sessions.token_hash = $1 AND preview_sessions.host = $2 AND preview_sessions.expires_at > $3;

-- name: DeletePreviewSession :exec
DELETE FROM preview_sessions WHERE token_hash = $1;

-- name: DeleteExpiredPreviewSessions :exec
DELETE FROM preview_sessions WHERE expires_at <= $1;
