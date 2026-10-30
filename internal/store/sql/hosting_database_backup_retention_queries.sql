-- name: LockDatabaseBackupCopyAdmission :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-backup-copy-admission',0));

-- name: LockDatabaseBackupRetentionPolicy :one
SELECT body FROM hosting_database_backup_retention_policies WHERE resource_id=$1 FOR SHARE;

-- name: ReadDatabaseBackupRetentionRequest :one
SELECT actor_id,request_hash,response FROM hosting_database_backup_retention_requests WHERE resource_id=$1 AND kind=$2 AND request_id=$3;

-- name: CreateDatabaseBackupRetentionRequest :exec
INSERT INTO hosting_database_backup_retention_requests(resource_id,kind,request_id,actor_id,request_hash,response) VALUES($1,$2,$3,$4,$5,$6);

-- name: SaveDatabaseBackupRetentionPolicy :exec
INSERT INTO hosting_database_backup_retention_policies(resource_id,project_id,actor_id,revision,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(resource_id) DO UPDATE SET actor_id=EXCLUDED.actor_id,revision=EXCLUDED.revision,body=EXCLUDED.body;

-- name: FindOwnedDatabaseBackupLocalCopy :one
SELECT body FROM hosting_database_backup_copies WHERE location_hash=$1 AND superseded_by_copy_id IS NULL AND availability IN ('available','reserved') AND body->'local'->>'created'='true' LIMIT 1;

-- name: LockDatabaseBackupLocalCopyPredecessor :one
SELECT id,body FROM hosting_database_backup_copies c WHERE c.backup_id=$1 AND c.location='local' AND location_hash=$2 AND availability<>'deleted' AND superseded_by_copy_id IS NULL FOR UPDATE;

-- name: DeferDatabaseBackupCopySupersession :exec
SELECT nerocd_database_backup_defer_copy_supersession();

-- name: SupersedeDatabaseBackupCopy :exec
UPDATE hosting_database_backup_copies SET superseded_by_copy_id=$2 WHERE id=$1;

-- name: CreateDatabaseBackupLocalCopy :exec
INSERT INTO hosting_database_backup_copies(id,backup_id,resource_id,runner_id,location,identity_hash,location_hash,body,availability,created_at) VALUES($1,$2,$3,$4,'local',$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING;

-- name: FindOwnedDatabaseBackupRemoteCopy :one
SELECT body FROM hosting_database_backup_copies WHERE location_hash=$1 AND superseded_by_copy_id IS NULL AND availability IN ('available','reserved') AND body->'remote'->>'created'='true' LIMIT 1;

-- name: ReadDatabaseBackupCopySource :one
SELECT source_resource_id,created_at FROM hosting_database_backups WHERE id=$1;

-- name: CreateDatabaseBackupRemoteCopy :exec
INSERT INTO hosting_database_backup_copies(id,backup_id,transfer_id,resource_id,runner_id,location,identity_hash,location_hash,body,availability,created_at) VALUES($1,$2,$3,$4,$5,'remote',$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING;

-- name: ListLegacyDatabaseBackupIDs :many
SELECT id FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup' AND state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=hosting_database_backups.id AND c.location='local') ORDER BY id LIMIT 100001;

-- name: ListLegacyDatabaseBackupUploadIDs :many
SELECT t.id FROM hosting_database_backup_transfers t JOIN hosting_database_backups b ON b.id=t.backup_id WHERE b.source_resource_id=$1 AND t.mode='upload' AND t.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.transfer_id=t.id) ORDER BY t.id LIMIT 100001;

-- name: ListDatabaseBackupRetentionInventory :many
SELECT c.body,c.availability,
 ((SELECT count(DISTINCT other.id) FROM hosting_database_backup_copies other WHERE other.availability<>'deleted' AND other.superseded_by_copy_id IS NULL AND other.id<>c.id AND other.location_hash=c.location_hash)
 + (SELECT count(*) FROM hosting_database_backups other WHERE c.location='local' AND other.mode='backup' AND other.id<>c.backup_id AND other.state='succeeded' AND other.blob->>'digest'=c.body->'item'->>'digest' AND other.plan->'source'->'snapshot'->>'server_id'=c.body->>'server_id' AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies known WHERE known.backup_id=other.id AND known.location='local' AND known.availability='deleted'))
 + (SELECT count(*) FROM hosting_database_backup_transfers other WHERE c.location='remote' AND other.id<>c.transfer_id AND other.mode='upload' AND other.state='succeeded' AND nerocd_backup_remote_scope(other.plan->'target'->'target'->>'endpoint',other.plan->'target'->'target'->>'bucket',other.plan->>'object_key')=nerocd_backup_remote_scope(c.body->'target'->'target'->>'endpoint',c.body->'target'->'target'->>'bucket',c.body->>'object_key')))::bigint AS shared_references,
 EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.runner_id=c.runner_id AND b.ownership_held UNION ALL SELECT 1 FROM hosting_database_backup_downloads d WHERE d.runner_id=c.runner_id AND d.state IN ('waiting','streaming') AND d.expires_at>clock_timestamp()) AS active,
 EXISTS(SELECT 1 FROM hosting_database_backup_transfers t WHERE t.backup_id=c.backup_id AND t.mode='upload' AND t.state IN ('queued','dispatched','applying','failed','uncertain')) AS upload_pending,
 EXISTS(SELECT 1 FROM hosting_database_backup_retention_items i WHERE i.location_hash=c.location_hash AND i.reservation_held) AS reserved
 FROM hosting_database_backup_copies c WHERE c.resource_id=$1 AND c.availability<>'deleted' AND c.superseded_by_copy_id IS NULL ORDER BY c.created_at,c.id LIMIT 100001;

