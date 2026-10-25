package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func remoteBackupFixture(t *testing.T) (networkCaptureFixture, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	f := backupFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	credentials, err := app.NewBackupTargetCredentials([]byte("synthetic-access-canary"), []byte("synthetic-secret-canary"))
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	in := domain.HostingDatabaseBackupTargetInput{RequestID: "create_target", Name: "Owned remote", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true}
	target, err := f.service.SaveDatabaseBackupTarget(f.ctx, f.resource.ProjectID, "", in, credentials)
	if err != nil {
		t.Fatal("target admission", err)
	}
	replay, err := f.service.SaveDatabaseBackupTarget(f.ctx, f.resource.ProjectID, "", in, credentials)
	if err != nil || !reflect.DeepEqual(replay, target) {
		t.Fatal("target replay changed", err)
	}
	changed, _ := app.NewBackupTargetCredentials([]byte("different-access"), []byte("different-secret"))
	defer changed.Close()
	if _, err = f.service.SaveDatabaseBackupTarget(f.ctx, f.resource.ProjectID, "", in, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("same request accepted different credentials", err)
	}
	view, err := f.service.ListDatabaseBackupTargets(f.ctx, f.resource.ProjectID)
	if err != nil || len(view) != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "canary") || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), "secret_id") {
		t.Fatal("safe target metadata leaked secret")
	}
	validation, err := f.service.ValidateDatabaseBackupTarget(f.ctx, f.resource.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "validate_target", ExpectedRevision: 1, DestinationID: f.op.Snapshot.DestinationID})
	if err != nil {
		t.Fatal("validation admission", err)
	}
	settleRemoteTransfer(t, f, validation.ID, "")
	return f, target
}
func claimRemoteTransfer(t *testing.T, f networkCaptureFixture, id string) (domain.HostingAuthority, domain.HostingDatabaseBackupTransferPlan) {
	t.Helper()
	if err := f.pg.PrepareDatabaseBackupTransfer(f.ctx, id); err != nil {
		t.Fatal("transfer prepare", err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseBackupTransfer || claim.Run.RunSpec.HostingOperationID != id {
		t.Fatal("transfer claim", claim.Run.ID, err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, err := f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a)
	if err != nil || p.Validate() != nil {
		t.Fatal("transfer plan", err)
	}
	return a, p
}
func settleRemoteTransfer(t *testing.T, f networkCaptureFixture, id, failure string) domain.HostingDatabaseBackupTransfer {
	t.Helper()
	a, p := claimRemoteTransfer(t, f, id)
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "remote_intent_" + id, PlanHash: p.Hash, Phase: "intent", DaemonID: f.op.Release.DaemonID}
	if _, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal("remote intent", err)
	}
	in.Phase = "complete"
	if failure != "" {
		in.Phase = "failed"
	}
	in.FailureCode = failure
	in.ReceiptID = "remote_result_" + id
	out, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if err != nil {
		t.Fatal("remote result", err)
	}
	replay, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if err != nil || !reflect.DeepEqual(out, replay) {
		t.Fatal("remote receipt replay changed", err)
	}
	return out
}

func TestPostgresRemoteBackupLocalCompletionRetryAndRemoteRestore(t *testing.T) {
	f, target := remoteBackupFixture(t)
	in := domain.HostingDatabaseBackupInput{RequestID: "remote_backup", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}}
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	b = completeBackup(t, f, b.ID)
	transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, b.ID, false)
	if err != nil || len(transfers) != 1 || transfers[0].Mode != "upload" || transfers[0].Blob == nil {
		t.Fatal("atomic upload admission", err)
	}
	failed := settleRemoteTransfer(t, f, transfers[0].ID, "access_denied")
	local, err := f.service.GetDatabaseBackup(f.ctx, b.ID)
	if err != nil || local.State != "succeeded" || local.OwnershipHeld || local.Blob == nil {
		t.Fatal("upload failure changed local success", err)
	}
	retryIn := domain.HostingDatabaseBackupTransferRetryInput{RequestID: "retry_remote", ExpectedTransferID: failed.ID}
	retry, err := f.service.RetryDatabaseBackupTransfer(f.ctx, b.ID, retryIn)
	if err != nil || retry.ID == failed.ID || domain.HostingDocumentHash(retry.Blob) != domain.HostingDocumentHash(b.Blob) {
		t.Fatal("retry identity", err)
	}
	replay, err := f.service.RetryDatabaseBackupTransfer(f.ctx, b.ID, retryIn)
	if err != nil || replay.ID != retry.ID {
		t.Fatal("retry replay", err)
	}
	upload := settleRemoteTransfer(t, f, retry.ID, "")
	// Configuration edits do not redirect an admitted historical object. Key
	// replacement is separately revocation, tested below.
	edit := domain.HostingDatabaseBackupTargetInput{RequestID: "edit_target", ExpectedRevision: 1, Name: target.Name, Endpoint: "https://new-backup.example", Bucket: target.Bucket, Region: target.Region, Enabled: true}
	if _, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, edit, nil); err != nil {
		t.Fatal("target edit", err)
	}
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, b.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "remote_restore", Name: "Remote restored target", RemoteSource: &domain.BackupRemoteSource{TransferID: upload.ID, ExpectedTargetRevision: 1}})
	if err != nil {
		t.Fatal("historic remote restore admission", err)
	}
	if err = f.pg.PrepareDatabaseBackup(f.ctx, restored.ID); !errors.Is(err, hostingjobs.ErrDatabaseBackupWaiting) {
		t.Fatal("restore escaped download barrier", err)
	}
	targetDB, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	prepareBackupRestoreChild(t, f, restored, targetDB)
	transfers, err = f.service.ListDatabaseBackupTransfers(f.ctx, b.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var download domain.HostingDatabaseBackupTransfer
	for _, x := range transfers {
		if x.Mode == "download" {
			download = x
		}
	}
	if download.ID == "" || download.RestoreOperationID != restored.ID {
		t.Fatal("missing exact restore download")
	}
	a, p := claimRemoteTransfer(t, f, download.ID)
	if p.Target.Target.Endpoint != target.Endpoint || p.ObjectKey != domain.BackupRemoteObjectKey(b.ID, *b.Blob) {
		t.Fatal("historic remote source redirected")
	}
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "download_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: f.op.Release.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "download_complete"
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, restored.ID)
	var backups int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups WHERE mode='backup'`).Scan(&backups); err != nil || backups != 1 {
		t.Fatal("retry reran dump admission", err)
	}
}

func TestPostgresRemoteBackupExpiredUploadClassification(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "expired_backup", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	a, p := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "expiry_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: f.op.Release.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err == nil {
		t.Fatal("expired lease obtained effect authority")
	}
	if _, err = f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "expired_secret", PlanHash: p.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: in.DaemonID}); err == nil {
		t.Fatal("expired lease obtained credentials")
	}
	in.Phase = "complete"
	in.ReceiptID = "expired_complete"
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err == nil {
		t.Fatal("expired complete accepted")
	}
	in.Phase = "uncertain"
	in.ReceiptID = "expired_uncertain"
	in.FailureCode = "interrupted"
	bad := in
	bad.Fence += "_wrong"
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, bad); err == nil {
		t.Fatal("foreign tuple classified expired upload")
	}
	out, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if err != nil || out.State != "uncertain" {
		t.Fatal("expired metadata classification", err)
	}
	replay, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if err != nil || !reflect.DeepEqual(out, replay) {
		t.Fatal("expired lost ACK changed", err)
	}
	var classified bool
	if err = f.db.QueryRow(f.ctx, `SELECT expired_classification FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1 AND phase='uncertain'`, a.OperationID).Scan(&classified); err != nil || !classified {
		t.Fatal("classification reason not persisted", err)
	}
	retry, err := f.service.RetryDatabaseBackupTransfer(f.ctx, b.ID, domain.HostingDatabaseBackupTransferRetryInput{RequestID: "fresh_after_expiry", ExpectedTransferID: out.ID})
	if err != nil {
		t.Fatal(err)
	}
	newAuthority, _ := claimRemoteTransfer(t, f, retry.ID)
	if newAuthority.RunID == a.RunID || newAuthority.Fence == a.Fence {
		t.Fatal("retry reused expired authority")
	}
}

func TestPostgresRemoteBackupConcurrentCompleteEnqueuesOnce(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "concurrent_remote", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	a, p := claimBackup(t, f, b.ID)
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "concurrent_intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	in.Phase = "complete"
	in.ReceiptID = "concurrent_complete"
	in.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: "postgresql_custom"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	outs := make(chan domain.HostingDatabaseBackupOperation, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
			outs <- out
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	close(outs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent completion", e)
		}
	}
	var first *domain.HostingDatabaseBackupOperation
	for out := range outs {
		if first != nil && !reflect.DeepEqual(*first, out) {
			t.Fatal("historical completion acknowledgment changed")
		}
		copy := out
		first = &copy
	}
	var transfers, jobs int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_transfers WHERE backup_id=$1 AND mode='upload'),(SELECT count(*) FROM river_job WHERE kind=$2 AND args->>'operation_id'=$3)`, b.ID, hostingjobs.DatabaseBackupTransferKind, "db_upload_"+b.ID).Scan(&transfers, &jobs); err != nil || transfers != 1 || jobs != 1 {
		t.Fatal("completion replay duplicated transfer or queue", err)
	}
}

