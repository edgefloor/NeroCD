package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
)

func backupRemoteScheduleSelection(ctx context.Context, tx pgx.Tx, id string, revision int64) (*domain.BackupTargetSelection, error) {
	var out domain.BackupTargetSelection
	err := tx.QueryRow(ctx, `SELECT target_id,target_revision FROM hosting_database_backup_schedule_remote_targets WHERE schedule_id=$1 AND schedule_revision=$2`, id, revision).Scan(&out.TargetID, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.Validate() != nil {
		return nil, ErrConflict
	}
	return &out, nil
}
func (s *PostgresStore) saveBackupRemoteScheduleSelection(ctx context.Context, tx pgx.Tx, actor, resource, id string, revision int64, selection *domain.BackupTargetSelection) error {
	if selection == nil {
		return nil
	}
	v, h, _, err := databaseBackupSource(ctx, tx, actor, resource)
	if err != nil {
		return err
	}
	limits := s.databaseBackupPolicy()
	check := domain.HostingDatabaseBackupPlan{ActorID: actor, RunnerID: h.Snapshot.RunnerID, Source: h, Operation: domain.HostingDatabaseBackupOperation{ProjectID: v.Resource.ProjectID}, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
	if _, err = backupRemoteSelect(ctx, tx, check, *selection); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_remote_targets(schedule_id,schedule_revision,target_id,target_revision) VALUES($1,$2,$3,$4)`, id, revision, selection.TargetID, selection.Revision)
	return err
}
