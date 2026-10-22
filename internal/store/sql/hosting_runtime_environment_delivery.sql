-- name: LockHostingRuntimeEnvironmentDelivery :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-runtime-delivery:'||sqlc.arg(delivery_id)::text,0));

-- name: GetHostingRuntimeEnvironmentDeliveryAudit :one
SELECT action,target_id,actor_id,metadata FROM audit_events WHERE id=$1;

-- name: HostingRuntimeEnvironmentDeliveryWindow :one
SELECT clock_timestamp()::timestamptz AS database_now,expires_at FROM run_leases WHERE id=$1;

-- name: HostingRuntimeEnvironmentDeliveryNow :one
SELECT clock_timestamp()::timestamptz;
