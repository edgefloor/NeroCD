package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func storageRemoteControlFixture(t *testing.T, compose ...bool) (archiveControlFixture, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	f := newArchiveControlFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	credentials, err := app.NewBackupTargetCredentials([]byte("storage-fixture-access"), []byte("storage-fixture-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	target, err := f.service.SaveDatabaseBackupTarget(f.owner, f.resource.ProjectID, "", domain.HostingDatabaseBackupTargetInput{RequestID: "storage_target", Name: "Storage target", Endpoint: "https://backup.example", Bucket: "storage-backups", Region: "us-east-1", Enabled: true}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := f.service.ValidateStorageBackupTarget(f.owner, f.resource.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "storage_validation", ExpectedRevision: 1, DestinationID: f.storage.DestinationID})
	if err != nil {
		t.Fatal(err)
	}
	settleStorageRemote(t, f, validation.ID, "")
	if len(compose) > 0 && compose[0] {
		caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingComposeStorageArchiveCapability, domain.HostingComposeStorageRemoteCapability}
		if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, caps); err != nil {
			t.Fatal(err)
		}
		f.resource, f.source = deployBackupComponentStack(t, f.pg, f.resource, domain.Server{ID: "server_owned_storage", RunnerID: f.runner}, domain.Destination{ID: "destination_owned_storage", ServerID: "server_owned_storage"}, "a", f.helper.DaemonID)
	}
	return f, target
}
func claimStorageRemote(t *testing.T, f archiveControlFixture, id string) (domain.HostingAuthority, domain.HostingStorageBackupTransferPlan) {
	t.Helper()
	if err := f.pg.PrepareStorageBackupTransfer(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	claim, err := f.service.ClaimRun(f.runnerContext)
	if err != nil || claim.Run.RunSpec.HostingOperationID != id || claim.Run.RunSpec.Type != domain.RunTypeStorageBackupTransfer {
		t.Fatal("storage transfer claim", claim.Run.ID, err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, err := f.pg.StorageBackupTransferPlan(f.ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	if p.Operation.Mode == "validate" && (p.Limits.Validate() != nil || p.Limits.ArchiveBytes != p.MaxBytes) {
		t.Fatal("validation plan omitted configured positive limits")
	}
	return a, p
}
func settleStorageRemote(t *testing.T, f archiveControlFixture, id, failure string) domain.HostingStorageBackupTransfer {
	t.Helper()
	a, p := claimStorageRemote(t, f, id)
	in := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "intent_" + id, PlanHash: p.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
	if _, err := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	in.Phase = "complete"
	if failure != "" {
		in.Phase = "failed"
	}
	in.FailureCode = failure
	in.ReceiptID = in.Phase + "_" + id
	out, err := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func requestRemoteStorageBackup(t *testing.T, f archiveControlFixture, target domain.HostingDatabaseBackupTarget) domain.HostingStorageBackup {
	t.Helper()
	in := domain.HostingStorageBackupInput{RequestID: strings.Repeat("2", 32), ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}
	if f.resource.Source.Kind == "compose" {
		in.Volume = "db"
		in.ExpectedDeployedOperationID = f.source.ID
		in.AcknowledgeInterruption = true
	}
	discovery, e := f.service.StorageBackupVolumes(f.owner, f.resource.ID)
	if f.resource.Source.Kind == "compose" && (e != nil || !discovery.Available) {
		t.Fatal("pre-admission discovery", discovery, e)
	}
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay(t *testing.T) {
	t.Run("resume_failure_recovery_enqueues_once", func(t *testing.T) { testStorageRemoteRecoveredUpload(t, false) })
	f, target := storageRemoteControlFixture(t)
	b := requestRemoteStorageBackup(t, f, target)
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "resume_intent", "resumed"} {
		var states []domain.HostingStorageArchiveWriterState
		var blob *domain.HostingStorageArchiveBlob
		switch phase {
		case "writer_snapshot", "stop_intent", "resume_intent", "resumed":
			states = archiveStates(p, true)
		case "stopped":
			states = archiveStates(p, false)
		case "archive_published":
			blob = &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("9", 64), SizeBytes: 1536, Format: domain.HostingStorageArchiveFormat}
		}
		op = f.progress(t, op, a, phase, states, blob)
	}
	blob := op.StorageArchive.Progress[5].Blob
	for _, progress := range op.StorageArchive.Progress {
		if progress.Phase == "archive_published" {
			blob = progress.Blob
		}
	}
	in := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "atomic_complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed", Blob: blob}}
	if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION storage_remote_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage_backup_remote.requested' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER storage_remote_fail_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION storage_remote_fail_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, in); err == nil {
		t.Fatal("completion ignored upload audit failure")
	}
	var uploads int
	var state string
	var held bool
	if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_backup_transfers WHERE backup_id=$1),(SELECT state FROM hosting_operations WHERE id=$2),(SELECT hold_operation_id IS NOT NULL FROM hosting_binding_state WHERE binding_id=$3)`, b.ID, op.ID, f.storage.BindingID).Scan(&uploads, &state, &held); err != nil || uploads != 0 || state != "applying" || !held {
		t.Fatal("completion rollback lost atomicity", uploads, state, held, err)
	}
	if _, err := f.db.Exec(f.ctx, `DROP TRIGGER storage_remote_fail_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		out, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, in)
		if err != nil || out.State != "succeeded" || out.MutationHold {
			t.Fatal("local completion", err)
		}
	}
	if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_transfers WHERE backup_id=$1`, b.ID).Scan(&uploads); err != nil || uploads != 1 {
		t.Fatal("duplicate upload", uploads, err)
	}
}
func TestPostgresStorageRemoteRetryPreservesArchiveAndRevocation(t *testing.T) {
	t.Run("committed_receipt_after_revocation", testStorageRemoteCommittedReceiptAfterRevocation)
	f, target := storageRemoteControlFixture(t)
	b := f.finishBackup(t, requestRemoteStorageBackup(t, f, target))
	rows, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	failed := settleStorageRemote(t, f, rows[0].ID, "access_denied")
	// Drain both River and dispatch recovery before admitting the retry so the
	// target is revoked while the transfer is still awaiting preparation.
	if err := f.stopExecution(); err != nil {
		t.Fatal("stop remote dispatch", err)
	}
	input := domain.HostingStorageBackupTransferRetryInput{RequestID: "retry_upload", ExpectedTransferID: failed.ID}
	retry, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, input)
	if err != nil || again.ID != retry.ID {
		t.Fatal("retry replay duplicated", err)
	}
	if _, err = f.service.RetryStorageBackupTransfer(f.owner, b.ID, domain.HostingStorageBackupTransferRetryInput{RequestID: "stale_retry", ExpectedTransferID: failed.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale predecessor accepted", err)
	}
	queued, err := f.service.GetStorageBackupTransfer(f.owner, retry.ID)
	if err != nil || queued.State != "queued" {
		t.Fatalf("retry was prepared before revocation: state=%q err=%v", queued.State, err)
	}
	var noRun bool
	if err := f.db.QueryRow(f.ctx, `SELECT run_id IS NULL FROM hosting_storage_backup_transfers WHERE id=$1`, retry.ID).Scan(&noRun); err != nil || !noRun {
		t.Fatalf("queued retry already has a run: no_run=%t err=%v", noRun, err)
	}
	current, err := f.service.GetStorageBackup(f.owner, b.ID)
	if err != nil || current.State != "succeeded" || domain.HostingDocumentHash(current.Blob) != domain.HostingDocumentHash(b.Blob) {
		t.Fatal("upload failure changed local archive", err)
	}
	if _, err = f.service.SaveDatabaseBackupTarget(f.owner, f.resource.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "disable_target", ExpectedRevision: 1, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareStorageBackupTransfer(f.ctx, retry.ID); err != nil {
		t.Fatal(err)
	}
	denied, err := f.service.GetStorageBackupTransfer(f.owner, retry.ID)
	if err != nil || denied.State != "failed" || denied.FailureCode != "authority_denied" {
		t.Fatalf("disabled target dispatched: state=%q failure=%q err=%v", denied.State, denied.FailureCode, err)
	}
	if err := f.db.QueryRow(f.ctx, `SELECT run_id IS NULL FROM hosting_storage_backup_transfers WHERE id=$1`, retry.ID).Scan(&noRun); err != nil || !noRun {
		t.Fatalf("denied retry acquired a run: no_run=%t err=%v", noRun, err)
	}
}
func TestPostgresStorageRemoteDownloadFailureAndHeldRetry(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
	b := f.finishBackup(t, requestRemoteStorageBackup(t, f, target))
	rows, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	uploaded := settleStorageRemote(t, f, rows[0].ID, "")
	restored, err := f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("3", 32), Name: "Remote restored uploads", RemoteSource: &domain.BackupRemoteSource{TransferID: uploaded.ID, ExpectedTargetRevision: target.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	var download string
	if err = f.db.QueryRow(f.ctx, `SELECT id FROM hosting_storage_backup_transfers WHERE restore_operation_id=$1`, restored.Operation.ID).Scan(&download); err != nil {
		t.Fatal(err)
	}
	failed := settleStorageRemote(t, f, download, "object_mismatch")
	op, err := f.service.GetStorageRestore(f.owner, restored.Operation.ID)
	if err != nil || op.Operation.State != "failed" || !op.Operation.RestoreOwnershipHeld || op.Operation.RunID != "" {
		t.Fatal("failed dependency not visibly held", err)
	}
	retry, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, domain.HostingStorageBackupTransferRetryInput{RequestID: "retry_download", ExpectedTransferID: failed.ID})
	if err != nil || retry.Mode != "download" || retry.RestoreOperationID != restored.Operation.ID {
		t.Fatal("retry changed held restore", err)
	}
	var priorState string
	if err = f.db.QueryRow(f.ctx, `SELECT state FROM hosting_storage_backup_transfers WHERE id=$1`, failed.ID).Scan(&priorState); err != nil || priorState != "failed" {
		t.Fatal("retry erased failure history", err)
	}
	if err = f.pg.PrepareStorageArchive(f.ctx, restored.Operation.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("extraction dispatched before verified download", err)
	}
}

// A published archive does not authorize upload until durable writer resumption.
func testStorageRemoteRecoveredUpload(t *testing.T, compose bool) {
	f, target := storageRemoteControlFixture(t, compose)
	b := requestRemoteStorageBackup(t, f, target)
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	var published *domain.HostingStorageArchiveBlob
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published"} {
		var states []domain.HostingStorageArchiveWriterState
		var blob *domain.HostingStorageArchiveBlob
		switch phase {
		case "writer_snapshot", "stop_intent":
			states = archiveStates(p, true)
		case "stopped":
			states = archiveStates(p, false)
		case "archive_published":
			blob = &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("8", 64), SizeBytes: 1536, Format: domain.HostingStorageArchiveFormat}
			published = blob
		}
		op = f.progress(t, op, a, phase, states, blob)
	}
	fail := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "resume_failed_complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "uncertain", Uncertain: true, FailureCode: "resume_failed"}}
	for range 2 {
		out, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, fail)
		if err != nil || out.State != "reconciliation_required" || !out.MutationHold {
			t.Fatal("resume failure lost hold", err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_transfers WHERE backup_id=$1 AND mode='upload'`, b.ID).Scan(&n); err != nil || n != want {
			t.Fatal("upload dispatch ordering", n, want, err)
		}
	}
	count(0)
	if compose {
		if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(array_remove(capabilities,$2),$3) WHERE id=$1`, f.runner, domain.HostingStorageBackupRemoteCapability, domain.RunTypeStorageBackupTransfer); err != nil {
			t.Fatal(err)
		}
	}
	input := domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("4", 32), ExpectedPlanHash: p.Hash}
	recovery, err := f.service.RequestStorageArchiveRecovery(f.owner, op.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	recovery, ra := f.claim(t, recovery.ID, "recover")
	recovery = f.begin(t, recovery, ra)
	count(0)
	finish := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: ra, ReceiptID: "resume_recovered_complete", PlanHash: recovery.StorageArchive.Plan.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "not_attempted", ResumeOutcome: "resumed"}}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, finish); err == nil {
		t.Fatal("recovery completed without durable resumed evidence")
	}
	count(0)
	evidence := recovery.StorageArchive.Plan.RecoveryEvidence
	recovery = f.progress(t, recovery, ra, "resume_intent", evidence.Writers, nil)
	recovery = f.progress(t, recovery, ra, "resumed", evidence.Writers, nil)
	completeRecovery := func(in domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
		return f.service.RunnerStorageArchiveComplete(f.runnerContext, in)
	}
	var offline *store.PostgresStore
	if compose {
		if err = f.stopExecution(); err != nil {
			t.Fatal(err)
		}
		offline, err = store.OpenPostgres(f.ctx, f.db.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if e := offline.Close(); e != nil {
				t.Error(e)
			}
		})
		if err = offline.ConfigureStorageArchive(&f.helper, p.Limits); err != nil {
			t.Fatal(err)
		}
		if recovery.StorageArchive.Plan.RemoteTarget != nil || recovery.StorageArchive.Plan.CompilerVersion != domain.HostingComposeStorageRemoteVersion || recovery.StorageArchive.Plan.RecoveryEvidence.OriginalPlanHash != p.Hash {
			t.Fatal("remote recovery lineage changed")
		}
		completeRecovery = func(in domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
			return offline.StorageArchiveComplete(f.ctx, f.runner, in)
		}
	}
	for range 2 {
		out, e := completeRecovery(finish)
		if e != nil || out.State != "succeeded" || out.MutationHold {
			t.Fatal("recovery completion", e)
		}
	}
	count(1)
	if compose {
		rows, e := offline.ListStorageBackupTransfers(f.ctx, "usr_bootstrap", b.ID, false)
		if e != nil || len(rows) != 1 || rows[0].State != "queued" || rows[0].TargetID != target.ID || rows[0].TargetRevision != target.Revision || domain.HostingDocumentHash(rows[0].Blob) != domain.HostingDocumentHash(published) {
			t.Fatal("deferred upload identity", rows, e)
		}
		transfer := rows[0].ID
		var jobs int
		if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1`, transfer).Scan(&jobs); e != nil || jobs != 0 {
			t.Fatal("unavailable dispatcher got job", jobs, e)
		}
		if _, e = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3] WHERE id=$1`, f.runner, domain.HostingStorageBackupRemoteCapability, domain.RunTypeStorageBackupTransfer); e != nil {
			t.Fatal(e)
		}
		stop, e := offline.StartHostingExecution(f.ctx)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := stop(cleanup); e != nil {
				t.Error(e)
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for {
			if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1`, transfer).Scan(&jobs); e != nil {
				t.Fatal(e)
			}
			if jobs == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("existing recovery did not enqueue original transfer", jobs)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if e = offline.PrepareStorageBackupTransfer(f.ctx, transfer); e != nil {
			t.Fatal(e)
		}
		if e = offline.PrepareStorageBackupTransfer(f.ctx, transfer); e != nil {
			t.Fatal(e)
		}
		if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1`, transfer).Scan(&jobs); e != nil || jobs != 1 {
			t.Fatal("duplicate recovered job", jobs, e)
		}
	}
	if _, err = f.service.RequestStorageArchiveRecovery(f.owner, op.ID, input); err != nil {
		t.Fatal("recovery request replay", err)
	}
	count(1)
	saved, err := f.service.GetStorageBackup(f.owner, b.ID)
	if err != nil || saved.State != "succeeded" || domain.HostingDocumentHash(saved.Blob) != domain.HostingDocumentHash(published) {
		t.Fatal("recovery changed archive identity", err)
	}
}

func testStorageRemoteCommittedReceiptAfterRevocation(t *testing.T) {
	for _, kind := range []string{"target", "key", "actor", "grant"} {
		t.Run(kind, func(t *testing.T) {
			f, target := storageRemoteControlFixture(t)
			b := f.finishBackup(t, requestRemoteStorageBackup(t, f, target))
			rows, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			a, p := claimStorageRemote(t, f, rows[0].ID)
			intent := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "revoke_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
			if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, intent); err != nil {
				t.Fatal(err)
			}
			complete := intent
			complete.Phase = "complete"
			complete.ReceiptID = "revoke_complete"
			committed, err := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, complete)
			if err != nil {
				t.Fatal(err)
			}
			// The result was committed but its response was lost before the journal ACK.
			pending, err := f.service.ValidateStorageBackupTarget(f.owner, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "pending_before_revoke", ExpectedRevision: target.Revision, DestinationID: f.storage.DestinationID})
			if err != nil {
				t.Fatal(err)
			}
			pendingAuthority, pendingPlan := claimStorageRemote(t, f, pending.ID)
			switch kind {
			case "target":
				_, err = f.service.SaveDatabaseBackupTarget(f.owner, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "receipt_disable", ExpectedRevision: target.Revision, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil)
			case "key":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=$1`, p.Target.Secret.SecretID)
			case "actor":
				_, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, p.ActorID)
			case "grant":
				_, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, p.Operation.DestinationID, p.Operation.ProjectID)
			}
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, complete)
			if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(committed) {
				t.Fatal("revocation blocked exact receipt ACK", err)
			}
			for _, changed := range []domain.HostingStorageBackupTransferMutation{func() domain.HostingStorageBackupTransferMutation {
				v := complete
				v.ReceiptID = "changed_receipt"
				return v
			}(), func() domain.HostingStorageBackupTransferMutation { v := complete; v.Fence = "changed_fence"; return v }()} {
				if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, changed); err == nil {
					t.Fatal("changed body or tuple replayed")
				}
			}
			fresh := domain.HostingStorageBackupTransferMutation{HostingAuthority: pendingAuthority, ReceiptID: "fresh_after_revoke", PlanHash: pendingPlan.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
			if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, fresh); err == nil {
				t.Fatal("revoked authority admitted new effect")
			}
			var receipts int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_transfer_receipts WHERE operation_id=$1`, pending.ID).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatal("revoked effect wrote receipt", err)
			}
		})
	}
}

func TestPostgresComposeStorageRemoteRecoveryWithoutUploadConfiguration(t *testing.T) {
	testStorageRemoteRecoveredUpload(t, true)
}
func TestPostgresComposeStorageRemoteRetryPreservesBlob(t *testing.T) {
	f, target := storageRemoteControlFixture(t, true)
	b := f.finishBackup(t, requestRemoteStorageBackup(t, f, target))
	rows, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	failed := settleStorageRemote(t, f, rows[0].ID, "access_denied")
	if err = f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingStorageBackupTransferRetryInput{RequestID: "compose_retry", ExpectedTransferID: failed.ID}
	retry, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, in)
	if err != nil || again.ID != retry.ID {
		t.Fatal("retry replay", err)
	}
	if retry.Blob == nil || domain.HostingDocumentHash(retry.Blob) != domain.HostingDocumentHash(b.Blob) {
		t.Fatal("retry changed blob")
	}
	var archives int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_archive_plans WHERE backup_id=$1`, b.ID).Scan(&archives); err != nil || archives != 1 {
		t.Fatal("retry archived again", archives, err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RetryStorageBackupTransfer(f.owner, b.ID, in); err == nil {
		t.Fatal("revoked retry replay allowed")
	}
}

func TestPostgresComposeStorageRemoteSelectionRollback(t *testing.T) {
	f, target := storageRemoteControlFixture(t, true)
	if err := f.stopExecution(); err != nil {
		t.Fatal(err)
	}
	counts := func() [5]int {
		t.Helper()
		var out [5]int
		if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_archive_plans WHERE source_resource_id=$1),(SELECT count(*) FROM hosting_storage_backups WHERE resource_id=$1),(SELECT count(*) FROM hosting_binding_state WHERE binding_id=$2 AND hold_operation_id IS NOT NULL),(SELECT count(*) FROM river_job),(SELECT count(*) FROM audit_events WHERE action='hosting.storage_archive.requested' AND metadata->>'resource_id'=$1)`, f.resource.ID, f.source.BindingID).Scan(&out[0], &out[1], &out[2], &out[3], &out[4]); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := counts()
	input := domain.HostingStorageBackupInput{RequestID: strings.Repeat("6", 32), ExpectedResourceGeneration: f.resource.Generation, Volume: "db", ExpectedDeployedOperationID: f.source.ID, AcknowledgeInterruption: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision + 1}}
	if _, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, input); !errors.Is(err, store.ErrConflict) {
		t.Fatal("invalid target revision", err)
	}
	if after := counts(); after != before {
		t.Fatal("invalid target left partial archive", before, after)
	}
	input.RemoteTarget.Revision = target.Revision
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingComposeStorageRemoteCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, input); err == nil {
		t.Fatal("old reader admitted remote plan")
	}
	if after := counts(); after != before {
		t.Fatal("capability failure left partial archive", before, after)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2] WHERE id=$1`, f.runner, domain.HostingComposeStorageRemoteCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `CREATE FUNCTION reject_compose_archive_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture queue failure';END $$;CREATE TRIGGER reject_compose_archive_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION reject_compose_archive_job()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, input); err == nil {
		t.Fatal("queue failure accepted")
	}
	if after := counts(); after != before {
		t.Fatal("queue failure left partial archive", before, after)
	}
	if _, err := f.db.Exec(f.ctx, `DROP TRIGGER reject_compose_archive_job ON river_job`); err != nil {
		t.Fatal(err)
	}
	saved, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, input)
	if err != nil || replay.ID != saved.ID {
		t.Fatal("held remote selection replay", err)
	}
	changed := input
	selection := *input.RemoteTarget
	selection.Revision++
	changed.RemoteTarget = &selection
	if _, err = f.service.RequestStorageBackup(f.owner, f.resource.ID, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed target replay accepted", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RequestStorageBackup(f.owner, f.resource.ID, input); err == nil {
		t.Fatal("revoked archive request replay allowed")
	}

}

func TestPostgresComposeStorageRemoteMigrationRejectsMixedFamilies(t *testing.T) {
	f, target := storageRemoteControlFixture(t, true)
	b := requestRemoteStorageBackup(t, f, target)
	var raw []byte
	if err := f.db.QueryRow(f.ctx, `SELECT body FROM hosting_storage_archive_plans WHERE operation_id=$1`, b.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan domain.HostingStorageArchivePlan
	if json.Unmarshal(raw, &plan) != nil {
		t.Fatal("stored remote plan")
	}
	for _, kind := range []string{"local_with_target", "remote_without_target", "policy_mismatch", "restore", "image_family", "fake_image_storage", "downgraded_recovery"} {
		t.Run(kind, func(t *testing.T) {
			p := plan
			p.OperationID = "hop_bad_" + kind
			var storage *string
			switch kind {
			case "local_with_target":
				p.CompilerVersion = domain.HostingComposeStorageArchiveVersion
				p.PolicyVersion = p.CompilerVersion
			case "remote_without_target":
				p.RemoteTarget = nil
			case "policy_mismatch":
				p.PolicyVersion = domain.HostingComposeStorageArchiveVersion
			case "restore":
				p.Mode = "restore"
			case "image_family":
				p.CompilerVersion = domain.HostingStorageArchiveVersion
				p.PolicyVersion = p.CompilerVersion
			case "fake_image_storage":
				storage = &f.storage.ID
			case "downgraded_recovery":
				p.Mode = "recover"
				p.RemoteTarget = nil
				p.RecoveryOfOperationID = plan.OperationID
				p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: plan.Hash}
				p.CompilerVersion = domain.HostingComposeStorageArchiveVersion
				p.PolicyVersion = p.CompilerVersion
			}
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
			body, _ := json.Marshal(p)
			tx, err := f.db.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(f.ctx) }()
			if _, err = tx.Exec(f.ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES($1,$2,$3,$4,$1,$5,'queued','usr_bootstrap')`, p.OperationID, p.ProjectID, p.SourceResourceID(), p.SourceBindingID(), p.Hash); err != nil {
				t.Fatal(err)
			}
			var original *string
			if p.RecoveryOfOperationID != "" {
				original = &p.RecoveryOfOperationID
			}
			_, err = tx.Exec(f.ctx, `INSERT INTO hosting_storage_archive_plans(operation_id,mode,backup_id,source_resource_id,source_binding_id,source_storage_id,recovery_of_operation_id,body,body_hash,source_family,source_volume) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'compose',$10)`, p.OperationID, p.Mode, p.BackupID, p.SourceResourceID(), p.SourceBindingID(), storage, original, body, p.Hash, p.ComposeSource.Volume.Name)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
				t.Fatal("malformed family not rejected by DB check", err)
			}
		})
	}
}
