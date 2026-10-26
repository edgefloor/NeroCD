package store_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/notificationtransport"
	"nerocd/internal/store"
)

func notificationStore(t *testing.T, f networkCaptureFixture, receiver *httptest.Server) (*store.PostgresStore, *app.Service) {
	t.Helper()
	pg, err := store.OpenPostgres(t.Context(), f.db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresStore(t, pg) })
	roots := x509.NewCertPool()
	roots.AddCert(receiver.Certificate())
	sender, err := notificationtransport.New(notificationtransport.Policy{FixtureTLSOrigins: []string{receiver.URL}, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureBackupNotifications(managedRing(t), sender, "https://nerocd.example"); err != nil {
		t.Fatal(err)
	}
	return pg, hostingDatabaseApplication(t, pg)
}
func notificationChannel(t *testing.T, service *app.Service, ctx context.Context, project, url string) domain.NotificationChannel {
	t.Helper()
	c := &domain.NotificationConfiguration{WebhookURL: url + "/private?token=canary_endpoint"}
	defer c.Close()
	v, err := service.SaveNotificationChannel(ctx, project, "", domain.NotificationChannelInput{RequestID: "notification_create", Name: "backup alerts", Type: "webhook", Enabled: true, Events: []string{"backup_failed", "upload_failed"}}, c)
	if err != nil {
		t.Fatal("save notification channel", err)
	}
	return v
}
func failedNotificationBackup(t *testing.T, f networkCaptureFixture, id string) (domain.HostingDatabaseBackupOperation, domain.HostingDatabaseBackupMutation) {
	t.Helper()
	v, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: id, ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal(err)
	}
	a, p := claimBackup(t, f, v.ID)
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "failed_" + v.ID, PlanHash: p.Hash, Phase: "failed", FailureCode: "execution_failed"}
	return v, in
}
func TestPostgresBackupNotificationAtomicReplayAndChannelRevision(t *testing.T) {
	f := backupFixture(t)
	var calls atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer receiver.Close()
	pg, service := notificationStore(t, f, receiver)
	channel := notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
	backup, in := failedNotificationBackup(t, f, "notify_atomic")
	if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION reject_notification_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_backup.notification_event' THEN RAISE EXCEPTION 'test rejected audit'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_notification_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_notification_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err == nil {
		t.Fatal("audit failure committed source")
	}
	var state string
	var count int
	if err := f.db.QueryRow(f.ctx, `SELECT state,(SELECT count(*) FROM hosting_notification_events) FROM hosting_database_backups WHERE id=$1`, backup.ID).Scan(&state, &count); err != nil || state != "dispatched" || count != 0 {
		t.Fatal("source/outbox rollback mismatch", state, count, err)
	}
	if _, err := f.db.Exec(f.ctx, `DROP TRIGGER reject_notification_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal("replay", err)
	}
	deliveries, err := service.ListNotificationDeliveries(f.ctx, f.resource.ProjectID)
	if err != nil || len(deliveries) != 1 || deliveries[0].SourceID != backup.ID || deliveries[0].SourceState != "failed" {
		t.Fatal("immutable source missing", deliveries, err)
	}
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_notification_events`).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay duplicated event", count, err)
	}
	_, err = service.SaveNotificationChannel(f.ctx, channel.ProjectID, channel.ID, domain.NotificationChannelInput{RequestID: "disable_notification", ExpectedGeneration: channel.Generation, Name: channel.Name, Type: channel.Type, Enabled: false, Events: channel.Events}, nil)
	if err != nil {
		t.Fatal("disable", err)
	}
	if err = pg.SendBackupNotification(f.ctx, deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	after, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, deliveries[0].ID)
	if err != nil || after.State != "suppressed" || after.AttemptCount != 0 || calls.Load() != 0 {
		t.Fatal("obsolete delivery sent", after, err)
	}
	if _, err = service.SaveNotificationChannel(f.ctx, channel.ProjectID, channel.ID, domain.NotificationChannelInput{RequestID: "stale_notification", ExpectedGeneration: channel.Generation, Name: channel.Name, Type: channel.Type, Enabled: true, Events: channel.Events}, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
}
func TestPostgresBackupNotificationSerialSendAndActorRevocation(t *testing.T) {
	f := backupFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	payload := make(chan domain.NotificationPayload, 2)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var p domain.NotificationPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		payload <- p
		entered <- struct{}{}
		<-release
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	pg, service := notificationStore(t, f, receiver)
	notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
	backup, in := failedNotificationBackup(t, f, "notify_send")
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	deliveries, err := service.ListNotificationDeliveries(f.ctx, f.resource.ProjectID)
	if err != nil || len(deliveries) != 1 {
		t.Fatal(err)
	}
	id := deliveries[0].ID
	// The actor that requested the backup can lose authority after the accepted
	// source event. It must not suppress alerts through a project-owned channel.
	if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- pg.SendBackupNotification(t.Context(), id) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("delivery did not start")
	}
	if err = pg.SendBackupNotification(t.Context(), id); err != nil {
		close(release)
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		close(release)
		t.Fatal("concurrent duplicate sent")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	p := <-payload
	if p.SourceID != backup.ID || p.DeliveryID != id || p.SourceState != "failed" || !strings.HasPrefix(p.RecoveryURL, "https://nerocd.example/projects?") || strings.Contains(p.RecoveryURL, "canary") {
		t.Fatal("unsafe or missing payload identity", p)
	}
	var state string
	var attempts int
	if err = f.db.QueryRow(t.Context(), `SELECT state,attempt_count FROM hosting_notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts); err != nil || state != "delivered" || attempts != 1 {
		t.Fatal("delivery settlement", state, attempts, err)
	}
}
func TestPostgresBackupNotificationRetryBudgetsAndConfigurationFailures(t *testing.T) {
	f := backupFixture(t)
	var code atomic.Int32
	code.Store(503)
	var calls atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(code.Load()))
		_, _ = w.Write([]byte("canary_raw_receiver"))
	}))
	defer receiver.Close()
	pg, service := notificationStore(t, f, receiver)
	channel := notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
	backup, in := failedNotificationBackup(t, f, "notify_retry")
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, backup.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		if attempt > 1 {
			if _, err := f.db.Exec(f.ctx, `UPDATE hosting_notification_deliveries SET next_attempt_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := pg.SendBackupNotification(f.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.SendBackupNotification(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
	if err != nil || current.AttemptCount != 5 || current.State != "failed" || len(current.Attempts) != 5 || calls.Load() != 5 {
		t.Fatal("retry budget reset", current, err)
	}
	code.Store(401)
	for i, key := range []string{"notify_config_a", "notify_config_b", "notify_config_c"} {
		b, mutation := failedNotificationBackup(t, f, key)
		if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err != nil {
			t.Fatal(err)
		}
		if err = f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, b.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err = pg.SendBackupNotification(f.ctx, id); err != nil {
			t.Fatal(err)
		}
		current, err = service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
		if err != nil || current.State != "failed" || current.AttemptCount != 1 || current.FailureCode != "receiver_configuration" {
			t.Fatal("permanent config failure retried", current, err)
		}
		channels, e := service.ListNotificationChannels(f.ctx, channel.ProjectID)
		if e != nil || channels[0].ConsecutiveFailures != i+1 || channels[0].Enabled != (i < 2) {
			t.Fatal("config counter mismatch", channels, e)
		}
	}
	updated, err := service.SaveNotificationChannel(f.ctx, channel.ProjectID, channel.ID, domain.NotificationChannelInput{RequestID: "repair_channel", ExpectedGeneration: channel.Generation, Name: channel.Name, Type: channel.Type, Enabled: true, Events: channel.Events}, nil)
	if err != nil || updated.ConsecutiveFailures != 0 || !updated.Enabled {
		t.Fatal("config change failed to reset counter", updated, err)
	}
}

func TestPostgresBackupNotificationExpiredClaimRetainsOriginalBudgets(t *testing.T) {
	f := backupFixture(t)
	var calls atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer receiver.Close()
	pg, service := notificationStore(t, f, receiver)
	channel := notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
	backup, in := failedNotificationBackup(t, f, "notify_expired_claim")
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, backup.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE hosting_notification_deliveries SET state='sending',attempt_count=1,fence='old_worker',claim_expires_at=clock_timestamp()-INTERVAL '1 second' WHERE id=$1;`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `INSERT INTO hosting_notification_attempts(delivery_id,number,fence,state) VALUES($1,1,'old_worker','sending')`, id); err != nil {
		t.Fatal(err)
	}
	if err := pg.SendBackupNotification(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
	if err != nil || current.State != "delivered" || current.AttemptCount != 2 || len(current.Attempts) != 2 || current.Attempts[0].State != "uncertain" || current.Attempts[0].FailureCode != "delivery_uncertain" || calls.Load() != 1 {
		t.Fatal("expired claim lost attempt history", current, err)
	}
	second, mutation := failedNotificationBackup(t, f, "notify_expired_budget")
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, second.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_notification_deliveries SET deadline_at=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = pg.SendBackupNotification(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	current, err = service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
	if err != nil || current.State != "failed" || current.AttemptCount != 0 || current.FailureCode != "attempt_budget_exhausted" || calls.Load() != 1 {
		t.Fatal("original deadline reset", current, err)
	}
}

func TestPostgresBackupNotificationSMTPUsesCurrentMembersAfterActorRevocation(t *testing.T) {
	f := backupFixture(t)
	certSource := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certSource.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certSource.TLS.Certificates})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error("close SMTP fixture listener", err)
		}
	}()
	pg, err := store.OpenPostgres(t.Context(), f.db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresStore(t, pg) })
	roots := x509.NewCertPool()
	roots.AddCert(certSource.Certificate())
	sender, err := notificationtransport.New(notificationtransport.Policy{FixtureTLSOrigins: []string{"smtp+tls://" + listener.Addr().String()}, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureBackupNotifications(managedRing(t), sender, "https://nerocd.example"); err != nil {
		t.Fatal(err)
	}
	service := hostingDatabaseApplication(t, pg)
	if _, err = f.db.Exec(f.ctx, `INSERT INTO users(id,email,name,status) VALUES('usr_notify_a','authorized@example.com','authorized','active'),('usr_notify_b','revoked@example.com','revoked','active');INSERT INTO project_members(id,project_id,user_id,role) VALUES('mem_notify_a','proj_platform','usr_notify_a','viewer'),('mem_notify_b','proj_platform','usr_notify_b','viewer')`); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	configuration := &domain.NotificationConfiguration{SMTPHost: host, SMTPPort: number, SMTPSecurity: "tls", SMTPUsername: "fixture", SMTPPassword: "canary_smtp_password", FromAddress: "sender@example.com", RecipientUserIDs: []string{"usr_notify_a", "usr_notify_b"}}
	defer configuration.Close()
	channel, err := service.SaveNotificationChannel(f.ctx, f.resource.ProjectID, "", domain.NotificationChannelInput{RequestID: "create_smtp", Name: "email alerts", Type: "email", Enabled: true, Events: []string{"backup_failed"}}, configuration)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, key := range []string{"notify_members_a", "notify_members_b"} {
		backup, in := failedNotificationBackup(t, f, key)
		if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
			t.Fatal(err)
		}
		var id string
		if err = f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, backup.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err = f.db.Exec(f.ctx, `DELETE FROM project_members WHERE user_id='usr_notify_b';UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	recipient := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer func() {
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Error("close SMTP fixture connection", err)
			}
		}()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 fixture ESMTP\r\n")
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(conn, "250-fixture\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				recipient <- line
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case line == "DATA\r\n":
				_, _ = io.WriteString(conn, "354 data\r\n")
				for total := 0; ; {
					part, e := reader.ReadString('\n')
					if e != nil {
						return
					}
					total += len(part)
					if total > 16384 {
						return
					}
					if part == ".\r\n" {
						break
					}
				}
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case line == "QUIT\r\n":
				_, _ = io.WriteString(conn, "221 bye\r\n")
				return
			default:
				return
			}
		}
	}()
	if err = pg.SendBackupNotification(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-recipient:
		if !strings.Contains(line, "authorized@example.com") || strings.Contains(line, "revoked@example.com") {
			t.Fatal("revoked recipient used", line)
		}
	case <-time.After(time.Second):
		t.Fatal("no current member delivery")
	}
	<-done
	if _, err = f.db.Exec(t.Context(), `DELETE FROM project_members WHERE user_id='usr_notify_a'`); err != nil {
		t.Fatal(err)
	}
	if err = pg.SendBackupNotification(t.Context(), ids[1]); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = f.db.QueryRow(t.Context(), `SELECT state FROM hosting_notification_deliveries WHERE id=$1`, ids[1]).Scan(&state); err != nil || state != "suppressed" {
		t.Fatal("missing current members did not suppress", state, err)
	}
	_ = channel
}

