-- +goose Up
ALTER TABLE agent_sessions ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN git_project TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN git_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN git_repo_path TEXT NOT NULL DEFAULT '';

-- Account-level git commit identity. Empty fields fall back to the user's
-- agenthold profile name and email.
CREATE TABLE user_git_configs (
    user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    author_name  TEXT NOT NULL DEFAULT '',
    author_email TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL
);

-- HTTPS credentials for git hosts (github.com, gitlab.com, self-hosted).
-- Sealed with the master key (aad = id); injected by the egress proxy so tokens
-- never enter sandboxes.
CREATE TABLE git_credentials (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host       TEXT NOT NULL,
    username   TEXT NOT NULL DEFAULT '',
    ciphertext BLOB NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (user_id, host)
);
CREATE INDEX git_credentials_user ON git_credentials(user_id);

-- +goose Down
DROP TABLE git_credentials;
DROP TABLE user_git_configs;
ALTER TABLE agent_sessions DROP COLUMN git_repo_path;
ALTER TABLE agent_sessions DROP COLUMN git_branch;
ALTER TABLE agent_sessions DROP COLUMN git_project;
ALTER TABLE agent_sessions DROP COLUMN cwd;
