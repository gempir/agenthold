-- +goose Up
-- MCP servers available to agents. agent_id = '' means workspace level (attach
-- per agent); otherwise the server belongs to that agent alone.
CREATE TABLE mcp_servers (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL,
    transport    TEXT NOT NULL CHECK (transport IN ('stdio', 'http')),
    command      TEXT NOT NULL DEFAULT '',
    args         TEXT NOT NULL DEFAULT '[]', -- JSON array
    env          TEXT NOT NULL DEFAULT '{}', -- JSON object
    url          TEXT NOT NULL DEFAULT '',
    headers      TEXT NOT NULL DEFAULT '{}', -- JSON object
    created_by   TEXT NOT NULL REFERENCES users(id),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, agent_id, name)
);

CREATE TABLE agent_mcp_servers (
    agent_id  TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    server_id TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, server_id)
);

-- The workspace skills library (Agent Skills: a SKILL.md per skill).
CREATE TABLE skills (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL, -- directory name: lowercase letters, digits, dashes
    description  TEXT NOT NULL,
    content      TEXT NOT NULL, -- SKILL.md body (without front matter)
    created_by   TEXT NOT NULL REFERENCES users(id),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, name)
);

CREATE TABLE agent_skills (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, skill_id)
);

-- +goose Down
DROP TABLE agent_skills;
DROP TABLE skills;
DROP TABLE agent_mcp_servers;
DROP TABLE mcp_servers;
