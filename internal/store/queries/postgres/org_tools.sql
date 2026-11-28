-- name: CreateOrgTool :exec
INSERT INTO org_tools (
    id, slug, name, description, kind, auth_mode, config_json,
    encrypted_secret, secret_hosts_json, enabled_by_default,
    allowed_roles_json, created_by, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: GetOrgTool :one
SELECT * FROM org_tools WHERE id = $1;

-- name: GetOrgToolBySlug :one
SELECT * FROM org_tools WHERE slug = $1;

-- name: ListOrgTools :many
SELECT * FROM org_tools ORDER BY name, slug;

-- name: UpdateOrgTool :exec
UPDATE org_tools
SET slug = $1, name = $2, description = $3, kind = $4, auth_mode = $5,
    config_json = $6, encrypted_secret = $7, secret_hosts_json = $8,
    enabled_by_default = $9, allowed_roles_json = $10, updated_at = $11
WHERE id = $12;

-- name: DeleteOrgTool :execrows
DELETE FROM org_tools WHERE id = $1;

-- name: ListUserToolPreferences :many
SELECT * FROM user_tool_preferences WHERE user_id = $1;

-- name: UpsertUserToolPreference :exec
INSERT INTO user_tool_preferences (user_id, org_tool_id, enabled)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, org_tool_id) DO UPDATE SET enabled = excluded.enabled;

-- name: DeleteUserToolPreference :execrows
DELETE FROM user_tool_preferences WHERE user_id = $1 AND org_tool_id = $2;

-- name: CreateOrgSkill :exec
INSERT INTO org_skills (id, name, description, content, enabled_by_default, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetOrgSkill :one
SELECT * FROM org_skills WHERE id = $1;

-- name: GetOrgSkillByName :one
SELECT * FROM org_skills WHERE name = $1;

-- name: ListOrgSkills :many
SELECT * FROM org_skills ORDER BY name;

-- name: ListDefaultOrgSkills :many
SELECT * FROM org_skills WHERE enabled_by_default = 1 ORDER BY name;

-- name: UpdateOrgSkill :exec
UPDATE org_skills
SET name = $1, description = $2, content = $3, enabled_by_default = $4, updated_at = $5
WHERE id = $6;

-- name: DeleteOrgSkill :execrows
DELETE FROM org_skills WHERE id = $1;

-- name: UpsertOrgProviderLogin :exec
INSERT INTO org_provider_logins (provider, kind, label, encrypted_payload, enabled, updated_by, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider) DO UPDATE SET
    kind = excluded.kind,
    label = excluded.label,
    encrypted_payload = excluded.encrypted_payload,
    enabled = excluded.enabled,
    updated_by = excluded.updated_by,
    updated_at = excluded.updated_at;

-- name: GetOrgProviderLogin :one
SELECT * FROM org_provider_logins WHERE provider = $1;

-- name: ListOrgProviderLogins :many
SELECT * FROM org_provider_logins ORDER BY provider;

-- name: DeleteOrgProviderLogin :execrows
DELETE FROM org_provider_logins WHERE provider = $1;
