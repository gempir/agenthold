-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListAPITokens :many
SELECT * FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = ? AND user_id = ?;

-- name: GetTokenUser :one
SELECT sqlc.embed(users), api_tokens.id AS token_id, api_tokens.last_used_at
FROM api_tokens JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.token_hash = ? AND (api_tokens.expires_at IS NULL OR api_tokens.expires_at > ?);

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = ? WHERE id = ?;

-- name: CreateSchedule :exec
INSERT INTO schedules (id, agent_id, session_id, name, cron, timezone, prompt, enabled, created_by, created_at, updated_at, next_run_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id = ?;

-- name: ListAgentSchedules :many
SELECT * FROM schedules WHERE agent_id = ? ORDER BY name;

-- name: UpdateSchedule :exec
UPDATE schedules SET name = ?, cron = ?, timezone = ?, prompt = ?, enabled = ?, next_run_at = ?, updated_at = ? WHERE id = ?;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE id = ?;

-- name: DueSchedules :many
SELECT * FROM schedules WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at;

-- name: RecordScheduleRun :exec
UPDATE schedules SET last_run_at = ?, next_run_at = ?, last_error = ? WHERE id = ?;

-- name: CreateWebhook :exec
INSERT INTO webhooks (id, agent_id, session_id, name, token_hash, prompt_template, signing_secret, signature_header, enabled, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = ?;

-- name: GetWebhookByToken :one
SELECT * FROM webhooks WHERE token_hash = ?;

-- name: ListAgentWebhooks :many
SELECT * FROM webhooks WHERE agent_id = ? ORDER BY name;

-- name: UpdateWebhook :exec
UPDATE webhooks SET name = ?, prompt_template = ?, signing_secret = ?, signature_header = ?, enabled = ?, updated_at = ? WHERE id = ?;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE id = ?;

-- name: RecordWebhookDelivery :exec
UPDATE webhooks SET last_delivery_at = ?, last_error = ? WHERE id = ?;
