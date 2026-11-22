-- name: CreateCustomHarness :exec
INSERT INTO custom_harnesses (
    id, owner_id, kind, name, description, registry_id, registry_version,
    distribution, command, args_json, env_json, icon_url, website_url,
    repository_url, custom_models_json, auth_method, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCustomHarness :one
SELECT * FROM custom_harnesses WHERE id = ?;

-- name: ListCustomHarnessesByOwner :many
SELECT * FROM custom_harnesses WHERE owner_id = ? ORDER BY name, id;

-- name: UpdateCustomHarness :exec
UPDATE custom_harnesses
SET name = ?, description = ?, registry_version = ?, distribution = ?,
    command = ?, args_json = ?, env_json = ?, icon_url = ?, website_url = ?,
    repository_url = ?, custom_models_json = ?, auth_method = ?, updated_at = ?
WHERE id = ? AND owner_id = ?;

-- name: DeleteCustomHarness :execrows
DELETE FROM custom_harnesses WHERE id = ? AND owner_id = ?;

-- name: CountAgentsByHarness :one
SELECT COUNT(*) FROM agents WHERE harness = ?;
