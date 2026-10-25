-- name: ReadDatabaseBackup :one
SELECT * FROM hosting_database_backups WHERE id=$1;
-- name: LockDatabaseBackup :one
SELECT * FROM hosting_database_backups WHERE id=$1 FOR UPDATE;
-- name: ListDatabaseBackups :many
SELECT * FROM hosting_database_backups WHERE source_resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;
-- name: FindDatabaseBackupRequest :one
SELECT * FROM hosting_database_backups WHERE source_resource_id=$1 AND mode=$2 AND backup_id=$3 AND request_id=$4;
-- name: ReadDatabaseBackupReceipt :one
SELECT * FROM hosting_database_backup_receipts WHERE operation_id=$1 AND phase=$2;
-- name: ReadDatabaseBackupByRun :one
SELECT * FROM hosting_database_backups WHERE run_id=$1;
-- name: DatabaseBackupSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_database_backups') IS NOT NULL)::boolean;
