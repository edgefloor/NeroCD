package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func backupRemoteScheduleSelection(ctx context.Context, tx pgx.Tx, id string, revision int64) (*domain.BackupTargetSelection, error) {
	var out domain.BackupTargetSelection
	readDatabaseBackupScheduleRemoteSelectionRow, err := sqlcgen.New(tx).ReadDatabaseBackupScheduleRemoteSelection(ctx, sqlcgen.ReadDatabaseBackupScheduleRemoteSelectionParams{ScheduleID: id, ScheduleRevision: int64(revision)})
	out.TargetID = readDatabaseBackupScheduleRemoteSelectionRow.TargetID
	out.Revision = readDatabaseBackupScheduleRemoteSelectionRow.TargetRevision
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
func (s *PostgresStore) saveBackupRemoteScheduleSelection(ctx context.Context, tx pgx.Tx, actor, resource, id string, revision int64, enabled bool, selection *domain.BackupTargetSelection) error {
	if selection == nil {
		return nil
	}
	saved, err := sqlcgen.New(tx).ReadDatabaseBackupSchedule(ctx, id)
	if err != nil {
		return hostingError(err)
	}
	if saved.ResourceID != resource || saved.Revision != revision || saved.Component != "" && saved.ComponentBackupVersion != domain.HostingDatabaseMySQLBackupComponentVersion {
		return ErrConflict
	}
	if !enabled && revision > 1 {
		prior, err := backupRemoteScheduleSelection(ctx, tx, id, revision-1)
		if err != nil {
			return err
		}
		if prior != nil && *prior == *selection {
			// The outer schedule transaction already reauthorized the owner,
			// resource, binding and destination. Retaining this exact historical
			// selection requires project identity, not usable credentials.
			snapshot, err := backupRemoteTargetSnapshot(ctx, tx, selection.TargetID, selection.Revision)
			if err != nil {
				return err
			}
			var project string
			project, err = sqlcgen.New(tx).ReadDatabaseBackupScheduleResourceProject(ctx, resource)
			if err != nil {
				return hostingError(err)
			}
			if snapshot.Target.ProjectID != project {
				return ErrConflict
			}
			err = sqlcgen.New(tx).CreateDatabaseBackupScheduleRemoteSelection(ctx, sqlcgen.CreateDatabaseBackupScheduleRemoteSelectionParams{ScheduleID: id, ScheduleRevision: int64(revision), TargetID: selection.TargetID, TargetRevision: int64(selection.Revision)})
			return err
		}
	}
	limits := s.databaseBackupPolicy()
	check := domain.HostingDatabaseBackupPlan{ActorID: actor, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
	if saved.Component != "" {
		h, op, _, sourceErr := databaseBackupComponentSource(ctx, tx, actor, resource, saved.Component, domain.RoleOwner)
		if sourceErr != nil {
			return sourceErr
		}
		if domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, saved.Component) != saved.ComponentBackupVersion {
			return ErrConflict
		}
		check.RunnerID, check.Source, check.SourcePlan = h.Snapshot.RunnerID, h, *op.Plan
		check.Operation = domain.HostingDatabaseBackupOperation{ProjectID: op.ProjectID, Version: saved.ComponentBackupVersion}
		check.Component = &domain.HostingDatabaseBackupComponentPlan{Source: saved.Component}
		if err = databaseBackupAgent(ctx, tx, check); err != nil {
			return err
		}
	} else {
		v, h, _, sourceErr := databaseBackupSource(ctx, tx, actor, resource)
		if sourceErr != nil {
			return sourceErr
		}
		check.RunnerID, check.Source = h.Snapshot.RunnerID, h
		check.Operation = domain.HostingDatabaseBackupOperation{ProjectID: v.Resource.ProjectID, Version: domain.HostingDatabaseBackupVersionFor(v.Engine, v.RecipeVersion)}
	}
	if _, err = backupRemoteSelect(ctx, tx, check, *selection); err != nil {
		return err
	}
	err = sqlcgen.New(tx).CreateEmptyDatabaseBackupScheduleRemoteSelection(ctx, sqlcgen.CreateEmptyDatabaseBackupScheduleRemoteSelectionParams{ScheduleID: id, ScheduleRevision: int64(revision), TargetID: selection.TargetID, TargetRevision: int64(selection.Revision)})
	return err
}
