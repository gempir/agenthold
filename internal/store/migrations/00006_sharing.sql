-- +goose Up
-- Who besides the owner and workspace admins may use an agent.
CREATE TABLE agent_grants (
    id           TEXT PRIMARY KEY,
    agent_id     TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- 'user': one workspace member; 'workspace': every member of the agent's workspace.
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'workspace')),
    subject_id   TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    created_by   TEXT NOT NULL REFERENCES users(id),
    created_at   INTEGER NOT NULL,
    UNIQUE (agent_id, subject_type, subject_id)
);
CREATE INDEX agent_grants_subject ON agent_grants(subject_type, subject_id);

CREATE TABLE audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           INTEGER NOT NULL, -- unix milliseconds
    workspace_id TEXT NOT NULL,
    agent_id     TEXT NOT NULL DEFAULT '',
    user_id      TEXT NOT NULL,
    action       TEXT NOT NULL,
    target       TEXT NOT NULL DEFAULT '',
    details      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_agent ON audit_log(agent_id, id);
CREATE INDEX audit_log_workspace ON audit_log(workspace_id, id);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE agent_grants;