func TestPostgresBackupNotificationDirectControllerSources(t *testing.T) {
	t.Run("accepted_schedule_retirement", func(t *testing.T) {
		f := scheduledBackupFixture(t)
		receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
		defer receiver.Close()
		_, service := notificationStore(t, f, receiver)
		notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
		_, backup := scheduleOldUnstarted(t, f)
		if err := f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
			t.Fatal(err)
		}
		items, err := service.ListNotificationDeliveries(f.ctx, f.resource.ProjectID)
		if err != nil || len(items) != 1 || items[0].SourceID != backup.ID || items[0].Event != "backup_failed" {
			t.Fatal("accepted retirement missing", items, err)
		}
		var receipts int
		if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_notification_source_receipts WHERE source_id=$1`, backup.ID).Scan(&receipts); err != nil || receipts != 1 {
			t.Fatal("controller receipt missing", receipts, err)
		}
	})
	t.Run("upload_admission_denied_preserves_backup", func(t *testing.T) {
		f := scheduledBackupFixture(t)
		receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
		defer receiver.Close()
		_, service := notificationStore(t, f, receiver)
		notificationChannel(t, service, f.ctx, f.resource.ProjectID, receiver.URL)
		if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability); err != nil {
			t.Fatal(err)
		}
		credentials, err := app.NewBackupTargetCredentials([]byte("fixture_access"), []byte("fixture_secret"))
		if err != nil {
			t.Fatal(err)
		}
		defer credentials.Close()
		target, err := f.service.SaveDatabaseBackupTarget(f.ctx, f.resource.ProjectID, "", domain.HostingDatabaseBackupTargetInput{RequestID: "notify_remote_target", Name: "fixture target", Endpoint: "https://backup.example", Bucket: "fixture", Region: "us-east-1", Enabled: true}, credentials)
		if err != nil {
			t.Fatal(err)
		}
		validation, err := f.service.ValidateDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "notify_validate", ExpectedRevision: target.Revision, DestinationID: f.op.Snapshot.DestinationID})
		if err != nil {
			t.Fatal(err)
		}
		settleRemoteTransfer(t, f, validation.ID, "")
		backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "notify_upload_source", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}})
		if err != nil {
			t.Fatal(err)
		}
		backup = completeBackup(t, f, backup.ID)
		transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
		if err != nil || len(transfers) != 1 {
			t.Fatal(err)
		}
		_, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "notify_disable_target", ExpectedRevision: target.Revision, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.pg.PrepareDatabaseBackupTransfer(f.ctx, transfers[0].ID); err != nil {
			t.Fatal(err)
		}
		items, err := service.ListNotificationDeliveries(f.ctx, f.resource.ProjectID)
		if err != nil || len(items) != 1 || items[0].SourceID != transfers[0].ID || items[0].Event != "upload_failed" {
			t.Fatal("upload denial event missing or validation included", items, err)
		}
		current, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
		if err != nil || current.State != "succeeded" || current.Blob == nil || current.Blob.Digest != backup.Blob.Digest {
			t.Fatal("upload failure altered backup", err)
		}
	})
}

func TestPostgresBackupNotificationRiverExplicitTestRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pg, db := openPostgresIntegrationStore(t, ctx, "notify_river")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	var calls atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer receiver.Close()
	roots := x509.NewCertPool()
	roots.AddCert(receiver.Certificate())
	sender, err := notificationtransport.New(notificationtransport.Policy{FixtureTLSOrigins: []string{receiver.URL}, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureBackupNotifications(managedRing(t), sender, "https://nerocd.example"); err != nil {
		t.Fatal(err)
	}
	for {
		tx, e := db.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		changed, e := hostingjobs.MigrateNextTx(ctx, tx)
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	service := hostingDatabaseApplication(t, pg)
	actorCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	channel := notificationChannel(t, service, actorCtx, "proj_platform", receiver.URL)
	d, err := service.TestNotificationChannel(actorCtx, channel.ProjectID, channel.ID, domain.NotificationTestInput{RequestID: "test_delivery", ExpectedGeneration: channel.Generation})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.TestNotificationChannel(actorCtx, channel.ProjectID, channel.ID, domain.NotificationTestInput{RequestID: "test_delivery", ExpectedGeneration: channel.Generation})
	if err != nil || d.ID != replay.ID {
		t.Fatal("test replay changed identity", err)
	}
	for {
		current, e := service.GetNotificationDelivery(actorCtx, channel.ProjectID, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		if current.State == "delivered" {
			if current.Event != "test" || current.SourceState != "test" || current.AttemptCount != 1 || calls.Load() != 1 {
				t.Fatal("explicit test delivery mismatch", current)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("River did not recover pending test", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	channels, err := service.ListNotificationChannels(actorCtx, channel.ProjectID)
	if err != nil || len(channels) != 1 || channels[0].ConsecutiveFailures != 0 {
		t.Fatal("test changed actual-event counters", err)
	}
}

type notificationSMTPRepairMessage struct {
	recipients []string
	body       string
}
type notificationSMTPRepairPeer struct {
	address   string
	policy    notificationtransport.Policy
	accepted  *atomic.Int32
	delivered *atomic.Int32
	messages  <-chan notificationSMTPRepairMessage
}

func newNotificationSMTPRepairPeer(t *testing.T, mode string, drops int, beforeAuthReply func(int) error) notificationSMTPRepairPeer {
	t.Helper()
	certificate := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(certificate.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	failures := make(chan error, 1)
	messages := make(chan notificationSMTPRepairMessage, 5)
	accepted, delivered := &atomic.Int32{}, &atomic.Int32{}
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("persistence SMTP peer did not join")
		}
		select {
		case err := <-failures:
			t.Error("persistence SMTP peer", err)
		default:
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Certificate())
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			attempt := int(accepted.Add(1))
			err = func() error {
				defer func(connection net.Conn) {
					if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
						t.Error("close SMTP repair fixture connection", err)
					}
				}(connection)
				_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
				reader := bufio.NewReader(connection)
				write := func(value string) error { _, err := io.WriteString(connection, value); return err }
				if mode == "starttls" {
					if err := write("220 fixture ESMTP\r\n"); err != nil {
						return err
					}
					line, err := reader.ReadString('\n')
					if err != nil || !strings.HasPrefix(line, "EHLO ") {
						return errors.New("missing SMTP hello")
					}
					if err = write("250-fixture\r\n250 STARTTLS\r\n"); err != nil {
						return err
					}
					line, err = reader.ReadString('\n')
					if err != nil || line != "STARTTLS\r\n" {
						return errors.New("missing STARTTLS request")
					}
					if err = write("220 upgrade\r\n"); err != nil {
						return err
					}
				}
				if attempt <= drops {
					return nil
				}
				secured := tls.Server(connection, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certificate.TLS.Certificates})
				if err := secured.Handshake(); err != nil {
					return err
				}
				connection = secured
				reader = bufio.NewReader(connection)
				if mode == "tls" {
					if err := write("220 fixture ESMTP\r\n"); err != nil {
						return err
					}
				}
				observation := notificationSMTPRepairMessage{}
				for {
					line, err := reader.ReadString('\n')
					if errors.Is(err, io.EOF) {
						return nil
					}
					if err != nil {
						return err
					}
					switch {
					case strings.HasPrefix(line, "EHLO "):
						if err = write("250-fixture\r\n250 AUTH PLAIN\r\n"); err != nil {
							return err
						}
					case strings.HasPrefix(line, "AUTH PLAIN "):
						decoded, e := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(line, "AUTH PLAIN ")))
						if e != nil || string(decoded) != "\x00fixture\x00canary-repair-password" {
							return errors.New("unexpected SMTP credentials")
						}
						if beforeAuthReply != nil {
							if e = beforeAuthReply(attempt); e != nil {
								return e
							}
						}
						if err = write("235 authenticated\r\n"); err != nil {
							return err
						}
					case strings.HasPrefix(line, "MAIL FROM:"):
						if err = write("250 accepted\r\n"); err != nil {
							return err
						}
					case strings.HasPrefix(line, "RCPT TO:"):
						observation.recipients = append(observation.recipients, strings.TrimSpace(line))
						if err = write("250 accepted\r\n"); err != nil {
							return err
						}
					case line == "DATA\r\n":
						if err = write("354 data\r\n"); err != nil {
							return err
						}
						var body strings.Builder
						for {
							part, e := reader.ReadString('\n')
							if e != nil {
								return e
							}
							if part == ".\r\n" {
								break
							}
							body.WriteString(part)
							if body.Len() > 16384 {
								return errors.New("SMTP body exceeded fixture bound")
							}
						}
						observation.body = body.String()
						messages <- observation
						delivered.Add(1)
						if err = write("250 queued\r\n"); err != nil {
							return err
						}
					case line == "QUIT\r\n":
						return write("221 bye\r\n")
					default:
						return errors.New("unexpected SMTP command")
					}
				}
			}()
			if err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
		}
	}()
	return notificationSMTPRepairPeer{address: listener.Addr().String(), policy: notificationtransport.Policy{FixtureTLSOrigins: []string{"smtp+" + mode + "://" + listener.Addr().String()}, RootCAs: roots}, accepted: accepted, delivered: delivered, messages: messages}
}

func notificationSMTPRepairStore(t *testing.T, f networkCaptureFixture, peer notificationSMTPRepairPeer, mode string) (*store.PostgresStore, *app.Service, domain.NotificationChannel, string) {
	t.Helper()
	pg, err := store.OpenPostgres(t.Context(), f.db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresStore(t, pg) })
	sender, err := notificationtransport.New(peer.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureBackupNotifications(managedRing(t), sender, "https://nerocd.example"); err != nil {
		t.Fatal(err)
	}
	service := hostingDatabaseApplication(t, pg)
	if _, err = f.db.Exec(f.ctx, `INSERT INTO users(id,email,name,status) VALUES('usr_notify_repair','old@example.com','repair recipient','active');INSERT INTO project_members(id,project_id,user_id,role) VALUES('mem_notify_repair','proj_platform','usr_notify_repair','viewer')`); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(peer.address)
	number, _ := strconv.Atoi(port)
	configuration := &domain.NotificationConfiguration{SMTPHost: host, SMTPPort: number, SMTPSecurity: mode, SMTPUsername: "fixture", SMTPPassword: "canary-repair-password", FromAddress: "sender@example.com", RecipientUserIDs: []string{"usr_notify_repair"}}
	defer configuration.Close()
	channel, err := service.SaveNotificationChannel(f.ctx, f.resource.ProjectID, "", domain.NotificationChannelInput{RequestID: "create_repair_email", Name: "repair email", Type: "email", Enabled: true, Events: []string{"backup_failed"}}, configuration)
	if err != nil {
		t.Fatal(err)
	}
	backup, in := failedNotificationBackup(t, f, "notification_repair_source")
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	var delivery string
	if err = f.db.QueryRow(f.ctx, `SELECT d.id FROM hosting_notification_deliveries d JOIN hosting_notification_events e ON e.id=d.event_id WHERE e.source_id=$1`, backup.ID).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	return pg, service, channel, delivery
}

func assertNotificationSMTPRepairPending(t *testing.T, f networkCaptureFixture, service *app.Service, channel domain.NotificationChannel, id string, attempt int, code string) {
	t.Helper()
	v, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
	if err != nil || v.State != "pending" || v.AttemptCount != attempt || v.FailureCode != code || len(v.Attempts) != attempt {
		t.Fatal("transient SMTP attempt became terminal or lost history", v, err)
	}
	channels, err := service.ListNotificationChannels(f.ctx, channel.ProjectID)
	if err != nil || len(channels) != 1 || !channels[0].Enabled || channels[0].ConsecutiveFailures != 0 {
		t.Fatal("transient SMTP failure changed configuration counter", channels, err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_notification_deliveries SET next_attempt_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBackupNotificationSMTPTransientTLSRetriesWithoutDisablingChannel(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			f := backupFixture(t)
			peer := newNotificationSMTPRepairPeer(t, mode, 3, nil)
			pg, service, channel, id := notificationSMTPRepairStore(t, f, peer, mode)
			for attempt := 1; attempt <= 3; attempt++ {
				if err := pg.SendBackupNotification(f.ctx, id); err != nil {
					t.Fatal(err)
				}
				assertNotificationSMTPRepairPending(t, f, service, channel, id, attempt, "smtp_unavailable")
			}
			if peer.delivered.Load() != 0 {
				t.Fatal("failed TLS attempt delivered a message")
			}
			if err := pg.SendBackupNotification(f.ctx, id); err != nil {
				t.Fatal(err)
			}
			v, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
			if err != nil || v.State != "delivered" || v.AttemptCount != 4 || peer.delivered.Load() != 1 || peer.accepted.Load() != 4 {
				t.Fatal("SMTP did not recover with original delivery", v, err)
			}
			message := <-peer.messages
			if !strings.Contains(message.body, "Message-ID: <"+id+"@nerocd.invalid>") {
				t.Fatal("SMTP retry changed delivery identity")
			}
		})
	}
}

func TestPostgresBackupNotificationSMTPChangedAddressRetriesCurrentMember(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			f := backupFixture(t)
			peer := newNotificationSMTPRepairPeer(t, mode, 0, func(attempt int) error {
				if attempt != 1 {
					return nil
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				_, err := f.db.Exec(ctx, `UPDATE users SET email='current@example.com' WHERE id='usr_notify_repair'`)
				return err
			})
			pg, service, channel, id := notificationSMTPRepairStore(t, f, peer, mode)
			if err := pg.SendBackupNotification(f.ctx, id); err != nil {
				t.Fatal(err)
			}
			assertNotificationSMTPRepairPending(t, f, service, channel, id, 1, "recipients_unavailable")
			if peer.delivered.Load() != 0 {
				t.Fatal("obsolete address received message")
			}
			if err := pg.SendBackupNotification(f.ctx, id); err != nil {
				t.Fatal(err)
			}
			v, err := service.GetNotificationDelivery(f.ctx, channel.ProjectID, id)
			if err != nil || v.State != "delivered" || v.AttemptCount != 2 || peer.delivered.Load() != 1 || peer.accepted.Load() != 2 {
				t.Fatal("current member retry did not deliver", v, err)
			}
			message := <-peer.messages
			if len(message.recipients) != 1 || !strings.Contains(message.recipients[0], "current@example.com") || strings.Contains(message.body, "old@example.com") {
				t.Fatal("changed member address was not re-resolved")
			}
		})
	}
}

func TestPostgresStorageBackupNotificationAtomicReceiptAndUncertainty(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(strconv.FormatBool(uncertain), func(t *testing.T) {
			f := newArchiveControlFixture(t)
			payload := make(chan domain.NotificationPayload, 2)
			receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p domain.NotificationPayload
				if json.NewDecoder(r.Body).Decode(&p) != nil {
					t.Error("invalid payload")
				}
				payload <- p
				w.WriteHeader(204)
			}))
			defer receiver.Close()
			pg, service := notificationStore(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{db: f.db}}, receiver)
			notificationChannel(t, service, f.owner, f.resource.ProjectID, receiver.URL)
			b := f.backup(t)
			op, a := f.claim(t, b.OperationID, "backup")
			in := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "storage_notification_failed", PlanHash: op.StorageArchive.Plan.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "not_needed", FailureCode: "helper_failed"}}
			if uncertain {
				in.Result.ResumeOutcome = "uncertain"
				in.Result.Uncertain = true
				in.Result.FailureCode = "resume_failed"
			}
			if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION reject_storage_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture notification failure'; END $$; CREATE TRIGGER reject_storage_notification BEFORE INSERT ON hosting_notification_events FOR EACH ROW EXECUTE FUNCTION reject_storage_notification()`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, in); err == nil {
				t.Fatal("notification failure committed source")
			}
			var state string
			var count int
			if err := f.db.QueryRow(f.ctx, `SELECT state,(SELECT count(*) FROM hosting_storage_archive_receipts WHERE operation_id=$1 AND phase='complete') FROM hosting_operations WHERE id=$1`, b.OperationID).Scan(&state, &count); err != nil || state != "prepared" || count != 0 {
				t.Fatalf("partial terminal commit %s %d %v", state, count, err)
			}
			if _, err := f.db.Exec(f.ctx, `DROP TRIGGER reject_storage_notification ON hosting_notification_events`); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				out, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, in)
				if err != nil || out.MutationHold != uncertain {
					t.Fatalf("terminal evidence hold %v %v", out.MutationHold, err)
				}
			}
			deliveries, err := service.ListNotificationDeliveries(f.owner, f.resource.ProjectID)
			if err != nil || len(deliveries) != 1 {
				t.Fatalf("deliveries %#v %v", deliveries, err)
			}
			d := deliveries[0]
			if d.SourceKind != "storage_backup" || d.SourceID != b.OperationID || d.LocalCopyAvailable || uncertain && (d.SourceState != "uncertain") {
				t.Fatalf("wrong source %#v", d)
			}
			if err = pg.SendBackupNotification(f.ctx, d.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-payload:
				if got.EventID != d.EventID || got.DeliveryID != d.ID || !strings.HasPrefix(got.Message, "Storage backup") || !strings.Contains(got.RecoveryURL, "source_kind=storage_backup") {
					t.Fatalf("wrong payload %#v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("webhook not delivered")
			}
			if err = pg.SendBackupNotification(f.ctx, d.ID); err != nil {
				t.Fatal(err)
			}
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_notification_events`).Scan(&count); err != nil || count != 1 {
				t.Fatal("event replay duplicated", count, err)
			}
		})
	}
}
func TestPostgresStorageUploadNotificationPreservesLocalAndExcludesDownload(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer receiver.Close()
	_, service := notificationStore(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{db: f.db}}, receiver)
	notificationChannel(t, service, f.owner, f.resource.ProjectID, receiver.URL)
	b := f.finishBackup(t, requestRemoteStorageBackup(t, f, target))
	transfers, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if err != nil || len(transfers) != 1 {
		t.Fatal(err)
	}
	failed := settleStorageRemote(t, f, transfers[0].ID, "access_denied")
	d, err := service.ListNotificationDeliveries(f.owner, f.resource.ProjectID)
	if err != nil || len(d) != 1 || d[0].SourceKind != "storage_transfer" || d[0].SourceID != failed.ID || !d[0].LocalCopyAvailable {
		t.Fatalf("upload source %#v %v", d, err)
	}
	current, err := f.service.GetStorageBackup(f.owner, b.ID)
	if err != nil || current.State != "succeeded" || domain.HostingDocumentHash(current.Blob) != domain.HostingDocumentHash(b.Blob) {
		t.Fatal("local archive changed", err)
	}
	retry, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, domain.HostingStorageBackupTransferRetryInput{RequestID: "notify_upload_retry", ExpectedTransferID: failed.ID})
	if err != nil {
		t.Fatal(err)
	}
	uploaded := settleStorageRemote(t, f, retry.ID, "")
	restored, err := f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("9", 32), Name: "alert exclusion", RemoteSource: &domain.BackupRemoteSource{TransferID: uploaded.ID, ExpectedTargetRevision: target.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	var download string
	if err = f.db.QueryRow(f.ctx, `SELECT id FROM hosting_storage_backup_transfers WHERE restore_operation_id=$1`, restored.Operation.ID).Scan(&download); err != nil {
		t.Fatal(err)
	}
	settleStorageRemote(t, f, download, "object_mismatch")
	d, err = service.ListNotificationDeliveries(f.owner, f.resource.ProjectID)
	if err != nil || len(d) != 1 {
		t.Fatalf("download emitted backup failure %#v %v", d, err)
	}
}
