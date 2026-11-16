-- +goose Up
-- is_template: the machine is a template. It is never started; "new from
-- template" clones it. cloned_from: the agent this one was cloned from ('' for
-- agents created from scratch). No foreign key: deleting the source is fine.
ALTER TABLE agents ADD COLUMN is_template BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN cloned_from TEXT NOT NULL DEFAULT '';
-- resume: boot the sandbox again once the operation that stopped it (a clone
-- of its disks) is over. Persisted so a restart of the server in between does
-- not leave a machine that was running asleep.
ALTER TABLE sandboxes ADD COLUMN resume BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sandboxes DROP COLUMN resume;
ALTER TABLE agents DROP COLUMN cloned_from;
ALTER TABLE agents DROP COLUMN is_template;
