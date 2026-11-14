-- name: UpsertGrant :exec
INSERT INTO agent_grants (id, agent_id, subject_type, subject_id, role, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, subject_type, subject_id) DO UPDATE SET role = excluded.role;

-- name: ListAgentGrants :many
SELECT * FROM agent_grants WHERE agent_id = ? ORDER BY subject_type DESC, created_at;

-- name: GrantRolesFor :many
-- The roles a user holds on an agent through grants.
SELECT role FROM agent_grants
WHERE agent_id = ? AND (subject_type = 'workspace' OR (subject_type = 'user' AND subject_id = ?));

-- name: GetGrant :one
SELECT * FROM agent_grants WHERE id = ? AND agent_id = ?;

-- name: DeleteGrant :execrows
DELETE FROM agent_grants WHERE id = ? AND agent_id = ?;

-- name: DeleteUserGrantsInWorkspace :exec
-- When someone leaves a workspace, their grants on its agents go too.
DELETE FROM agent_grants WHERE subject_type = 'user' AND subject_id = sqlc.arg(user_id)
AND agent_id IN (SELECT id FROM agents WHERE workspace_id = sqlc.arg(workspace_id));

-- name: InsertAudit :exec
INSERT INTO audit_log (at, workspace_id, agent_id, user_id, action, target, details) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListAgentAudit :many
SELECT * FROM audit_log WHERE agent_id = ? AND id < ? ORDER BY id DESC LIMIT ?;

-- name: ListWorkspaceAudit :many
SELECT * FROM audit_log WHERE workspace_id = ? AND id < ? ORDER BY id DESC LIMIT ?;
