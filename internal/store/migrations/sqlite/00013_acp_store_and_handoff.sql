-- +goose Up
-- Portable context handoff tracking on sessions, plus user-configured ACP
-- Registry and local ACP command harnesses.
ALTER TABLE agent_sessions ADD COLUMN acp_harness TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN handoff_seq INTEGER NOT NULL DEFAULT 0;

CREATE TABLE custom_harnesses (
    id                 TEXT PRIMARY KEY,
    owner_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL CHECK (kind IN ('registry', 'local')),
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    registry_id        TEXT NOT NULL DEFAULT '',
    registry_version   TEXT NOT NULL DEFAULT '',
    distribution       TEXT NOT NULL DEFAULT '',
    command            TEXT NOT NULL DEFAULT '',
    args_json          TEXT NOT NULL DEFAULT '[]',
    env_json           TEXT NOT NULL DEFAULT '{}',
    icon_url           TEXT NOT NULL DEFAULT '',
    website_url        TEXT NOT NULL DEFAULT '',
    repository_url     TEXT NOT NULL DEFAULT '',
    custom_models_json TEXT NOT NULL DEFAULT '[]',
    auth_method        TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX custom_harnesses_owner ON custom_harnesses(owner_id);

-- +goose Down
DROP INDEX custom_harnesses_owner;
DROP TABLE custom_harnesses;
ALTER TABLE agent_sessions DROP COLUMN handoff_seq;
ALTER TABLE agent_sessions DROP COLUMN acp_harness;
