-- name: CreateExposure :exec
INSERT INTO exposures (id, agent_id, port, name, access, wake_on_request, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetExposure :one
SELECT * FROM exposures WHERE id = ? AND agent_id = ?;

-- name: GetExposureByName :one
SELECT * FROM exposures WHERE name = ?;

-- name: GetExposureByPort :one
SELECT * FROM exposures WHERE agent_id = ? AND port = ?;

-- name: ListExposures :many
SELECT * FROM exposures WHERE agent_id = ? ORDER BY port;

-- name: CountExposures :one
SELECT COUNT(*) FROM exposures WHERE agent_id = ?;

-- name: UpdateExposure :exec
UPDATE exposures SET name = ?, access = ?, wake_on_request = ? WHERE id = ?;

-- name: DeleteExposure :execrows
DELETE FROM exposures WHERE id = ?;

-- name: ReleaseExposureName :exec
INSERT INTO exposure_released_names (name, owner_id, released_at) VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET owner_id = excluded.owner_id, released_at = excluded.released_at;

-- name: GetReleasedExposureName :one
SELECT * FROM exposure_released_names WHERE name = ?;

-- name: ClaimExposureName :exec
DELETE FROM exposure_released_names WHERE name = ?;

-- name: DeleteOldReleasedExposureNames :exec
DELETE FROM exposure_released_names WHERE released_at <= ?;

-- name: CreatePreviewCode :exec
INSERT INTO preview_codes (code_hash, user_id, exposure_id, host, state_hash, expires_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: TakePreviewCode :one
DELETE FROM preview_codes WHERE code_hash = ? AND expires_at > ? RETURNING *;

-- name: DeleteExpiredPreviewCodes :exec
DELETE FROM preview_codes WHERE expires_at <= ?;

-- name: CreatePreviewSession :exec
INSERT INTO preview_sessions (token_hash, user_id, exposure_id, host, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: TrimPreviewSessions :exec
DELETE FROM preview_sessions WHERE preview_sessions.user_id = sqlc.arg(user_id) AND preview_sessions.exposure_id = sqlc.arg(exposure_id)
AND token_hash NOT IN (
    SELECT token_hash FROM preview_sessions AS keep
    WHERE keep.user_id = sqlc.arg(user_id) AND keep.exposure_id = sqlc.arg(exposure_id)
    ORDER BY keep.created_at DESC, keep.token_hash LIMIT 10
);

-- name: GetPreviewSessionUser :one
SELECT sqlc.embed(users) FROM preview_sessions JOIN users ON users.id = preview_sessions.user_id
WHERE preview_sessions.token_hash = ? AND preview_sessions.exposure_id = ? AND preview_sessions.host = ? AND preview_sessions.expires_at > ?;

-- name: DeletePreviewSession :exec
DELETE FROM preview_sessions WHERE token_hash = ?;

-- name: DeleteUserPreviewSessions :exec
DELETE FROM preview_sessions WHERE user_id = ?;

-- name: DeleteExpiredPreviewSessions :exec
DELETE FROM preview_sessions WHERE expires_at <= ?;

-- name: CreatePreviewConsent :exec
INSERT INTO preview_consents (user_id, exposure_id, created_at) VALUES (?, ?, ?)
ON CONFLICT (user_id, exposure_id) DO NOTHING;

-- name: HasPreviewConsent :one
SELECT COUNT(*) FROM preview_consents WHERE user_id = ? AND exposure_id = ?;
