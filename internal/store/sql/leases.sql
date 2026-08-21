-- name: AcquireRunLogLock :exec
SELECT pg_advisory_xact_lock(hashtext($1));

-- name: LockAuthorizedLease :one
SELECT id
FROM run_leases
WHERE id = sqlc.arg(lease_id)
  AND run_id = sqlc.arg(run_id)
  AND runner_id = sqlc.arg(runner_id)
  AND attempt = sqlc.arg(attempt)
  AND fence = sqlc.arg(fence)
  AND status = 'active'
  AND expires_at > clock_timestamp()
FOR UPDATE;

-- name: RenewAuthorizedLease :one
UPDATE run_leases
SET expires_at = clock_timestamp() + (sqlc.arg(ttl_microseconds)::bigint * interval '1 microsecond')
WHERE id = sqlc.arg(lease_id)
  AND runner_id = sqlc.arg(runner_id)
  AND attempt = sqlc.arg(attempt)
  AND fence = sqlc.arg(fence)
  AND status = 'active'
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: GetLeaseRunID :one
SELECT run_id FROM run_leases WHERE id = $1;

-- name: LockRunID :one
SELECT id FROM task_runs WHERE id = $1 FOR UPDATE;

-- name: CompleteAuthorizedLease :one
UPDATE run_leases
SET status = sqlc.arg(status), completed_at = clock_timestamp(), completion_key = sqlc.arg(completion_key)
WHERE id = sqlc.arg(lease_id)
  AND runner_id = sqlc.arg(runner_id)
  AND attempt = sqlc.arg(attempt)
  AND fence = sqlc.arg(fence)
  AND status = 'active'
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: GetLeaseForCompletion :one
SELECT * FROM run_leases
WHERE id=sqlc.arg(lease_id)
  AND runner_id=sqlc.arg(runner_id)
  AND attempt=sqlc.arg(attempt)
  AND fence=sqlc.arg(fence);

-- name: GetLeaseReplayIdentity :one
SELECT id FROM run_leases
WHERE id=sqlc.arg(lease_id)
  AND run_id=sqlc.arg(run_id)
  AND runner_id=sqlc.arg(runner_id)
  AND attempt=sqlc.arg(attempt)
  AND fence=sqlc.arg(fence);

-- name: GetCommittedCompletion :one
SELECT * FROM run_leases
WHERE id=sqlc.arg(lease_id)
  AND runner_id=sqlc.arg(runner_id)
  AND attempt=sqlc.arg(attempt)
  AND fence=sqlc.arg(fence)
  AND completion_key=sqlc.arg(completion_key)
  AND status=sqlc.arg(status)
  AND completed_at IS NOT NULL;

-- name: LockCancellableRunStatus :one
SELECT status FROM task_runs WHERE id = $1 FOR UPDATE;

-- name: DatabaseClock :one
SELECT clock_timestamp()::timestamptz AS database_time;

-- name: CancelActiveLeasesForRun :exec
UPDATE run_leases
SET status = 'canceled', completed_at = clock_timestamp()
WHERE run_id = $1
  AND status = 'active';

-- name: CancelRun :exec
UPDATE task_runs
SET status = 'canceled', runner_id = NULL, finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(run_id);

-- name: GetActiveLeaseForRun :one
SELECT *
FROM run_leases
WHERE run_id = $1
  AND status = 'active'
  AND expires_at > clock_timestamp()
ORDER BY created_at DESC
LIMIT 1;

-- name: GetActiveLeaseForRunner :one
SELECT *
FROM run_leases
WHERE id = sqlc.arg(lease_id)
  AND runner_id = sqlc.arg(runner_id)
  AND status = 'active'
  AND expires_at > clock_timestamp();

-- name: ExpireDeploymentAttemptsForRun :exec
UPDATE deployment_attempts AS deployment_attempt
SET status='failed', finished_at=clock_timestamp()
WHERE deployment_attempt.run_id=$1 AND deployment_attempt.status='active'
  AND deployment_attempt.lease_id IN (
    SELECT lease.id FROM run_leases AS lease WHERE lease.run_id=$1 AND lease.status='expired'
  );
