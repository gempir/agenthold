-- name: CreateImage :exec
INSERT INTO images (id, name, description, apt_packages, setup_script, env_json, secret_ids, is_default, latest_rev, created_by, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(apt_packages), sqlc.arg(setup_script), sqlc.arg(env_json), sqlc.arg(secret_ids), sqlc.arg(is_default), 0, sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: GetImage :one
SELECT * FROM images WHERE id = sqlc.arg(id);

-- name: GetImageByName :one
SELECT * FROM images WHERE name = sqlc.arg(name);

-- name: ListImages :many
SELECT * FROM images ORDER BY is_default DESC, name ASC, id ASC;

-- name: GetDefaultImage :one
SELECT * FROM images WHERE is_default = 1 LIMIT 1;

-- name: UpdateImage :exec
UPDATE images
SET name = sqlc.arg(name), description = sqlc.arg(description), apt_packages = sqlc.arg(apt_packages),
    setup_script = sqlc.arg(setup_script), env_json = sqlc.arg(env_json), secret_ids = sqlc.arg(secret_ids),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: ClearDefaultImages :exec
UPDATE images SET is_default = 0 WHERE is_default != 0;

-- name: SetImageDefault :exec
UPDATE images SET is_default = sqlc.arg(is_default), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: SetImageLatestRev :exec
UPDATE images SET latest_rev = sqlc.arg(latest_rev), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id) AND latest_rev < sqlc.arg(latest_rev);

-- name: DeleteImage :exec
DELETE FROM images WHERE id = sqlc.arg(id);

-- name: CountImageAgents :one
SELECT COUNT(*) FROM agents WHERE image_id = sqlc.arg(image_id);

-- name: SetAgentImage :exec
UPDATE agents SET image_id = sqlc.arg(image_id), image_revision = sqlc.arg(image_revision), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: SetNodeArch :exec
UPDATE nodes SET arch = sqlc.arg(arch) WHERE id = sqlc.arg(id);

-- name: CreateImageBuild :exec
INSERT INTO image_builds (id, image_id, revision, arch, node_id, status, recipe_json, created_at)
VALUES (sqlc.arg(id), sqlc.arg(image_id), sqlc.arg(revision), sqlc.arg(arch), sqlc.arg(node_id), sqlc.arg(status), sqlc.arg(recipe_json), sqlc.arg(created_at));

-- name: GetImageBuild :one
SELECT * FROM image_builds WHERE id = sqlc.arg(id);

-- name: GetImageBuildByRevArch :one
SELECT * FROM image_builds WHERE image_id = sqlc.arg(image_id) AND revision = sqlc.arg(revision) AND arch = sqlc.arg(arch);

-- name: ListImageBuilds :many
SELECT * FROM image_builds WHERE image_id = sqlc.arg(image_id) ORDER BY revision DESC, arch ASC;

-- name: ListImageBuildsByRev :many
SELECT * FROM image_builds WHERE image_id = sqlc.arg(image_id) AND revision = sqlc.arg(revision) ORDER BY arch ASC;

-- name: ListAllImageBuilds :many
SELECT * FROM image_builds ORDER BY image_id ASC, revision DESC, arch ASC;

-- name: MaxImageRevision :one
SELECT CAST(COALESCE(MAX(revision), 0) AS BIGINT) FROM image_builds WHERE image_id = sqlc.arg(image_id);

-- name: StartImageBuild :exec
UPDATE image_builds SET status = 'building', node_id = sqlc.arg(node_id), started_at = sqlc.arg(started_at) WHERE id = sqlc.arg(id);

-- name: CompleteImageBuild :exec
UPDATE image_builds
SET status = 'ready', error = '', base_key = sqlc.arg(base_key),
    layer_sha256 = sqlc.arg(layer_sha256), layer_size = sqlc.arg(layer_size),
    zstd_sha256 = sqlc.arg(zstd_sha256), zstd_size = sqlc.arg(zstd_size),
    log_text = sqlc.arg(log_text), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id);

-- name: FailImageBuild :exec
UPDATE image_builds
SET status = 'failed', error = sqlc.arg(error), log_text = sqlc.arg(log_text), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id);

-- name: MarkStaleImageBuildsFailed :exec
UPDATE image_builds
SET status = 'failed', error = 'interrupted by server restart', finished_at = sqlc.arg(now)
WHERE status IN ('queued', 'building');

-- name: CountLayerSHAInBuilds :one
SELECT COUNT(*) FROM image_builds WHERE layer_sha256 = sqlc.arg(layer_sha256) AND status = 'ready';
