-- name: StorageRetentionLockPool :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-storage-copy-admission',0));

-- name: StorageRetentionReadPolicy :one
SELECT body FROM hosting_storage_backup_retention_policies WHERE resource_id=$1 FOR SHARE;

-- name: StorageRetentionReadRequest :one
SELECT actor_id,request_hash,response FROM hosting_storage_backup_retention_requests WHERE resource_id=$1 AND kind=$2 AND request_id=$3;

-- name: StorageRetentionInsertRequest :exec
INSERT INTO hosting_storage_backup_retention_requests(resource_id,kind,request_id,actor_id,request_hash,response) VALUES($1,$2,$3,$4,$5,$6);

-- name: StorageRetentionSavePolicy :exec
INSERT INTO hosting_storage_backup_retention_policies(resource_id,project_id,actor_id,revision,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(resource_id) DO UPDATE SET actor_id=EXCLUDED.actor_id,revision=EXCLUDED.revision,body=EXCLUDED.body;

-- name: StorageRetentionPreviewTime :one
SELECT clock_timestamp()::timestamptz AS evaluated_at;

-- name: StorageRetentionInsertPreview :exec
INSERT INTO hosting_storage_backup_retention_previews(id,resource_id,actor_id,policy_revision,body,copies,plan_hash) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: StorageRetentionListExecutions :many
SELECT body FROM hosting_storage_backup_retention_executions WHERE resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: StorageRetentionReadExecutionBody :one
SELECT body FROM hosting_storage_backup_retention_executions WHERE id=$1 AND resource_id=$2;

-- name: StorageRetentionFindExecution :one
SELECT id FROM hosting_storage_backup_retention_executions WHERE preview_id=$1 AND resource_id=$2;

-- name: StorageRetentionReadPreview :one
SELECT body,copies FROM hosting_storage_backup_retention_previews WHERE id=$1 AND resource_id=$2 AND policy_revision=$3;

-- name: StorageRetentionInsertExecution :exec
INSERT INTO hosting_storage_backup_retention_executions(id,resource_id,actor_id,runner_id,preview_id,plan,plan_hash,body,reservation_held) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true);

-- name: StorageRetentionReadCopyLocation :one
SELECT location_hash FROM hosting_storage_backup_copies WHERE id=$1;

-- name: StorageRetentionInsertItem :exec
INSERT INTO hosting_storage_backup_retention_items(execution_id,copy_id,identity_hash,location_hash,state,reservation_held) VALUES($1,$2,$3,$4,'pending',true);

-- name: StorageRetentionReserveCopy :exec
UPDATE hosting_storage_backup_copies SET availability='reserved' WHERE id=$1;

-- name: StorageRetentionLockRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments n ON n.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND n.credential_hash=r.token_hash AND n.used_at IS NOT NULL AND n.revoked_at IS NULL AND $2::text=ANY(r.capabilities) AND $3::text=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) FOR SHARE OF r,n;

-- name: StorageRetentionLockPlacement :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 FOR SHARE OF d,s;

-- name: StorageRetentionActiveUse :one
SELECT COALESCE((EXISTS(SELECT 1 FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id WHERE p.body->>'runner_id'=$1::text AND (o.state IN ('queued','dispatched','preparing','prepared','applying') OR p.backup_id=$2::text AND (o.state='reconciliation_required' OR EXISTS(SELECT 1 FROM hosting_binding_state WHERE hold_operation_id=o.id)))) OR EXISTS(SELECT 1 FROM hosting_storage_backup_transfers WHERE runner_id=$1::text AND (state IN ('queued','dispatched','applying') OR backup_id=$2::text AND state='uncertain'))),false)::boolean AS active;

-- name: StorageRetentionReadReusableLocal :one
SELECT body FROM hosting_storage_backup_copies WHERE backup_id=$1 AND location='local' AND location_hash=$2 AND superseded_by_copy_id IS NULL AND availability IN ('available','unproven');

-- name: StorageRetentionSupersedeLocal :exec
UPDATE hosting_storage_backup_copies SET superseded_by_copy_id=$2 WHERE backup_id=$1 AND location='local' AND superseded_by_copy_id IS NULL AND availability IN ('available','unproven','deleted');

-- name: StorageRetentionInsertCopy :exec
INSERT INTO hosting_storage_backup_copies(id,backup_id,transfer_id,resource_id,runner_id,location,identity_hash,location_hash,body,availability,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(id) DO NOTHING;

-- name: StorageRetentionLegacyLocal :many
SELECT b.operation_id FROM hosting_storage_backups b JOIN hosting_operations o ON o.id=b.operation_id WHERE b.resource_id=$1 AND o.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies c WHERE c.backup_id=b.id AND c.location='local') LIMIT 100001;

