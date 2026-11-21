-- +goose Up
-- Enterprise custom images: declarative recipes built inside isolated microVMs
-- into read-only ext4 layers stacked above the base image.
CREATE TABLE images (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    description  TEXT NOT NULL DEFAULT '',
    apt_packages TEXT NOT NULL DEFAULT '[]',
    setup_script TEXT NOT NULL DEFAULT '',
    env_json     TEXT NOT NULL DEFAULT '{}',
    secret_ids   TEXT NOT NULL DEFAULT '[]',
    is_default   INTEGER NOT NULL DEFAULT 0,
    latest_rev   INTEGER NOT NULL DEFAULT 0,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE image_builds (
    id           TEXT PRIMARY KEY,
    image_id     TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    revision     INTEGER NOT NULL,
    arch         TEXT NOT NULL,
    node_id      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL CHECK (status IN ('queued', 'building', 'ready', 'failed')),
    error        TEXT NOT NULL DEFAULT '',
    recipe_json  TEXT NOT NULL DEFAULT '{}',
    base_key     TEXT NOT NULL DEFAULT '',
    layer_sha256 TEXT NOT NULL DEFAULT '',
    layer_size   INTEGER NOT NULL DEFAULT 0,
    zstd_sha256  TEXT NOT NULL DEFAULT '',
    zstd_size    INTEGER NOT NULL DEFAULT 0,
    log_text     TEXT NOT NULL DEFAULT '',
    started_at   INTEGER NOT NULL DEFAULT 0,
    finished_at  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    UNIQUE (image_id, revision, arch)
);
CREATE INDEX image_builds_image ON image_builds(image_id, revision DESC);

ALTER TABLE agents ADD COLUMN image_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN image_revision INTEGER NOT NULL DEFAULT 0;
CREATE INDEX agents_image_id ON agents(image_id);

ALTER TABLE nodes ADD COLUMN arch TEXT NOT NULL DEFAULT '';

-- +goose Down
DROP INDEX agents_image_id;
ALTER TABLE nodes DROP COLUMN arch;
ALTER TABLE agents DROP COLUMN image_revision;
ALTER TABLE agents DROP COLUMN image_id;
DROP INDEX image_builds_image;
DROP TABLE image_builds;
DROP TABLE images;
