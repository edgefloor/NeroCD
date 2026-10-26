-- name: HostingNotificationSchemaCompatible :one
SELECT to_regclass(current_schema()||'.hosting_notification_channels') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_secrets') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_events') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_deliveries') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_attempts') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_requests') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_notification_source_receipts') IS NOT NULL
AND EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_notification_events') AND tgname='hosting_notification_event_source' AND tgenabled='O') AS compatible;

-- name: ReadNotificationSecret :one
SELECT status,envelope_version,key_id,ciphertext FROM hosting_notification_secrets WHERE project_id=$1 AND id=$2 AND version=$3;

-- name: LockNotificationProject :one
SELECT id FROM projects WHERE id=$1 FOR UPDATE;

-- name: ListNotificationMembers :many
SELECT u.email FROM project_members m JOIN users u ON u.id=m.user_id WHERE m.project_id=$1 AND m.user_id=ANY($2::text[]) AND u.status='active' AND u.archived_at IS NULL ORDER BY u.id;

-- name: ReadNotificationSaveRequest :one
SELECT request_hash,operation,response,secret_id,secret_version FROM hosting_notification_requests WHERE project_id=$1 AND request_id=$2;

-- name: LockNotificationChannel :one
SELECT id,project_id,name,type,enabled,generation,events,true AS configured,consecutive_failures,created_at,updated_at FROM hosting_notification_channels WHERE id=$1 AND project_id=$2 FOR UPDATE;

-- name: ReadNotificationChannelSecret :one
SELECT secret_id,secret_version FROM hosting_notification_channels WHERE id=$1;

-- name: InsertNotificationSecret :exec
INSERT INTO hosting_notification_secrets(id,version,project_id,status,envelope_version,key_id,ciphertext) VALUES($1,$2,$3,'active',$4,$5,$6);

-- name: CountNotificationChannels :one
SELECT count(*) FROM hosting_notification_channels WHERE project_id=$1;

-- name: CreateNotificationChannel :exec
INSERT INTO hosting_notification_channels(id,project_id,name,type,enabled,generation,events,secret_id,secret_version) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8);

-- name: UpdateNotificationChannel :exec
UPDATE hosting_notification_channels SET name=$2,enabled=$3,generation=generation+1,events=$4,secret_id=$5,secret_version=$6,consecutive_failures=0,updated_at=clock_timestamp() WHERE id=$1;

-- name: ReadNotificationChannel :one
SELECT id,project_id,name,type,enabled,generation,events,true AS configured,consecutive_failures,created_at,updated_at FROM hosting_notification_channels WHERE id=$1;

-- name: InsertNotificationSaveRequest :exec
INSERT INTO hosting_notification_requests(project_id,request_id,actor_id,operation,request_hash,response,secret_id,secret_version) VALUES($1,$2,$3,'save',$4,$5,$6,$7);

-- name: ListNotificationChannels :many
SELECT id,project_id,name,type,enabled,generation,events,true AS configured,consecutive_failures,created_at,updated_at FROM hosting_notification_channels WHERE project_id=$1 ORDER BY created_at,id LIMIT 100;

-- name: ListNotificationDeliveries :many
SELECT d.id,d.event_id,d.project_id,d.channel_id,d.channel_generation,e.event,e.source_kind,e.source_id,e.source_state,d.state,d.attempt_count,5::integer AS max_attempts,d.failure_code,e.local_copy_available,e.recovery_url,d.created_at,d.updated_at FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.project_id=$1 ORDER BY d.created_at DESC,d.id DESC LIMIT 100;

-- name: ReadNotificationDelivery :one
SELECT d.id,d.event_id,d.project_id,d.channel_id,d.channel_generation,e.event,e.source_kind,e.source_id,e.source_state,d.state,d.attempt_count,5::integer AS max_attempts,d.failure_code,e.local_copy_available,e.recovery_url,d.created_at,d.updated_at FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.project_id=$1 AND d.id=$2;

-- name: ListNotificationAttempts :many
SELECT number,state,failure_code,started_at,finished_at FROM hosting_notification_attempts WHERE delivery_id=$1 ORDER BY number;

-- name: ReadNotificationTestRequest :one
SELECT request_hash,operation,response FROM hosting_notification_requests WHERE project_id=$1 AND request_id=$2;

