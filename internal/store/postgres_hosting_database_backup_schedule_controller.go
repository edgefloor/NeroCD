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
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.hosting_database_backup_schedules') IS NOT NULL`).Scan(&present); err != nil {
		return err
	}
	if !present {
		return nil
	}
	var discoveryTime time.Time
	if err := s.pool.QueryRow(ctx, `SELECT clock_timestamp() AS backup_schedule_discovery_time`).Scan(&discoveryTime); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id FROM hosting_database_backup_schedules s WHERE s.enabled OR EXISTS(SELECT 1 FROM hosting_database_backup_schedule_occurrences o JOIN hosting_database_backups b ON b.id=o.operation_id WHERE o.schedule_id=s.id AND o.state='admitted' AND b.state='queued') ORDER BY s.checked_at,s.id LIMIT 100`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
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
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_schedules SET checked_at=$2 WHERE id=$1`, id, now); e != nil {
			return false, e
		}
		if schedule.Enabled {
			due, ok, e := domain.NewestHostingDatabaseBackupOccurrence(schedule.Frequency, schedule.Timezone, discoveryTime, schedule.LastScheduledAt)
			if e != nil {
				return false, e
			}
			if ok {
				if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_schedules SET last_scheduled_at=$2 WHERE id=$1`, id, due); e != nil {
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
					target, targetErr := sqlcgen.New(admission).LockHostingResourceTarget(ctx, schedule.ResourceID)
					e = targetErr
					if e == nil {
						_, e = sqlcgen.New(admission).LockHostingExactRunner(ctx, sqlcgen.LockHostingExactRunnerParams{ID: target.BindingID, ResourceID: schedule.ResourceID, ID_2: target.DestinationID, ID_3: target.ServerID, ID_4: target.RunnerID, ProjectID: target.ProjectID, Capability: domain.HostingDatabaseBackupCapability})
						if errors.Is(e, pgx.ErrNoRows) {
							e = ErrConflict
						}
					}
				}
				if e == nil {
					var busy bool
					e = admission.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_database_backups WHERE ownership_held AND mode='backup' AND source_binding_id=$1)`, schedule.BindingID).Scan(&busy)
					if e == nil && busy {
						reason = "source_busy"
						e = ErrConflict
					}
				}
				if e == nil {
					var remote *domain.BackupTargetSelection
					remote, e = backupRemoteScheduleSelection(ctx, admission, schedule.ID, schedule.Revision)
					if e == nil {
						_, e = s.requestDatabaseBackupTx(ctx, admission, operation, schedule.ResourceID, schedule.ActorID, domain.HostingDatabaseBackupInput{RequestID: occurrence, ExpectedResourceGeneration: value.Resource.Generation, RemoteTarget: remote})
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
				if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, occurrence, id, schedule.Revision, due, operationID, state, reason); e != nil {
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
	rows, err := tx.Query(ctx, `SELECT o.id,o.operation_id,o.scheduled_at FROM hosting_database_backup_schedule_occurrences o JOIN hosting_database_backups b ON b.id=o.operation_id WHERE o.schedule_id=$1 AND o.state='admitted' AND o.enqueued_at<$2 AND b.state='queued' AND b.run_id IS NULL ORDER BY o.scheduled_at DESC LIMIT 100`, schedule.ID, now.Add(-60*time.Minute))
	if err != nil {
		return err
	}
	type lost struct {
		id, operation string
		due           time.Time
	}
	pending := []lost{}
	for rows.Next() {
		var v lost
		if err = rows.Scan(&v.id, &v.operation, &v.due); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
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
			if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_retirements(operation_id,occurrence_id,plan_hash,reason,ownership_held) VALUES($1,$2,$3,$4,true)`, backup.ID, v.id, backup.PlanHash, backupScheduleDenial(currentErr)); e != nil {
				return e
			}
			// Revocation ends discovery/dispatch authority, but cannot release a
			// previously held source on behalf of its now-revoked owner.
			if _, e = tx.Exec(ctx, `UPDATE hosting_database_backups SET state='failed',failure_code='interrupted',updated_at=$2 WHERE id=$1 AND state='queued' AND run_id IS NULL`, backup.ID, now); e != nil {
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
			if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_retirements(operation_id,occurrence_id,plan_hash,reason,ownership_held) VALUES($1,$2,$3,$4,false)`, backup.ID, v.id, backup.PlanHash, reason); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE hosting_database_backups SET state='failed',ownership_held=false,failure_code='interrupted',updated_at=$2 WHERE id=$1 AND state='queued' AND run_id IS NULL`, backup.ID, now); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_schedule_occurrences SET state='missed',reason=$2 WHERE id=$1`, v.id, reason); e != nil {
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
		if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_schedule_occurrences SET enqueued_at=$2 WHERE id=$1`, v.id, now); e != nil {
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
		rows, e := tx.Query(ctx, `SELECT b.id FROM hosting_database_backups b WHERE b.state IN ('queued','waiting_target') AND b.run_id IS NULL AND b.updated_at<clock_timestamp()-interval '60 minutes' AND b.created_at>=clock_timestamp()-interval '24 hours' AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_schedule_occurrences o WHERE o.operation_id=b.id) ORDER BY b.updated_at,b.id LIMIT 100 FOR UPDATE OF b SKIP LOCKED`)
		if e != nil {
			return false, e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return false, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		for _, id := range ids {
			if _, e = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); e != nil {
				return false, e
			}
			if _, e = tx.Exec(ctx, `UPDATE hosting_database_backups SET updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
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
	logger.WarnContext(ctx, "PostgreSQL backup schedule controller pass failed", "error_class", class)
}
