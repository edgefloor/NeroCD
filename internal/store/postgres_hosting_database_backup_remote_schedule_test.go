package store_test

import (
	"errors"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresRemoteBackupDisabledScheduleRetainsUnusableSelection(t *testing.T) {
	for _, kind := range []string{"target_edited", "target_disabled", "keys_replaced"} {
		t.Run(kind, func(t *testing.T) {
			f, target := remoteBackupFixture(t)
			in := domain.HostingDatabaseBackupScheduleInput{RequestID: "schedule_create", ExpectedResourceGeneration: f.resource.Generation, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}}
			saved, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", in)
			if err != nil {
				t.Fatal(err)
			}
			edit := domain.HostingDatabaseBackupTargetInput{RequestID: "target_change", ExpectedRevision: 1, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: kind != "target_disabled"}
			var credentials *app.BackupTargetCredentials
			if kind == "keys_replaced" {
				credentials, err = app.NewBackupTargetCredentials([]byte("replacement-access"), []byte("replacement-secret"))
				if err != nil {
					t.Fatal(err)
				}
				defer credentials.Close()
			}
			if _, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, edit, credentials); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "schedule_disable"
			in.ExpectedVersion = saved.Revision
			in.Enabled = false
			disabled, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in)
			if err != nil || disabled.Enabled || !reflect.DeepEqual(disabled.RemoteTarget, in.RemoteTarget) {
				t.Fatal("owner could not disable pinned unusable target", err)
			}
			replay, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in)
			if err != nil || !reflect.DeepEqual(disabled, replay) {
				t.Fatal("disable replay changed saved revision", err)
			}
			in.RequestID = "reenable_rejected"
			in.ExpectedVersion = disabled.Revision
			in.Enabled = true
			if _, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in); !errors.Is(err, store.ErrConflict) {
				t.Fatal("reenabled unusable remote selection", err)
			}
			in.Enabled = false
			in.RequestID = "disable_with_omitted_selection"
			in.RemoteTarget = nil
			kept, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in)
			if err != nil || !reflect.DeepEqual(kept.RemoteTarget, disabled.RemoteTarget) {
				t.Fatal("disable discarded immutable selection", err)
			}
			// Disabled state cannot introduce even a valid target from another project.
			foreignKeys, err := app.NewBackupTargetCredentials([]byte("foreign-access"), []byte("foreign-secret"))
			if err != nil {
				t.Fatal(err)
			}
			defer foreignKeys.Close()
			foreign, err := f.service.SaveDatabaseBackupTarget(f.ctx, "proj_security", "", domain.HostingDatabaseBackupTargetInput{RequestID: "foreign_target", Name: "Foreign", Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: true}, foreignKeys)
			if err != nil {
				t.Fatal(err)
			}
			in.RequestID = "foreign_selection"
			in.ExpectedVersion = kept.Revision
			in.RemoteTarget = &domain.BackupTargetSelection{TargetID: foreign.ID, Revision: 1}
			if _, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in); err == nil {
				t.Fatal("disabled update accepted foreign selection")
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, f.op.CreatedBy); err != nil {
				t.Fatal(err)
			}
			in.RequestID = "revoked_owner"
			in.RemoteTarget = kept.RemoteTarget
			if _, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, saved.ID, in); !errors.Is(err, auth.ErrForbidden) {
				t.Fatal("disabled update bypassed owner authority", err)
			}
		})
	}
}

func composeMySQLRemoteScheduleFixture(t *testing.T) (composeMySQLBackupFixture, networkCaptureFixture, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	f := newComposeMySQLBackupFixture(t)
	runner := f.sourceOp.Snapshot.RunnerID
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	network, target := remoteBackupFixtureFor(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: f.pg, db: f.db, resource: f.source.Resource, runner: runner, op: f.sourceOp}, service: f.service, ctx: f.ctx})
	return f, network, target
}

