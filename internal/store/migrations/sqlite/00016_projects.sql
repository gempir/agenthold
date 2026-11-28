-- +goose Up
-- A project groups the repositories and folders a user's agents work on.
-- slug names its directory on agents' disks (projects/<slug>); instructions
-- are written into every workspace of the project for the agents to read.
CREATE TABLE projects (
    id           TEXT PRIMARY KEY,
    owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    slug         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    instructions TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (owner_id, slug)
);
CREATE INDEX projects_owner ON projects(owner_id);

-- The repositories (kind 'repo': cloned from url) and folders (kind
-- 'folder': created empty, or the existing directory path) of a project.
-- name is the source's directory in workspaces. path, when set, is where
-- the main checkout lives on an agent's disk instead of projects/<slug>/<name>.
CREATE TABLE project_sources (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL,
    url            TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL DEFAULT '',
    default_branch TEXT NOT NULL DEFAULT '',
    position       INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    UNIQUE (project_id, name)
);

-- Work items of a project. agent_id is the assignee ('' none), session_id
-- the thread working on it ('' none). status: open, in_progress, review,
-- done, cancelled. created_by is a user id, or 'agent:<id>'.
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
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX project_tasks_project ON project_tasks(project_id);

-- A thread of a project works in a workspace of it (cwd). project_branch is
-- the branch the thread's worktrees are on; '' when it works directly in the
-- main checkouts.
ALTER TABLE agent_sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN project_branch TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN project_branch;
ALTER TABLE agent_sessions DROP COLUMN project_id;
DROP TABLE project_tasks;
DROP TABLE project_sources;
DROP TABLE projects;
