-- All allocation paths acquire quota serialization before placement or session locks.
-- name: LockRuntimeLogQuota :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-runtime-log-quota',0));
-- name: RuntimeLogSessionScope :one
SELECT actor_id,resource_id FROM hosting_runtime_log_sessions WHERE id=$1 AND actor_id=$2;
-- name: ReadRuntimeLogSession :one
SELECT * FROM hosting_runtime_log_sessions WHERE id=$1;
-- name: ReadRuntimeLogOpenReceipt :one
SELECT * FROM hosting_runtime_log_sessions WHERE actor_id=$1 AND resource_id=$2 AND request_id=$3;
-- name: LockRuntimeLogSession :one
SELECT * FROM hosting_runtime_log_sessions WHERE id=$1 FOR UPDATE;
-- name: RuntimeLogQuotas :one
SELECT count(*) FILTER(WHERE actor_id=sqlc.arg(actor_id) AND resource_id=sqlc.arg(resource_id) AND ended_at IS NULL AND state IN('waiting','following','disconnected') AND demand_expires_at>clock_timestamp())::integer AS active_resource,
 count(*) FILTER(WHERE actor_id=sqlc.arg(actor_id) AND ended_at IS NULL AND state IN('waiting','following','disconnected') AND demand_expires_at>clock_timestamp())::integer AS active_actor,
 count(*) FILTER(WHERE actor_id=sqlc.arg(actor_id))::integer AS retained_actor,
 count(*)::integer AS retained_project
 FROM hosting_runtime_log_sessions WHERE project_id=sqlc.arg(project_id);
-- name: CreateRuntimeLogSession :one
WITH stamp AS MATERIALIZED (SELECT clock_timestamp() AS now)
INSERT INTO hosting_runtime_log_sessions(id,actor_id,resource_id,project_id,runner_id,request_id,request_hash,request_expires_at,target,authority_hash,credential_hash,demand_expires_at,purge_after)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,stamp.now+interval '30 seconds',stamp.now+interval '330 seconds' FROM stamp RETURNING *;
-- name: RenewRuntimeLogDemand :one
WITH stamp AS MATERIALIZED (SELECT clock_timestamp() AS now)
UPDATE hosting_runtime_log_sessions SET demand_expires_at=stamp.now+interval '30 seconds',purge_after=stamp.now+interval '330 seconds' FROM stamp
WHERE id=$1 AND ended_at IS NULL AND demand_expires_at>clock_timestamp() RETURNING hosting_runtime_log_sessions.*;
-- name: EndRuntimeLogSession :one
WITH stamp AS MATERIALIZED (SELECT clock_timestamp() AS now)
UPDATE hosting_runtime_log_sessions SET state=$2,ended_at=COALESCE(ended_at,stamp.now),demand_expires_at=LEAST(demand_expires_at,stamp.now),epoch_expires_at=NULL,purge_after=LEAST(purge_after,COALESCE(ended_at,stamp.now)+interval '5 minutes') FROM stamp WHERE id=$1 RETURNING hosting_runtime_log_sessions.*;
-- name: ListRuntimeLogClaimCandidates :many
SELECT id FROM hosting_runtime_log_sessions WHERE runner_id=$1 AND id>sqlc.arg(after_session_id)::text AND ended_at IS NULL AND state IN('waiting','following','disconnected') AND demand_expires_at>clock_timestamp() ORDER BY id LIMIT sqlc.arg(page_limit)::integer;
-- name: RuntimeLogCollectorCount :one
SELECT count(*)::integer FROM hosting_runtime_log_sessions WHERE runner_id=$1 AND ended_at IS NULL AND state='following' AND demand_expires_at>clock_timestamp() AND epoch_expires_at>clock_timestamp();
-- name: SetRuntimeLogEpoch :one
UPDATE hosting_runtime_log_sessions SET instance_id=$2,collection_epoch=$3,epoch_expires_at=LEAST(clock_timestamp()+interval '10 seconds',demand_expires_at),state='following',source_gap=source_gap OR (collection_epoch<>'' AND collection_epoch<>$3),receipt=CASE WHEN collection_epoch=$3 THEN receipt ELSE NULL END WHERE id=$1 RETURNING *;
-- name: MarkRuntimeLogGap :exec
UPDATE hosting_runtime_log_sessions SET source_gap=true WHERE id=$1;
-- name: InsertRuntimeLogEvent :exec
INSERT INTO hosting_runtime_log_events(session_id,sequence,component,stream,message,source_timestamp,message_truncated,received_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: ListRuntimeLogRetention :many
SELECT sequence,octet_length(message)::integer AS bytes FROM hosting_runtime_log_events WHERE session_id=$1 ORDER BY sequence;
-- name: DeleteRuntimeLogPrefix :exec
DELETE FROM hosting_runtime_log_events WHERE session_id=$1 AND sequence<=$2;
-- name: AcceptRuntimeLogBatch :one
UPDATE hosting_runtime_log_sessions SET last_sequence=$2,deleted_through_sequence=$3,retained_rows=$4,retained_bytes=$5,receipt=$6,state=$7 WHERE id=$1 RETURNING *;
-- name: ListRuntimeLogEvents :many
SELECT * FROM hosting_runtime_log_events WHERE session_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3;
-- name: TryRuntimeLogJanitor :one
SELECT pg_try_advisory_xact_lock(hashtextextended('nerocd-runtime-log-purge',0)) AS acquired;
-- name: PurgeRuntimeLogSessions :many
WITH stamp AS MATERIALIZED (SELECT clock_timestamp() AS now),
oldest AS (SELECT id FROM hosting_runtime_log_sessions WHERE purge_after<=(SELECT now FROM stamp) ORDER BY purge_after,id LIMIT 8 FOR UPDATE SKIP LOCKED)
DELETE FROM hosting_runtime_log_sessions s USING oldest o WHERE s.id=o.id RETURNING s.id;
-- name: LockRuntimeLogOriginalDestination :one
SELECT d.id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 AND d.server_id=$2 AND s.runner_id=$3 FOR SHARE OF d,s;
-- name: RuntimeLogCurrentBinding :one
SELECT id,destination_id,runtime_namespace FROM resource_bindings WHERE resource_id=$1 FOR SHARE;
