-- name: MarkStaleRunnerForClaim :execrows
UPDATE runners
SET status = 'stale'
WHERE id = sqlc.arg(runner_id)
  AND status = 'active'
  AND last_heartbeat_at < sqlc.arg(stale_before);

-- name: EnsureRunnerClaimCursor :exec
INSERT INTO runner_claim_cursors (runner_id)
VALUES ($1)
ON CONFLICT (runner_id) DO NOTHING;

-- name: LockRunnerClaimCursor :one
SELECT claim_order_at, run_id
FROM runner_claim_cursors
WHERE runner_id = $1
FOR UPDATE;

-- name: ListQueuedClaimCandidatesFromHead :many
SELECT id, claim_order_at
FROM task_runs
WHERE status = 'queued'
ORDER BY claim_order_at ASC, id ASC
FOR UPDATE SKIP LOCKED
LIMIT $1;

-- name: ListQueuedClaimCandidatesAfterCursor :many
SELECT id, claim_order_at
FROM task_runs
WHERE status = 'queued'
  AND (claim_order_at, id) > (sqlc.arg(claim_order_at), sqlc.arg(run_id)::text)
ORDER BY claim_order_at ASC, id ASC
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(candidate_limit);

-- name: StoreRunnerClaimCursor :exec
UPDATE runner_claim_cursors
SET claim_order_at = sqlc.narg(claim_order_at),
    run_id = NULLIF(sqlc.arg(run_id)::text, ''),
    updated_at = clock_timestamp()
WHERE runner_id = sqlc.arg(runner_id);

-- name: ClaimQueuedRun :one
UPDATE task_runs
SET status = 'running',
    runner_id = sqlc.arg(runner_id),
    next_attempt = next_attempt + 1
WHERE id = sqlc.arg(run_id)
  AND status = 'queued'
RETURNING next_attempt - 1 AS attempt;

-- name: CreateActiveRunLease :one
INSERT INTO run_leases (
    id, run_id, runner_id, status, expires_at, created_at, attempt, fence
)
VALUES (
    sqlc.arg(id), sqlc.arg(run_id), sqlc.arg(runner_id), sqlc.arg(status),
    clock_timestamp() + (sqlc.arg(ttl_microseconds)::bigint * interval '1 microsecond'),
    clock_timestamp(), sqlc.arg(attempt), sqlc.arg(fence)
)
RETURNING *;

-- name: ListExpiredRunningRunIDs :many
SELECT runs.id
FROM task_runs AS runs
WHERE runs.status = 'running'
  AND EXISTS (
      SELECT 1
      FROM run_leases AS leases
      WHERE leases.run_id = runs.id
        AND leases.status = 'active'
        AND leases.expires_at <= clock_timestamp()
  )
ORDER BY runs.id
FOR UPDATE OF runs SKIP LOCKED
LIMIT $1;

-- name: ExpireActiveLeasesForRun :execrows
UPDATE run_leases
SET status = 'expired', completed_at = clock_timestamp()
WHERE run_id = $1
  AND status = 'active'
  AND expires_at <= clock_timestamp();

-- name: RequeueExpiredRun :exec
UPDATE task_runs
SET status = 'queued', runner_id = NULL, finished_at = NULL,
    claim_order_at = clock_timestamp()
WHERE id = $1
  AND status = 'running';

-- name: QueueApprovedRun :one
UPDATE task_runs
SET status = 'queued', claim_order_at = clock_timestamp()
WHERE id = $1
  AND status = 'waiting_approval'
RETURNING id;
