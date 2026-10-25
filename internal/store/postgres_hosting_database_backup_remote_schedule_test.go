package store_test

import (
	"errors"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"reflect"
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
			// Disabled state cannot introduce a foreign or nonexistent selection.
			in.RequestID = "foreign_selection"
			in.ExpectedVersion = kept.Revision
			in.RemoteTarget = &domain.BackupTargetSelection{TargetID: "foreign-target", Revision: 1}
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
