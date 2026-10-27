package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

func storageScheduleInput(f archiveControlFixture, n string) domain.HostingStorageBackupScheduleInput {
	return domain.HostingStorageBackupScheduleInput{RequestID: strings.Repeat(n, 32), ExpectedResourceGeneration: f.resource.Generation, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
}
func TestPostgresStorageBackupScheduleAtomicConcurrencyAndDisable(t *testing.T) {
	f := newArchiveControlFixture(t)
	if err := f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	in := storageScheduleInput(f, "a")
	scheduled, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil || replay.ID != scheduled.ID || replay.Revision != 1 {
		t.Fatalf("replay %#v %v", replay, err)
	}
	changed := in
	changed.Timezone = "Europe/Budapest"
	if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", changed); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.pg.ReconcileStorageBackupSchedules(f.ctx) }()
	}
	wg.Wait()
	close(errs)
	for err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, scheduled.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history %#v %v", history, err)
	}
	occurrence := history[0]
	if occurrence.OperationID == "" || occurrence.BackupID == "" || !occurrence.OwnershipHeld || occurrence.State != "queued" {
		t.Fatalf("admission %#v", occurrence)
	}
	var jobs int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1`, occurrence.OperationID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("queue count %d %v", jobs, err)
	}
	// Advance only the fixture's durable cursor to expose a second due minute.
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_storage_backup_schedules SET last_scheduled_at=last_scheduled_at+interval '1 minute' WHERE id=$1`, scheduled.ID); err != nil {
		t.Fatal(err)
	}
	disabled := in
	disabled.RequestID = strings.Repeat("b", 32)
	disabled.ExpectedVersion = 1
	disabled.Enabled = false
	saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, scheduled.ID, disabled)
	if err != nil || saved.Revision != 2 || saved.Enabled {
		t.Fatalf("disable %#v %v", saved, err)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	history, err = f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, scheduled.ID)
	if err != nil || len(history) != 1 || history[0].OperationID != occurrence.OperationID || !history[0].OwnershipHeld {
		t.Fatalf("disable altered admitted work %#v %v", history, err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_storage_backup_schedule_versions SET enabled=true WHERE schedule_id=$1`, scheduled.ID); err == nil {
		t.Fatal("immutable version changed")
	}
}
func TestPostgresStorageBackupScheduleAuthorityOverlapAndRollback(t *testing.T) {
	for _, kind := range []string{"revoked", "overlap", "queue_rollback"} {
		t.Run(kind, func(t *testing.T) {
			f := newArchiveControlFixture(t)
			if err := f.stopExecution(); err != nil {
				t.Fatal(err)
			}
			in := storageScheduleInput(f, "c")
			schedule, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "revoked":
				if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				if _, err = f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("d", 32), ExpectedResourceGeneration: f.resource.Generation}); err != nil {
					t.Fatal(err)
				}
			case "queue_rollback":
				if _, err = f.db.Exec(f.ctx, `CREATE FUNCTION fail_schedule_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture queue failure'; END $$; CREATE TRIGGER fail_schedule_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION fail_schedule_job()`); err != nil {
					t.Fatal(err)
				}
			}
			err = f.pg.ReconcileStorageBackupSchedules(f.ctx)
			if kind == "queue_rollback" {
				if err == nil {
					t.Fatal("queue failure accepted")
				}
				var count int
				var cursor *time.Time
				if err = f.db.QueryRow(f.ctx, `SELECT last_scheduled_at,(SELECT count(*) FROM hosting_storage_backup_schedule_occurrences) FROM hosting_storage_backup_schedules WHERE id=$1`, schedule.ID).Scan(&cursor, &count); err != nil || cursor != nil || count != 0 {
					t.Fatalf("partial transaction cursor=%v count=%d err=%v", cursor, count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var state, reason string
			var op *string
			if err = f.db.QueryRow(f.ctx, `SELECT state,reason,operation_id FROM hosting_storage_backup_schedule_occurrences WHERE schedule_id=$1`, schedule.ID).Scan(&state, &reason, &op); err != nil {
				t.Fatal(err)
			}
			if op != nil || kind == "revoked" && (state != "denied" || reason != "authority_denied") || kind == "overlap" && (state != "skipped" || reason != "source_busy") {
				t.Fatalf("wrong denial %s %s %v", state, reason, op)
			}
			if kind == "revoked" {
				if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in); !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrUnauthenticated) {
					t.Fatalf("revoked replay %v", err)
				}
			}
		})
	}
}
func TestPostgresStorageBackupScheduleSafeProjection(t *testing.T) {
	f := newArchiveControlFixture(t)
	in := storageScheduleInput(f, "e")
	schedule, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(schedule)
	if strings.Contains(string(raw), "volume_name") || strings.Contains(string(raw), "actor_id") {
		t.Fatal("unsafe schedule projection")
	}
}

