-- name: CountDatabaseBackupSchedules :one
SELECT count(*) FROM hosting_database_backup_schedules WHERE resource_id=$1;

-- name: CreateDatabaseBackupSchedule :exec
INSERT INTO hosting_database_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,component,component_backup_version) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$9,$10,$11);

-- name: UpdateDatabaseBackupSchedule :exec
UPDATE hosting_database_backup_schedules SET actor_id=$2,revision=revision+1,frequency=$3,timezone=$4,enabled=$5,updated_at=clock_timestamp() WHERE id=$1;

-- name: CreateDatabaseBackupScheduleVersion :exec
INSERT INTO hosting_database_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,component,component_backup_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: CreateDatabaseBackupScheduleRequest :exec
INSERT INTO hosting_database_backup_schedule_requests(resource_id,request_id,actor_id,request_hash,schedule_id,revision,response) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: DatabaseBackupScheduleSchemaPresent :one
SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='hosting_database_backup_schedules' AND column_name='component_backup_version')::boolean;

-- name: ReadDatabaseBackupScheduleDiscoveryClock :one
SELECT clock_timestamp()::timestamptz AS backup_schedule_discovery_time;

-- name: ListDatabaseBackupSchedulesToReconcile :many
SELECT s.id FROM hosting_database_backup_schedules s WHERE s.enabled OR EXISTS(SELECT 1 FROM hosting_database_backup_schedule_occurrences o JOIN hosting_database_backups b ON b.id=o.operation_id WHERE o.schedule_id=s.id AND o.state='admitted' AND b.state='queued') ORDER BY s.checked_at,s.id LIMIT 100;

-- name: ReadDatabaseBackupScheduleClock :one
SELECT clock_timestamp()::timestamptz;

-- name: CheckDatabaseBackupSchedule :exec
UPDATE hosting_database_backup_schedules SET checked_at=$2 WHERE id=$1;

-- name: AdvanceDatabaseBackupSchedule :exec
UPDATE hosting_database_backup_schedules SET last_scheduled_at=$2 WHERE id=$1;

-- name: DatabaseBackupSourceBusy :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backups WHERE ownership_held AND mode='backup' AND source_binding_id=$1);

-- name: CreateDatabaseBackupScheduleOccurrence :exec
INSERT INTO hosting_database_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ListRecoverableDatabaseBackupOccurrences :many
SELECT o.id,o.operation_id,o.scheduled_at FROM hosting_database_backup_schedule_occurrences o JOIN hosting_database_backups b ON b.id=o.operation_id WHERE o.schedule_id=$1 AND o.state='admitted' AND o.enqueued_at<$2 AND b.state='queued' AND b.run_id IS NULL ORDER BY o.scheduled_at DESC LIMIT 100;

-- name: RecordUnreleasedDatabaseBackupRetirement :exec
INSERT INTO hosting_database_backup_schedule_retirements(operation_id,occurrence_id,plan_hash,reason,ownership_held) VALUES($1,$2,$3,$4,true);

-- name: FailExpiredDatabaseBackup :exec
UPDATE hosting_database_backups SET state='failed',failure_code='interrupted',updated_at=$2 WHERE id=$1 AND state='queued' AND run_id IS NULL;

-- name: RecordReleasedDatabaseBackupRetirement :exec
INSERT INTO hosting_database_backup_schedule_retirements(operation_id,occurrence_id,plan_hash,reason,ownership_held) VALUES($1,$2,$3,$4,false);

-- name: ReleaseExpiredDatabaseBackup :exec
UPDATE hosting_database_backups SET state='failed',ownership_held=false,failure_code='interrupted',updated_at=$2 WHERE id=$1 AND state='queued' AND run_id IS NULL;

-- name: MissDatabaseBackupOccurrence :exec
UPDATE hosting_database_backup_schedule_occurrences SET state='missed',reason=$2 WHERE id=$1;

-- name: EnqueueDatabaseBackupOccurrence :exec
UPDATE hosting_database_backup_schedule_occurrences SET enqueued_at=$2 WHERE id=$1;

-- name: ListUnscheduledDatabaseBackupsToRecover :many
SELECT b.id FROM hosting_database_backups b WHERE b.state IN ('queued','waiting_target') AND b.run_id IS NULL AND b.updated_at<clock_timestamp()-interval '60 minutes' AND b.created_at>=clock_timestamp()-interval '24 hours' AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_schedule_occurrences o WHERE o.operation_id=b.id) ORDER BY b.updated_at,b.id LIMIT 100 FOR UPDATE OF b SKIP LOCKED;

-- name: TouchDatabaseBackup :exec
UPDATE hosting_database_backups SET updated_at=clock_timestamp() WHERE id=$1;

-- name: ReadDatabaseBackupScheduleRemoteSelection :one
SELECT target_id,target_revision FROM hosting_database_backup_schedule_remote_targets WHERE schedule_id=$1 AND schedule_revision=$2;

-- name: ReadDatabaseBackupScheduleResourceProject :one
SELECT project_id FROM resources WHERE id=$1;

-- name: CreateDatabaseBackupScheduleRemoteSelection :exec
INSERT INTO hosting_database_backup_schedule_remote_targets(schedule_id,schedule_revision,target_id,target_revision) VALUES($1,$2,$3,$4);

-- name: CreateEmptyDatabaseBackupScheduleRemoteSelection :exec
INSERT INTO hosting_database_backup_schedule_remote_targets(schedule_id,schedule_revision,target_id,target_revision) VALUES($1,$2,$3,$4);
