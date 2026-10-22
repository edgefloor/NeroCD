-- name: LockHostingSourceDelivery :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-source-delivery:'||sqlc.arg(delivery_id)::text,0));

-- name: GetHostingSourceDeliveryAudit :one
SELECT action,target_id,actor_id,metadata FROM audit_events WHERE id=$1;

-- name: HostingSourceDeliveryLeaseWindow :one
SELECT clock_timestamp()::timestamptz AS database_now,expires_at FROM run_leases WHERE id=$1;

-- name: HostingSourceDeliveryNow :one
SELECT clock_timestamp()::timestamptz;
