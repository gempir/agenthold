-- name: CreateMCPServer :exec
INSERT INTO mcp_servers (id, owner_id, agent_id, name, transport, command, args, env, url, headers, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = $1;

-- name: UpdateMCPServer :exec
UPDATE mcp_servers SET name = $1, transport = $2, command = $3, args = $4, env = $5, url = $6, headers = $7, updated_at = $8
WHERE id = $9;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = $1;

-- name: ListUserMCPServers :many
-- The user's library (servers not bound to one agent).
SELECT * FROM mcp_servers WHERE owner_id = $1 AND agent_id = '' ORDER BY name;

-- name: ListAgentOwnMCPServers :many
SELECT * FROM mcp_servers WHERE agent_id = $1 ORDER BY name;

-- name: ListAgentMCPServers :many
-- Everything the agent uses: its own servers and attached library servers.
SELECT * FROM mcp_servers WHERE mcp_servers.agent_id = sqlc.arg(agent_id)
   OR id IN (SELECT server_id FROM agent_mcp_servers WHERE agent_mcp_servers.agent_id = sqlc.arg(agent_id))
ORDER BY name;

-- name: AttachMCPServer :exec
INSERT INTO agent_mcp_servers (agent_id, server_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DetachMCPServer :execrows
DELETE FROM agent_mcp_servers WHERE agent_id = $1 AND server_id = $2;

-- name: CreateSkill :exec
INSERT INTO skills (id, owner_id, name, description, content, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetSkill :one
SELECT * FROM skills WHERE id = $1;

-- name: UpdateSkill :exec
UPDATE skills SET name = $1, description = $2, content = $3, updated_at = $4 WHERE id = $5;

-- name: DeleteSkill :exec
DELETE FROM skills WHERE id = $1;

-- name: ListUserSkills :many
SELECT * FROM skills WHERE owner_id = $1 ORDER BY name;

-- name: ListAgentSkills :many
SELECT skills.* FROM skills JOIN agent_skills ON agent_skills.skill_id = skills.id
WHERE agent_skills.agent_id = $1 ORDER BY skills.name;

-- name: AttachSkill :exec
INSERT INTO agent_skills (agent_id, skill_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DetachSkill :execrows
DELETE FROM agent_skills WHERE agent_id = $1 AND skill_id = $2;
