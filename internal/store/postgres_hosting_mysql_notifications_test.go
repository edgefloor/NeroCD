package store_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
)

func TestPostgresMySQLUploadNotificationPreservesBackupAndReceipt(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		name := "immediate_failed"
		if scheduled {
			name = "scheduled_uncertain"
		}
		t.Run(name, func(t *testing.T) {
			f, target := mysqlRemoteFixture(t)
			var code atomic.Int32
			code.Store(http.StatusNoContent)
			if !scheduled {
				code.Store(http.StatusServiceUnavailable)
			}
			payloads := make(chan domain.NotificationPayload, 3)
			receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload domain.NotificationPayload
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Error("invalid notification payload")
				}
				payloads <- payload
				w.WriteHeader(int(code.Load()))
			}))
			defer receiver.Close()
			sender, service := notificationStore(t, f, receiver)
			notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
			var backup domain.HostingDatabaseBackupOperation
			var err error
			if scheduled {
				input := scheduledBackupInput(t, f, "notify_mysql_schedule", true)
				input.RemoteTarget = &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}
				schedule, e := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", input)
				if e != nil {
					t.Fatal(e)
				}
				for range 2 {
					if e = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); e != nil {
						t.Fatal(e)
					}
				}
				occurrences := scheduledOccurrences(t, f, schedule.ID)
				if len(occurrences) != 1 || occurrences[0].OperationID == "" {
					t.Fatal("schedule did not admit one MySQL backup")
				}
				backup.ID = occurrences[0].OperationID
			} else {
				backup, err = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "notify_mysql_remote", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}})
				if err != nil {
					t.Fatal(err)
				}
			}
			backupAuthority, backupPlan := claimBackup(t, f, backup.ID)
			publication := domain.HostingDatabaseBackupMutation{HostingAuthority: backupAuthority, ReceiptID: "notify_mysql_backup_intent", PlanHash: backupPlan.Hash, Phase: "intent"}
			if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, publication); err != nil {
				t.Fatal(err)
			}
			publication.Phase, publication.ReceiptID = "complete", "notify_mysql_backup_complete"
			publication.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: "mysql_sql"}
			publication.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 1, Bytes: publication.Blob.Bytes, Created: true}
			backup, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, publication)
			if err != nil {
				t.Fatal("owned MySQL local publication", err)
			}
			uploads, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
			if err != nil || len(uploads) != 1 || uploads[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion {
				t.Fatal("missing exact MySQL upload", err)
			}
			a, p := claimRemoteTransfer(t, f, uploads[0].ID)
			mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "notify_mysql_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
			if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
				t.Fatal(err)
			}
			mutation.Phase, mutation.ReceiptID, mutation.FailureCode = "failed", "notify_mysql_terminal", "access_denied"
			if scheduled {
				mutation.Phase, mutation.FailureCode = "uncertain", "remote_unavailable"
			}
			if !scheduled {
				if _, err = f.db.Exec(f.ctx, `CREATE FUNCTION reject_mysql_notification_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_backup.notification_event' THEN RAISE EXCEPTION 'test rejected audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_mysql_notification_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_mysql_notification_audit()`); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err == nil {
					t.Fatal("notification audit failure committed upload outcome")
				}
				var events, receipts int
				if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_notification_events),(SELECT count(*) FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1 AND receipt_id=$2)`, a.OperationID, mutation.ReceiptID).Scan(&events, &receipts); err != nil || events != 0 || receipts != 0 {
					t.Fatal("upload outcome/event transaction was not rolled back", err)
				}
				if _, err = f.db.Exec(f.ctx, `DROP TRIGGER reject_mysql_notification_audit ON audit_events`); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
					t.Fatal("terminal receipt replay", err)
				}
			}
			deliveries, err := service.ListNotificationDeliveries(f.ctx, f.resource.ProjectID)
			if err != nil || len(deliveries) != 1 || deliveries[0].Event != "upload_failed" || deliveries[0].SourceID != a.OperationID || deliveries[0].SourceState != mutation.Phase || !deliveries[0].LocalCopyAvailable {
				t.Fatal("MySQL upload notification source identity", err)
			}
			var count int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_notification_events`).Scan(&count); err != nil || count != 1 {
				t.Fatal("replay duplicated upload event", err)
			}
			id := deliveries[0].ID
			if err = sender.SendBackupNotification(f.ctx, id); err != nil {
				t.Fatal(err)
			}
			if !scheduled {
				pending, e := service.GetNotificationDelivery(f.ctx, f.resource.ProjectID, id)
				if e != nil || pending.State != "pending" || pending.AttemptCount != 1 {
					t.Fatal("notification failure did not retain retry", e)
				}
				code.Store(http.StatusNoContent)
				if _, err = f.db.Exec(f.ctx, `UPDATE hosting_notification_deliveries SET next_attempt_at=clock_timestamp() WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				if err = sender.SendBackupNotification(f.ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			delivered, err := service.GetNotificationDelivery(f.ctx, f.resource.ProjectID, id)
			attempts := 1
			if !scheduled {
				attempts = 2
			}
			if err != nil || delivered.State != "delivered" || delivered.AttemptCount != attempts {
				t.Fatal("MySQL notification delivery did not settle", err)
			}
			for range attempts {
				payload := <-payloads
				encoded, _ := json.Marshal(payload)
				if payload.SourceID != a.OperationID || payload.Event != "upload_failed" || !payload.LocalCopyAvailable || !strings.HasPrefix(payload.RecoveryURL, "https://nerocd.example/projects?") || strings.Contains(string(encoded), "canary") {
					t.Fatal("unsafe MySQL notification payload")
				}
			}
			local, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
			if err != nil || local.State != "succeeded" || !reflect.DeepEqual(local.Blob, backup.Blob) {
				t.Fatal("upload or notification failure changed successful local backup", err)
			}
			if policy, e := f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID); e != nil || policy != nil {
				t.Fatal("notifications implicitly configured MySQL retention", e)
			}
		})
	}
}

func TestPostgresMySQLSkippedScheduleDoesNotNotify(t *testing.T) {
	f := scheduledBackupFixtureFor(t, "mysql")
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("skipped occurrence sent a notification")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	_, service := notificationStore(t, f, receiver)
	notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
	schedule, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", scheduledBackupInput(t, f, "notify_mysql_skipped", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE project_id=$1`, f.resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Revoked destination authority also denies history reads. Inspect only the
	// finite persisted occurrence facts, as in the existing schedule denial test.
	var state, reason string
	var operation *string
	if err = f.db.QueryRow(f.ctx, `SELECT state,reason,operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, schedule.ID).Scan(&state, &reason, &operation); err != nil || state != "skipped" || reason != "source_unavailable" || operation != nil {
		t.Fatal("source denial was not a discovery-only skipped occurrence", err)
	}
	var events, backups int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_notification_events),(SELECT count(*) FROM hosting_database_backups)`).Scan(&events, &backups); err != nil || events != 0 || backups != 0 {
		t.Fatal("skipped occurrence created a backup or notification", err)
	}
}
