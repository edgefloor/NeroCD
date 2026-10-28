package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func TestPostgresMySQLBackupScheduleLifecycle(t *testing.T) {
	f := scheduledBackupFixtureFor(t, "mysql")
	in := scheduledBackupInput(t, f, "mysql_schedule", true)
	saved, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", in)
	if err != nil || saved.Version != domain.HostingDatabaseMySQLBackupScheduleVersion || saved.Component != "" || saved.RemoteTarget != nil {
		t.Fatal("native local MySQL schedule", err)
	}
	replay, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", in)
	if err != nil || !reflect.DeepEqual(saved, replay) {
		t.Fatal("create retry changed schedule", err)
	}
	for range 2 {
		if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
			t.Fatal("due discovery", err)
		}
	}
	history := scheduledOccurrences(t, f, saved.ID)
	if len(history) != 1 || history[0].OperationID == "" || history[0].State != "queued" || history[0].ScheduleRevision != 1 {
		t.Fatal("ticks did not retain exactly one MySQL occurrence", history)
	}
	var jobs int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND args=jsonb_build_object('operation_id',$2::text)`, hostingjobs.DatabaseBackupKind, history[0].OperationID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("due operation queue identity", err, jobs)
	}
	in.RequestID, in.ExpectedVersion, in.Enabled = "mysql_schedule_disable", 1, false
	disabled, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in)
	if err != nil || disabled.ID != saved.ID || disabled.Revision != 2 || disabled.LastScheduledAt == nil || disabled.Version != saved.Version {
		t.Fatal("disable lost schedule identity or cursor", err)
	}
	// Disabling discovery leaves the previously admitted operation executable.
	a, plan := claimBackup(t, f, history[0].OperationID)
	if plan.Operation.Version != domain.HostingDatabaseMySQLBackupVersion || plan.Source.Snapshot.Database.Engine != "mysql" || plan.Operation.RemoteTarget != nil || plan.Component != nil {
		t.Fatal("schedule did not use existing exact MySQL backup protocol")
	}
	if !reflect.DeepEqual(plan.Credentials, plan.SourcePlan.Database.Credentials) {
		t.Fatal("scheduled backup substituted existing source credential refs")
	}
	mutation := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "mysql_schedule_intent", PlanHash: plan.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal("scheduled backup intent", err)
	}
	mutation.Phase, mutation.ReceiptID = "complete", "mysql_schedule_complete"
	mutation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: "mysql_sql"}
	done, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation)
	if err != nil || done.State != "succeeded" || done.OwnershipHeld || done.Blob == nil || done.Blob.Format != "mysql_sql" {
		t.Fatal("scheduled MySQL artifact completion", err)
	}
	again, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation)
	if err != nil || !reflect.DeepEqual(again, done) {
		t.Fatal("scheduled completion replay", err)
	}
	if got := scheduledOccurrences(t, f, saved.ID); len(got) != 1 || got[0].State != "succeeded" || got[0].OwnershipHeld || got[0].ScheduleRevision != 1 {
		t.Fatal("immutable occurrence completion projection", got)
	}
	// Historical request receipts retain their original revision after edits.
	original := in
	original.RequestID, original.ExpectedVersion, original.Enabled = "mysql_schedule", 0, true
	replay, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", original)
	if err != nil || !reflect.DeepEqual(replay, saved) {
		t.Fatal("historical schedule retry changed", err)
	}
	in.RequestID, in.ExpectedVersion, in.Enabled, in.Timezone = "mysql_schedule_edit", 2, true, "Etc/UTC"
	edited, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in)
	if err != nil || edited.Revision != 3 || edited.Timezone != "Etc/UTC" || !edited.LastScheduledAt.Equal(*disabled.LastScheduledAt) {
		t.Fatal("edit reset immutable cursor", err)
	}
	for _, change := range []func(*domain.HostingDatabaseBackupScheduleInput){
		func(v *domain.HostingDatabaseBackupScheduleInput) { v.ExpectedVersion = 1 },
		func(v *domain.HostingDatabaseBackupScheduleInput) { v.ExpectedResourceGeneration++ },
		func(v *domain.HostingDatabaseBackupScheduleInput) { v.Frequency = "weekly"; v.RequestID = in.RequestID },
	} {
		stale := in
		stale.RequestID, stale.ExpectedVersion = "mysql_schedule_stale", 3
		change(&stale)
		if _, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, stale); !errors.Is(err, store.ErrConflict) {
			t.Fatal("stale or changed retry accepted", err)
		}
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil || len(scheduledOccurrences(t, f, saved.ID)) != 1 {
		t.Fatal("edit replayed already admitted slot", err)
	}
	listed, err := f.service.ListDatabaseBackupSchedules(f.ctx, f.resource.ID)
	if err != nil || len(listed) != 1 || listed[0].Version != saved.Version || listed[0].Revision != 3 {
		t.Fatal("MySQL list lost recipe version", err)
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"component", "remote_target", "credentials", "password", "location_id", "runner_id", "source_plan", "/var/"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("schedule disclosed private or unsupported fields", forbidden)
		}
	}
}

func TestPostgresMySQLBackupScheduleClosedScope(t *testing.T) {
	f := scheduledBackupFixtureFor(t, "mysql")
	in := scheduledBackupInput(t, f, "mysql_scope", false)
	for _, change := range []func(*domain.HostingDatabaseBackupScheduleInput){
		func(v *domain.HostingDatabaseBackupScheduleInput) {
			v.RemoteTarget = &domain.BackupTargetSelection{TargetID: "remote", Revision: 1}
		},
		func(v *domain.HostingDatabaseBackupScheduleInput) {
			v.Component = "mysql"
			v.ExpectedDeployedOperationID = f.op.ID
		},
		func(v *domain.HostingDatabaseBackupScheduleInput) { v.ExpectedDeployedOperationID = f.op.ID },
	} {
		invalid := in
		change(&invalid)
		if _, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", invalid); err == nil {
			t.Fatal("MySQL accepted remote or component schedule")
		}
	}
	// A real principal who owns another project cannot create or inspect this schedule.
	other, err := f.service.CreateProject(f.ctx, app.ProjectInput{Name: "MySQL schedule other project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.UpsertProjectMember(f.ctx, domain.ProjectMember{ID: "mysql_schedule_other_owner", ProjectID: other.ID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(f.ctx, auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	if _, err = f.service.SaveDatabaseBackupSchedule(viewer, f.resource.ID, "", in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign project owner created schedule", err)
	}
	if _, err = f.service.ListDatabaseBackupSchedules(viewer, f.resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign project owner inspected schedules", err)
	}
	if _, err = f.pg.GetDatabaseBackupRetention(f.ctx, f.op.CreatedBy, f.resource.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("MySQL gained retention policy access", err)
	}
	retention := domain.HostingDatabaseBackupRetentionInput{RequestID: "mysql_retention", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}
	if _, err = f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, retention); !errors.Is(err, store.ErrConflict) {
		t.Fatal("MySQL gained automatic retention", err)
	}
	var schedules, policies int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_schedules),(SELECT count(*) FROM hosting_database_backup_retention_policies)`).Scan(&schedules, &policies); err != nil || schedules != 0 || policies != 0 {
		t.Fatal("denial left schedule or retention policy", err, schedules, policies)
	}
}

func TestPostgresMySQLBackupScheduleCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"source", "grant", "owner", "backup_capability", "engine_capability", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			f := scheduledBackupFixtureFor(t, "mysql")
			saved := saveBackupSchedule(t, f, "mysql_authority", scheduledBackupInput(t, f, "mysql_authority", true))
			var err error
			switch scenario {
			case "source":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_binding_state SET healthy=NULL WHERE binding_id=$1`, f.op.BindingID)
			case "grant":
				_, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.op.Snapshot.DestinationID)
			case "owner":
				_, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, f.op.CreatedBy)
			case "backup_capability":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupCapability)
			case "engine_capability":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability)
			case "overlap":
				_, err = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "mysql_overlap", ExpectedResourceGeneration: f.resource.Generation})
			}
			if err != nil {
				t.Fatal("authority change", err)
			}
			if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
				t.Fatal("denial recording", err)
			}
			var state, reason string
			var operation *string
			if err = f.db.QueryRow(f.ctx, `SELECT state,reason,operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, saved.ID).Scan(&state, &reason, &operation); err != nil || operation != nil || state != "skipped" && state != "denied" || reason == "" {
				t.Fatal("unavailable source admitted scheduled mutation", err, state, reason)
			}
			if scenario == "grant" && (state != "skipped" || reason != "source_unavailable") {
				t.Fatal("native grant denial classification changed", state, reason)
			}
			var backups int
			want := 0
			if scenario == "overlap" {
				want = 1
			}
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&backups); err != nil || backups != want {
				t.Fatal("denial partially admitted backup", err, backups)
			}
		})
	}
}

func TestPostgresDatabaseBackupScheduleUnsupportedNativeRecipe(t *testing.T) {
	pg, _, service, ctx, input := hostingDatabaseFixture(t, "redis")
	view, err := service.CreateHostingDatabase(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule := domain.HostingDatabaseBackupScheduleInput{RequestID: "redis_schedule", ExpectedResourceGeneration: 1, Frequency: "daily", Timezone: "UTC", Enabled: true}
	if _, err = service.SaveDatabaseBackupSchedule(ctx, view.Resource.ID, "", schedule); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unsupported native recipe gained schedule admission", err)
	}
	if _, err = pg.ListDatabaseBackupSchedules(ctx, view.Resource.CreatedBy, view.Resource.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unsupported native recipe gained schedule listing", err)
	}
}
