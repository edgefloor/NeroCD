-- name: ReadDatabaseBackupTransfer :one
SELECT * FROM hosting_database_backup_transfers WHERE id=$1;

-- name: LockDatabaseBackupTransfer :one
SELECT * FROM hosting_database_backup_transfers WHERE id=$1 FOR UPDATE;
