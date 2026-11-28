-- name: CreateUsageRecord :exec
INSERT INTO usage_records (
    id, user_id, agent_id, session_id, harness, model,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListUsageRecordsSince :many
SELECT * FROM usage_records
WHERE created_at >= ?
ORDER BY created_at ASC;

-- name: ListUserUsageRecordsSince :many
SELECT * FROM usage_records
WHERE user_id = ? AND created_at >= ?
ORDER BY created_at ASC;

-- name: ListModelPrices :many
SELECT * FROM model_prices ORDER BY model ASC;

-- name: GetModelPrice :one
SELECT * FROM model_prices WHERE model = ?;

-- name: UpsertModelPrice :exec
INSERT INTO model_prices (model, input_per_m, output_per_m, cache_read_per_m, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (model) DO UPDATE SET
    input_per_m = excluded.input_per_m,
    output_per_m = excluded.output_per_m,
    cache_read_per_m = excluded.cache_read_per_m,
    updated_at = excluded.updated_at;

-- name: DeleteModelPrice :execrows
DELETE FROM model_prices WHERE model = ?;