-- name: CreateDatabaseBackupRetentionPreview :exec
INSERT INTO hosting_database_backup_retention_previews(id,resource_id,actor_id,policy_revision,body,copies,plan_hash) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ListDatabaseBackupRetentionExecutionBodies :many
SELECT body FROM hosting_database_backup_retention_executions WHERE resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: ReadDatabaseBackupRetentionExecutionBody :one
SELECT body FROM hosting_database_backup_retention_executions WHERE id=$1 AND resource_id=$2;

-- name: FindDatabaseBackupRetentionPreviewExecution :one
SELECT id FROM hosting_database_backup_retention_executions WHERE preview_id=$1 AND resource_id=$2;

-- name: ReadDatabaseBackupRetentionPreview :one
SELECT body,copies FROM hosting_database_backup_retention_previews WHERE id=$1 AND resource_id=$2 AND policy_revision=$3;

-- name: DatabaseBackupRetentionRunnerBusy :one
SELECT (EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.runner_id=$1 AND b.ownership_held) OR EXISTS(SELECT 1 FROM hosting_database_backup_transfers t WHERE t.runner_id=$1 AND t.mode IN ('upload','download') AND state IN ('queued','dispatched','applying','uncertain')))::boolean;

-- name: CreateDatabaseBackupRetentionExecution :exec
INSERT INTO hosting_database_backup_retention_executions(id,resource_id,actor_id,runner_id,preview_id,plan,plan_hash,body,reservation_held) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true);

-- name: ReadDatabaseBackupCopyLocation :one
SELECT location_hash FROM hosting_database_backup_copies WHERE id=$1;

-- name: CreateDatabaseBackupRetentionItem :exec
INSERT INTO hosting_database_backup_retention_items(execution_id,copy_id,identity_hash,location_hash,state,reservation_held) VALUES($1,$2,$3,$4,'pending',true);

-- name: ReserveDatabaseBackupCopy :exec
UPDATE hosting_database_backup_copies SET availability='reserved' WHERE id=$1;

-- name: LockDatabaseBackupRetentionRunner :one
SELECT r.id FROM runners r JOIN runner_enrollments n ON n.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND n.credential_hash=r.token_hash AND n.used_at IS NOT NULL AND n.revoked_at IS NULL AND $2::text=ANY(r.capabilities) AND $3::text=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) FOR SHARE OF r,n;

-- name: LockDatabaseBackupRetentionDestination :one
SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 FOR SHARE OF d,s;

-- name: DatabaseBackupLocalArchiveDenied :one
SELECT (EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=$1 AND c.location='local') AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=$1 AND c.location='local' AND superseded_by_copy_id IS NULL AND availability IN ('available','unproven')))::boolean;

-- name: DatabaseBackupRemoteArchiveDenied :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backup_copies WHERE location_hash=$1 AND availability NOT IN ('available','unproven'));

-- name: CreateDatabaseBackupRetentionPublication :exec
INSERT INTO hosting_database_backup_retention_publications(id,resource_id,actor_id,policy_revision) SELECT $1,p.resource_id,p.actor_id,p.revision FROM hosting_database_backup_retention_policies p WHERE p.resource_id=$2 AND body->>'enabled'='true' AND body->>'automatic'='true' ON CONFLICT DO NOTHING;

-- name: ListPendingDatabaseBackupRetentionPublications :many
SELECT id,resource_id,actor_id,policy_revision FROM hosting_database_backup_retention_publications WHERE state='pending' ORDER BY created_at,id LIMIT 50;

-- name: SetDatabaseBackupRetentionPublicationState :exec
UPDATE hosting_database_backup_retention_publications SET state=$2,reason=$3 WHERE id=$1 AND state='pending';

