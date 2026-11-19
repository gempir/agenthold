-- name: UpsertLocalNode :exec
INSERT INTO nodes (id, name, state, created_at, last_seen_at) VALUES ('local', sqlc.arg(name), 'online', sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (id) DO UPDATE SET name = excluded.name;

-- name: CreateNode :exec
INSERT INTO nodes (id, name, cert_fingerprint, state, created_at, last_seen_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(cert_fingerprint), 'offline', sqlc.arg(created_at), sqlc.arg(created_at));

-- name: GetNode :one
SELECT * FROM nodes WHERE id = sqlc.arg(id);

-- name: GetNodeByFingerprint :one
SELECT * FROM nodes WHERE cert_fingerprint = sqlc.arg(cert_fingerprint) AND cert_fingerprint != '';

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY name, id;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = sqlc.arg(id);

-- name: SetNodeState :exec
UPDATE nodes SET state = sqlc.arg(state), last_seen_at = sqlc.arg(last_seen_at) WHERE id = sqlc.arg(id);

-- name: SetNodeCapacity :exec
UPDATE nodes SET cpus = sqlc.arg(cpus), memory_bytes = sqlc.arg(memory_bytes), disk_bytes = sqlc.arg(disk_bytes),
    memory_total_bytes = sqlc.arg(memory_total_bytes), disk_allocated_bytes = sqlc.arg(disk_allocated_bytes), last_seen_at = sqlc.arg(last_seen_at)
WHERE id = sqlc.arg(id);

-- name: SetNodeVersion :exec
UPDATE nodes SET version = sqlc.arg(version) WHERE id = sqlc.arg(id);

-- name: SetRemoteNodesOffline :exec
-- At controller start no edge node is connected yet.
UPDATE nodes SET state = 'offline' WHERE id != 'local';

-- name: CountNodeSandboxes :one
SELECT COUNT(*) FROM sandboxes WHERE node_id = sqlc.arg(node_id);

-- name: ListNodeSandboxes :many
SELECT * FROM sandboxes WHERE node_id = sqlc.arg(node_id) ORDER BY agent_id;

-- name: GetSandboxByDriverID :one
SELECT * FROM sandboxes WHERE driver_id = sqlc.arg(driver_id) AND driver_id != '' LIMIT 1;

-- name: CreateNodeJoinToken :exec
INSERT INTO node_join_tokens (token_hash, name, created_by, created_at, expires_at)
VALUES (sqlc.arg(token_hash), sqlc.arg(name), sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(expires_at));

-- name: ConsumeNodeJoinToken :one
-- Single use: only one caller can move used_at away from zero.
UPDATE node_join_tokens SET used_at = sqlc.arg(now)
WHERE token_hash = sqlc.arg(token_hash) AND used_at = 0 AND expires_at > sqlc.arg(now)
RETURNING name;

-- name: DeleteStaleNodeJoinTokens :exec
DELETE FROM node_join_tokens WHERE expires_at <= sqlc.arg(before);

-- name: CountUserIdentities :one
SELECT COUNT(*) FROM identities WHERE user_id = sqlc.arg(user_id) AND provider = sqlc.arg(provider);
