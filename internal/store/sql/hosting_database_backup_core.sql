-- name: DatabaseBackupTargetOwnershipHeld :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backups WHERE target_binding_id=$1 AND ownership_held);

-- name: FindDatabaseBackupSourceRequest :one
SELECT id FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup' AND request_id=$2;

-- name: CreateDatabaseBackup :exec
INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,runner_id,plan,plan_hash) VALUES($1,$2,$3,$4,$5,$1,'backup',$6,$7,$8,'queued',$9,$10,$11);

-- name: CreateDatabaseBackupRemoteSelection :exec
INSERT INTO hosting_database_backup_remote_selections(operation_id,target_id,target_revision) VALUES($1,$2,$3);

-- name: CreateDatabaseBackupRestore :exec
INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,target_resource_id,target_binding_id,deployment_operation_id,runner_id,plan,plan_hash,blob) VALUES($1,$2,$3,$4,$5,$6,'restore',$7,$8,$9,'waiting_target',$10,$11,$12,$13,$14,$15,$16);

-- name: ReadDatabaseBackupDownloadState :one
SELECT state FROM hosting_database_backup_transfers WHERE restore_operation_id=$1;

-- name: MarkDatabaseBackupExecutionUncertain :exec
UPDATE hosting_database_backups SET state='uncertain',failure_code='execution_failed',updated_at=clock_timestamp() WHERE id=$1;

-- name: DispatchDatabaseBackup :exec
UPDATE hosting_database_backups SET state='dispatched',run_id=$2,plan=$3,plan_hash=$4,updated_at=clock_timestamp() WHERE id=$1;

-- name: LockDatabaseBackupAuthority :one
SELECT b.id FROM hosting_database_backups b JOIN task_runs t ON t.id=b.run_id JOIN run_leases l ON l.run_id=t.id WHERE b.id=$1 AND b.run_id=$2 AND b.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup','hosting_operation_id',b.id,'hosting_mode',b.mode,'inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: SetDatabaseBackupMutationState :exec
UPDATE hosting_database_backups SET state=$2,ownership_held=$3,blob=$4,failure_code=$5,updated_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDatabaseBackupTask :exec
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDatabaseBackupLease :exec
UPDATE run_leases SET status=$3,completed_at=clock_timestamp(),completion_key=$2 WHERE id=$1 AND status='active';

-- name: CreateDatabaseBackupReceipt :exec
INSERT INTO hosting_database_backup_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: LockDatabaseBackupTargetRevision :one
SELECT revision FROM hosting_database_backup_targets WHERE id=$1 AND project_id=$2 FOR SHARE;

-- name: DatabaseBackupRemoteTargetValidated :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backup_transfers WHERE target_id=$1 AND target_revision=$2 AND destination_id=$3 AND runner_id=$4 AND daemon_id=$5 AND mode='validate' AND state='succeeded');

-- name: FindDatabaseBackupTransferRequest :one
SELECT id,request_hash FROM hosting_database_backup_transfers WHERE project_id=$1 AND request_id=$2;

-- name: ReadLatestDatabaseBackupUpload :one
SELECT id FROM hosting_database_backup_transfers WHERE backup_id=$1 AND mode='upload' ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: ReadDatabaseBackupRemoteCredentialExpiry :one
SELECT least(l.expires_at,clock_timestamp()+interval '1 minute')::timestamptz FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r;

-- name: ReadDatabaseBackupRemoteDelivery :one
SELECT metadata FROM hosting_database_backup_remote_deliveries WHERE delivery_id=$1;

-- name: CreateDatabaseBackupRemoteDelivery :exec
INSERT INTO hosting_database_backup_remote_deliveries(delivery_id,operation_id,run_id,lease_id,attempt,fence,runner_id,request_hash,metadata,use_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);

-- name: DatabaseBackupRemoteDeliveryValid :one
SELECT ($1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz)::boolean;

-- name: ReadValidDatabaseBackupRemoteDelivery :one
SELECT metadata FROM hosting_database_backup_remote_deliveries WHERE delivery_id=$1 AND operation_id=$2 AND runner_id=$3 AND use_until>clock_timestamp() AND use_until<=$4;

-- name: ReadDatabaseBackupTargetSnapshot :one
SELECT snapshot FROM hosting_database_backup_target_versions WHERE target_id=$1 AND revision=$2;

-- name: LockDatabaseBackupRemoteCredential :one
SELECT status,envelope_version,key_id,ciphertext,created_by,created_at FROM hosting_database_backup_remote_credentials WHERE id=$1 AND project_id=$2 AND version=$3 FOR SHARE;

-- name: CreateDatabaseBackupRemoteCredential :exec
INSERT INTO hosting_database_backup_remote_credentials(id,project_id,version,status,envelope_version,key_id,ciphertext,created_by,created_at) VALUES($1,$2,1,'active',$3,$4,$5,$6,$7);

-- name: LockDatabaseBackupTargetProject :one
SELECT id FROM projects WHERE id=$1 FOR UPDATE;

-- name: ReadDatabaseBackupTargetRequest :one
SELECT request_hash,actor_id,target_id,revision,credentials_supplied,response FROM hosting_database_backup_target_requests WHERE project_id=$1 AND request_id=$2;

-- name: ReadDatabaseBackupTargetClock :one
SELECT clock_timestamp()::timestamptz;

-- name: LockDatabaseBackupTarget :one
SELECT project_id,revision,created_at FROM hosting_database_backup_targets WHERE id=$1 FOR UPDATE;

-- name: CountDatabaseBackupTargets :one
SELECT count(*) FROM hosting_database_backup_targets WHERE project_id=$1;

