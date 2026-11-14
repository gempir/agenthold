-- +goose Up
-- SHA-256 of the token the current boot of the sandbox uses to dial the tunnel.
ALTER TABLE sandboxes ADD COLUMN boot_token_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sandboxes DROP COLUMN boot_token_hash;
