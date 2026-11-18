-- +goose Up
-- model_options: the agent's default model options as a JSON object of ACP
-- session config option ids to values ({"effort": "high"}), next to model.
ALTER TABLE agents ADD COLUMN model_options TEXT NOT NULL DEFAULT '{}';
-- A session's own model choice; '' and '{}' mean the agent's default.
ALTER TABLE agent_sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN model_options TEXT NOT NULL DEFAULT '{}';

-- The models and options an agent's harness last reported (its ACP session
-- config options, as JSON), so the picker works while the machine sleeps.
-- harness: the harness that reported them (a harness change makes them stale).
CREATE TABLE agent_model_catalogs (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    harness  TEXT NOT NULL,
    data     TEXT NOT NULL,
    seen_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE agent_model_catalogs;
ALTER TABLE agent_sessions DROP COLUMN model_options;
ALTER TABLE agent_sessions DROP COLUMN model;
ALTER TABLE agents DROP COLUMN model_options;
