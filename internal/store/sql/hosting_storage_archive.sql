-- name: StorageArchiveSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_storage_archive_plans') IS NOT NULL)::boolean;
-- name: StorageArchiveSchemaCompatible :one
SELECT nerocd_storage_archive_schema_compatible()::boolean;
-- name: ReadStorageArchivePlan :one
SELECT * FROM hosting_storage_archive_plans WHERE operation_id=$1;
-- name: ReadStorageArchiveOperation :one
SELECT * FROM hosting_operations WHERE id=$1;
-- name: InsertStorageArchivePlan :exec
INSERT INTO hosting_storage_archive_plans(operation_id,mode,backup_id,source_resource_id,source_binding_id,source_storage_id,target_resource_id,target_binding_id,target_storage_id,recovery_of_operation_id,body,body_hash,source_family,source_volume) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14);
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

-- name: ReadStorageArchiveHolds :one
SELECT EXISTS(SELECT 1 FROM hosting_binding_state WHERE hold_operation_id=sqlc.arg(operation_id)::text)::boolean AS mutation_hold,
 EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=sqlc.arg(binding_id)::text AND NOT restore_admitted)::boolean AS restore_ownership_held;
-- name: LockStorageArchiveRunner :one
SELECT r.id FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id JOIN projects project ON project.id=sqlc.arg(project_id)::text
WHERE r.id=sqlc.arg(runner_id)::text AND s.id=sqlc.arg(server_id)::text AND d.id=sqlc.arg(destination_id)::text AND project.archived_at IS NULL AND r.status='active' AND r.token_hash<>''
AND 'storage_archive'=ANY(r.capabilities) AND 'hosting_storage_archive_v1'=ANY(r.capabilities) AND 'hosting_owned_storage_v1'=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities))
AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) FOR SHARE OF r;
-- name: AdmitStorageArchiveHold :exec
UPDATE hosting_binding_state SET hold_operation_id=sqlc.arg(operation_id)::text WHERE binding_id=sqlc.arg(binding_id)::text AND hold_operation_id IS NULL;
-- name: ReadStorageArchiveHoldOwner :one
SELECT coalesce(hold_operation_id,'')::text FROM hosting_binding_state WHERE binding_id=$1;
-- name: TransferStorageArchiveHold :execrows
UPDATE hosting_binding_state SET hold_operation_id=sqlc.arg(operation_id)::text WHERE binding_id=sqlc.arg(binding_id)::text AND hold_operation_id=sqlc.arg(previous_operation_id)::text;
-- name: LockStorageArchiveStorage :one
SELECT id FROM hosting_storage WHERE id=$1 FOR UPDATE;
-- name: ResolveStorageArchiveTargetHold :exec
UPDATE hosting_binding_state SET restore_admitted=true WHERE binding_id=sqlc.arg(binding_id)::text AND hold_operation_id=sqlc.arg(operation_id)::text;
-- name: StorageArchiveHasPublishedBlob :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_archive_blobs WHERE operation_id=$1)::boolean;
-- name: LockStorageArchiveImage :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-archive-image:'||sqlc.arg(daemon_id)::text||':'||sqlc.arg(image_id)::text,0));
-- name: StorageArchiveImageReferenced :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id
 WHERE p.body->'source_healthy'->'release'->>'daemon_id'=sqlc.arg(daemon_id)::text AND p.body->'source_healthy'->'release'->>'image_id'=sqlc.arg(image_id)::text
 AND (EXISTS(SELECT 1 FROM hosting_storage_backups b WHERE b.operation_id=p.operation_id) OR (p.target_resource_id IS NOT NULL AND o.state NOT IN ('succeeded','failed','canceled'))))::boolean;
-- name: StorageArchiveImageCleanupPending :one
SELECT EXISTS(SELECT 1 FROM hosting_retained_releases WHERE state='cleanup_pending' AND body->'release'->>'daemon_id'=sqlc.arg(daemon_id)::text AND body->'release'->>'image_id'=sqlc.arg(image_id)::text)::boolean;
-- name: ReserveStorageArchiveRestoreTarget :exec
INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,daemon_id,hold_operation_id,restore_admitted) VALUES($1,$2,$3,$4,$5,false);

-- name: CheckStorageArchiveAuthorityClock :one
-- Last read for new effects: evaluate both moving clocks after all lock waits.
SELECT (SELECT COALESCE(bool_and(status='active' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())),true)::boolean FROM api_tokens WHERE id=sqlc.arg(actor_id)::text AND service_account_id IS NULL) AS actor_valid,
 EXISTS(SELECT 1 FROM run_leases WHERE id=sqlc.arg(lease_id)::text AND expires_at>clock_timestamp() AND (NOT sqlc.arg(require_active)::boolean OR status='active'))::boolean AS lease_valid;

-- name: FlushStorageArchiveConstraints :exec
-- Finish potentially blocking deferred checks before the final authority clock.
SELECT nerocd_storage_archive_flush_constraints();

-- name: LockComposeStorageArchiveRunner :one
SELECT r.id FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id JOIN projects project ON project.id=sqlc.arg(project_id)::text
WHERE r.id=sqlc.arg(runner_id)::text AND s.id=sqlc.arg(server_id)::text AND d.id=sqlc.arg(destination_id)::text AND project.archived_at IS NULL AND r.status='active' AND r.token_hash<>''
AND 'storage_archive'=ANY(r.capabilities) AND 'hosting_storage_archive_v1'=ANY(r.capabilities) AND 'hosting_owned_storage_v1'=ANY(r.capabilities) AND r.capabilities @> ARRAY['hosting_compose_storage_archive_v1','hosting_managed_compose_v1']::text[] AND NOT('shell'=ANY(r.capabilities))
AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) FOR SHARE OF r;
