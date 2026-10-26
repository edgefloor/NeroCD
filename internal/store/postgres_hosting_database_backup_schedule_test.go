package store_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

// Keep the real River client/transaction admission, but stop its consumer so
// controller tests can examine a genuinely unstarted queue without racing a
// preparer. Runner evidence below uses the existing store lifecycle fixture.
func scheduledBackupFixture(t *testing.T) networkCaptureFixture {
	t.Helper()
	var stop func(context.Context) error
	pg, db, r, server, destination := hostingExecutionFixture(t, &stop)
	if err := stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	service := hostingDatabaseApplication(t, pg)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	view, err := service.CreateHostingDatabase(ctx, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("a", 32), ProjectID: r.ProjectID, ProjectEnvironmentID: r.ProjectEnvironmentID, Name: "scheduled source", DestinationID: destination.ID, Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=ARRAY['compose_deploy',$2,$3,$4,$5,$6]::text[] WHERE id=$1`, server.RunnerID, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability); err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(db).ReadHostingStorage(ctx, view.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	storage := domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	f := storageMaterializationFixture{pg: pg, db: db, resource: view.Resource, runner: server.RunnerID, storage: storage}
	op, err := service.RequestHostingDatabaseDeployment(ctx, view.Resource.ID, databaseExecutionInput("scheduled_source"))
	if err != nil {
		t.Fatal(err)
	}
	f = prepareDatabaseExecution(t, f, op)
	ack, err := pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "scheduled_source_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("a", 64), ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}); err != nil {
		t.Fatal(err)
	}
	return networkCaptureFixture{storageMaterializationFixture: f, service: service, ctx: ctx}
}

func scheduleOldUnstarted(t *testing.T, f networkCaptureFixture) (domain.HostingDatabaseBackupSchedule, domain.HostingDatabaseBackupOperation) {
	t.Helper()
	s := saveBackupSchedule(t, f, "schedule_old", scheduledBackupInput(t, f, "create_old", false))
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "old_scheduled_backup", ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Truncate(time.Minute).Add(-25 * time.Hour)
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_schedules SET last_scheduled_at=$2 WHERE id=$1`, s.ID, due); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason,enqueued_at) VALUES('old_occurrence',$1,1,$2,$3,'admitted','',clock_timestamp()-interval '61 minutes')`, s.ID, due, b.ID); err != nil {
		t.Fatal(err)
	}
	return s, b
}

func TestPostgresDatabaseBackupScheduleRetirementRace(t *testing.T) {
	f := scheduledBackupFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	f.ctx = ctx
	s, b := scheduleOldUnstarted(t, f)
	blocker, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := blocker.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error("rollback schedule fixture blocker", err)
		}
	}()
	if _, err = blocker.Exec(f.ctx, `SELECT id FROM hosting_database_backups WHERE id=$1 FOR UPDATE`, b.ID); err != nil {
		t.Fatal(err)
	}
	// Both real transactions contend on the immutable operation row. Whichever
	// wins must exclude the other effect; a serialization/deadlock retry is safe.
	started := make(chan struct{})
	results := make(chan error, 2)
	go func() { close(started); results <- f.pg.ReconcileDatabaseBackupSchedules(f.ctx) }()
	<-started
	go func() { results <- f.pg.PrepareDatabaseBackup(f.ctx, b.ID) }()
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err = <-results:
		case <-ctx.Done():
			t.Fatal("retirement and preparer did not join within their bounded lifetime")
		}
		if err != nil {
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || (pgerr.Code != "40P01" && pgerr.Code != "40001") {
				t.Fatal("unexpected race failure", err)
			}
		}
	}
	current, err := f.pg.GetDatabaseBackup(f.ctx, f.op.CreatedBy, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var run *string
	var decisions, attempts int
	if f.db.QueryRow(f.ctx, `SELECT run_id,(SELECT count(*) FROM hosting_database_backup_schedule_retirements WHERE operation_id=b.id),(SELECT count(*) FROM run_leases l JOIN task_runs r ON r.id=l.run_id WHERE r.run_spec->>'hosting_operation_id'=b.id) FROM hosting_database_backups b WHERE b.id=$1`, b.ID).Scan(&run, &decisions, &attempts) != nil {
		t.Fatal("race identity unavailable")
	}
	switch current.State {
	case "failed":
		if run != nil || decisions != 1 || attempts != 0 || current.OwnershipHeld {
			t.Fatal("retirement crossed run/hold authority")
		}
		if err = f.pg.PrepareDatabaseBackup(f.ctx, b.ID); err != nil {
			t.Fatal("stale River delivery failed closed", err)
		}
		if _, err = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("stale delivery recreated a task", err)
		}
	case "dispatched":
		if run == nil || decisions != 0 || !current.OwnershipHeld {
			t.Fatal("dispatch raced a retirement receipt")
		}
		claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
		if err != nil || claim.Run.ID != *run {
			t.Fatal("dispatch lost exact claim", err)
		}
		if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
			t.Fatal(err)
		}
		current, err = f.pg.GetDatabaseBackup(f.ctx, f.op.CreatedBy, b.ID)
		if err != nil || !current.OwnershipHeld || current.State != "dispatched" {
			t.Fatal("active attempt retired", err)
		}
	default:
		t.Fatal("unexpected race settlement", current.State)
	}
	_ = s
}

func TestPostgresDatabaseBackupScheduleRetirementAuditRollback(t *testing.T) {
	f := scheduledBackupFixture(t)
	s, b := scheduleOldUnstarted(t, f)
	if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION reject_schedule_retirement_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_backup.schedule_retired' THEN RAISE EXCEPTION 'synthetic audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_schedule_retirement_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_schedule_retirement_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err == nil {
		t.Fatal("retirement audit rejection committed")
	}
	current, err := f.pg.GetDatabaseBackup(f.ctx, f.op.CreatedBy, b.ID)
	if err != nil || current.State != "queued" || !current.OwnershipHeld {
		t.Fatal("audit rejection changed held operation", err)
	}
	var decisions int
	if f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_schedule_retirements`).Scan(&decisions) != nil || decisions != 0 {
		t.Fatal("audit rejection left retirement evidence")
	}
	if got := scheduledOccurrences(t, f, s.ID); len(got) != 1 || got[0].State != "queued" {
		t.Fatal("audit rejection changed occurrence", got)
	}
	if _, err = f.db.Exec(f.ctx, `DROP TRIGGER reject_schedule_retirement_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := scheduledOccurrences(t, f, s.ID); len(got) != 1 || got[0].State != "missed" {
		t.Fatal("retirement did not recover after rollback", got)
	}
}
func scheduledBackupInput(t *testing.T, f networkCaptureFixture, request string, enabled bool) domain.HostingDatabaseBackupScheduleInput {
	t.Helper()
	var now time.Time
	if err := f.db.QueryRow(f.ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return domain.HostingDatabaseBackupScheduleInput{RequestID: request, ExpectedResourceGeneration: f.resource.Generation, Frequency: fmt.Sprintf("%d %d * * *", now.UTC().Minute(), now.UTC().Hour()), Timezone: "UTC", Enabled: enabled}
}
func saveBackupSchedule(t *testing.T, f networkCaptureFixture, id string, in domain.HostingDatabaseBackupScheduleInput) domain.HostingDatabaseBackupSchedule {
	t.Helper()
	v, err := f.pg.SaveDatabaseBackupSchedule(f.ctx, id, f.resource.ID, f.op.CreatedBy, "", in)
	if err != nil {
		t.Fatal("schedule admission", err)
	}
	return v
}
func scheduledOccurrences(t *testing.T, f networkCaptureFixture, id string) []domain.HostingDatabaseBackupOccurrence {
	t.Helper()
	v, err := f.pg.ListDatabaseBackupOccurrences(f.ctx, f.op.CreatedBy, f.resource.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestPostgresDatabaseBackupScheduleLifecycle(t *testing.T) {
	f := scheduledBackupFixture(t)
	in := scheduledBackupInput(t, f, "create_schedule", true)
	created, createErr := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", in)
	if createErr != nil {
		t.Fatal("public application schedule admission", createErr)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.pg.ReconcileDatabaseBackupSchedules(f.ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history := scheduledOccurrences(t, f, created.ID)
	if len(history) != 1 || history[0].OperationID == "" || history[0].State != "queued" || history[0].ScheduleRevision != 1 {
		t.Fatal("concurrent ticks duplicated or lost occurrence", history)
	}
	var jobs int
	if f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND args=jsonb_build_object('operation_id',$2::text)`, hostingjobs.DatabaseBackupKind, history[0].OperationID).Scan(&jobs) != nil || jobs != 1 {
		t.Fatal("occurrence queue is not ID-only atomic")
	}
	// Accepted work remains valid after disabling, with the original version.
	in.RequestID = "disable_schedule"
	in.ExpectedVersion = 1
	in.Enabled = false
	disabled, err := f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.resource.ID, f.op.CreatedBy, created.ID, in)
	if err != nil || disabled.ID != created.ID || disabled.Revision != 2 || disabled.LastScheduledAt == nil {
		t.Fatal("disable lost identity/cursor", err)
	}
	original := in
	original.RequestID = "create_schedule"
	original.ExpectedVersion = 0
	original.Enabled = true
	replay, err := f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.resource.ID, f.op.CreatedBy, "", original)
	if err != nil || !reflect.DeepEqual(replay, created) {
		t.Fatal("historical request replay changed", err)
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, history[0].OperationID)
	if got := scheduledOccurrences(t, f, created.ID); len(got) != 1 || got[0].State != "succeeded" || got[0].OwnershipHeld || got[0].ScheduleRevision != 1 {
		t.Fatal("linked completion not projected", got)
	}
	in.RequestID = "reenable_schedule"
	in.ExpectedVersion = 2
	in.Enabled = true
	in.Timezone = "Etc/UTC"
	enabled, err := f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.resource.ID, f.op.CreatedBy, created.ID, in)
	if err != nil || enabled.Revision != 3 || !enabled.LastScheduledAt.Equal(*disabled.LastScheduledAt) {
		t.Fatal("reenable reset cursor", err)
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(scheduledOccurrences(t, f, created.ID)) != 1 {
		t.Fatal("edit reenqueued previous slot")
	}
	in.RequestID = "stale_schedule"
	in.ExpectedVersion = 1
	if _, err = f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.resource.ID, f.op.CreatedBy, created.ID, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale edit accepted", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_schedule_occurrences SET operation_id=NULL WHERE schedule_id=$1`, created.ID); err == nil {
		t.Fatal("immutable linkage changed")
	}
}
func TestPostgresDatabaseBackupScheduleAtomicRollback(t *testing.T) {
	f := scheduledBackupFixture(t)
	s := saveBackupSchedule(t, f, "schedule_atomic", scheduledBackupInput(t, f, "create_atomic", true))
	if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION reject_scheduled_backup_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='hosting.database_backup.v1' THEN RAISE EXCEPTION 'synthetic queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_scheduled_backup_queue BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION reject_scheduled_backup_queue()`); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err == nil {
		t.Fatal("queue rejection committed")
	}
	var backups, occurrences int
	var cursor *time.Time
	if f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_schedule_occurrences),last_scheduled_at FROM hosting_database_backup_schedules WHERE id=$1`, s.ID).Scan(&backups, &occurrences, &cursor) != nil || backups != 0 || occurrences != 0 || cursor != nil {
		t.Fatal("queue rejection left partial occurrence or hold")
	}
	if _, err := f.db.Exec(f.ctx, `DROP TRIGGER reject_scheduled_backup_queue ON river_job`); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(scheduledOccurrences(t, f, s.ID)) != 1 {
		t.Fatal("rollback lost due slot")
	}
}
func TestPostgresDatabaseBackupScheduleCurrentAuthority(t *testing.T) {
	for _, kind := range []string{"owner", "grant", "runner", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			f := scheduledBackupFixture(t)
			s := saveBackupSchedule(t, f, "schedule_authority", scheduledBackupInput(t, f, "create_authority", true))
			switch kind {
			case "owner":
				if _, err := f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, f.op.CreatedBy); err != nil {
					t.Fatal(err)
				}
			case "grant":
				if _, err := f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.op.Snapshot.DestinationID); err != nil {
					t.Fatal(err)
				}
			case "runner":
				if _, err := f.db.Exec(f.ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				if _, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "manual_overlap", ExpectedResourceGeneration: f.resource.Generation}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
				t.Fatal(err)
			}
			// A revoked viewer cannot read this result; inspect only finite persisted facts.
			var state, reason string
			var operation *string
			if err := f.db.QueryRow(f.ctx, `SELECT state,reason,operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, s.ID).Scan(&state, &reason, &operation); err != nil || operation != nil || state != "denied" && state != "skipped" || reason == "" {
				t.Fatal("denial lost durable history or admitted effects", err, state, reason)
			}
		})
	}
}

