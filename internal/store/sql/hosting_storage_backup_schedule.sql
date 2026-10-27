-- name: ReadStorageBackupSchedule :one
SELECT * FROM hosting_storage_backup_schedules WHERE id=$1;
-- name: LockStorageBackupSchedule :one
SELECT * FROM hosting_storage_backup_schedules WHERE id=$1 FOR UPDATE;
-- name: ListStorageBackupSchedules :many
SELECT * FROM hosting_storage_backup_schedules WHERE resource_id=$1 ORDER BY created_at,id LIMIT 100;
-- name: ReadStorageBackupScheduleRequest :one
SELECT * FROM hosting_storage_backup_schedule_requests WHERE resource_id=$1 AND request_id=$2;
-- name: ListStorageBackupOccurrences :many
SELECT o.id,o.schedule_id,o.schedule_revision,o.scheduled_at,o.operation_id,o.created_at,
 s.project_id,s.resource_id,coalesce(s.source_volume,'')::text AS volume,v.frequency,v.timezone,
 coalesce(b.id,'')::text AS backup_id,
 (CASE WHEN o.state='admitted' THEN op.state ELSE o.state END)::text AS state,
 (CASE WHEN o.state='admitted' THEN coalesce(r.body->'result'->>'failure_code','') ELSE o.reason END)::text AS reason,
 coalesce(bs.hold_operation_id=op.id OR EXISTS(SELECT 1 FROM hosting_storage_archive_plans recovery WHERE recovery.operation_id=bs.hold_operation_id AND recovery.recovery_of_operation_id=op.id),false)::boolean AS ownership_held
 FROM hosting_storage_backup_schedule_occurrences o
 JOIN hosting_storage_backup_schedules s ON s.id=o.schedule_id
 JOIN hosting_storage_backup_schedule_versions v ON v.schedule_id=o.schedule_id AND v.revision=o.schedule_revision
 LEFT JOIN hosting_operations op ON op.id=o.operation_id
 LEFT JOIN hosting_storage_backups b ON b.operation_id=op.id
 LEFT JOIN hosting_binding_state bs ON bs.binding_id=s.binding_id
 LEFT JOIN hosting_storage_archive_receipts r ON r.operation_id=op.id AND r.phase='complete'
 WHERE o.schedule_id=$1 ORDER BY o.scheduled_at DESC,o.id DESC LIMIT 100;

-- name: ReadStorageBackupScheduleSelection :one
SELECT remote_target FROM hosting_storage_backup_schedule_versions WHERE schedule_id=$1 AND revision=$2;
-- name: CountStorageBackupSchedules :one
SELECT count(*) FROM hosting_storage_backup_schedules WHERE resource_id=$1;
-- name: CreateStorageBackupSchedule :exec
INSERT INTO hosting_storage_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,storage_id,source_reference_hash,source_family,source_volume,acknowledge_interruption) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$9,$10,$11,$12,$13,$14);
-- name: UpdateStorageBackupSchedule :exec
UPDATE hosting_storage_backup_schedules SET actor_id=$2,revision=revision+1,frequency=$3,timezone=$4,enabled=$5,updated_at=clock_timestamp() WHERE id=$1;
-- name: InsertStorageBackupScheduleVersion :exec
INSERT INTO hosting_storage_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,remote_target,acknowledge_interruption) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
-- name: InsertStorageBackupScheduleRequest :exec
INSERT INTO hosting_storage_backup_schedule_requests(resource_id,request_id,actor_id,request_hash,schedule_id,revision,response) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: StorageBackupScheduleSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_storage_backup_schedules') IS NOT NULL)::boolean AS present;
-- name: StorageBackupScheduleDiscoveryClock :one
SELECT clock_timestamp()::timestamptz AS discovery_time;
-- name: DiscoverStorageBackupSchedules :many
SELECT id FROM hosting_storage_backup_schedules WHERE enabled ORDER BY checked_at,id LIMIT 100;
-- name: TouchStorageBackupSchedule :exec
UPDATE hosting_storage_backup_schedules SET checked_at=clock_timestamp() WHERE id=$1;
-- name: AdvanceStorageBackupSchedule :exec
UPDATE hosting_storage_backup_schedules SET last_scheduled_at=$2 WHERE id=$1;
-- name: InsertStorageBackupOccurrence :exec
INSERT INTO hosting_storage_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason) VALUES($1,$2,$3,$4,$5,$6,$7);
