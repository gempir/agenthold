-- name: CreateAgent :exec
INSERT INTO agents (id, owner_id, name, image, harness, model, cpus, memory_mb, idle_minutes, permission_policy, login_user_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: GetAgentWithSandbox :one
SELECT sqlc.embed(agents), sqlc.embed(sandboxes)
FROM agents JOIN sandboxes ON sandboxes.agent_id = agents.id
WHERE agents.id = $1;

-- name: ListUserAgents :many
-- Agents the user owns or that are shared with them.
SELECT sqlc.embed(agents), sqlc.embed(sandboxes)
FROM agents JOIN sandboxes ON sandboxes.agent_id = agents.id
WHERE agents.owner_id = sqlc.arg(user_id)
   OR agents.id IN (SELECT agent_shares.agent_id FROM agent_shares WHERE agent_shares.user_id = sqlc.arg(user_id))
ORDER BY agents.name, agents.id;

-- name: ListAllMachines :many
-- Every machine with its sandbox and owner (the admin overview).
SELECT sqlc.embed(agents), sqlc.embed(sandboxes), sqlc.embed(users)
FROM agents
JOIN sandboxes ON sandboxes.agent_id = agents.id
JOIN users ON users.id = agents.owner_id
ORDER BY agents.name, agents.id;

-- name: SetSandboxSubnet :exec
UPDATE sandboxes SET subnet = $1 WHERE agent_id = $2;

-- name: ListSandboxSubnets :many
SELECT agent_id, subnet FROM sandboxes WHERE subnet != '';

-- name: UpdateAgent :exec
UPDATE agents SET name = $1, image = $2, cpus = $3, memory_mb = $4, idle_minutes = $5, updated_at = $6
WHERE id = $7;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = $1;

-- name: CreateSandbox :exec
INSERT INTO sandboxes (agent_id, node_id, driver, state, state_changed_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE agent_id = $1;

-- name: ListSandboxes :many
SELECT * FROM sandboxes ORDER BY agent_id;

-- name: TransitionSandbox :execrows
-- Moves a sandbox to a new state only if it is currently in one of the allowed states.
UPDATE sandboxes SET state = sqlc.arg(to_state), error = sqlc.arg(error), state_changed_at = sqlc.arg(now)
WHERE agent_id = sqlc.arg(agent_id) AND state IN (SELECT jsonb_array_elements_text(sqlc.arg(from_states)::jsonb));

-- name: SetSandboxState :exec
UPDATE sandboxes SET state = $1, error = $2, state_changed_at = $3 WHERE agent_id = $4;

-- name: SetSandboxBuilt :exec
UPDATE sandboxes SET driver_id = $1, applied_spec = $2 WHERE agent_id = $3;

-- name: SetBootToken :exec
UPDATE sandboxes SET boot_token_hash = $1 WHERE agent_id = $2;

-- name: CreateClonedAgent :exec
-- A clone keeps the settings of its source, including whether its disks hold harness credentials.
INSERT INTO agents (id, owner_id, name, image, harness, model, cpus, memory_mb, idle_minutes, permission_policy, credentials_in_sandbox, login_user_id, is_template, cloned_from, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: SetAgentTemplate :exec
UPDATE agents SET is_template = $1, updated_at = $2 WHERE id = $3;

-- name: SetSandboxResume :exec
UPDATE sandboxes SET resume = $1 WHERE agent_id = $2;
