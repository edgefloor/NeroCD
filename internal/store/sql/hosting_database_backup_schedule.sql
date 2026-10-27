-- name: ReadDatabaseBackupSchedule :one
SELECT * FROM hosting_database_backup_schedules WHERE id=$1;
-- name: LockDatabaseBackupSchedule :one
SELECT * FROM hosting_database_backup_schedules WHERE id=$1 FOR UPDATE;
-- name: ListDatabaseBackupSchedules :many
SELECT * FROM hosting_database_backup_schedules WHERE resource_id=$1 ORDER BY created_at,id LIMIT 100;
-- name: ReadDatabaseBackupScheduleRequest :one
SELECT * FROM hosting_database_backup_schedule_requests WHERE resource_id=$1 AND request_id=$2;
-- name: ListDatabaseBackupOccurrences :many
SELECT o.id,o.schedule_id,o.schedule_revision,o.scheduled_at,o.operation_id,o.created_at,
 s.project_id,s.resource_id,v.frequency,v.timezone,v.component,
 (CASE WHEN o.state='admitted' THEN b.state ELSE o.state END)::text AS state,
 (CASE WHEN o.state='admitted' THEN coalesce(ret.reason,b.failure_code) ELSE o.reason END)::text AS reason,
 coalesce(b.ownership_held,false)::boolean AS ownership_held
 FROM hosting_database_backup_schedule_occurrences o
 JOIN hosting_database_backup_schedules s ON s.id=o.schedule_id
 JOIN hosting_database_backup_schedule_versions v ON v.schedule_id=o.schedule_id AND v.revision=o.schedule_revision
 LEFT JOIN hosting_database_backups b ON b.id=o.operation_id
 LEFT JOIN hosting_database_backup_schedule_retirements ret ON ret.operation_id=b.id
 WHERE o.schedule_id=$1 ORDER BY o.scheduled_at DESC,o.id DESC LIMIT 100;
