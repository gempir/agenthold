-- +goose Up
DROP TABLE IF EXISTS project_tasks;

-- +goose Down
CREATE TABLE project_tasks (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'open',
    agent_id   TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    note       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);
CREATE INDEX project_tasks_project ON project_tasks(project_id);

