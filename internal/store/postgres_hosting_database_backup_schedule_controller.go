package store

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// ReconcileDatabaseBackupSchedules is one bounded controller pass, not a cron
// execution service. Every effect remains an ordinary fenced backup operation.
func (s *PostgresStore) ReconcileDatabaseBackupSchedules(ctx context.Context) error {
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseBackupEnabled() {
		return nil
	}
	var present bool
	present, err := sqlcgen.New(s.pool).DatabaseBackupScheduleSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	var discoveryTime time.Time
	discoveryTime, err = sqlcgen.New(s.pool).ReadDatabaseBackupScheduleDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	ids, err := sqlcgen.New(s.pool).ListDatabaseBackupSchedulesToReconcile(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.reconcileDatabaseBackupSchedule(ctx, id, discoveryTime); err != nil {
			return err
		}
	}
	return s.recoverUnscheduledDatabaseBackups(ctx)
}

func backupScheduleDenial(err error) string {
	switch {
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, auth.ErrUnauthenticated):
		return "authority_denied"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound), errors.Is(err, ErrHostingDatabaseUnavailable):
		return "source_unavailable"
	default:
		return ""
	}
}

func (s *PostgresStore) reconcileDatabaseBackupSchedule(ctx context.Context, id string, discoveryTime time.Time) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		observed, e := q.ReadDatabaseBackupSchedule(ctx, id)
		if e != nil {
			return false, hostingError(e)
		}
		if e = lockHostingResourceDestination(ctx, tx, observed.ResourceID); e != nil {
			return false, e
		}
		schedule, e := q.LockDatabaseBackupSchedule(ctx, id)
		if e != nil {
			return false, e
		}
		var now time.Time
		now, e = sqlcgen.New(tx).ReadDatabaseBackupScheduleClock(ctx)
		if e != nil {
			return false, e
		}
		if e = sqlcgen.New(tx).CheckDatabaseBackupSchedule(ctx, sqlcgen.CheckDatabaseBackupScheduleParams{ID: id, CheckedAt: now}); e != nil {
			return false, e
		}
		if schedule.Enabled {
			due, ok, e := domain.NewestHostingDatabaseBackupOccurrence(schedule.Frequency, schedule.Timezone, discoveryTime, schedule.LastScheduledAt)
			if e != nil {
				return false, e
			}
			if ok {
				if e = sqlcgen.New(tx).AdvanceDatabaseBackupSchedule(ctx, sqlcgen.AdvanceDatabaseBackupScheduleParams{ID: id, LastScheduledAt: &due}); e != nil {
					return false, e
				}
				schedule.LastScheduledAt = &due
				suffix := strings.TrimPrefix(domain.HostingDocumentHash(struct {
					Schedule string
					Due      time.Time
				}{id, due}), "sha256:")
				occurrence, operation := "dbocc_"+suffix, "dbb_schedule_"+suffix
				state, reason := "admitted", ""
				// A savepoint lets a definitive current-authority denial retain a durable
				// occurrence without retaining any partially admitted operation/queue job.
				admission, e := tx.Begin(ctx)
				if e != nil {
					return false, e
				}
				value, e := databaseBackupScheduleAuthority(ctx, admission, schedule.ActorID, schedule.ResourceID, domain.RoleOwner)
				if e == nil && (value.Binding.ID != schedule.BindingID || value.Binding.DestinationID != schedule.DestinationID) {
					reason = "placement_changed"
					e = ErrConflict
				}
				if e == nil {
					if schedule.Component != "" {
						value.Capability = domain.HostingDatabaseBackupCapabilityFor(schedule.ComponentBackupVersion)
					}
					target, targetErr := sqlcgen.New(admission).LockHostingResourceTarget(ctx, schedule.ResourceID)
					e = targetErr
					if e == nil {
						_, e = sqlcgen.New(admission).LockHostingExactRunner(ctx, sqlcgen.LockHostingExactRunnerParams{ID: target.BindingID, ResourceID: schedule.ResourceID, ID_2: target.DestinationID, ID_3: target.ServerID, ID_4: target.RunnerID, ProjectID: target.ProjectID, Capability: value.Capability})
						if errors.Is(e, pgx.ErrNoRows) {
							e = ErrConflict
						}
					}
				}
				if e == nil {
					var busy bool
					busy, e = sqlcgen.New(admission).DatabaseBackupSourceBusy(ctx, schedule.BindingID)
					if e == nil && busy {
						reason = "source_busy"
						e = ErrConflict
					}
				}
				deployed := ""
				if e == nil && schedule.Component != "" {
					var h domain.HostingHealthy
					var op domain.HostingOperation
					h, op, _, e = databaseBackupComponentSource(ctx, admission, schedule.ActorID, schedule.ResourceID, schedule.Component, domain.RoleOwner)
					if e == nil && domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, schedule.Component) != schedule.ComponentBackupVersion {
						e = ErrConflict
					}
					deployed = h.OperationID
				}
				if e == nil {
					var remote *domain.BackupTargetSelection
					remote, e = backupRemoteScheduleSelection(ctx, admission, schedule.ID, schedule.Revision)
					if e == nil && remote != nil && schedule.Component != "" && schedule.ComponentBackupVersion != domain.HostingDatabaseMySQLBackupComponentVersion {
						e = ErrConflict
					}
					if e == nil {
						_, e = s.requestDatabaseBackupTx(ctx, admission, operation, schedule.ResourceID, schedule.ActorID, domain.HostingDatabaseBackupInput{Component: schedule.Component, ExpectedDeployedOperationID: deployed, RequestID: occurrence, ExpectedResourceGeneration: value.Resource.Generation, RemoteTarget: remote})
					}
				}
				if e != nil {
					if rollbackErr := admission.Rollback(ctx); rollbackErr != nil {
						return false, rollbackErr
					}
					if reason == "" {
						reason = backupScheduleDenial(e)
					}
					if reason == "" {
						return false, e
					}
					state = "skipped"
					if reason == "authority_denied" {
						state = "denied"
					}
					operation = ""
				} else if e = admission.Commit(ctx); e != nil {
					return false, e
				}
				var operationID *string
				if operation != "" {
					operationID = &operation
				}
				if e = sqlcgen.New(tx).CreateDatabaseBackupScheduleOccurrence(ctx, sqlcgen.CreateDatabaseBackupScheduleOccurrenceParams{ID: occurrence, ScheduleID: id, ScheduleRevision: int64(schedule.Revision), ScheduledAt: due, OperationID: operationID, State: state, Reason: reason}); e != nil {
					return false, e
				}
			}
		}
		if e = s.recoverDatabaseBackupScheduleOccurrences(ctx, tx, schedule, now); e != nil {
			return false, e
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) recoverDatabaseBackupScheduleOccurrences(ctx context.Context, tx pgx.Tx, schedule sqlcgen.HostingDatabaseBackupSchedule, now time.Time) error {
	rows, err := sqlcgen.New(tx).ListRecoverableDatabaseBackupOccurrences(ctx, sqlcgen.ListRecoverableDatabaseBackupOccurrencesParams{ScheduleID: schedule.ID, EnqueuedAt: now.Add(-60 * time.Minute)})
	if err != nil {
		return err
	}
	type lost struct {
		id, operation string
		due           time.Time
	}
	pending := make([]lost, 0, len(rows))
	for _, row := range rows {
		if row.OperationID == nil {
			return pgx.ScanArgError{ColumnIndex: 1, FieldName: "operation_id", Err: errors.New("cannot scan NULL into *string")}
		}
		pending = append(pending, lost{id: row.ID, operation: *row.OperationID, due: row.ScheduledAt})
	}
	for _, v := range pending {
		backup, e := sqlcgen.New(tx).LockDatabaseBackup(ctx, v.operation)
		if e != nil {
			return e
		}
		// No task/lease can ever have mutated a queued operation. Never move a
		// dispatched/applying/uncertain operation or attempt to replace its RunID.
		if backup.State != "queued" || backup.RunID != nil {
			continue
		}
		authority, e := tx.Begin(ctx)
		if e != nil {
			return e
		}
		_, currentErr := databaseBackupCurrent(ctx, authority, backup)
		if currentErr != nil {
			if e = authority.Rollback(ctx); e != nil {
				return e
			}
			if backupScheduleDenial(currentErr) == "" {
				return currentErr
			}
			if e = sqlcgen.New(tx).RecordUnreleasedDatabaseBackupRetirement(ctx, sqlcgen.RecordUnreleasedDatabaseBackupRetirementParams{OperationID: backup.ID, OccurrenceID: v.id, PlanHash: backup.PlanHash, Reason: backupScheduleDenial(currentErr)}); e != nil {
				return e
			}
			// Revocation ends discovery/dispatch authority, but cannot release a
			// previously held source on behalf of its now-revoked owner.
			if e = sqlcgen.New(tx).FailExpiredDatabaseBackup(ctx, sqlcgen.FailExpiredDatabaseBackupParams{ID: backup.ID, UpdatedAt: now}); e != nil {
				return e
			}
			receipt, receiptErr := notificationDirectReceipt(ctx, tx, "backup", backup.ID, "schedule_retired")
			if receiptErr != nil {
				return receiptErr
			}
			if e = notificationBackupFailure(ctx, tx, backup.ID, receipt); e != nil {
				return e
			}
			if e = databaseBackupAudit(ctx, tx, backup.CreatedBy, backup.ID, "schedule_retired", backup.ProjectID); e != nil {
				return e
			}
			continue
		}
		if e = authority.Commit(ctx); e != nil {
			return e
		}
		reason := ""
		if schedule.LastScheduledAt != nil && schedule.LastScheduledAt.After(v.due) {
			reason = "newer_occurrence"
		} else if v.due.Before(now.Add(-24 * time.Hour)) {
			reason = "too_old"
		}
		if reason != "" {
			if e = sqlcgen.New(tx).RecordReleasedDatabaseBackupRetirement(ctx, sqlcgen.RecordReleasedDatabaseBackupRetirementParams{OperationID: backup.ID, OccurrenceID: v.id, PlanHash: backup.PlanHash, Reason: reason}); e != nil {
				return e
			}
			if e = sqlcgen.New(tx).ReleaseExpiredDatabaseBackup(ctx, sqlcgen.ReleaseExpiredDatabaseBackupParams{ID: backup.ID, UpdatedAt: now}); e != nil {
				return e
			}
			receipt, receiptErr := notificationDirectReceipt(ctx, tx, "backup", backup.ID, "schedule_retired")
			if receiptErr != nil {
				return receiptErr
			}
			if e = notificationBackupFailure(ctx, tx, backup.ID, receipt); e != nil {
				return e
			}
			if e = sqlcgen.New(tx).MissDatabaseBackupOccurrence(ctx, sqlcgen.MissDatabaseBackupOccurrenceParams{ID: v.id, Reason: reason}); e != nil {
				return e
			}
			if e = databaseBackupAudit(ctx, tx, backup.CreatedBy, backup.ID, "schedule_retired", backup.ProjectID); e != nil {
				return e
			}
			continue
		}
		if _, e = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: backup.ID}); e != nil {
			return e
		}
		if e = sqlcgen.New(tx).EnqueueDatabaseBackupOccurrence(ctx, sqlcgen.EnqueueDatabaseBackupOccurrenceParams{ID: v.id, EnqueuedAt: now}); e != nil {
			return e
		}
	}
	return nil
}

