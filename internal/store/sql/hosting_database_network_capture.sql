-- name: DatabaseNetworkCaptureSchema :one
SELECT nerocd_database_network_capture_schema_compatible()::boolean;
-- name: ReadDatabaseNetworkCapture :one
SELECT c.*,r.receipt FROM hosting_database_network_captures c LEFT JOIN hosting_database_network_origin_receipts r ON r.capture_id=c.id WHERE c.id=$1;
-- name: InsertDatabaseNetworkCapture :exec
INSERT INTO hosting_database_network_captures(id,source_operation_id,resource_id,binding_id,project_id,run_id,plan,plan_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
-- name: ReadDatabaseNetworkCaptureCreation :one
SELECT r.* FROM hosting_storage_materializations m JOIN hosting_storage_materialized_receipts r ON r.operation_id=m.materialized_operation_id AND r.receipt_id=m.materialized_receipt_id WHERE m.storage_id=$1;
-- name: ReadDatabaseNetworkOriginReceipt :one
SELECT * FROM hosting_database_network_origin_receipts WHERE capture_id=$1;
-- name: InsertDatabaseNetworkOriginReceipt :exec
INSERT INTO hosting_database_network_origin_receipts(capture_id,receipt_id,authority,body,body_hash,receipt,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: CheckDatabaseNetworkCaptureAuthority :one
SELECT l.* FROM run_leases l JOIN hosting_database_network_captures c ON c.run_id=l.run_id JOIN task_runs t ON t.id=l.run_id
 WHERE c.id=sqlc.arg(capture_id)::text AND l.id=sqlc.arg(lease_id)::text AND l.run_id=sqlc.arg(run_id)::text AND l.runner_id=sqlc.arg(runner_id)::text AND l.attempt=sqlc.arg(attempt)::integer AND l.fence=sqlc.arg(fence)::text AND (NOT sqlc.arg(require_live)::boolean OR (l.status='active' AND l.expires_at>clock_timestamp() AND t.status='running')) FOR UPDATE OF l;
-- name: FlushDatabaseNetworkCapture :exec
SELECT nerocd_database_network_capture_flush();
-- name: DatabaseNetworkCaptureFinalClock :one
SELECT (SELECT coalesce(bool_and(status='active' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())),true)::boolean FROM api_tokens WHERE id=sqlc.arg(actor_id)::text AND service_account_id IS NULL) AS actor_valid,
 (NOT sqlc.arg(require_live)::boolean OR EXISTS(SELECT 1 FROM run_leases WHERE id=sqlc.arg(lease_id)::text AND expires_at>clock_timestamp()))::boolean AS lease_valid;