func TestPostgresDatabaseBackupScheduleLostUnstartedRecovery(t *testing.T) {
	for _, kind := range []string{"newest", "newer", "old", "claimed"} {
		t.Run(kind, func(t *testing.T) {
			f := scheduledBackupFixture(t)
			s := saveBackupSchedule(t, f, "schedule_recovery", scheduledBackupInput(t, f, "create_recovery", false))
			b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "lost_scheduled_operation", ExpectedResourceGeneration: f.resource.Generation})
			if err != nil {
				t.Fatal(err)
			}
			due := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Hour)
			if kind == "old" {
				due = due.Add(-24 * time.Hour)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_schedules SET last_scheduled_at=$2 WHERE id=$1`, s.ID, due); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason,enqueued_at) VALUES('lost_occurrence',$1,1,$2,$3,'admitted','',clock_timestamp()-interval '61 minutes')`, s.ID, due, b.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE river_job SET state='discarded',finalized_at=clock_timestamp() WHERE kind=$1 AND args->>'operation_id'=$2`, hostingjobs.DatabaseBackupKind, b.ID); err != nil {
				t.Fatal(err)
			}
			if kind == "newer" {
				if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_schedules SET last_scheduled_at=$2 WHERE id=$1`, s.ID, due.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			var authority domain.HostingAuthority
			if kind == "claimed" {
				authority, _ = claimBackup(t, f, b.ID)
				if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, authority.LeaseID); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_schedule_retirements(operation_id,occurrence_id,plan_hash,reason,ownership_held) SELECT id,'lost_occurrence',plan_hash,'too_old',false FROM hosting_database_backups WHERE id=$1`, b.ID); err == nil {
					t.Fatal("database boundary accepted retirement of an existing expired attempt")
				}
			}
			if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
				t.Fatal(err)
			}
			h := scheduledOccurrences(t, f, s.ID)
			if len(h) != 1 || h[0].OperationID != b.ID {
				t.Fatal("recovery replaced original identity", h)
			}
			var available int
			if f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND args->>'operation_id'=$2 AND state='available'`, hostingjobs.DatabaseBackupKind, b.ID).Scan(&available) != nil {
				t.Fatal("queue read")
			}
			switch kind {
			case "newest":
				if available != 1 || h[0].State != "queued" || !h[0].OwnershipHeld {
					t.Fatal("newest lost queue not recovered", h, available)
				}
			case "claimed":
				if available != 0 || h[0].State != "dispatched" || !h[0].OwnershipHeld {
					t.Fatal("expired attempt rerun or unlocked", h, available)
				}
			default:
				if available != 0 || h[0].State != "missed" || h[0].OwnershipHeld {
					t.Fatal("old work not recorded missed", h, available)
				}
			}
		})
	}
}
