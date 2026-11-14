-- name: SetSandboxSubnet :exec
UPDATE sandboxes SET subnet = ? WHERE agent_id = ?;

-- name: ListSandboxSubnets :many
SELECT agent_id, subnet FROM sandboxes WHERE subnet != '';

-- name: SetNetworkPolicy :exec
UPDATE agents SET network_policy = ?, updated_at = ? WHERE id = ?;

-- name: CreateSecret :exec
INSERT INTO secrets (id, workspace_id, owner_id, name, ciphertext, rule, placeholder, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = ?;

-- name: ListWorkspaceSecrets :many
SELECT * FROM secrets WHERE workspace_id = ? ORDER BY name;

-- name: UpdateSecret :exec
UPDATE secrets SET name = ?, ciphertext = ?, rule = ?, updated_at = ? WHERE id = ?;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = ?;

-- name: AttachSecret :exec
INSERT INTO agent_secrets (agent_id, secret_id, env_var, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, secret_id) DO UPDATE SET env_var = excluded.env_var;

-- name: DetachSecret :execrows
DELETE FROM agent_secrets WHERE agent_id = ? AND secret_id = ?;

-- name: ListAgentSecrets :many
SELECT sqlc.embed(secrets), agent_secrets.env_var
FROM agent_secrets JOIN secrets ON secrets.id = agent_secrets.secret_id
WHERE agent_secrets.agent_id = ?
ORDER BY secrets.name;

-- name: InsertEgressEvent :exec
INSERT INTO egress_events (agent_id, at, kind, host, ip, port, method, path, status, allowed, reason, injected, bytes_up, bytes_down, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListEgressEvents :many
SELECT * FROM egress_events WHERE agent_id = ? AND id < ? ORDER BY id DESC LIMIT ?;

-- name: PruneEgressEvents :exec
-- Keeps the newest N events of an agent.
DELETE FROM egress_events WHERE egress_events.agent_id = sqlc.arg(agent_id) AND egress_events.id <= (
    SELECT e.id FROM egress_events AS e WHERE e.agent_id = sqlc.arg(agent_id) ORDER BY e.id DESC LIMIT 1 OFFSET sqlc.arg(keep)
);
