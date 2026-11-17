-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, created_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAPITokens :many
SELECT * FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = $1 AND user_id = $2;

-- name: GetTokenUser :one
SELECT sqlc.embed(users), api_tokens.id AS token_id, api_tokens.last_used_at
FROM api_tokens JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.token_hash = $1 AND (api_tokens.expires_at IS NULL OR api_tokens.expires_at > $2);

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = $1 WHERE id = $2;

-- name: CreateSchedule :exec
INSERT INTO schedules (id, agent_id, session_id, name, cron, timezone, prompt, enabled, created_by, created_at, updated_at, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id = $1;

-- name: ListAgentSchedules :many
SELECT * FROM schedules WHERE agent_id = $1 ORDER BY name;

-- name: UpdateSchedule :exec
UPDATE schedules SET name = $1, cron = $2, timezone = $3, prompt = $4, enabled = $5, next_run_at = $6, updated_at = $7 WHERE id = $8;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE id = $1;

-- name: DueSchedules :many
SELECT * FROM schedules WHERE enabled = 1 AND next_run_at <= $1 ORDER BY next_run_at;

-- name: RecordScheduleRun :exec
UPDATE schedules SET last_run_at = $1, next_run_at = $2, last_error = $3 WHERE id = $4;

-- name: CreateWebhook :exec
INSERT INTO webhooks (id, agent_id, session_id, name, token_hash, prompt_template, signing_secret, signature_header, enabled, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = $1;

-- name: GetWebhookByToken :one
SELECT * FROM webhooks WHERE token_hash = $1;

-- name: ListAgentWebhooks :many
SELECT * FROM webhooks WHERE agent_id = $1 ORDER BY name;

-- name: UpdateWebhook :exec
UPDATE webhooks SET name = $1, prompt_template = $2, signing_secret = $3, signature_header = $4, enabled = $5, updated_at = $6 WHERE id = $7;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE id = $1;

-- name: RecordWebhookDelivery :exec
UPDATE webhooks SET last_delivery_at = $1, last_error = $2 WHERE id = $3;

-- name: DisableAgentSchedules :exec
UPDATE schedules SET enabled = 0, updated_at = $1 WHERE agent_id = $2 AND enabled = 1;

-- name: DisableAgentWebhooks :exec
UPDATE webhooks SET enabled = 0, updated_at = $1 WHERE agent_id = $2 AND enabled = 1;
