-- name: UpsertRunnerOperationalObservation :exec
INSERT INTO runner_operational_observations (runner_id, observed_at, journal_depth, retry_count, renew_failures)
VALUES ($1, clock_timestamp(), $2, $3, $4)
ON CONFLICT (runner_id) DO UPDATE SET observed_at=EXCLUDED.observed_at, journal_depth=EXCLUDED.journal_depth,
 retry_count=EXCLUDED.retry_count, renew_failures=EXCLUDED.renew_failures;

-- name: GetRunnerOperationalObservation :one
SELECT observed_at, journal_depth, retry_count, renew_failures
FROM runner_operational_observations
WHERE runner_id = $1;

-- name: RecordBackupOperationalResult :exec
INSERT INTO backup_operational_results (outcome, reason) VALUES ($1, $2);

-- name: OperationalSnapshotBase :one
WITH now_value AS (SELECT clock_timestamp() AS at),
 queue AS (SELECT count(*) FILTER (WHERE status IN ('queued','waiting_approval')) AS depth, COALESCE(extract(epoch FROM (clock_timestamp() - min(started_at) FILTER (WHERE status IN ('queued','waiting_approval')))), 0)::float8 AS oldest_age FROM task_runs),
 leases AS (SELECT count(*) FILTER (WHERE status='active') AS active, count(*) FILTER (WHERE status='expired') AS expired FROM run_leases),
 runners_state AS (SELECT COALESCE(extract(epoch FROM ((SELECT at FROM now_value) - min(last_heartbeat_at))), 0)::float8 AS oldest_age FROM runners),
 runs AS (SELECT count(*) FILTER (WHERE status='succeeded') AS succeeded_count, COALESCE(sum(extract(epoch FROM (finished_at-started_at))) FILTER (WHERE status='succeeded' AND finished_at IS NOT NULL),0)::float8 AS succeeded_duration, count(*) FILTER (WHERE status='failed') AS failed_count, COALESCE(sum(extract(epoch FROM (finished_at-started_at))) FILTER (WHERE status='failed' AND finished_at IS NOT NULL),0)::float8 AS failed_duration, count(*) FILTER (WHERE status='canceled') AS canceled_count, COALESCE(sum(extract(epoch FROM (finished_at-started_at))) FILTER (WHERE status='canceled' AND finished_at IS NOT NULL),0)::float8 AS canceled_duration FROM task_runs),
 observations AS (SELECT COALESCE(sum(journal_depth),0)::bigint AS journal_depth, COALESCE(sum(retry_count),0)::bigint AS retry_count, COALESCE(sum(renew_failures),0)::bigint AS renew_failures FROM runner_operational_observations),
 backup AS (SELECT extract(epoch FROM ((SELECT at FROM now_value)-completed_at))::float8 AS age, outcome, reason FROM backup_operational_results ORDER BY completed_at DESC, id DESC LIMIT 1),
 schedule AS (SELECT CASE WHEN NOT enabled THEN 'disabled' WHEN EXISTS (SELECT 1 FROM backup_schedule_runs WHERE status='running') THEN 'running' WHEN next_run_at <= (SELECT at FROM now_value) THEN 'due' WHEN consecutive_failures > 0 THEN 'backoff' ELSE 'waiting' END AS status, GREATEST(extract(epoch FROM (next_run_at-(SELECT at FROM now_value))),0)::float8 AS next_seconds, consecutive_failures FROM backup_schedule WHERE singleton)
SELECT (SELECT at FROM now_value)::timestamptz AS collected_at, queue.depth, queue.oldest_age AS queue_oldest_age, leases.active AS active_leases, leases.expired AS expired_leases, runners_state.oldest_age AS runner_oldest_age,
 runs.succeeded_count, runs.succeeded_duration, runs.failed_count, runs.failed_duration, runs.canceled_count, runs.canceled_duration,
 observations.journal_depth, observations.retry_count, observations.renew_failures, backup.age AS backup_age, backup.outcome, backup.reason,
 schedule.status AS backup_schedule_status, schedule.next_seconds AS backup_schedule_next_seconds, schedule.consecutive_failures AS backup_schedule_failures
FROM queue, leases, runners_state, runs, observations CROSS JOIN schedule LEFT JOIN backup ON true;

-- name: OperationalDeploymentCounts :many
SELECT status, count(*) FROM deployments GROUP BY status;

-- name: OperationalDeploymentHealth :one
SELECT count(*) FILTER (WHERE health_passed IS TRUE), count(*) FILTER (WHERE health_passed IS FALSE), count(*) FILTER (WHERE rollback_of_id IS NOT NULL AND status='rolled_back'), count(*) FILTER (WHERE rollback_of_id IS NOT NULL AND status='rollback_failed') FROM deployments;