// Manual immediate/restore queue recovery also keeps the original operation ID.
// Only an operation with no task can be republished; runner attempts are never
// recreated. Current full authority is checked again by the normal preparer.
func (s *PostgresStore) recoverUnscheduledDatabaseBackups(ctx context.Context) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, e := sqlcgen.New(tx).ListUnscheduledDatabaseBackupsToRecover(ctx)
		if e != nil {
			return false, e
		}
		for _, id := range ids {
			if _, e = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); e != nil {
				return false, e
			}
			if e = sqlcgen.New(tx).TouchDatabaseBackup(ctx, id); e != nil {
				return false, e
			}
		}
		return true, nil
	})
	return err
}

// One observer belongs to the serial hosting recovery loop. A failed pass may
// roll back before any occurrence exists, so report a finite class here without
// exposing SQL details, operation input, credentials or arbitrary error text.
type databaseBackupScheduleObserver struct {
	nextReport time.Time
}

func (o *databaseBackupScheduleObserver) run(ctx context.Context, now time.Time, logger *slog.Logger, reconcile func(context.Context) error) {
	attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := reconcile(attempt)
	cancel()
	if err == nil || ctx.Err() != nil || now.Before(o.nextReport) {
		return
	}
	class := "unavailable"
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		class = "deadline"
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, auth.ErrUnauthenticated):
		class = "authority_denied"
	case errors.As(err, &pg):
		switch pg.Code {
		case "40001", "40P01":
			class = "transaction_conflict"
		case "23502", "23503", "23505", "23514":
			class = "database_constraint"
		}
	}
	o.nextReport = now.Add(time.Minute)
	logger.WarnContext(ctx, "database backup schedule controller pass failed", "error_class", class)
}
