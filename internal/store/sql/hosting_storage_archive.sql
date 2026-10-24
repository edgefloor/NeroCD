-- name: StorageArchiveSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_storage_archive_plans') IS NOT NULL)::boolean;
-- name: StorageArchiveSchemaCompatible :one
SELECT nerocd_storage_archive_schema_compatible()::boolean;
-- name: ReadStorageArchivePlan :one
SELECT * FROM hosting_storage_archive_plans WHERE operation_id=$1;
-- name: ReadStorageArchiveOperation :one
SELECT * FROM hosting_operations WHERE id=$1;
-- name: InsertStorageArchivePlan :exec
INSERT INTO hosting_storage_archive_plans(operation_id,mode,backup_id,source_resource_id,source_binding_id,source_storage_id,target_resource_id,target_binding_id,target_storage_id,recovery_of_operation_id,body,body_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);
-- name: ReadStorageArchiveReceipts :many
SELECT * FROM hosting_storage_archive_receipts WHERE operation_id=$1 ORDER BY created_at,phase;
-- name: ReadStorageArchiveReceipt :one
SELECT * FROM hosting_storage_archive_receipts WHERE operation_id=$1 AND phase=$2;
-- name: InsertStorageArchiveReceipt :one
INSERT INTO hosting_storage_archive_receipts(operation_id,phase,receipt_id,authority,body,body_hash,plan_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;
-- name: InsertStorageBackup :exec
INSERT INTO hosting_storage_backups(id,operation_id,project_id,resource_id,request_id,request_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: ReadStorageBackup :one
SELECT * FROM hosting_storage_backups WHERE id=$1;
-- name: ReadStorageBackupRequest :one
SELECT * FROM hosting_storage_backups WHERE resource_id=$1 AND request_id=$2;
-- name: ListStorageBackups :many
SELECT * FROM hosting_storage_backups WHERE resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3;
-- name: CountStorageBackups :one
SELECT count(*) FROM hosting_storage_backups WHERE resource_id=$1;
-- name: ReadStorageArchiveBlob :one
SELECT body FROM hosting_storage_archive_blobs WHERE backup_id=$1;
-- name: InsertStorageArchiveBlob :exec
INSERT INTO hosting_storage_archive_blobs(backup_id,operation_id,body) VALUES($1,$2,$3);
-- name: InsertStorageRestore :exec
INSERT INTO hosting_storage_restores(operation_id,backup_id,resource_id,request_id,request_hash,created_by) VALUES($1,$2,$3,$4,$5,$6);
-- name: ReadStorageRestore :one
SELECT * FROM hosting_storage_restores WHERE operation_id=$1;
-- name: ReadStorageRestoreRequest :one
SELECT * FROM hosting_storage_restores WHERE backup_id=$1 AND request_id=$2;
-- name: InsertStorageArchiveRecovery :exec
INSERT INTO hosting_storage_archive_recoveries(operation_id,original_operation_id,request_id,request_hash,created_by) VALUES($1,$2,$3,$4,$5);
-- name: ReadStorageArchiveRecoveryRequest :one
SELECT * FROM hosting_storage_archive_recoveries WHERE original_operation_id=$1 AND request_id=$2;
-- name: CreateStorageArchiveTaskTarget :exec
INSERT INTO hosting_storage_archive_task_targets(run_id,operation_id,runner_id,mode) VALUES($1,$2,$3,$4);
-- name: ReadStorageArchiveTaskTarget :one
SELECT * FROM hosting_storage_archive_task_targets WHERE run_id=$1;
-- name: CheckStorageArchiveAuthority :one
SELECT l.id FROM run_leases l JOIN hosting_storage_archive_task_targets t ON t.run_id=l.run_id JOIN hosting_storage_archive_plans p ON p.operation_id=t.operation_id JOIN task_runs tr ON tr.id=t.run_id
WHERE l.id=$1 AND l.run_id=$2 AND l.runner_id=$3 AND l.attempt=$4 AND l.fence=$5 AND t.operation_id=$6 AND t.runner_id=$3 AND t.mode=p.mode
AND tr.run_spec=jsonb_build_object('type','storage_archive','hosting_operation_id',t.operation_id,'hosting_mode',t.mode,'inputs','{}'::jsonb)
AND (NOT sqlc.arg(require_live)::boolean OR (l.status='active' AND l.expires_at>clock_timestamp())) FOR UPDATE OF l;
-- name: RecoverQueuedStorageArchives :many
SELECT o.id FROM hosting_operations o JOIN hosting_storage_archive_plans p ON p.operation_id=o.id WHERE o.state='queued' AND o.run_id IS NULL ORDER BY o.updated_at,o.id LIMIT 100 FOR UPDATE OF o SKIP LOCKED;
-- name: ReadStorageRestoreOrigin :one
SELECT * FROM hosting_storage_restore_origins WHERE storage_id=$1;
-- name: InsertStorageRestoreOrigin :exec
INSERT INTO hosting_storage_restore_origins(storage_id,origin_operation_id,origin_plan_hash,origin,origin_hash,authority) VALUES($1,$2,$3,$4,$5,$6);
-- name: ReadStorageRestoreMaterialized :one
SELECT * FROM hosting_storage_restore_materialized WHERE storage_id=$1;
-- name: InsertStorageRestoreMaterialized :one
INSERT INTO hosting_storage_restore_materialized(storage_id,operation_id,receipt_id,authority,body,body_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: ReadStorageRestoreUseAdmission :one
SELECT * FROM hosting_storage_restore_use_admissions WHERE operation_id=$1;
-- name: InsertStorageRestoreUseAdmission :exec
INSERT INTO hosting_storage_restore_use_admissions(operation_id,storage_id,plan_hash,authority) VALUES($1,$2,$3,$4);
-- name: ReadStorageRestoreUseReceipt :one
SELECT * FROM hosting_storage_restore_use_receipts WHERE operation_id=$1;
-- name: InsertStorageRestoreUseReceipt :one
INSERT INTO hosting_storage_restore_use_receipts(operation_id,storage_id,receipt_id,authority,body,body_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;