func TestPostgresRemoteBackupCurrentAuthorityDeniedWithoutEffects(t *testing.T) {
	for _, kind := range []string{"grant", "owner", "runner", "key", "target"} {
		t.Run(kind, func(t *testing.T) {
			f, target := remoteBackupFixture(t)
			b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "revoked_remote", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
			if err != nil {
				t.Fatal(err)
			}
			completeBackup(t, f, b.ID)
			a, p := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
			switch kind {
			case "grant":
				_, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, p.Operation.DestinationID, p.Operation.ProjectID)
			case "owner":
				_, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, p.ActorID)
			case "runner":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner)
			case "key":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=$1`, p.Target.Secret.SecretID)
			case "target":
				_, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "disable", ExpectedRevision: 1, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err == nil {
				t.Fatal("revoked authority obtained plan")
			}
			in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "denied_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
			if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err == nil {
				t.Fatal("revoked authority admitted intent")
			}
			if _, err = f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "denied_credentials", PlanHash: p.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: p.DaemonID}); err == nil {
				t.Fatal("revoked authority disclosed credentials")
			}
			var receipts, deliveries int
			if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1),(SELECT count(*) FROM hosting_database_backup_remote_deliveries WHERE operation_id=$1)`, a.OperationID).Scan(&receipts, &deliveries); err != nil || receipts != 0 || deliveries != 0 {
				t.Fatal("denial created effect or delivery evidence", err)
			}
		})
	}
}