-- name: StorageRetentionLegacyRemote :many
SELECT t.id FROM hosting_storage_backup_transfers t JOIN hosting_storage_backups b ON b.id=t.backup_id WHERE b.resource_id=$1 AND t.mode='upload' AND t.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies c WHERE c.transfer_id=t.id) LIMIT 100001;

-- name: StorageRetentionInventory :many
SELECT c.body,c.availability,COALESCE((EXISTS(SELECT 1 FROM hosting_storage_backup_copies other WHERE other.id<>c.id AND other.location_hash=c.location_hash AND other.availability<>'deleted' AND other.superseded_by_copy_id IS NULL) OR (c.location='local' AND EXISTS(SELECT 1 FROM hosting_storage_archive_blobs blob JOIN hosting_storage_backups b ON b.id=blob.backup_id JOIN hosting_storage_archive_plans plan ON plan.operation_id=b.operation_id WHERE b.id<>c.backup_id AND blob.body->>'sha256'=c.body->'item'->>'digest' AND plan.body->>'runner_id'=c.runner_id AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies known WHERE known.backup_id=b.id AND known.location='local' AND known.availability='deleted'))) OR (c.location='remote' AND EXISTS(SELECT 1 FROM hosting_storage_backup_transfers transfer WHERE transfer.id<>c.transfer_id AND transfer.mode='upload' AND transfer.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies prior WHERE prior.transfer_id=transfer.id AND prior.availability='deleted') AND nerocd_backup_remote_scope(transfer.plan->'target'->'target'->>'endpoint',transfer.plan->'target'->'target'->>'bucket',transfer.plan->>'object_key')=nerocd_backup_remote_scope(c.body->'target'->'target'->>'endpoint',c.body->'target'->'target'->>'bucket',c.body->>'object_key')))),false)::boolean AS shared,EXISTS(SELECT 1 FROM hosting_storage_backup_retention_items i WHERE i.location_hash=c.location_hash AND i.reservation_held) AS reserved FROM hosting_storage_backup_copies c WHERE c.resource_id=$1 AND c.availability<>'deleted' AND c.superseded_by_copy_id IS NULL ORDER BY c.created_at,c.id LIMIT 100001;

-- name: StorageRetentionRestoreDenied :one
SELECT COALESCE((EXISTS(SELECT 1 FROM hosting_storage_backup_copies a WHERE a.backup_id=sqlc.arg(backup_id) AND a.location=sqlc.arg(location) AND (sqlc.arg(transfer_id)::text='' OR a.transfer_id=sqlc.arg(transfer_id)) AND a.superseded_by_copy_id IS NULL) AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies b WHERE b.backup_id=sqlc.arg(backup_id) AND b.location=sqlc.arg(location) AND (sqlc.arg(transfer_id)::text='' OR b.transfer_id=sqlc.arg(transfer_id)) AND b.superseded_by_copy_id IS NULL AND b.availability IN ('available','unproven'))),false)::boolean AS denied;

-- name: StorageRetentionLockAuthority :one
SELECT e.id FROM hosting_storage_backup_retention_executions e JOIN task_runs t ON t.id=e.run_id JOIN run_leases l ON l.run_id=t.id WHERE e.id=$1 AND e.run_id=$2 AND e.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','storage_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: StorageRetentionDispatch :exec
UPDATE hosting_storage_backup_retention_executions SET run_id=$2,body=$3 WHERE id=$1;

-- name: StorageRetentionReadReceipt :one
SELECT body_hash,response FROM hosting_storage_backup_retention_receipts WHERE execution_id=$1 AND copy_id=$2 AND phase=$3 AND attempt=$4;

-- name: StorageRetentionSetCopyAvailability :exec
UPDATE hosting_storage_backup_copies SET availability=$2 WHERE id=$1;

-- name: StorageRetentionSetItem :exec
UPDATE hosting_storage_backup_retention_items SET state=$3,reservation_held=$4 WHERE execution_id=$1 AND copy_id=$2;

-- name: StorageRetentionCompleteTask :exec
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: StorageRetentionCompleteLease :exec
UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active';

-- name: StorageRetentionSetExecution :exec
UPDATE hosting_storage_backup_retention_executions SET body=$2,reservation_held=$3 WHERE id=$1;

