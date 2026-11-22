-- name: CreateCustomHarness :exec
INSERT INTO custom_harnesses (
    id, owner_id, kind, name, description, registry_id, registry_version,
    distribution, command, args_json, env_json, icon_url, website_url,
    repository_url, custom_models_json, auth_method, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18);

-- name: GetCustomHarness :one
SELECT * FROM custom_harnesses WHERE id = $1;

-- name: ListCustomHarnessesByOwner :many
SELECT * FROM custom_harnesses WHERE owner_id = $1 ORDER BY name, id;

-- name: UpdateCustomHarness :exec
UPDATE custom_harnesses
SET name = $1, description = $2, registry_version = $3, distribution = $4,
    command = $5, args_json = $6, env_json = $7, icon_url = $8, website_url = $9,
    repository_url = $10, custom_models_json = $11, auth_method = $12, updated_at = $13
WHERE id = $14 AND owner_id = $15;

-- name: DeleteCustomHarness :execrows
DELETE FROM custom_harnesses WHERE id = $1 AND owner_id = $2;

-- name: CountAgentsByHarness :one
SELECT COUNT(*) FROM agents WHERE harness = $1;
