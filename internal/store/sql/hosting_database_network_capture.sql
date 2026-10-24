-- name: DatabaseNetworkCaptureSchema :one
SELECT nerocd_database_network_capture_schema_compatible()::boolean;

-- The bounded candidate list is an optimization, never current authorization.
-- Returning one extra ID distinguishes a full scan window from queue exhaustion.
-- name: ListDatabaseNetworkCaptureCandidates :many
SELECT c.id FROM hosting_database_network_captures c
JOIN task_runs t ON t.id=c.run_id
JOIN hosting_operations source ON source.id=c.source_operation_id
JOIN resources resource ON resource.id=c.resource_id
JOIN projects project ON project.id=c.project_id
JOIN hosting_binding_state state ON state.binding_id=c.binding_id
JOIN destination_project_grants grant_row ON grant_row.destination_id=c.plan->>'destination_id' AND grant_row.project_id=c.project_id
JOIN runners runner ON runner.id=c.plan->>'runner_id'
WHERE runner.id=sqlc.arg(runner_id)::text AND runner.token_hash=sqlc.arg(credential_hash)::text AND runner.status='active'
 AND 'hosting_database_network_capture_v1'=ANY(runner.capabilities) AND NOT('shell'=ANY(runner.capabilities))
 AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=runner.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=runner.token_hash)
 AND project.archived_at IS NULL AND grant_row.revoked_at IS NULL
 AND source.state='succeeded' AND state.healthy->>'operation_id'=source.id
 AND resource.generation::text=c.plan->'source_plan'->'snapshot'->>'resource_generation'
 AND state.hold_operation_id IS NULL AND state.restore_admitted
 AND NOT EXISTS(SELECT 1 FROM hosting_binding_restore_holds h WHERE h.binding_id=c.binding_id)
 AND NOT EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=c.binding_id AND o.state NOT IN('succeeded','failed','canceled'))
 AND NOT EXISTS(SELECT 1 FROM hosting_database_network_origin_receipts r WHERE r.capture_id=c.id)
 AND ((t.status='queued' AND NOT EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=t.id))
 OR (t.status='running' AND t.runner_id=runner.id AND EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=t.id AND l.runner_id=runner.id AND l.status='active' AND l.expires_at>clock_timestamp())))
ORDER BY CASE WHEN t.status='running' THEN 0 ELSE 1 END,c.created_at,c.id LIMIT 33;

-- Lock only the task first. Later source, placement and runner checks are shared
-- with exact-ID claims. A contended task is retried by a later polling request.
-- name: LockDatabaseNetworkCaptureCandidate :one
SELECT c.id FROM hosting_database_network_captures c JOIN task_runs t ON t.id=c.run_id
WHERE c.id=sqlc.arg(capture_id)::text AND c.plan->>'runner_id'=sqlc.arg(runner_id)::text
 AND NOT EXISTS(SELECT 1 FROM hosting_database_network_origin_receipts r WHERE r.capture_id=c.id)
 AND ((t.status='queued' AND NOT EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=t.id))
 OR (t.status='running' AND t.runner_id=sqlc.arg(runner_id)::text AND EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=t.id AND l.runner_id=sqlc.arg(runner_id)::text AND l.status='active' AND l.expires_at>clock_timestamp())))
FOR UPDATE OF t SKIP LOCKED;

-- name: DatabaseNetworkCaptureHasLease :one
SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1);
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