func TestPostgresComposeMySQLRemoteScheduleDueAndHistoricalDisable(t *testing.T) {
	for _, redeploy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "redeployed"}[redeploy], func(t *testing.T) {
			f, network, target := composeMySQLRemoteScheduleFixture(t)
			input := domain.HostingDatabaseBackupScheduleInput{RequestID: "mysql_remote_schedule", ExpectedResourceGeneration: f.source.Resource.Generation, Component: "mysql", ExpectedDeployedOperationID: f.sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}
			// A healthy deployment alone does not authorize the component backup adapter.
			if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, network.runner, domain.HostingDatabaseMySQLBackupComponentCapability); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, "", input); !errors.Is(err, store.ErrConflict) {
				t.Fatal("remote schedule without local component adapter", err)
			}
			if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, network.runner, domain.HostingDatabaseMySQLBackupComponentCapability); err != nil {
				t.Fatal(err)
			}
			saved, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, "", input)
			if err != nil || saved.Version != domain.HostingDatabaseMySQLBackupComponentScheduleVersion || !reflect.DeepEqual(saved.RemoteTarget, input.RemoteTarget) {
				t.Fatal("MySQL remote schedule admission", err)
			}
			if redeploy {
				f.sourceOp = redeployScheduleStack(t, f.pg, domain.Server{RunnerID: network.runner}, f.source.Resource, f.sourceOp, false, func(a domain.HostingAuthority, p domain.HostingOperation) {
					var credential string
					if err := f.db.QueryRow(f.ctx, `SELECT token_hash FROM runners WHERE id=$1`, network.runner).Scan(&credential); err != nil {
						t.Fatal(err)
					}
					delivery, err := f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, network.runner, credential, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: p.Plan.Hash, RuntimeConfiguration: f.source.RuntimeConfiguration, DeliveryID: strings.Repeat("c", 32)})
					if err != nil {
						t.Fatal(err)
					}
					delivery.Close()
				})
				network.op = f.sourceOp
			}
			for range 2 {
				if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			occurrences, err := f.service.ListDatabaseBackupOccurrences(f.ctx, f.source.Resource.ID, saved.ID)
			if err != nil || len(occurrences) != 1 || occurrences[0].OperationID == "" || occurrences[0].ScheduleRevision != 1 {
				t.Fatal("remote due occurrence", err)
			}
			var plan domain.HostingDatabaseBackupPlan
			if err = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backups WHERE id=$1`, occurrences[0].OperationID).Scan(&plan); err != nil || plan.Operation.Version != domain.HostingDatabaseMySQLBackupComponentVersion || plan.Source.OperationID != f.sourceOp.ID || !reflect.DeepEqual(plan.Operation.RemoteTarget, input.RemoteTarget) || plan.Validate() != nil {
				t.Fatal("due remote target or current component changed", err)
			}
			backup := completeBackup(t, network, plan.Operation.ID)
			uploads, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
			if err != nil || len(uploads) != 1 || uploads[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion {
				t.Fatal("due MySQL upload missing", err)
			}
			settleRemoteTransfer(t, network, uploads[0].ID, "access_denied")
			// Historical disable does not need a currently usable source or credential.
			if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id IN (SELECT access_id FROM hosting_database_backup_target_versions WHERE target_id=$1)`, target.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, f.source.Binding.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, network.runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
				t.Fatal(err)
			}
			disable := input
			disable.RequestID = "disable_mysql_remote_schedule"
			disable.ExpectedVersion = 1
			disable.Enabled = false
			disable.ExpectedDeployedOperationID = ""
			disable.RemoteTarget = nil
			disabled, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, saved.ID, disable)
			if err != nil || disabled.Revision != 2 || disabled.Enabled || disabled.Version != saved.Version || !reflect.DeepEqual(disabled.RemoteTarget, input.RemoteTarget) {
				t.Fatal("historical MySQL remote schedule disable", err)
			}
			replay, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, saved.ID, disable)
			if err != nil || !reflect.DeepEqual(replay, disabled) {
				t.Fatal("disabled schedule retry changed", err)
			}
			original, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, "", input)
			if err != nil || !reflect.DeepEqual(original, saved) {
				t.Fatal("committed create replay required current source or credentials", err)
			}
			disable.RequestID = "reenable_unavailable_mysql_remote"
			disable.ExpectedVersion = 2
			disable.Enabled = true
			disable.ExpectedDeployedOperationID = f.sourceOp.ID
			disable.RemoteTarget = input.RemoteTarget
			if _, err = f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, saved.ID, disable); err == nil {
				t.Fatal("unavailable remote schedule reenabled")
			}
			listed, err := f.service.ListDatabaseBackupSchedules(f.ctx, f.source.Resource.ID)
			if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], disabled) {
				t.Fatal("disable/replay lost immutable historical selection", err)
			}
		})
	}
}

func TestPostgresComposeMySQLRemoteScheduleCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"component_capability", "remote_capability", "target_disabled", "credential_revoked", "grant", "actor"} {
		t.Run(scenario, func(t *testing.T) {
			f, network, target := composeMySQLRemoteScheduleFixture(t)
			input := domain.HostingDatabaseBackupScheduleInput{RequestID: "mysql_remote_schedule_authority", ExpectedResourceGeneration: f.source.Resource.Generation, Component: "mysql", ExpectedDeployedOperationID: f.sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}
			saved, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.source.Resource.ID, "", input)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "component_capability", "remote_capability":
				capability := domain.HostingDatabaseMySQLBackupComponentCapability
				if scenario == "remote_capability" {
					capability = domain.HostingDatabaseMySQLBackupRemoteCapability
				}
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, network.runner, capability)
			case "target_disabled":
				_, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "disable_remote_target", ExpectedRevision: target.Revision, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil)
			case "credential_revoked":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id IN(SELECT access_id FROM hosting_database_backup_target_versions WHERE target_id=$1)`, target.ID)
			case "grant":
				_, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.source.Binding.DestinationID)
			case "actor":
				_, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`)
			}
			if err != nil {
				t.Fatal("authority setup", err)
			}
			for range 2 {
				if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			wantState := "skipped"
			if scenario == "grant" || scenario == "actor" {
				wantState = "denied"
			}
			var state string
			var operation *string
			var backups, uploads int
			if err = f.db.QueryRow(f.ctx, `SELECT state,operation_id,(SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_transfers WHERE mode='upload') FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, saved.ID).Scan(&state, &operation, &backups, &uploads); err != nil || state != wantState || operation != nil || backups != 0 || uploads != 0 {
				t.Fatal("revoked remote schedule retained due work", err, state, backups, uploads)
			}
		})
	}
}
