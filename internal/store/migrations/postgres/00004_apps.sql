-- +goose Up
-- kind: what runs in the machine. 'agent' runs a harness; 'app' runs no
-- harness and exists to run services reached through exposures.
-- created_by_agent_id: the agent that created an app ('' when a user did).
-- No foreign key: deleting that agent keeps the app.
ALTER TABLE agents ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'app'));
ALTER TABLE agents ADD COLUMN created_by_agent_id TEXT NOT NULL DEFAULT '';
CREATE INDEX agents_created_by_agent ON agents(created_by_agent_id);

-- A service is a long-running command sandboxd supervises in a machine.
-- command runs with the login shell ("sh -lc") as the agent account in
-- workdir (relative to the home; '' is the home). env is a JSON object of
-- plain values: secrets reach a service through placeholders like every
-- other process, never through this table.
CREATE TABLE services (
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    command    TEXT NOT NULL,
    workdir    TEXT NOT NULL DEFAULT '',
    env        TEXT NOT NULL DEFAULT '{}',
    enabled    BIGINT NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id, name)
);

-- +goose Down
DROP TABLE services;
DROP INDEX agents_created_by_agent;
ALTER TABLE agents DROP COLUMN created_by_agent_id;
ALTER TABLE agents DROP COLUMN kind;