func TestPostgresStorageBackupScheduleGenerationAndFrozenSource(t *testing.T) {
	for _, variant := range []string{"generation", "storage_reference", "destination"} {
		t.Run(variant, func(t *testing.T) {
			f := newArchiveControlFixture(t)
			if err := f.stopExecution(); err != nil {
				t.Fatal(err)
			}
			var schedule string
			if variant == "generation" {
				saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", storageScheduleInput(f, "a"))
				if err != nil {
					t.Fatal(err)
				}
				schedule = saved.ID
				name := "renamed scheduled uploads"
				current, err := f.service.UpdateResource(f.owner, f.resource.ID, app.ResourcePatch{ExpectedGeneration: f.resource.Generation, Name: &name})
				if err != nil || current.Generation != f.resource.Generation+1 {
					t.Fatal("ordinary generation change", err)
				}
			} else {
				// These fixture-only snapshots represent stale immutable placement history.
				// Production APIs cannot rewrite the admitted storage or destination identity.
				destination := f.storage.DestinationID
				hash := domain.HostingDocumentHash(f.storage.Reference())
				if variant == "storage_reference" {
					hash = "sha256:" + strings.Repeat("0", 64)
				} else {
					_, other := createDestinationTarget(t, f.pg, "changed_schedule")
					destination = other.ID
				}
				schedule = "stale_" + variant
				tx, err := f.db.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := tx.Rollback(f.ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
						t.Error(err)
					}
				}()
				q := sqlcgen.New(tx)
				if err = q.CreateStorageBackupSchedule(f.ctx, sqlcgen.CreateStorageBackupScheduleParams{ID: schedule, ProjectID: f.resource.ProjectID, ResourceID: f.resource.ID, BindingID: f.storage.BindingID, DestinationID: destination, StorageID: &f.storage.ID, SourceFamily: "image", SourceReferenceHash: hash, ActorID: "usr_bootstrap", Frequency: "every_minute", Timezone: "UTC", Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if err = q.InsertStorageBackupScheduleVersion(f.ctx, sqlcgen.InsertStorageBackupScheduleVersionParams{ScheduleID: schedule, Revision: 1, ActorID: "usr_bootstrap", ExpectedResourceGeneration: f.resource.Generation, Frequency: "every_minute", Timezone: "UTC", Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
				t.Fatal(err)
			}
			history, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, schedule)
			if err != nil || len(history) != 1 {
				t.Fatalf("history %#v %v", history, err)
			}
			if variant == "generation" {
				if history[0].OperationID == "" || history[0].State != "queued" || !history[0].OwnershipHeld {
					t.Fatalf("ordinary generation blocked admission %#v", history[0])
				}
			} else if history[0].OperationID != "" || history[0].State != "skipped" || history[0].Reason != "placement_changed" || history[0].OwnershipHeld {
				t.Fatalf("changed source admitted %#v", history[0])
			}
		})
	}
}
func TestPostgresStorageBackupScheduleRecoveryKeepsOccurrenceHeld(t *testing.T) {
	f := newArchiveControlFixture(t)
	inSchedule := storageScheduleInput(f, "f")
	now := time.Now().UTC()
	inSchedule.Frequency = fmt.Sprintf("%d %d * * *", now.Minute(), now.Hour())
	schedule, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", inSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	history, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, schedule.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history %#v %v", history, err)
	}
	op, a := f.claim(t, history[0].OperationID, "backup")
	in := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "scheduled_uncertain", PlanHash: op.StorageArchive.Plan.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "uncertain", Uncertain: true, FailureCode: "resume_failed"}}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, in); err != nil {
		t.Fatal(err)
	}
	recovery, err := f.service.RequestStorageArchiveRecovery(f.owner, op.ID, domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("5", 32), ExpectedPlanHash: op.StorageArchive.Plan.Hash})
	if err != nil || !recovery.MutationHold {
		t.Fatalf("recovery hold %v", err)
	}
	history, err = f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, schedule.ID)
	if err != nil || len(history) != 1 || !history[0].OwnershipHeld || history[0].State != "reconciliation_required" {
		t.Fatalf("recovery transfer lost source hold %#v %v", history, err)
	}
}

