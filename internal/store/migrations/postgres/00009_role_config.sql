-- +goose Up
-- role_config: a hash of the provider's role configuration
-- (role_attribute_path) that provider_role was computed with. When it
-- changes, a start discards provider_role and signs the user out, so the
-- next sign-in evaluates the new configuration.
ALTER TABLE identities ADD COLUMN role_config TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE identities DROP COLUMN role_config;