-- name: RecentNotificationTest :one
SELECT EXISTS(SELECT 1 FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.channel_id=$1 AND e.event='test' AND d.created_at>clock_timestamp()-INTERVAL '10 seconds');

-- name: ReadNotificationDeliveryByEvent :one
SELECT d.id,d.event_id,d.project_id,d.channel_id,d.channel_generation,e.event,e.source_kind,e.source_id,e.source_state,d.state,d.attempt_count,5::integer AS max_attempts,d.failure_code,e.local_copy_available,e.recovery_url,d.created_at,d.updated_at FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.event_id=$1;

-- name: InsertNotificationTestRequest :exec
INSERT INTO hosting_notification_requests(project_id,request_id,actor_id,operation,request_hash,response) VALUES($1,$2,$3,'test',$4,$5);

-- name: InsertNotificationEvent :one
INSERT INTO hosting_notification_events(id,project_id,event,source_kind,source_id,source_state,terminal_event,source_receipt,failure_code,local_copy_available,recovery_url) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(source_kind,source_id,terminal_event) DO NOTHING RETURNING id;

-- name: SelectNotificationChannels :many
SELECT id,generation,secret_id,secret_version FROM hosting_notification_channels WHERE project_id=$1 AND enabled AND ($2::text=ANY(events) OR $2::text='test') AND ($3::text='' OR id=$3::text) ORDER BY id FOR SHARE;

-- name: InsertNotificationDelivery :exec
INSERT INTO hosting_notification_deliveries(id,event_id,project_id,channel_id,channel_generation,secret_id,secret_version,state) VALUES($1,$2,$3,$4,$5,$6,$7,'pending') ON CONFLICT(event_id,channel_id,channel_generation) DO NOTHING;

-- name: ReleaseNotificationSessionLock :one
SELECT pg_advisory_unlock(hashtextextended('nerocd-notification:'||$1::text,0));

-- name: AcquireNotificationSessionLock :one
SELECT pg_try_advisory_lock(hashtextextended('nerocd-notification:'||$1::text,0));

-- name: LockNotificationDelivery :one
SELECT d.id,d.event_id,d.project_id,d.channel_id,d.channel_generation,e.event,e.source_kind,e.source_id,e.source_state,d.state,d.attempt_count,5::integer AS max_attempts,d.failure_code,e.local_copy_available,e.recovery_url,d.created_at,d.updated_at FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.id=$1 FOR UPDATE OF d;

-- name: ReadNotificationClaim :one
SELECT secret_id,secret_version,deadline_at,next_attempt_at,fence,claim_expires_at FROM hosting_notification_deliveries WHERE id=$1;

-- name: NotificationClock :one
SELECT clock_timestamp()::timestamptz;

-- name: ExpireNotificationAttempt :exec
UPDATE hosting_notification_attempts SET state='uncertain',failure_code='delivery_uncertain',finished_at=clock_timestamp() WHERE delivery_id=$1 AND number=$2 AND fence=$3 AND state='sending';

-- name: ExhaustNotificationBudget :exec
UPDATE hosting_notification_deliveries SET state='failed',failure_code='attempt_budget_exhausted',fence='',claim_expires_at=NULL,updated_at=clock_timestamp() WHERE id=$1;

-- name: ReadNotificationChannelForSend :one
SELECT type,generation,enabled,events,secret_id,secret_version FROM hosting_notification_channels WHERE id=$1 AND project_id=$2 FOR SHARE;

-- name: SuppressChangedNotification :exec
UPDATE hosting_notification_deliveries SET state='suppressed',failure_code='channel_changed',fence='',claim_expires_at=NULL,updated_at=clock_timestamp() WHERE id=$1;

-- name: SuppressRevokedNotification :exec
UPDATE hosting_notification_deliveries SET state='suppressed',failure_code='configuration_revoked',fence='',claim_expires_at=NULL,updated_at=clock_timestamp() WHERE id=$1;

-- name: ClaimNotificationDelivery :exec
UPDATE hosting_notification_deliveries SET state='sending',attempt_count=attempt_count+1,fence=$2,claim_expires_at=$3,updated_at=clock_timestamp() WHERE id=$1;

