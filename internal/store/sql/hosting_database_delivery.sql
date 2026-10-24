-- name: LockHostingDatabaseDelivery :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-database-delivery:'||sqlc.arg(delivery_id)::text,0));

-- name: GetHostingDatabaseDeliveryAudit :one
SELECT action,target_id,actor_id,metadata FROM audit_events WHERE id=$1;

-- name: HostingDatabaseDeliveryWindow :one
SELECT clock_timestamp()::timestamptz AS database_now,expires_at FROM run_leases WHERE id=$1;

-- name: HostingDatabaseDeliveryNow :one
SELECT clock_timestamp()::timestamptz;

-- name: CheckHostingDatabaseDeliveryCredential :one
SELECT id FROM runners WHERE id=sqlc.arg(runner_id) AND token_hash=sqlc.arg(credential_hash)
 AND token_hash<>'' AND status='active' FOR SHARE;

-- name: CheckHostingDatabaseDeliverySecret :one
SELECT s.id FROM project_managed_secrets s JOIN project_managed_secret_versions v ON v.secret_id=s.id
 JOIN hosting_database_credentials c ON c.secret_id=s.id AND c.version=v.version
 WHERE s.id=sqlc.arg(secret_id) AND s.project_id=sqlc.arg(project_id) AND s.status='active'
 AND s.current_version=sqlc.arg(version) AND v.version=sqlc.arg(version)
 AND c.resource_id=sqlc.arg(resource_id) AND c.role=sqlc.arg(role) AND c.username=sqlc.arg(username)
 FOR SHARE OF s,v;
