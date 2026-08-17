-- name: CountRunLogs :one
SELECT count(*) FROM run_logs WHERE (sqlc.arg(run_id)::text='' OR run_id=sqlc.arg(run_id));

-- name: ListRunLogsPage :many
SELECT * FROM run_logs WHERE (sqlc.arg(run_id)::text='' OR run_id=sqlc.arg(run_id))
ORDER BY run_id, sequence LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: InsertRunLog :one
INSERT INTO run_logs (id,run_id,sequence,stream,message,created_at)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: InsertRunLogWithSequence :one
INSERT INTO run_logs (id,run_id,sequence,stream,message,created_at)
VALUES ($1,$2,COALESCE((SELECT MAX(run_logs.sequence)+1 FROM run_logs WHERE run_logs.run_id=$2 AND run_logs.sequence >= $3),$3),$4,$5,$6)
RETURNING *;

-- name: GetRunnerEventByKey :one
SELECT * FROM run_logs
WHERE run_id=sqlc.arg(run_id) AND event_key=sqlc.arg(event_key);

-- name: InsertRunnerEventWithSequence :one
INSERT INTO run_logs (id,run_id,sequence,stream,message,created_at,event_key,lease_id,attempt,requested_sequence)
VALUES (
  sqlc.arg(id),sqlc.arg(run_id),
  COALESCE((SELECT MAX(run_logs.sequence)+1 FROM run_logs WHERE run_logs.run_id=sqlc.arg(run_id) AND run_logs.sequence >= sqlc.arg(sequence)),sqlc.arg(sequence)),
  sqlc.arg(stream),sqlc.arg(message),sqlc.arg(created_at),sqlc.arg(event_key),sqlc.arg(lease_id),sqlc.arg(attempt),sqlc.arg(requested_sequence)
)
RETURNING *;