-- name: InsertNotificationAttempt :exec
INSERT INTO hosting_notification_attempts(delivery_id,number,fence,state) VALUES($1,$2,$3,'sending');

-- name: ReadNotificationEventFailure :one
SELECT failure_code FROM hosting_notification_events WHERE id=$1;

-- name: NotificationClaimCurrent :one
SELECT EXISTS(SELECT 1 FROM hosting_notification_deliveries d JOIN hosting_notification_channels c ON c.id=d.channel_id AND c.project_id=d.project_id JOIN hosting_notification_secrets s ON s.id=d.secret_id AND s.version=d.secret_version AND s.project_id=d.project_id JOIN hosting_notification_events e ON e.id=d.event_id WHERE d.id=$1 AND d.state='sending' AND d.fence=$2 AND d.claim_expires_at>clock_timestamp() AND d.deadline_at>clock_timestamp() AND d.attempt_count=$3 AND c.enabled AND c.generation=d.channel_generation AND c.secret_id=d.secret_id AND c.secret_version=d.secret_version AND s.status='active' AND (e.event='test' OR e.event=ANY(c.events)));

-- name: LockNotificationFinish :one
SELECT fence,state,attempt_count FROM hosting_notification_deliveries WHERE id=$1 FOR UPDATE;

-- name: FinishNotificationAttempt :exec
UPDATE hosting_notification_attempts SET state=$3,failure_code=$4,finished_at=clock_timestamp() WHERE delivery_id=$1 AND number=$2 AND fence=$5 AND state='sending';

-- name: FinishNotificationDelivery :exec
UPDATE hosting_notification_deliveries SET state=$2,failure_code=$3,fence='',claim_expires_at=NULL,next_attempt_at=clock_timestamp()+INTERVAL '10 seconds',updated_at=clock_timestamp() WHERE id=$1;

-- name: ResetNotificationFailureCount :exec
UPDATE hosting_notification_channels SET consecutive_failures=0 WHERE id=$1 AND generation=$2;

-- name: IncrementNotificationFailureCount :exec
UPDATE hosting_notification_channels SET consecutive_failures=LEAST(3,consecutive_failures+1),enabled=CASE WHEN consecutive_failures>=2 THEN false ELSE enabled END,updated_at=clock_timestamp() WHERE id=$1 AND generation=$2;

-- name: RecoverNotificationDeliveries :many
SELECT id FROM hosting_notification_deliveries WHERE (state='pending' AND next_attempt_at<=clock_timestamp()) OR (state='sending' AND claim_expires_at<=clock_timestamp()) ORDER BY next_attempt_at,id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: ReadNotificationBackupSource :one
SELECT b.project_id,b.source_resource_id,r.project_environment_id,b.mode,b.state,b.failure_code FROM hosting_database_backups b JOIN resources r ON r.id=b.source_resource_id WHERE b.id=$1;

-- name: ReadNotificationUploadSource :one
SELECT t.project_id,b.source_resource_id,r.project_environment_id,b.id,t.mode,t.state,t.failure_code,EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=b.id AND c.location='local' AND c.availability='available') FROM hosting_database_backup_transfers t JOIN hosting_database_backups b ON b.id=t.backup_id JOIN resources r ON r.id=b.source_resource_id WHERE t.id=$1;

-- name: InsertNotificationBackupSourceReceipt :exec
INSERT INTO hosting_notification_source_receipts(id,source_kind,source_id,reason,source_snapshot) SELECT $1,'backup',b.id,$3,jsonb_build_object('state',b.state,'failure_code',b.failure_code,'plan_hash',b.plan_hash) FROM hosting_database_backups b WHERE b.id=$2 AND b.mode='backup' AND b.state IN ('failed','uncertain') ON CONFLICT(id) DO NOTHING;

-- name: InsertNotificationUploadSourceReceipt :exec
INSERT INTO hosting_notification_source_receipts(id,source_kind,source_id,reason,source_snapshot) SELECT $1,'transfer',t.id,$3,jsonb_build_object('state',t.state,'failure_code',t.failure_code,'plan_hash',t.plan_hash) FROM hosting_database_backup_transfers t WHERE t.id=$2 AND t.mode='upload' AND t.state IN ('failed','uncertain') ON CONFLICT(id) DO NOTHING;
