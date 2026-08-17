-- name: CountRuns :one
SELECT count(*) FROM task_runs WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id));

-- name: GetRun :one
SELECT * FROM task_runs WHERE id=$1;

-- name: ListRunsPage :many
SELECT * FROM task_runs
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY started_at DESC, id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CreateRun :one
INSERT INTO task_runs (id,project_id,template_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,finished_at,created_at,claim_order_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$10,$10)
RETURNING *;

-- name: UpdateRunStatus :one
UPDATE task_runs SET status=$2, finished_at=$3,
 claim_order_at=CASE WHEN $2='queued' THEN clock_timestamp() ELSE claim_order_at END
WHERE id=$1 RETURNING *;

-- name: UpdateRunWorkflowState :one
UPDATE task_runs SET workflow_state=$2 WHERE id=$1 RETURNING *;
