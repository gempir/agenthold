-- name: CreateWorkflow :exec
INSERT INTO workflows (
    id, owner_id, name, description, definition_json, enabled, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = $1;

-- name: ListUserWorkflows :many
SELECT * FROM workflows WHERE owner_id = $1 ORDER BY updated_at DESC, name ASC;

-- name: ListEnabledWorkflows :many
SELECT * FROM workflows WHERE enabled = 1 ORDER BY created_at ASC;

-- name: UpdateWorkflow :exec
UPDATE workflows
SET name = $1, description = $2, definition_json = $3, enabled = $4, updated_at = $5
WHERE id = $6;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = $1;

-- name: CreateWorkflowRun :exec
INSERT INTO workflow_runs (
    id, workflow_id, owner_id, status, trigger_kind, input_json, output_json,
    error, current_step, started_at, finished_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = $1;

-- name: ListUserWorkflowRuns :many
SELECT * FROM workflow_runs
WHERE owner_id = $1
ORDER BY created_at DESC
LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: ListUserWorkflowRunsByStatus :many
SELECT * FROM workflow_runs
WHERE owner_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: ListWorkflowRunsByWorkflow :many
SELECT * FROM workflow_runs
WHERE workflow_id = $1
ORDER BY created_at DESC
LIMIT CAST(sqlc.arg('limit') AS BIGINT);

-- name: ListActiveWorkflowRuns :many
SELECT * FROM workflow_runs
WHERE status IN ('queued', 'running')
ORDER BY created_at ASC;

-- name: CountActiveRunsForWorkflow :one
SELECT COUNT(*) FROM workflow_runs
WHERE workflow_id = $1 AND status IN ('running', 'needs_input');

-- name: ListQueuedRunsForWorkflow :many
SELECT * FROM workflow_runs
WHERE workflow_id = $1 AND status = 'queued'
ORDER BY created_at ASC;

-- name: UpdateWorkflowRunState :exec
UPDATE workflow_runs
SET status = $1, current_step = $2, output_json = $3, error = $4,
    started_at = $5, finished_at = $6, updated_at = $7
WHERE id = $8;

-- name: CreateWorkflowStep :exec
INSERT INTO workflow_steps (
    id, run_id, step_index, name, kind, status, session_id, prompt_or_cmd,
    ask_title, ask_choices_json, answer_json, answered_by, output, error,
    started_at, finished_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: GetWorkflowStep :one
SELECT * FROM workflow_steps WHERE id = $1;

-- name: ListWorkflowSteps :many
SELECT * FROM workflow_steps WHERE run_id = $1 ORDER BY step_index ASC;

-- name: UpdateWorkflowStep :exec
UPDATE workflow_steps
SET status = $1, session_id = $2, prompt_or_cmd = $3, ask_title = $4,
    ask_choices_json = $5, answer_json = $6, answered_by = $7, output = $8,
    error = $9, started_at = $10, finished_at = $11
WHERE id = $12;