-- name: CreateDatabaseBackupTarget :exec
INSERT INTO hosting_database_backup_targets(id,project_id,revision,enabled,created_at,updated_at) VALUES($1,$2,1,$3,$4,$4);

-- name: RevokeDatabaseBackupRemoteCredentials :exec
UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=ANY($1::text[]) AND status='active';

-- name: CreateDatabaseBackupTargetVersion :exec
INSERT INTO hosting_database_backup_target_versions(target_id,revision,actor_id,snapshot,access_id,secret_id) VALUES($1,$2,$3,$4,$5,$6);

-- name: UpdateDatabaseBackupTarget :exec
UPDATE hosting_database_backup_targets SET revision=$2,enabled=$3,updated_at=$4 WHERE id=$1;

-- name: CreateDatabaseBackupTargetRequest :exec
INSERT INTO hosting_database_backup_target_requests(project_id,request_id,actor_id,request_hash,target_id,revision,credentials_supplied,response) VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: ListDatabaseBackupTargetSnapshots :many
SELECT v.snapshot FROM hosting_database_backup_targets t JOIN hosting_database_backup_target_versions v ON v.target_id=t.id AND v.revision=t.revision WHERE t.project_id=$1 ORDER BY t.created_at,t.id LIMIT 100;

-- name: LockDatabaseBackupTargetAuthority :one
SELECT enabled,project_id FROM hosting_database_backup_targets WHERE id=$1 FOR SHARE;

-- name: LockDatabaseBackupRemoteDestination :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 FOR SHARE OF d,s;

-- name: LockDatabaseBackupRemoteRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash AND $2::text=ANY(r.capabilities) AND $3::text=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) FOR SHARE OF r,e;

-- name: CreateDatabaseBackupTransfer :exec
INSERT INTO hosting_database_backup_transfers(id,project_id,target_id,target_revision,destination_id,actor_id,request_id,request_hash,mode,backup_id,restore_operation_id,predecessor_id,state,runner_id,daemon_id,blob,plan,plan_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'queued',$13,$14,$15,$16,$17,$18,$18);

-- name: ReadDatabaseBackupRemoteDestination :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1;

-- name: FailDatabaseBackupTransferAuthority :exec
UPDATE hosting_database_backup_transfers SET state='failed',failure_code='authority_denied',updated_at=clock_timestamp() WHERE id=$1;

-- name: DispatchDatabaseBackupTransfer :exec
UPDATE hosting_database_backup_transfers SET state='dispatched',run_id=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: LockDatabaseBackupTransferAuthority :one
SELECT b.id FROM hosting_database_backup_transfers b JOIN task_runs t ON t.id=b.run_id JOIN run_leases l ON l.run_id=t.id WHERE b.id=$1 AND b.run_id=$2 AND b.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup_transfer','hosting_operation_id',b.id,'hosting_mode',b.mode,'inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: ReadDatabaseBackupTransferReceipt :one
SELECT body_hash,response FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1 AND phase=$2;

-- name: SetDatabaseBackupTransferMutationState :exec
UPDATE hosting_database_backup_transfers SET state=$2,failure_code=$3,daemon_id=$4,updated_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDatabaseBackupTransferTask :exec
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDatabaseBackupTransferLease :exec
UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active';

-- name: ReadDatabaseBackupSourceResource :one
SELECT source_resource_id FROM hosting_database_backups WHERE id=$1;

-- name: CreateDatabaseBackupTransferReceipt :exec
INSERT INTO hosting_database_backup_transfer_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response,expired_classification) VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: LockExpiredDatabaseBackupUploadRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.credential_hash=r.token_hash AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND sqlc.arg(capability)::text=ANY(r.capabilities) FOR SHARE OF r,e;

-- name: ExpiredDatabaseBackupUploadAllowed :one
SELECT EXISTS(SELECT 1 FROM run_leases l JOIN runners r ON r.id=l.runner_id JOIN runner_enrollments e ON e.runner_id=r.id JOIN hosting_database_backup_transfer_receipts i ON i.operation_id=$1 AND i.phase='intent' WHERE l.id=$2 AND l.run_id=$3 AND l.runner_id=$4 AND l.attempt=$5 AND l.fence=$6 AND l.expires_at<=clock_timestamp() AND l.status IN ('active','expired') AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND sqlc.arg(capability)::text=ANY(r.capabilities) AND i.authority=$7 AND i.body->>'plan_hash'=$8::text AND i.body->>'daemon_id'=$9::text AND NOT EXISTS(SELECT 1 FROM run_leases newer WHERE newer.run_id=l.run_id AND newer.attempt>l.attempt));

-- name: ListQueuedDatabaseBackupTransferIDs :many
SELECT id FROM hosting_database_backup_transfers WHERE state='queued' ORDER BY created_at,id LIMIT 100;

-- name: ReadDatabaseBackupTransferProject :one
SELECT project_id FROM hosting_database_backups WHERE id=$1;

-- name: ReadDatabaseBackupValidationProject :one
SELECT project_id FROM hosting_database_backup_targets WHERE id=$1;

-- name: ListDatabaseBackupTransferIDs :many
SELECT id FROM hosting_database_backup_transfers WHERE backup_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: ListDatabaseBackupValidationIDs :many
SELECT id FROM hosting_database_backup_transfers WHERE target_id=$1 AND mode='validate' ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: MySQLRemoteBackupSchemaPresent :one
SELECT (to_regprocedure(current_schema()||'.nerocd_mysql_remote_backup_schema_compatible()') IS NOT NULL)::boolean;

-- name: MySQLRemoteRestoreSchemaPresent :one
SELECT (to_regprocedure(current_schema()||'.nerocd_mysql_remote_restore_schema_compatible()') IS NOT NULL)::boolean;
