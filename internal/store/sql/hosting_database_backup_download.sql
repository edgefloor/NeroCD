-- name: InsertDatabaseDownload :exec
INSERT INTO hosting_database_backup_downloads(id,backup_id,actor_id,runner_id,run_id,plan,plan_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: ReadDatabaseDownload :one
SELECT * FROM hosting_database_backup_downloads WHERE id=$1;
-- name: LockDatabaseDownload :one
SELECT * FROM hosting_database_backup_downloads WHERE id=$1 FOR UPDATE;
-- name: CheckDatabaseDownloadAuthority :one
SELECT l.id FROM hosting_database_backup_downloads d JOIN task_runs t ON t.id=d.run_id JOIN run_leases l ON l.run_id=t.id WHERE d.id=$1 AND d.run_id=$2 AND d.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND l.status='active' AND l.expires_at>clock_timestamp() AND d.expires_at>clock_timestamp() AND d.state IN ('waiting','streaming') AND t.run_spec=jsonb_build_object('type','database_backup_transfer','hosting_operation_id',d.id,'hosting_mode','browser_download','inputs','{}'::jsonb) FOR UPDATE OF l;
-- name: BeginDatabaseDownload :execrows
UPDATE hosting_database_backup_downloads SET state='streaming',authority=$2 WHERE id=$1 AND state='waiting' AND expires_at>clock_timestamp();
-- name: FinishDatabaseDownload :exec
UPDATE hosting_database_backup_downloads SET state=$2 WHERE id=$1 AND state IN ('waiting','streaming');
-- name: DatabaseDownloadActive :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backup_downloads WHERE runner_id=$1 AND state IN ('waiting','streaming') AND expires_at>clock_timestamp())::boolean;
-- name: ReadDatabaseDownloadCopies :many
SELECT body,location_hash,availability,(superseded_by_copy_id IS NOT NULL)::boolean AS superseded FROM hosting_database_backup_copies WHERE backup_id=$1 AND location='local' ORDER BY created_at DESC,id;
-- name: CheckDatabaseDownloadReservation :one
SELECT EXISTS(SELECT 1 FROM hosting_database_backup_retention_items WHERE location_hash=$1 AND reservation_held)::boolean;
-- name: CancelDatabaseDownloadTask :exec
UPDATE task_runs SET status='failed',finished_at=clock_timestamp() WHERE id=$1 AND status IN ('queued','running');
-- name: CancelDatabaseDownloadLeases :exec
UPDATE run_leases SET status='failed',completed_at=clock_timestamp(),completion_key='download_closed' WHERE run_id=$1 AND status='active';
