-- name: LockStorageRemoteTarget :one
SELECT enabled,project_id FROM hosting_database_backup_targets WHERE id=$1 FOR SHARE;

-- name: StorageRemotePlacement :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 FOR SHARE OF d,s;

-- name: LockStorageRemoteRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash AND $2::text=ANY(r.capabilities) AND $3::text=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) FOR SHARE OF r,e;

-- name: InsertStorageRemoteTransfer :exec
INSERT INTO hosting_storage_backup_transfers(id,project_id,target_id,target_revision,destination_id,actor_id,request_id,request_hash,mode,backup_id,restore_operation_id,predecessor_id,state,runner_id,daemon_id,blob,plan,plan_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'queued',$13,$14,$15,$16,$17,$18,$18);

-- name: LockStorageRemoteProject :one
SELECT id FROM projects WHERE id=$1 FOR UPDATE;

-- name: ReadStorageRemoteRequest :one
SELECT id,request_hash FROM hosting_storage_backup_transfers WHERE project_id=$1 AND request_id=$2;

-- name: LockStorageRemoteRevision :one
SELECT revision FROM hosting_database_backup_targets WHERE id=$1 AND project_id=$2 FOR SHARE;

-- name: StorageRemotePlacementUnlocked :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1;

-- name: DenyStorageRemoteTransfer :exec
UPDATE hosting_storage_backup_transfers SET state='failed',failure_code='authority_denied',updated_at=clock_timestamp() WHERE id=$1;

-- name: DispatchStorageRemoteTransfer :exec
UPDATE hosting_storage_backup_transfers SET state='dispatched',run_id=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: LockStorageRemoteAuthority :one
SELECT b.id FROM hosting_storage_backup_transfers b JOIN task_runs t ON t.id=b.run_id JOIN run_leases l ON l.run_id=t.id WHERE b.id=$1 AND b.run_id=$2 AND b.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','storage_backup_transfer','hosting_operation_id',b.id,'hosting_mode',b.mode,'inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: ReadStorageRemoteReceipt :one
SELECT body_hash,response FROM hosting_storage_backup_transfer_receipts WHERE operation_id=$1 AND phase=$2;

-- name: SetStorageRemoteTransferState :exec
UPDATE hosting_storage_backup_transfers SET state=$2,failure_code=$3,daemon_id=$4,updated_at=clock_timestamp() WHERE id=$1;

-- name: CompleteStorageRemoteTask :exec
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: CompleteStorageRemoteLease :exec
UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active';

-- name: InsertStorageRemoteReceipt :exec
INSERT INTO hosting_storage_backup_transfer_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response,expired_classification) VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: LockStorageRemoteExpiredRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.credential_hash=r.token_hash AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND 'hosting_storage_remote_backup_v1'=ANY(r.capabilities) FOR SHARE OF r,e;

-- name: StorageRemoteExpiredClassification :one
SELECT EXISTS(SELECT 1 FROM run_leases l JOIN runners r ON r.id=l.runner_id JOIN runner_enrollments e ON e.runner_id=r.id JOIN hosting_storage_backup_transfer_receipts i ON i.operation_id=$1 AND i.phase='intent' WHERE l.id=$2 AND l.run_id=$3 AND l.runner_id=$4 AND l.attempt=$5 AND l.fence=$6 AND l.expires_at<=clock_timestamp() AND l.status IN ('active','expired') AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND 'hosting_storage_remote_backup_v1'=ANY(r.capabilities) AND i.authority=$7 AND i.body->>'plan_hash'=$8::text AND i.body->>'daemon_id'=$9::text AND NOT EXISTS(SELECT 1 FROM run_leases newer WHERE newer.run_id=l.run_id AND newer.attempt>l.attempt));

-- name: QueuedStorageRemoteTransfers :many
SELECT id FROM hosting_storage_backup_transfers WHERE state='queued' ORDER BY created_at,id LIMIT 100;

-- name: StorageRemoteTargetValidated :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_backup_transfers WHERE target_id=$1 AND target_revision=$2 AND destination_id=$3 AND runner_id=$4 AND daemon_id=$5 AND mode='validate' AND state='succeeded');

-- name: LockStorageRemoteBackupResource :one
SELECT resource_id FROM hosting_storage_backups WHERE id=$1 FOR UPDATE;

-- name: ReadStorageRemoteBackupRequest :one
SELECT t.id,t.request_hash FROM hosting_storage_backup_transfers t WHERE t.project_id=(SELECT b.project_id FROM hosting_storage_backups b WHERE b.id=$1) AND t.request_id=$2;

-- name: LatestStorageRemoteTransfer :one
SELECT id FROM hosting_storage_backup_transfers WHERE backup_id=$1 AND mode=$2 AND restore_operation_id IS NOT DISTINCT FROM $3 ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: RetryStorageRemoteRestore :one
UPDATE hosting_operations SET state='queued',updated_at=clock_timestamp() WHERE id=$1 AND state='failed' AND run_id IS NULL RETURNING id;

-- name: FailStorageRemoteRestore :exec
UPDATE hosting_operations SET state='failed',updated_at=clock_timestamp() WHERE id=$1 AND state='queued' AND run_id IS NULL;

-- name: StorageRemoteCredentialDeadline :one
SELECT least(l.expires_at,clock_timestamp()+interval '1 minute')::timestamptz FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r;

-- name: ReadStorageRemoteDelivery :one
SELECT metadata FROM hosting_storage_backup_remote_deliveries WHERE delivery_id=$1;

-- name: InsertStorageRemoteDelivery :exec
INSERT INTO hosting_storage_backup_remote_deliveries(delivery_id,operation_id,run_id,lease_id,attempt,fence,runner_id,request_hash,metadata,use_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);

-- name: StorageRemoteDeliveryLive :one
SELECT coalesce($1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz,false)::boolean;

-- name: ReadLiveStorageRemoteDelivery :one
SELECT metadata FROM hosting_storage_backup_remote_deliveries WHERE delivery_id=$1 AND operation_id=$2 AND runner_id=$3 AND use_until>clock_timestamp() AND use_until<=$4;

-- name: StorageRemoteRestoreDownloaded :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_backup_transfers WHERE restore_operation_id=$1 AND mode='download' AND state='succeeded');

-- name: ReadStorageRemoteTransfer :one
SELECT * FROM hosting_storage_backup_transfers WHERE id=$1;

-- name: LockStorageRemoteTransfer :one
SELECT * FROM hosting_storage_backup_transfers WHERE id=$1 FOR UPDATE;

-- name: ReadStorageRemoteBackupProject :one
SELECT project_id FROM hosting_storage_backups WHERE id=$1;

-- name: ReadStorageRemoteTargetProject :one
SELECT project_id FROM hosting_database_backup_targets WHERE id=$1;

-- name: ListStorageRemoteBackupTransfers :many
SELECT id FROM hosting_storage_backup_transfers WHERE backup_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: ListStorageRemoteValidations :many
SELECT id FROM hosting_storage_backup_transfers WHERE target_id=$1 AND mode='validate' ORDER BY created_at DESC,id DESC LIMIT 100;
