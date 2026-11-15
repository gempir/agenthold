-- name: CreateMCPServer :exec
INSERT INTO mcp_servers (id, owner_id, agent_id, name, transport, command, args, env, url, headers, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = ?;

-- name: UpdateMCPServer :exec
UPDATE mcp_servers SET name = ?, transport = ?, command = ?, args = ?, env = ?, url = ?, headers = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = ?;

-- name: ListUserMCPServers :many
-- The user's library (servers not bound to one agent).
SELECT * FROM mcp_servers WHERE owner_id = ? AND agent_id = '' ORDER BY name;

-- name: ListAgentOwnMCPServers :many
SELECT * FROM mcp_servers WHERE agent_id = ? ORDER BY name;

-- name: ListAgentMCPServers :many
-- Everything the agent uses: its own servers and attached library servers.
SELECT * FROM mcp_servers WHERE mcp_servers.agent_id = sqlc.arg(agent_id)
   OR id IN (SELECT server_id FROM agent_mcp_servers WHERE agent_mcp_servers.agent_id = sqlc.arg(agent_id))
ORDER BY name;

-- name: AttachMCPServer :exec
INSERT INTO agent_mcp_servers (agent_id, server_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: DetachMCPServer :execrows
DELETE FROM agent_mcp_servers WHERE agent_id = ? AND server_id = ?;

-- name: CreateSkill :exec
INSERT INTO skills (id, owner_id, name, description, content, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetSkill :one
SELECT * FROM skills WHERE id = ?;

-- name: UpdateSkill :exec
UPDATE skills SET name = ?, description = ?, content = ?, updated_at = ? WHERE id = ?;

-- name: DeleteSkill :exec
DELETE FROM skills WHERE id = ?;

-- name: ListUserSkills :many
SELECT * FROM skills WHERE owner_id = ? ORDER BY name;

-- name: ListAgentSkills :many
SELECT skills.* FROM skills JOIN agent_skills ON agent_skills.skill_id = skills.id
WHERE agent_skills.agent_id = ? ORDER BY skills.name;

-- name: AttachSkill :exec
INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: DetachSkill :execrows
DELETE FROM agent_skills WHERE agent_id = ? AND skill_id = ?;
