-- name: CreateWorkflow :exec
INSERT INTO workflows (
    id, owner_id, name, description, definition_json, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkflow :one
SELECT * FROM workflows WHERE id = ?;

-- name: ListUserWorkflows :many
SELECT * FROM workflows WHERE owner_id = ? ORDER BY updated_at DESC, name ASC;

-- name: ListEnabledWorkflows :many
SELECT * FROM workflows WHERE enabled = 1 ORDER BY created_at ASC;

-- name: UpdateWorkflow :exec
UPDATE workflows
SET name = ?, description = ?, definition_json = ?, enabled = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = ?;

-- name: CreateWorkflowRun :exec
INSERT INTO workflow_runs (
    id, workflow_id, owner_id, status, trigger_kind, input_json, output_json,
    error, current_step, started_at, finished_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = ?;

-- name: ListUserWorkflowRuns :many
SELECT * FROM workflow_runs
WHERE owner_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListUserWorkflowRunsByStatus :many
SELECT * FROM workflow_runs
WHERE owner_id = ? AND status = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListWorkflowRunsByWorkflow :many
SELECT * FROM workflow_runs
WHERE workflow_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListActiveWorkflowRuns :many
SELECT * FROM workflow_runs
WHERE status IN ('queued', 'running')
ORDER BY created_at ASC;

-- name: CountActiveRunsForWorkflow :one
SELECT COUNT(*) FROM workflow_runs
WHERE workflow_id = ? AND status IN ('running', 'needs_input');

-- name: ListQueuedRunsForWorkflow :many
SELECT * FROM workflow_runs
WHERE workflow_id = ? AND status = 'queued'
ORDER BY created_at ASC;

-- name: UpdateWorkflowRunState :exec
UPDATE workflow_runs
SET status = ?, current_step = ?, output_json = ?, error = ?,
    started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?;

-- name: CreateWorkflowStep :exec
INSERT INTO workflow_steps (
    id, run_id, step_index, name, kind, status, session_id, prompt_or_cmd,
    ask_title, ask_choices_json, answer_json, answered_by, output, error,
    started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkflowStep :one
SELECT * FROM workflow_steps WHERE id = ?;

-- name: ListWorkflowSteps :many
SELECT * FROM workflow_steps WHERE run_id = ? ORDER BY step_index ASC;

-- name: UpdateWorkflowStep :exec
UPDATE workflow_steps
SET status = ?, session_id = ?, prompt_or_cmd = ?, ask_title = ?,
    ask_choices_json = ?, answer_json = ?, answered_by = ?, output = ?,
    error = ?, started_at = ?, finished_at = ?
WHERE id = ?;
