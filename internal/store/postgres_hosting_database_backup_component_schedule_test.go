package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresComposeDatabaseBackupSchedule(t *testing.T) {
	for _, scenario := range []string{"admit", "redeploy", "queue_rollback", "overlap", "removed", "grant", "capability", "held"} {
		t.Run(scenario, func(t *testing.T) {
			var stop func(context.Context) error
			pg, db, base, server, destination := hostingExecutionFixture(t, &stop)
			ctx := t.Context()
			caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseBackupComponentCapability}
			if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, server.RunnerID, caps); err != nil {
				t.Fatal(err)
			}
			resource, deployed := deployBackupComponentStack(t, pg, base, server, destination, "a")
			if err := stop(ctx); err != nil {
				t.Fatal(err)
			}
			in := domain.HostingDatabaseBackupScheduleInput{RequestID: "component_schedule", ExpectedResourceGeneration: resource.Generation, Component: "postgresql", ExpectedDeployedOperationID: deployed.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
			created, err := pg.SaveDatabaseBackupSchedule(ctx, "dbs_component", resource.ID, "usr_bootstrap", "", in)
			if err != nil || created.Component != "postgresql" || created.Version != domain.HostingDatabaseBackupComponentScheduleVersion {
				t.Fatal("create component schedule", created, err)
			}
			for _, change := range []func(*domain.HostingDatabaseBackupScheduleInput){
				func(v *domain.HostingDatabaseBackupScheduleInput) { v.Component = "other" },
				func(v *domain.HostingDatabaseBackupScheduleInput) {
					v.Component = ""
					v.ExpectedDeployedOperationID = ""
				},
				func(v *domain.HostingDatabaseBackupScheduleInput) { v.ExpectedVersion = 2 },
				func(v *domain.HostingDatabaseBackupScheduleInput) { v.ExpectedResourceGeneration++ },
			} {
				edit := in
				edit.RequestID = "invalid_edit"
				edit.ExpectedVersion = 1
				edit.Enabled = false
				change(&edit)
				if _, err = pg.SaveDatabaseBackupSchedule(ctx, "unused", resource.ID, "usr_bootstrap", created.ID, edit); !errors.Is(err, store.ErrConflict) {
					t.Fatal("invalid immutable/CAS edit accepted", err)
				}
			}
			if _, err = db.Exec(ctx, `UPDATE hosting_database_backup_schedules SET component='other' WHERE id=$1`, created.ID); err == nil {
				t.Fatal("SQL changed immutable component")
			}
			err = nil
			switch scenario {
			case "queue_rollback":
				_, err = db.Exec(ctx, `CREATE FUNCTION reject_component_schedule_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='hosting.database_backup.v1' THEN RAISE EXCEPTION 'synthetic queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_component_schedule_queue BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION reject_component_schedule_queue()`)
			case "overlap":
				_, err = pg.RequestDatabaseBackup(ctx, "dbb_overlap", resource.ID, "usr_bootstrap", domain.HostingDatabaseBackupInput{RequestID: "overlap", ExpectedResourceGeneration: resource.Generation, Component: in.Component, ExpectedDeployedOperationID: deployed.ID}, destinationEvent("schedule_overlap"))
			case "removed":
				// Settle a new deployment that removes the selected database.
				changed := resource
				changed.Source.Compose = &domain.HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\nvolumes:\n  db:\n", PublicComponent: "app", ContainerPort: 80}
				resource, err = pg.UpdateResource(ctx, changed, resource.Generation, store.WithAudit(destinationEvent("remove_component")))
				if err == nil {
					deployed = redeployScheduleStack(t, pg, server, resource, deployed, true)
				}
			case "redeploy":
				deployed = redeployScheduleStack(t, pg, server, resource, deployed, false)
			case "grant":
				_, err = db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, destination.ID)
			case "capability":
				_, err = db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingDatabaseBackupComponentCapability)
			case "held":
				// The settled source is temporarily unavailable without modifying saved configuration.
				_, err = db.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, deployed.BindingID)
			}
			if err != nil {
				t.Fatal("scenario setup", err)
			}
			err = pg.ReconcileDatabaseBackupSchedules(ctx)
			if scenario == "queue_rollback" {
				if err == nil {
					t.Fatal("queue rejection committed")
				}
				var backups, occurrences int
				var cursor *time.Time
				if e := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_schedule_occurrences),last_scheduled_at FROM hosting_database_backup_schedules WHERE id=$1`, created.ID).Scan(&backups, &occurrences, &cursor); e != nil || backups != 0 || occurrences != 0 || cursor != nil {
					t.Fatal("partial occurrence/queue/hold", e, backups, occurrences, cursor)
				}
				if _, err = db.Exec(ctx, `DROP TRIGGER reject_component_schedule_queue ON river_job`); err != nil {
					t.Fatal(err)
				}
				err = pg.ReconcileDatabaseBackupSchedules(ctx)
			}
			if err != nil {
				t.Fatal("reconcile", err)
			}
			var state, reason string
			var operation *string
			if err = db.QueryRow(ctx, `SELECT state,reason,operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, created.ID).Scan(&state, &reason, &operation); err != nil {
				t.Fatal(err)
			}
			want := "source_unavailable"
			switch scenario {
			case "admit", "redeploy", "queue_rollback":
				want = ""
			case "overlap":
				want = "source_busy"
			case "grant":
				want = "authority_denied"
			}
			if reason != want || (operation != nil) != (want == "") {
				t.Fatal("wrong durable due result", state, reason, operation)
			}
			if operation != nil {
				var plan domain.HostingDatabaseBackupPlan
				var raw []byte
				if err = db.QueryRow(ctx, `SELECT plan FROM hosting_database_backups WHERE id=$1`, *operation).Scan(&raw); err != nil || json.Unmarshal(raw, &plan) != nil {
					t.Fatal("plan", err)
				}
				if plan.Component == nil || plan.Component.Source != in.Component || plan.Source.OperationID != deployed.ID || plan.Operation.SourceDeployedOperationID != deployed.ID || plan.Validate() != nil {
					t.Fatal("scheduled component lost exact plan")
				}
				history, e := pg.ListDatabaseBackupOccurrences(ctx, "usr_bootstrap", resource.ID, created.ID)
				if e != nil || len(history) != 1 || history[0].Component != "postgresql" {
					t.Fatal("historical component projection", history, e)
				}
				// Another component schedule cannot link this operation through direct SQL.
				tx, e := db.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if e := tx.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
						t.Error(e)
					}
				})
				_, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,component,component_backup_version,last_scheduled_at) SELECT 'dbs_wrong',project_id,resource_id,binding_id,destination_id,actor_id,1,frequency,timezone,false,'other',component_backup_version,last_scheduled_at FROM hosting_database_backup_schedules WHERE id=$1`, created.ID)
				if e != nil {
					t.Fatal(e)
				}
				_, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,component,component_backup_version) VALUES('dbs_wrong',1,'usr_bootstrap',1,'every_minute','UTC',false,'other','postgresql.compose-logical-local.v1')`)
				if e != nil {
					t.Fatal(e)
				}
				// The BEFORE trigger validates component identity before uniqueness constraints.
				if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_occurrences(id,schedule_id,schedule_revision,scheduled_at,operation_id,state,reason) SELECT 'dbocc_wrong','dbs_wrong',1,last_scheduled_at,$1,'admitted','' FROM hosting_database_backup_schedules WHERE id='dbs_wrong'`, *operation); e == nil {
					t.Fatal("mismatched component operation accepted")
				} else {
					var constraint *pgconn.PgError
					if !errors.As(e, &constraint) || constraint.Code != "23514" || constraint.Message != "backup occurrence operation mismatch" {
						t.Fatal("wrong mismatch rejection", e)
					}
				}
				if e = tx.Rollback(ctx); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "grant" {
				if _, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", resource.ID, "usr_bootstrap", "", in); !errors.Is(e, auth.ErrForbidden) {
					t.Fatal("revoked grant replay bypassed current access", e)
				}
			}
			if scenario == "held" || scenario == "removed" || scenario == "capability" {
				replay, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", resource.ID, "usr_bootstrap", "", in)
				if e != nil || !reflect.DeepEqual(replay, created) {
					t.Fatal("outage request replay", replay, e)
				}
				list, e := pg.ListDatabaseBackupSchedules(ctx, "usr_bootstrap", resource.ID)
				if e != nil || len(list) != 1 {
					t.Fatal("outage list", e)
				}
				if _, e = pg.ListDatabaseBackupOccurrences(ctx, "usr_bootstrap", resource.ID, created.ID); e != nil {
					t.Fatal("outage history", e)
				}
				disable := in
				disable.RequestID = "disable_outage"
				disable.ExpectedResourceGeneration = resource.Generation
				disable.ExpectedVersion = 1
				disable.Enabled = false
				disable.ExpectedDeployedOperationID = ""
				disabled, e := pg.SaveDatabaseBackupSchedule(ctx, "unused", resource.ID, "usr_bootstrap", created.ID, disable)
				if e != nil || disabled.Enabled || disabled.Component != in.Component || disabled.Revision != 2 {
					t.Fatal("disable during outage", disabled, e)
				}
			}
		})
	}
}

func redeployScheduleStack(t *testing.T, pg *store.PostgresStore, server domain.Server, resource domain.Resource, previous domain.HostingOperation, remove bool, beforeApply ...func(domain.HostingAuthority, domain.HostingOperation)) domain.HostingOperation {
	t.Helper()
	ctx := t.Context()
	input := hostingDeploymentRequest()
	input.ExpectedGeneration = resource.Generation
	input.ExpectedRuntimeConfigurationGeneration = previous.Snapshot.RuntimeConfiguration.Generation
	input.IdempotencyKey = "schedule-redeploy"
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	op, err := pg.RequestHostingDeployment(ctx, "hop_schedule_redeploy", resource.ID, "usr_bootstrap", input, destinationEvent("schedule_redeploy"))
	if err != nil {
		t.Fatal("redeploy admission", err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal("redeploy prepare", err)
	}
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, server.RunnerID, a); err != nil {
		t.Fatal("redeploy runner plan", err)
	}
	images := append([]domain.HostingStackImage(nil), previous.Plan.Stack.Images...)
	if remove {
		images = images[:1]
	}
	release := domain.HostingRelease{DaemonID: previous.Release.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	prepared, err := pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "schedule_redeploy_prepared", Release: release, StackImages: images})
	if err != nil {
		t.Fatal("redeploy prepared", err)
	}
	for _, before := range beforeApply {
		before(a, prepared)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
		t.Fatal("redeploy begin", err)
	}
	volumes := append([]domain.HostingStackVolume(nil), previous.Result.Stack.Volumes...)
	if _, err = pg.HostingStackVolumes(ctx, server.RunnerID, domain.HostingStackVolumeRequest{HostingAuthority: a, PlanHash: prepared.Plan.Hash, Volumes: volumes}); err != nil {
		t.Fatal("redeploy volumes", err)
	}
	state := *previous.Result.Stack
	state.Components = append([]domain.HostingStackComponent(nil), state.Components...)
	if remove {
		state.Components = state.Components[:1]
	}
	for i := range state.Components {
		state.Components[i].OperationID = op.ID
		state.Components[i].ContainerID = strings.Repeat("c", 63) + string(rune('1'+i))
	}
	state.Volumes = volumes
	op, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "schedule_redeploy_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}})
	if err != nil {
		t.Fatal("redeploy settle", err)
	}
	return op
}
