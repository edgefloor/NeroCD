-- name: GetRunLogRetentionPolicy :one
SELECT enabled, keep_days, batch_size, version, updated_by, updated_at FROM run_log_retention_policy WHERE singleton;
-- name: UpdateRunLogRetentionPolicy :one
UPDATE run_log_retention_policy SET enabled=$1, keep_days=$2, batch_size=$3, version=version+1, updated_by=$4, updated_at=clock_timestamp() WHERE singleton RETURNING enabled, keep_days, batch_size, version, updated_by, updated_at;

-- name: LockRunLogRetentionPolicy :one
SELECT enabled, keep_days, batch_size, version, updated_by, updated_at, clock_timestamp()::timestamptz AS database_time
FROM run_log_retention_policy WHERE singleton FOR UPDATE;

-- name: GetRunLogRetentionReceiptForUpdate :one
SELECT request_id, body_sha256, keep_days, batch_size, policy_version, cutoff, eligible_logs, eligible_bytes, deleted_count, deleted_bytes, audit_id, created_at
FROM run_log_retention_receipts WHERE request_id=$1 FOR UPDATE;

-- name: CountRunLogRetentionCandidates :one
SELECT count(*)::bigint AS eligible_logs, COALESCE(sum(octet_length(l.message)),0)::bigint AS eligible_bytes
FROM run_logs l JOIN task_runs r ON r.id=l.run_id
WHERE r.status IN ('succeeded','failed','canceled')
  AND l.created_at < sqlc.arg(cutoff)
  AND NOT EXISTS (SELECT 1 FROM run_leases x WHERE x.run_id=l.run_id AND x.status='active');

-- name: ListRunLogRetentionCandidateRuns :many
SELECT DISTINCT l.run_id
FROM run_logs l JOIN task_runs r ON r.id=l.run_id
WHERE r.status IN ('succeeded','failed','canceled')
  AND l.created_at < sqlc.arg(cutoff)
  AND NOT EXISTS (SELECT 1 FROM run_leases x WHERE x.run_id=l.run_id AND x.status='active')
ORDER BY l.run_id
LIMIT sqlc.arg(candidate_limit);

-- name: LockTerminalRunForRetention :one
SELECT id FROM task_runs WHERE id=$1 AND status IN ('succeeded','failed','canceled') FOR UPDATE;

-- name: LockActiveLeasesForRetention :many
SELECT id FROM run_leases WHERE run_id=$1 AND status='active' FOR UPDATE;

-- name: ListRunLogRetentionCandidatesForRun :many
SELECT id, octet_length(message)::bigint AS message_bytes
FROM run_logs
WHERE run_id=sqlc.arg(run_id) AND created_at<sqlc.arg(cutoff)
ORDER BY created_at, id
LIMIT sqlc.arg(candidate_limit)
FOR UPDATE;

-- name: DeleteRunLogRetentionCandidates :many
DELETE FROM run_logs WHERE id = ANY(sqlc.arg(log_ids)::text[])
RETURNING id, octet_length(message)::bigint AS message_bytes;

-- name: CreateRunLogRetentionReceipt :one
INSERT INTO run_log_retention_receipts (request_id,body_sha256,keep_days,batch_size,policy_version,cutoff,eligible_logs,eligible_bytes,deleted_count,deleted_bytes,audit_id)
VALUES (sqlc.arg(request_id),sqlc.arg(body_sha256),sqlc.arg(keep_days),sqlc.arg(batch_size),sqlc.arg(policy_version),sqlc.arg(cutoff),sqlc.arg(eligible_logs),sqlc.arg(eligible_bytes),sqlc.arg(deleted_count),sqlc.arg(deleted_bytes),sqlc.arg(audit_id))
RETURNING request_id, body_sha256, keep_days, batch_size, policy_version, cutoff, eligible_logs, eligible_bytes, deleted_count, deleted_bytes, audit_id, created_at;
-- name: PreviewRunLogRetention :one
WITH p AS (SELECT keep_days FROM run_log_retention_policy WHERE singleton AND enabled), c AS (SELECT clock_timestamp()-make_interval(days => p.keep_days) cutoff FROM p)
SELECT COALESCE((SELECT cutoff FROM c),clock_timestamp())::timestamptz AS cutoff, COALESCE((SELECT count(*) FROM run_logs l JOIN task_runs r ON r.id=l.run_id CROSS JOIN c WHERE r.status IN ('succeeded','failed','canceled') AND l.created_at<c.cutoff AND NOT EXISTS (SELECT 1 FROM run_leases x WHERE x.run_id=l.run_id AND x.status='active')),0)::bigint AS eligible_logs, COALESCE((SELECT sum(octet_length(l.message)) FROM run_logs l JOIN task_runs r ON r.id=l.run_id CROSS JOIN c WHERE r.status IN ('succeeded','failed','canceled') AND l.created_at<c.cutoff AND NOT EXISTS (SELECT 1 FROM run_leases x WHERE x.run_id=l.run_id AND x.status='active')),0)::bigint AS eligible_bytes;
