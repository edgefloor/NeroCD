-- InsertRunLogWithSequence
INSERT INTO run_logs (id, run_id, sequence, stream, message, created_at)
VALUES ($1, $2, COALESCE((SELECT MAX(sequence) + 1 FROM run_logs WHERE run_id = $3 AND sequence >= $4), $5), $6, $7, $8)
RETURNING id, run_id, sequence, stream, message, created_at;

-- ExpireLeasesAndRequeueRuns
WITH expired AS (
    UPDATE run_leases
    SET status = 'expired', completed_at = $1
    WHERE status = 'active' AND expires_at <= $1
    RETURNING run_id
)
UPDATE task_runs
SET status = 'queued', runner_id = NULL, finished_at = NULL
WHERE id IN (SELECT run_id FROM expired) AND status = 'running'
RETURNING id;

-- MarkStaleRunners
UPDATE runners
SET status = 'stale'
WHERE status = 'active' AND last_heartbeat_at < $1
RETURNING id;