func TestPostgresRemoteBackupSchedulePinsSelection(t *testing.T) {
	f, target := remoteBackupFixture(t)
	in := domain.HostingDatabaseBackupScheduleInput{RequestID: "remote_schedule", ExpectedResourceGeneration: f.resource.Generation, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}}
	schedule, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", in)
	if err != nil || schedule.RemoteTarget == nil {
		t.Fatal("remote schedule save", err)
	}
	listed, err := f.service.ListDatabaseBackupSchedules(f.ctx, f.resource.ID)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].RemoteTarget, in.RemoteTarget) {
		t.Fatal("schedule selection projection", err)
	}
	if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
		t.Fatal(err)
	}
	var operation string
	if err = f.db.QueryRow(f.ctx, `SELECT operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1 AND operation_id IS NOT NULL`, schedule.ID).Scan(&operation); err != nil {
		t.Fatal("scheduled admission", err)
	}
	view, err := f.service.GetDatabaseBackup(f.ctx, operation)
	if err != nil || !reflect.DeepEqual(view.RemoteTarget, in.RemoteTarget) {
		t.Fatal("occurrence followed wrong target revision", err)
	}
	completeBackup(t, f, operation)
	x, err := f.service.ListDatabaseBackupTransfers(f.ctx, operation, false)
	if err != nil || len(x) != 1 || x[0].TargetRevision != 1 {
		t.Fatal("scheduled local completion missing upload", err)
	}
}

