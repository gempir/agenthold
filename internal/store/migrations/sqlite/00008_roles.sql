-- +goose Up
-- role: the role sign-in configuration gives the user (admin, user or
-- disabled), evaluated at every sign-in and again at every server start.
-- role_override: an admin's choice in the Overview; '' follows role.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'disabled';
ALTER TABLE users ADD COLUMN role_override TEXT NOT NULL DEFAULT '';
-- What the identity's provider mapping said at its last sign-in ('' = no
-- match), so a server start can re-evaluate roles without the claims.
ALTER TABLE identities ADD COLUMN provider_role TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE identities DROP COLUMN provider_role;
ALTER TABLE users DROP COLUMN role_override;
ALTER TABLE users DROP COLUMN role;
