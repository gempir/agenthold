-- name: CreateAgent :exec
INSERT INTO agents (id, owner_id, name, image, harness, model, cpus, memory_mb, idle_minutes, permission_policy, login_user_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: GetAgentWithSandbox :one
SELECT sqlc.embed(agents), sqlc.embed(sandboxes)
FROM agents JOIN sandboxes ON sandboxes.agent_id = agents.id
WHERE agents.id = ?;

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
UPDATE sandboxes SET subnet = ? WHERE agent_id = ?;

-- name: ListSandboxSubnets :many
SELECT agent_id, subnet FROM sandboxes WHERE subnet != '';

-- name: UpdateAgent :exec
UPDATE agents SET name = ?, image = ?, cpus = ?, memory_mb = ?, idle_minutes = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;

-- name: CreateSandbox :exec
INSERT INTO sandboxes (agent_id, node_id, driver, state, state_changed_at) VALUES (?, ?, ?, ?, ?);

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE agent_id = ?;

-- name: ListSandboxes :many
SELECT * FROM sandboxes ORDER BY agent_id;

-- name: TransitionSandbox :execrows
-- Moves a sandbox to a new state only if it is currently in one of the allowed states.
UPDATE sandboxes SET state = sqlc.arg(to_state), error = sqlc.arg(error), state_changed_at = sqlc.arg(now)
WHERE agent_id = sqlc.arg(agent_id) AND state IN (sqlc.slice(from_states));

-- name: SetSandboxState :exec
UPDATE sandboxes SET state = ?, error = ?, state_changed_at = ? WHERE agent_id = ?;

-- name: SetSandboxBuilt :exec
UPDATE sandboxes SET driver_id = ?, applied_spec = ? WHERE agent_id = ?;

-- name: SetBootToken :exec
UPDATE sandboxes SET boot_token_hash = ? WHERE agent_id = ?;

-- name: CreateClonedAgent :exec
-- A clone keeps the settings of its source, including whether its disks hold harness credentials.
INSERT INTO agents (id, owner_id, name, image, harness, model, cpus, memory_mb, idle_minutes, permission_policy, credentials_in_sandbox, login_user_id, is_template, cloned_from, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetAgentTemplate :exec
UPDATE agents SET is_template = ?, updated_at = ? WHERE id = ?;

-- name: SetSandboxResume :exec
UPDATE sandboxes SET resume = ? WHERE agent_id = ?;

-- name: SetAgentExecutor :exec
UPDATE agents SET executor_enabled = ?, executor_approval_mode = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentExecutorEnabled :exec
UPDATE agents SET executor_enabled = ?, updated_at = ? WHERE id = ?;

-- name: SetAgentExecutorApprovalMode :exec
UPDATE agents SET executor_approval_mode = ?, updated_at = ? WHERE id = ?;
