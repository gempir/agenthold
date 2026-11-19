-- +goose Up
-- What the controller last learned about a node, kept so an offline node still
-- shows it: the agenthold release it runs (from its handshake), its installed
-- memory and the disk its machines' files occupy (st_blocks, from its capacity
-- report). 0 or '' means never reported (nodes of earlier releases do not).
ALTER TABLE nodes ADD COLUMN version TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN memory_total_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN disk_allocated_bytes INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN disk_allocated_bytes;
ALTER TABLE nodes DROP COLUMN memory_total_bytes;
ALTER TABLE nodes DROP COLUMN version;
