-- name: CreateOrgTool :exec
INSERT INTO org_tools (
    id, slug, name, description, kind, auth_mode, config_json,
    encrypted_secret, secret_hosts_json, enabled_by_default,
    allowed_roles_json, created_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOrgTool :one
SELECT * FROM org_tools WHERE id = ?;

-- name: GetOrgToolBySlug :one
SELECT * FROM org_tools WHERE slug = ?;

-- name: ListOrgTools :many
SELECT * FROM org_tools ORDER BY name, slug;

-- name: UpdateOrgTool :exec
UPDATE org_tools
SET slug = ?, name = ?, description = ?, kind = ?, auth_mode = ?,
    config_json = ?, encrypted_secret = ?, secret_hosts_json = ?,
    enabled_by_default = ?, allowed_roles_json = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteOrgTool :execrows
DELETE FROM org_tools WHERE id = ?;

-- name: ListUserToolPreferences :many
SELECT * FROM user_tool_preferences WHERE user_id = ?;

-- name: UpsertUserToolPreference :exec
INSERT INTO user_tool_preferences (user_id, org_tool_id, enabled)
VALUES (?, ?, ?)
ON CONFLICT (user_id, org_tool_id) DO UPDATE SET enabled = excluded.enabled;

-- name: DeleteUserToolPreference :execrows
DELETE FROM user_tool_preferences WHERE user_id = ? AND org_tool_id = ?;

-- name: CreateOrgSkill :exec
INSERT INTO org_skills (id, name, description, content, enabled_by_default, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetOrgSkill :one
SELECT * FROM org_skills WHERE id = ?;

-- name: GetOrgSkillByName :one
SELECT * FROM org_skills WHERE name = ?;

-- name: ListOrgSkills :many
SELECT * FROM org_skills ORDER BY name;

-- name: ListDefaultOrgSkills :many
SELECT * FROM org_skills WHERE enabled_by_default = 1 ORDER BY name;

-- name: UpdateOrgSkill :exec
UPDATE org_skills
SET name = ?, description = ?, content = ?, enabled_by_default = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteOrgSkill :execrows
DELETE FROM org_skills WHERE id = ?;

-- name: UpsertOrgProviderLogin :exec
INSERT INTO org_provider_logins (provider, kind, label, encrypted_payload, enabled, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider) DO UPDATE SET
    kind = excluded.kind,
    label = excluded.label,
    encrypted_payload = excluded.encrypted_payload,
    enabled = excluded.enabled,
    updated_by = excluded.updated_by,
    updated_at = excluded.updated_at;

-- name: GetOrgProviderLogin :one
SELECT * FROM org_provider_logins WHERE provider = ?;

-- name: ListOrgProviderLogins :many
SELECT * FROM org_provider_logins ORDER BY provider;

-- name: DeleteOrgProviderLogin :execrows
DELETE FROM org_provider_logins WHERE provider = ?;
