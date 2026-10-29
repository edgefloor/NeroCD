package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresComposeMySQLScheduleLifecycle(t *testing.T) {
	for _, scenario := range []string{"admit", "redeploy", "held", "capability", "grant", "actor", "overlap", "saved_engine_drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := newComposeMySQLBackupFixture(t)
			in := domain.HostingDatabaseBackupScheduleInput{RequestID: "mysql_component_schedule", ExpectedResourceGeneration: f.source.Resource.Generation, Component: "mysql", ExpectedDeployedOperationID: f.sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
			var saved domain.HostingDatabaseBackupSchedule
			var err error
			if scenario == "saved_engine_drift" {
				// A legacy same-name PG schedule must not adopt the now-healthy MySQL component.
				tx, e := f.db.Begin(f.ctx)
				if e != nil {
					t.Fatal(e)
				}
				_, e = tx.Exec(f.ctx, `INSERT INTO hosting_database_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,component,component_backup_version) VALUES('dbs_mysql', $1,$2,$3,$4,'usr_bootstrap',1,'every_minute','UTC',true,'mysql','postgresql.compose-logical-local.v1')`, f.source.Resource.ProjectID, f.source.Resource.ID, f.source.Binding.ID, f.source.Binding.DestinationID)
				if e == nil {
					_, e = tx.Exec(f.ctx, `INSERT INTO hosting_database_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,component,component_backup_version) VALUES('dbs_mysql',1,'usr_bootstrap',1,'every_minute','UTC',true,'mysql','postgresql.compose-logical-local.v1')`)
				}
				if e != nil {
					_ = tx.Rollback(f.ctx)
					t.Fatal(e)
				}
				if e = tx.Commit(f.ctx); e != nil {
					t.Fatal(e)
				}
				list, e := f.pg.ListDatabaseBackupSchedules(f.ctx, "usr_bootstrap", f.source.Resource.ID)
				if e != nil || len(list) != 1 || list[0].Version != domain.HostingDatabaseBackupComponentScheduleVersion {
					t.Fatal("saved engine identity changed", e)
				}
				saved = list[0]
			} else {
				saved, err = f.pg.SaveDatabaseBackupSchedule(f.ctx, "dbs_mysql", f.source.Resource.ID, "usr_bootstrap", "", in)
				if err != nil || saved.Version != domain.HostingDatabaseMySQLBackupComponentScheduleVersion {
					t.Fatal("MySQL component schedule", err)
				}
				replay, e := f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.source.Resource.ID, "usr_bootstrap", "", in)
				if e != nil || !reflect.DeepEqual(saved, replay) {
					t.Fatal("create retry changed identity", e)
				}
				bad := in
				bad.RequestID = "mysql_schedule_wrong_cas"
				bad.ExpectedVersion = 2
				if _, e = f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.source.Resource.ID, "usr_bootstrap", saved.ID, bad); !errors.Is(e, store.ErrConflict) {
					t.Fatal("stale schedule CAS admitted", e)
				}
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_schedules SET component_backup_version=CASE WHEN component_backup_version='postgresql.compose-logical-local.v1' THEN 'mysql.compose-logical-local.v1' ELSE 'postgresql.compose-logical-local.v1' END WHERE id=$1`, saved.ID); err == nil {
				t.Fatal("saved engine identity mutated")
			}
			err = nil
			switch scenario {
			case "redeploy":
				f.sourceOp = redeployScheduleStack(t, f.pg, domain.Server{RunnerID: f.sourceOp.Snapshot.RunnerID}, f.source.Resource, f.sourceOp, false, func(a domain.HostingAuthority, p domain.HostingOperation) {
					var credential string
					if e := f.db.QueryRow(f.ctx, `SELECT token_hash FROM runners WHERE id=$1`, p.Snapshot.RunnerID).Scan(&credential); e != nil {
						t.Fatal(e)
					}
					delivery, e := f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, p.Snapshot.RunnerID, credential, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: p.Plan.Hash, RuntimeConfiguration: f.source.RuntimeConfiguration, DeliveryID: strings.Repeat("c", 32)})
					if e != nil {
						t.Fatal("redeploy runtime delivery", e)
					}
					delivery.Close()
				})
			case "held":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, f.source.Binding.ID)
			case "capability":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.sourceOp.Snapshot.RunnerID, domain.HostingDatabaseMySQLBackupComponentCapability)
			case "grant":
				_, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.source.Binding.DestinationID)
			case "actor":
				_, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`)
			case "overlap":
				_, err = f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, f.input())
			}
			if err != nil {
				t.Fatal("scenario setup", err)
			}
			for range 2 {
				if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
					t.Fatal("due discovery", err)
				}
			}
			var state, reason string
			var operation *string
			if err = f.db.QueryRow(f.ctx, `SELECT state,reason,operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, saved.ID).Scan(&state, &reason, &operation); err != nil {
				t.Fatal(err)
			}
			want := "source_unavailable"
			switch scenario {
			case "admit", "redeploy":
				want = ""
			case "grant", "actor":
				want = "authority_denied"
			case "overlap":
				want = "source_busy"
			}
			if reason != want || (operation != nil) != (want == "") {
				t.Fatal("wrong due outcome", state, reason)
			}
			if scenario == "grant" || scenario == "actor" {
				return
			}
			list, e := f.pg.ListDatabaseBackupSchedules(f.ctx, "usr_bootstrap", f.source.Resource.ID)
			if e != nil || len(list) != 1 || list[0].Version != saved.Version {
				t.Fatal("stored engine lost during discovery drift", e)
			}
			history, e := f.pg.ListDatabaseBackupOccurrences(f.ctx, "usr_bootstrap", f.source.Resource.ID, saved.ID)
			if e != nil || len(history) != 1 || history[0].Component != "mysql" {
				t.Fatal("history identity", e)
			}
			if _, e = f.pg.ListDatabaseBackupOccurrences(f.ctx, "usr_bootstrap", f.target.Resource.ID, saved.ID); !errors.Is(e, store.ErrNotFound) {
				t.Fatal("cross-resource schedule history", e)
			}
			disabled := in
			disabled.RequestID = "mysql_schedule_disable"
			disabled.ExpectedVersion = 1
			disabled.Enabled = false
			disabled.ExpectedDeployedOperationID = ""
			out, e := f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.source.Resource.ID, "usr_bootstrap", saved.ID, disabled)
			if e != nil || out.Enabled || out.Revision != 2 || out.Version != saved.Version {
				t.Fatal("disable lost stored identity", e)
			}
			if operation != nil {
				var raw []byte
				var p domain.HostingDatabaseBackupPlan
				if e = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backups WHERE id=$1`, *operation).Scan(&raw); e != nil || json.Unmarshal(raw, &p) != nil || p.Operation.Version != domain.HostingDatabaseMySQLBackupComponentVersion || p.Source.OperationID != f.sourceOp.ID || p.Component.Source != "mysql" || p.Validate() != nil {
					t.Fatal("scheduled operation changed current MySQL source", e)
				}
			}
			if scenario == "saved_engine_drift" {
				disabled.Enabled = true
				disabled.ExpectedVersion = 2
				disabled.ExpectedDeployedOperationID = f.sourceOp.ID
				disabled.RequestID = "mysql_schedule_retarget"
				if _, e = f.pg.SaveDatabaseBackupSchedule(f.ctx, "unused", f.source.Resource.ID, "usr_bootstrap", saved.ID, disabled); !errors.Is(e, store.ErrConflict) {
					t.Fatal("engine drift retargeted schedule", e)
				}
			}
		})
	}
}