-- name: StorageRetentionInsertReceipt :exec
INSERT INTO hosting_storage_backup_retention_receipts(execution_id,copy_id,phase,attempt,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: StorageRetentionRecoverQueued :many
SELECT id FROM hosting_storage_backup_retention_executions WHERE body->>'state'='queued' ORDER BY created_at,id LIMIT 100;

-- name: StorageRetentionCredentialDeadline :one
SELECT least(l.expires_at,clock_timestamp()+interval '1 minute')::timestamptz AS use_until FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r;

-- name: StorageRetentionReadDelivery :one
SELECT metadata FROM hosting_storage_backup_retention_deliveries WHERE delivery_id=$1;

-- name: StorageRetentionInsertDelivery :exec
INSERT INTO hosting_storage_backup_retention_deliveries(delivery_id,execution_id,copy_id,metadata,use_until) VALUES($1,$2,$3,$4,$5);

-- name: StorageRetentionDeliveryLive :one
SELECT COALESCE(($1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz),false)::boolean AS valid;

-- name: StorageRetentionReadLiveDelivery :one
SELECT metadata FROM hosting_storage_backup_retention_deliveries WHERE delivery_id=$1 AND execution_id=$2 AND copy_id=$3 AND use_until>clock_timestamp() AND use_until<=$4;

-- name: StorageRetentionReadResource :one
SELECT resource_id FROM hosting_storage_backup_retention_executions WHERE id=$1;

-- name: StorageRetentionLeaseLive :one
SELECT COALESCE((EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp())),false)::boolean AS live;

-- name: StorageRetentionNextAttempt :one
SELECT next_attempt FROM task_runs WHERE id=$1;

-- name: StorageRetentionExpireLeases :exec
UPDATE run_leases SET status='expired',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active' AND expires_at<=clock_timestamp();

-- name: StorageRetentionInsertResume :exec
INSERT INTO hosting_storage_backup_retention_resumes(execution_id,next_attempt,request_id,actor_id,plan_hash) VALUES($1,$2,$3,$4,$5);

-- name: StorageRetentionRequeueTask :exec
UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL,claim_order_at=clock_timestamp() WHERE id=$1;

-- name: StorageRetentionSetExecutionBody :exec
UPDATE hosting_storage_backup_retention_executions SET body=$2 WHERE id=$1;

-- name: StorageRetentionReadRetirement :one
SELECT a.next_attempt FROM hosting_storage_backup_retention_resumes a JOIN run_leases old ON old.run_id=$2 AND old.id=$3 WHERE a.execution_id=$1 AND a.next_attempt>$4 AND a.plan_hash=$5 AND old.attempt=$4 AND old.fence=$6 AND (old.status<>'active' OR old.expires_at<=clock_timestamp()) ORDER BY a.next_attempt LIMIT 1;

-- name: StorageRetentionPublish :exec
INSERT INTO hosting_storage_backup_retention_publications(id,resource_id,actor_id,policy_revision) SELECT $1,p.resource_id,p.actor_id,p.revision FROM hosting_storage_backup_retention_policies p WHERE p.resource_id=$2 AND p.body->>'enabled'='true' AND p.body->>'automatic'='true' ON CONFLICT DO NOTHING;

-- name: StorageRetentionPendingPublications :many
SELECT id,resource_id,actor_id,policy_revision FROM hosting_storage_backup_retention_publications WHERE state='pending' ORDER BY created_at,id LIMIT 50;

-- name: StorageRetentionSetPublication :exec
UPDATE hosting_storage_backup_retention_publications SET state=$2,reason=$3 WHERE id=$1 AND state='pending';

-- name: StorageRetentionReadExecution :one
SELECT plan,body,plan_hash,run_id FROM hosting_storage_backup_retention_executions WHERE id=$1;

-- name: StorageRetentionLockExecution :one
SELECT plan,body,plan_hash,run_id FROM hosting_storage_backup_retention_executions WHERE id=$1 FOR UPDATE;

-- name: StorageRetentionBackupCopies :many
SELECT body,availability,(superseded_by_copy_id IS NOT NULL)::boolean AS superseded FROM hosting_storage_backup_copies WHERE backup_id=$1 ORDER BY created_at,id;

-- name: StorageRetentionBackupResource :one
SELECT resource_id FROM hosting_storage_backups WHERE id=$1;

-- name: StorageRetentionDeferCopyProof :exec
SELECT nerocd_storage_retention_defer_copy();

-- name: StorageRetentionLegacyTransfersForBackup :many
SELECT t.id FROM hosting_storage_backup_transfers t WHERE t.backup_id=$1 AND t.mode='upload' AND t.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies c WHERE c.transfer_id=t.id) ORDER BY t.created_at,t.id;
