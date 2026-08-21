-- name: GetBackupSchedule :one
SELECT enabled, interval_seconds, retention_count, next_run_at,
       consecutive_failures, updated_at, clock_timestamp() AS database_time
FROM backup_schedule WHERE singleton;

-- name: ConfigureBackupSchedule :one
UPDATE backup_schedule
SET enabled=sqlc.arg(enabled), interval_seconds=sqlc.arg(interval_seconds),
    retention_count=sqlc.arg(retention_count), next_run_at=clock_timestamp(),
    consecutive_failures=0, updated_at=clock_timestamp()
WHERE singleton
RETURNING enabled, interval_seconds, retention_count, next_run_at,
          consecutive_failures, updated_at;

-- name: LockBackupSchedule :one
SELECT enabled, interval_seconds, retention_count, next_run_at,
       consecutive_failures, updated_at, clock_timestamp() AS database_time
FROM backup_schedule WHERE singleton FOR UPDATE;

-- name: ExpireStaleBackupScheduleRuns :exec
UPDATE backup_schedule_runs
SET status='failed', reason='database', completed_at=clock_timestamp()
WHERE status='running' AND started_at < sqlc.arg(stale_before);

-- name: CreateBackupScheduleRun :exec
INSERT INTO backup_schedule_runs (id, scheduled_for, status, reason)
VALUES (sqlc.arg(id), sqlc.arg(scheduled_for), 'running', 'none');

-- name: CompleteBackupScheduleRun :exec
UPDATE backup_schedule_runs
SET status=sqlc.arg(status), reason=sqlc.arg(reason), completed_at=clock_timestamp()
WHERE id=sqlc.arg(id) AND status='running';

-- name: SetBackupScheduleAfterSuccess :exec
UPDATE backup_schedule
SET next_run_at=sqlc.arg(next_run_at), consecutive_failures=0, updated_at=clock_timestamp()
WHERE singleton;

-- name: SetBackupScheduleAfterFailure :exec
UPDATE backup_schedule
SET next_run_at=sqlc.arg(next_run_at), consecutive_failures=sqlc.arg(consecutive_failures), updated_at=clock_timestamp()
WHERE singleton;

-- name: CountRunningBackupScheduleRuns :one
SELECT count(*)::bigint FROM backup_schedule_runs WHERE status='running';
