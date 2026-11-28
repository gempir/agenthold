-- +goose Up
-- Phase 2A: Organization-provided MCP & tools, org skills, org shared provider
-- logins, and per-user org tool preferences.
-- Timestamps are unix seconds. Booleans are INTEGER 0/1.

CREATE TABLE org_tools (
    id                 TEXT PRIMARY KEY,
    slug               TEXT NOT NULL UNIQUE,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    -- 'mcp_http' | 'mcp_stdio' | 'executor_app'
    kind               TEXT NOT NULL,
    -- 'none' | 'org_shared' | 'user_oauth'
    auth_mode          TEXT NOT NULL DEFAULT 'none',
    config_json        TEXT NOT NULL DEFAULT '{}',
    -- XChaCha20-Poly1305(master key, secret, aad = "org-tool:" + id); NULL when unset.
    encrypted_secret   BLOB,
    secret_hosts_json  TEXT NOT NULL DEFAULT '[]',
    enabled_by_default INTEGER NOT NULL DEFAULT 1,
    allowed_roles_json TEXT NOT NULL DEFAULT '["admin","user"]',
    created_by         TEXT,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE org_skills (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    description        TEXT NOT NULL,
    content            TEXT NOT NULL,
    enabled_by_default INTEGER NOT NULL DEFAULT 1,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE org_provider_logins (
    -- 'claude-code' | 'codex' | 'antigravity' | 'opencode'
    provider          TEXT PRIMARY KEY,
    -- 'api_key' | 'oauth_token' | 'gateway'
    kind              TEXT NOT NULL,
    label             TEXT NOT NULL DEFAULT '',
    -- XChaCha20-Poly1305(master key, payload json, aad = "org-provider:" + provider)
    encrypted_payload BLOB NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    updated_by        TEXT,
    updated_at        INTEGER NOT NULL
);

CREATE TABLE user_tool_preferences (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_tool_id TEXT NOT NULL REFERENCES org_tools(id) ON DELETE CASCADE,
    enabled     INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (user_id, org_tool_id)
);

CREATE INDEX user_tool_preferences_tool ON user_tool_preferences(org_tool_id);

-- +goose Down
DROP INDEX user_tool_preferences_tool;
DROP TABLE user_tool_preferences;
DROP TABLE org_provider_logins;
DROP TABLE org_skills;
DROP TABLE org_tools;
