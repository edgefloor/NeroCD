-- name: LockHostingRuntimeEnvironmentDelivery :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-runtime-delivery:'||sqlc.arg(delivery_id)::text,0));

-- name: GetHostingRuntimeEnvironmentDeliveryAudit :one
SELECT action,target_id,actor_id,metadata FROM audit_events WHERE id=$1;

-- name: HostingRuntimeEnvironmentDeliveryWindow :one
SELECT clock_timestamp()::timestamptz AS database_now,expires_at FROM run_leases WHERE id=$1;

-- name: HostingRuntimeEnvironmentDeliveryNow :one
SELECT clock_timestamp()::timestamptz;

-- name: CheckHostingRuntimeEnvironmentCredential :one
SELECT id FROM runners WHERE id=sqlc.arg(runner_id) AND token_hash=sqlc.arg(credential_hash)
 AND token_hash<>'' AND status='active' FOR SHARE;
