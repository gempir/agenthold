-- +goose Up
ALTER TABLE agents ADD COLUMN permission_policy TEXT NOT NULL DEFAULT 'auto-approve';
-- Set once a harness login completed in the agent's terminal: the agent can read its own credentials.
ALTER TABLE agents ADD COLUMN credentials_in_sandbox INTEGER NOT NULL DEFAULT 0;

CREATE TABLE agent_sessions (
    id             TEXT PRIMARY KEY,
    agent_id       TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- The harness's session id ('' until first used).
    acp_session_id TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'running')),
    created_by     TEXT NOT NULL REFERENCES users(id),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX agent_sessions_agent ON agent_sessions(agent_id, updated_at);

-- Append-only event log of a session.
CREATE TABLE agent_session_events (
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    at         INTEGER NOT NULL, -- unix milliseconds
    type       TEXT NOT NULL,
    user_id    TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL,
    PRIMARY KEY (session_id, seq)
);

-- +goose Down
DROP TABLE agent_session_events;
DROP TABLE agent_sessions;
ALTER TABLE agents DROP COLUMN credentials_in_sandbox;
ALTER TABLE agents DROP COLUMN permission_policy;
