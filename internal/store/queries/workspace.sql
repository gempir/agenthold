-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, name, personal, created_at, updated_at) VALUES (?, ?, ?, ?, ?);

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = ?;

-- name: RenameWorkspace :exec
UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?;

-- name: DeleteWorkspace :exec
DELETE FROM workspaces WHERE id = ?;

-- name: SetPersonalWorkspace :exec
INSERT INTO personal_workspaces (user_id, workspace_id) VALUES (?, ?);

-- name: GetPersonalWorkspaceID :one
SELECT workspace_id FROM personal_workspaces WHERE user_id = ?;

-- name: ListUserWorkspaces :many
SELECT sqlc.embed(workspaces), memberships.role
FROM memberships JOIN workspaces ON workspaces.id = memberships.workspace_id
WHERE memberships.user_id = ?
ORDER BY workspaces.personal DESC, workspaces.name;

-- name: UpsertMembership :exec
INSERT INTO memberships (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = excluded.role;

-- name: GetMembership :one
SELECT * FROM memberships WHERE workspace_id = ? AND user_id = ?;

-- name: ListMembers :many
SELECT sqlc.embed(users), memberships.role, memberships.created_at AS joined_at
FROM memberships JOIN users ON users.id = memberships.user_id
WHERE memberships.workspace_id = ?
ORDER BY users.name, users.email;

-- name: CountOwners :one
SELECT COUNT(*) FROM memberships WHERE workspace_id = ? AND role = 'owner';

-- name: DeleteMembership :exec
DELETE FROM memberships WHERE workspace_id = ? AND user_id = ?;

-- name: CreateInvite :exec
INSERT INTO invites (id, workspace_id, token_hash, role, email, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPendingInvites :many
SELECT * FROM invites
WHERE workspace_id = ? AND accepted_at IS NULL AND expires_at > ?
ORDER BY created_at DESC;

-- name: GetInviteByToken :one
SELECT * FROM invites WHERE token_hash = ?;

-- name: DeleteInvite :execrows
DELETE FROM invites WHERE id = ? AND workspace_id = ?;

-- name: MarkInviteAccepted :execrows
UPDATE invites SET accepted_by = ?, accepted_at = ?
WHERE id = ? AND accepted_at IS NULL;
