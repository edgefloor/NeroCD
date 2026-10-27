package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ReconcileStorageBackupSchedules discovers at most 100 rotating schedules using
// one database clock. Execution and queue recovery belong to the archive family.
func (s *PostgresStore) ReconcileStorageBackupSchedules(ctx context.Context) error {
	if s.storageArchiveConfiguration == nil || s.hostingJobs == nil {
		return nil
	}
	q := sqlcgen.New(s.pool)
	present, err := q.StorageBackupScheduleSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	now, err := q.StorageBackupScheduleDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	ids, err := q.DiscoverStorageBackupSchedules(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.reconcileStorageBackupSchedule(ctx, id, now); err != nil {
			return err
		}
	}
	return nil
}
func storageBackupScheduleDenial(err error) string {
	if errors.Is(err, ErrStorageArchiveUnavailable) || errors.Is(err, ErrHostingStorageUnavailable) || errors.Is(err, pgx.ErrNoRows) {
		return "source_unavailable"
	}
	return backupScheduleDenial(err)
}
func (s *PostgresStore) reconcileStorageBackupSchedule(ctx context.Context, id string, now time.Time) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		observed, e := q.ReadStorageBackupSchedule(ctx, id)
		if e != nil {
			return false, e
		}
		if e = lockHostingResourceDestination(ctx, tx, observed.ResourceID); e != nil {
			return false, e
		}
		schedule, e := q.LockStorageBackupSchedule(ctx, id)
		if e != nil {
			return false, e
		}
		if e = q.TouchStorageBackupSchedule(ctx, id); e != nil {
			return false, e
		}
		if !schedule.Enabled {
			return true, nil
		}
		due, ok, e := domain.NewestHostingDatabaseBackupOccurrence(schedule.Frequency, schedule.Timezone, now, schedule.LastScheduledAt)
		if e != nil || !ok {
			return false, e
		}
		if e = q.AdvanceStorageBackupSchedule(ctx, sqlcgen.AdvanceStorageBackupScheduleParams{ID: id, LastScheduledAt: &due}); e != nil {
			return false, e
		}
		hash := domain.HostingDocumentHash(struct {
			Schedule string
			Due      time.Time
		}{id, due})[7:]
		occurrence, operation := "sbocc_"+hash, "hop_schedule_"+hash
		state, reason := "admitted", ""
		admission, e := tx.Begin(ctx)
		if e != nil {
			return false, e
		}
		resource, binding, e := storageBackupScheduleAuthority(ctx, admission, schedule.ActorID, schedule.ResourceID, domain.RoleOwner)
		if e == nil && (binding.ID != schedule.BindingID || binding.DestinationID != schedule.DestinationID || (resource.Source.Kind == "compose" || resource.Source.Kind == "git") != (schedule.SourceFamily == "compose")) {
			reason = "placement_changed"
			e = ErrConflict
		}
		if e == nil {
			reservation, resErr := sqlcgen.New(admission).LockHostingPortReservation(ctx, binding.ID)
			e = resErr
			if e == nil && reservation.HoldOperationID != nil {
				reason = "source_busy"
				e = ErrConflict
			}
		}
		if e == nil {
			var busy bool
			busy, e = sqlcgen.New(admission).HostingBindingBusy(ctx, binding.ID)
			if e == nil && busy {
				reason = "source_busy"
				e = ErrConflict
			}
		}
		var storage domain.HostingStorage
		var compose *domain.HostingStorageArchiveComposeSource
		if e == nil {
			storage, compose, e = storageBackupScheduleSource(ctx, admission, schedule.ActorID, resource, proxyString(schedule.SourceVolume))
			if e == nil && (storageBackupScheduleOrigin(storage, compose) != schedule.SourceReferenceHash || compose == nil && storage.ID != proxyString(schedule.StorageID)) {
				reason = "placement_changed"
				e = ErrConflict
			}
		}
		if e == nil {
			var selection *domain.BackupTargetSelection
			selection, e = storageBackupScheduleSelection(ctx, admission, id, schedule.Revision)
			if e == nil {
				input := domain.HostingStorageBackupInput{RequestID: hash[:32], ExpectedResourceGeneration: resource.Generation, RemoteTarget: selection}
				if compose != nil {
					input.Volume = compose.Volume.Name
					input.ExpectedDeployedOperationID = compose.Healthy.OperationID
					input.AcknowledgeInterruption = schedule.AcknowledgeInterruption
				}
				_, e = s.requestStorageBackupTx(ctx, admission, operation, schedule.ResourceID, schedule.ActorID, input)
			}
		}
		if e != nil {
			if rollbackErr := admission.Rollback(ctx); rollbackErr != nil {
				return false, rollbackErr
			}
			if reason == "" {
				reason = storageBackupScheduleDenial(e)
			}
			if reason == "" {
				return false, e
			}
			state = "skipped"
			if reason == "authority_denied" {
				state = "denied"
			}
			operation = ""
		} else {
			if e = archiveFinalizeActor(ctx, admission, schedule.ActorID); e != nil {
				_ = admission.Rollback(ctx)
				return false, e
			}
			if e = admission.Commit(ctx); e != nil {
				return false, e
			}
		}
		var operationID *string
		if operation != "" {
			operationID = &operation
		}
		if e = q.InsertStorageBackupOccurrence(ctx, sqlcgen.InsertStorageBackupOccurrenceParams{ID: occurrence, ScheduleID: id, ScheduleRevision: schedule.Revision, ScheduledAt: due, OperationID: operationID, State: state, Reason: reason}); e != nil {
			return false, e
		}
		return true, nil
	})
	return err
}