func TestPostgresRemoteBackupCredentialDeliveryAndRevocation(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "credential_backup", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	a, p := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
	in := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "actual_delivery", PlanHash: p.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: p.DaemonID}
	ctx := auth.WithPrincipal(f.ctx, auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	credentials, err := f.service.RunnerBackupRemoteCredentials(ctx, in, "image-hosting-fixture-token")
	if err != nil {
		t.Fatal("purpose-specific encrypted delivery", err)
	}
	if credentials.Metadata().Request != in || credentials.Metadata().Purpose != domain.HostingDatabaseBackupRemoteCredentialPurpose {
		t.Fatal("delivery lost target/lease binding")
	}
	if err = credentials.Use(func(access, secret []byte) error {
		if string(access) != "synthetic-access-canary" || string(secret) != "synthetic-secret-canary" {
			t.Fatal("wrong encrypted credential reference")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(credentials)
	if string(raw) != "{}" {
		t.Fatal("ordinary serialization exposed credentials")
	}
	credentials.Close()
	var deliveries int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_remote_deliveries WHERE operation_id=$1`, a.OperationID).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatal("delivery not audited before release", err)
	}
	if _, err = f.service.RunnerBackupRemoteCredentials(ctx, in, "wrong-synthetic-token"); err == nil {
		t.Fatal("wrong bearer disclosed credentials")
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=$1`, p.Target.Secret.SecretID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.service.RunnerBackupRemoteCredentials(ctx, in, "image-hosting-fixture-token"); err == nil || got != nil {
		t.Fatal("revoked key replay disclosed bytes")
	}
}

func TestPostgresRemoteBackupExpiredClassificationRequiresSavedIntent(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "no_intent_backup", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	a, p := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "missing_intent_uncertain", PlanHash: p.Hash, Phase: "uncertain", FailureCode: "interrupted", DaemonID: p.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err == nil {
		t.Fatal("missing intent manufactured expired classification")
	}
	var count int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1`, a.OperationID).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied classification persisted receipt", err)
	}
}

func TestPostgresRemoteBackupTerminalSettlementRace(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "race_backup", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	a, p := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "race_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, phase := range []string{"complete", "uncertain"} {
		go func() {
			<-start
			request := in
			request.Phase = phase
			request.ReceiptID = "race_" + phase
			if phase == "uncertain" {
				request.FailureCode = "interrupted"
			}
			_, err := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, request)
			results <- err
		}()
	}
	close(start)
	accepted, denied := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
		} else {
			denied++
		}
	}
	if accepted != 1 || denied != 1 {
		t.Fatal("terminal race accepted conflicting receipts")
	}
	var state string
	var terminal int
	if err = f.db.QueryRow(f.ctx, `SELECT state,(SELECT count(*) FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1 AND phase IN ('complete','uncertain')) FROM hosting_database_backup_transfers WHERE id=$1`, a.OperationID).Scan(&state, &terminal); err != nil || terminal != 1 || state != "succeeded" && state != "uncertain" {
		t.Fatal("terminal outcome downgraded", err)
	}
}

func TestPostgresRemoteBackupUploadDoesNotOwnLiveSource(t *testing.T) {
	f, target := remoteBackupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "independent_upload", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	a, _ := claimRemoteTransfer(t, f, "db_upload_"+b.ID)
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("source_maintenance_during_upload")); err != nil {
		t.Fatal("immutable upload held source maintenance", err)
	}
	if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err != nil {
		t.Fatal("source maintenance invalidated immutable archive transfer", err)
	}
}
