-- name: LockHostingBuildLogState :one
SELECT * FROM hosting_build_log_states WHERE build_id=$1 AND run_id=$2 FOR UPDATE;

-- name: GetHostingBuildLogReceipt :one
SELECT * FROM hosting_build_log_receipts WHERE run_id=$1 AND event_key=$2;

-- name: InsertHostingBuildLogReceipt :exec
INSERT INTO hosting_build_log_receipts(build_id,run_id,event_key,authority,requested_sequence,sequence,body_hash) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: InsertHostingBuildLog :exec
INSERT INTO run_logs(id,run_id,sequence,stream,message,created_at,event_key,lease_id,attempt,requested_sequence,component,source_timestamp) VALUES($1,$2,$3,$4,$5,clock_timestamp(),$6,$7,$8,$9,$10,$11);

-- name: AdvanceHostingBuildLogSequence :execrows
UPDATE hosting_build_log_states SET last_sequence=$3 WHERE build_id=$1 AND last_sequence=$2 AND $3::integer>$2::integer;

-- name: ListHostingBuildLogItems :many
SELECT sequence,component,stream,left(message,16384)::text AS message,octet_length(message)>octet_length(left(message,16384)) AS message_truncated,created_at,source_timestamp FROM run_logs WHERE run_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3;

-- name: EarliestHostingBuildLogSequence :one
SELECT COALESCE(MIN(sequence),0)::integer FROM run_logs WHERE run_id=$1;

-- name: ListHostingBuildLogRetentionPrefix :many
SELECT l.id,l.sequence FROM run_logs l WHERE l.run_id=$1 AND l.created_at<$2 AND NOT EXISTS(SELECT 1 FROM run_logs newer WHERE newer.run_id=l.run_id AND newer.sequence<=l.sequence AND newer.created_at>=$2) ORDER BY l.sequence LIMIT $3 FOR UPDATE OF l;

-- name: AdvanceHostingBuildLogWatermark :exec
UPDATE hosting_build_log_states SET deleted_through_sequence=GREATEST(deleted_through_sequence,$2) WHERE run_id=$1;

-- name: GetHostingBuildLogStateForRun :one
SELECT * FROM hosting_build_log_states WHERE run_id=$1;