func TestPostgresStorageBackupScheduleRemoteTargetAdmission(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
	if err := f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	in := storageScheduleInput(f, "a")
	in.RemoteTarget = &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}
	assertCounts := func(schedules, versions, requests int) {
		t.Helper()
		var actualSchedules, actualVersions, actualRequests int
		if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_backup_schedules),(SELECT count(*) FROM hosting_storage_backup_schedule_versions),(SELECT count(*) FROM hosting_storage_backup_schedule_requests)`).Scan(&actualSchedules, &actualVersions, &actualRequests); err != nil {
			t.Fatal(err)
		}
		if actualSchedules != schedules || actualVersions != versions || actualRequests != requests {
			t.Fatalf("partial schedule state: schedules=%d versions=%d requests=%d", actualSchedules, actualVersions, actualRequests)
		}
	}
	rejected := in
	rejected.RequestID = strings.Repeat("b", 32)
	rejected.RemoteTarget = &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision + 1}
	if _, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", rejected); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unvalidated target revision accepted", err)
	}
	assertCounts(0, 0, 0)
	saved, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil {
		t.Fatal("remote schedule admission", err)
	}
	if saved.RemoteTarget == nil || *saved.RemoteTarget != *in.RemoteTarget {
		t.Fatal("remote schedule lost pinned target")
	}
	replay, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, "", in)
	if err != nil || replay.ID != saved.ID || replay.Revision != saved.Revision {
		t.Fatal("remote schedule replay", err)
	}
	assertCounts(1, 1, 1)
	update := in
	update.RequestID = strings.Repeat("c", 32)
	update.ExpectedVersion = saved.Revision
	update.Frequency = "* * * * *"
	changed, err := f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, update)
	if err != nil || changed.Revision != 2 || changed.RemoteTarget == nil || *changed.RemoteTarget != *in.RemoteTarget {
		t.Fatal("remote schedule update", err)
	}
	replay, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, update)
	if err != nil || replay.ID != saved.ID || replay.Revision != changed.Revision {
		t.Fatal("remote update replay", err)
	}
	assertCounts(1, 2, 2)
	rejected.ExpectedVersion = changed.Revision
	if _, err = f.service.SaveStorageBackupSchedule(f.owner, f.resource.ID, saved.ID, rejected); !errors.Is(err, store.ErrConflict) {
		t.Fatal("invalid remote update accepted", err)
	}
	assertCounts(1, 2, 2)
	current, err := f.service.ListStorageBackupSchedules(f.owner, f.resource.ID)
	if err != nil || len(current) != 1 || current[0].Revision != changed.Revision || current[0].Frequency != update.Frequency || current[0].RemoteTarget == nil || *current[0].RemoteTarget != *in.RemoteTarget {
		t.Fatal("rejected remote update changed current schedule", err)
	}
	if err = f.pg.ReconcileStorageBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	occurrences, err := f.service.ListStorageBackupOccurrences(f.owner, f.resource.ID, saved.ID)
	if err != nil || len(occurrences) != 1 || occurrences[0].ScheduleRevision != changed.Revision || occurrences[0].BackupID == "" || occurrences[0].OperationID == "" {
		t.Fatal("remote schedule did not create archive", err)
	}
	var raw []byte
	if err = f.db.QueryRow(f.ctx, `SELECT body->'remote_target' FROM hosting_storage_archive_plans WHERE operation_id=$1`, occurrences[0].OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var admitted domain.BackupTargetSelection
	if json.Unmarshal(raw, &admitted) != nil || admitted != *in.RemoteTarget {
		t.Fatal("scheduled archive lost immutable remote target")
	}
}