-- name: ReadDatabaseBackupRetentionCredentialExpiry :one
SELECT least(l.expires_at,clock_timestamp()+interval '1 minute')::timestamptz FROM run_leases l JOIN runners r ON r.id=l.runner_id WHERE l.id=$1 AND r.id=$2 AND r.token_hash=$3 AND r.token_hash<>'' AND r.status='active' FOR SHARE OF r;

-- name: ReadDatabaseBackupRetentionDelivery :one
SELECT metadata FROM hosting_database_backup_retention_deliveries WHERE delivery_id=$1;

-- name: CreateDatabaseBackupRetentionDelivery :exec
INSERT INTO hosting_database_backup_retention_deliveries(delivery_id,execution_id,copy_id,metadata,use_until) VALUES($1,$2,$3,$4,$5);

-- name: DatabaseBackupRetentionDeliveryValid :one
SELECT ($1::timestamptz>clock_timestamp() AND $1::timestamptz<=$2::timestamptz)::boolean;

-- name: ReadValidDatabaseBackupRetentionDelivery :one
SELECT metadata FROM hosting_database_backup_retention_deliveries WHERE delivery_id=$1 AND execution_id=$2 AND copy_id=$3 AND use_until>clock_timestamp() AND use_until<=$4;

-- name: LockDatabaseBackupRetentionAuthority :one
SELECT e.id FROM hosting_database_backup_retention_executions e JOIN task_runs t ON t.id=e.run_id JOIN run_leases l ON l.run_id=t.id WHERE e.id=$1 AND e.run_id=$2 AND e.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: DispatchDatabaseBackupRetention :exec
UPDATE hosting_database_backup_retention_executions SET run_id=$2,body=$3 WHERE id=$1;

-- name: ReadDatabaseBackupRetentionReceipt :one
SELECT body_hash,response FROM hosting_database_backup_retention_receipts WHERE execution_id=$1 AND copy_id=$2 AND phase=$3 AND attempt=$4;

-- name: SetDatabaseBackupCopyAvailability :exec
UPDATE hosting_database_backup_copies SET availability=$2 WHERE id=$1;

-- name: SetDatabaseBackupRetentionItem :exec
UPDATE hosting_database_backup_retention_items SET state=$3,reservation_held=$4 WHERE execution_id=$1 AND copy_id=$2;

-- name: CompleteDatabaseBackupRetentionTask :exec
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDatabaseBackupRetentionLease :exec
UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active';

-- name: SetDatabaseBackupRetentionExecutionState :exec
UPDATE hosting_database_backup_retention_executions SET body=$2,reservation_held=$3 WHERE id=$1;

-- name: CreateDatabaseBackupRetentionReceipt :exec
INSERT INTO hosting_database_backup_retention_receipts(execution_id,copy_id,phase,attempt,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: ListQueuedDatabaseBackupRetentionIDs :many
SELECT id FROM hosting_database_backup_retention_executions WHERE body->>'state'='queued' ORDER BY created_at,id LIMIT 100;

-- name: ReadDatabaseBackupRetentionResource :one
SELECT resource_id FROM hosting_database_backup_retention_executions WHERE id=$1;

-- name: DatabaseBackupRetentionLeaseLive :one
SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp());

-- name: ReadDatabaseBackupRetentionNextAttempt :one
SELECT next_attempt FROM task_runs WHERE id=$1;

-- name: ExpireDatabaseBackupRetentionLeases :exec
UPDATE run_leases SET status='expired',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active' AND expires_at<=clock_timestamp();

-- name: CreateDatabaseBackupRetentionResume :exec
INSERT INTO hosting_database_backup_retention_resumes(execution_id,next_attempt,request_id,actor_id,plan_hash) VALUES($1,$2,$3,$4,$5);

-- name: QueueDatabaseBackupRetentionTask :exec
UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL,claim_order_at=clock_timestamp() WHERE id=$1;

-- name: UpdateDatabaseBackupRetentionExecutionBody :exec
UPDATE hosting_database_backup_retention_executions SET body=$2 WHERE id=$1;

-- name: ReadDatabaseBackupRetentionRetirement :one
SELECT a.next_attempt FROM hosting_database_backup_retention_resumes a JOIN run_leases old ON old.run_id=$2 AND old.id=$3 WHERE a.execution_id=$1 AND a.next_attempt>$4 AND a.plan_hash=$5 AND old.attempt=$4 AND old.fence=$6 AND (old.status<>'active' OR old.expires_at<=clock_timestamp()) ORDER BY a.next_attempt LIMIT 1;

-- name: ReadDatabaseBackupRetentionExecution :one
SELECT plan,body,plan_hash,run_id FROM hosting_database_backup_retention_executions WHERE id=$1;

-- name: LockDatabaseBackupRetentionExecution :one
SELECT plan,body,plan_hash,run_id FROM hosting_database_backup_retention_executions WHERE id=$1 FOR UPDATE;
