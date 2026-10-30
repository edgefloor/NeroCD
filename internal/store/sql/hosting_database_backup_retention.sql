-- name: ReadDatabaseBackupRetentionPolicy :one
SELECT body FROM hosting_database_backup_retention_policies WHERE resource_id=$1 FOR SHARE;

-- name: ReadDatabaseBackupRetentionCopy :one
SELECT body,availability,identity_hash,location_hash FROM hosting_database_backup_copies WHERE id=$1 FOR SHARE;

-- name: ListDatabaseBackupRetentionExecutions :many
SELECT body FROM hosting_database_backup_retention_executions WHERE resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: MySQLBackupRetentionSchemaPresent :one
SELECT (to_regprocedure(current_schema()||'.nerocd_mysql_backup_retention_schema_compatible()') IS NOT NULL)::boolean;

-- name: MySQLComponentBackupRetentionSchemaPresent :one
SELECT (to_regprocedure(current_schema()||'.nerocd_mysql_component_retention_schema_compatible()') IS NOT NULL)::boolean;

-- name: ReadDatabaseBackupRetentionSourceScope :one
SELECT body FROM hosting_database_backup_retention_policies WHERE resource_id=$1;
