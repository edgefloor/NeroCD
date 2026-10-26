package store_test

import (
	"errors"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func storageRemoteControlFixture(t *testing.T) (archiveControlFixture, domain.HostingDatabaseBackupTarget) {
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
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("2", 32), ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestPostgresStorageRemoteLocalCompletionAtomicEnqueueAndReplay(t *testing.T) {
	t.Run("resume_failure_recovery_enqueues_once", testStorageRemoteRecoveredUpload)
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
func testStorageRemoteRecoveredUpload(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
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
	for range 2 {
		out, e := f.service.RunnerStorageArchiveComplete(f.runnerContext, finish)
		if e != nil || out.State != "succeeded" || out.MutationHold {
			t.Fatal("recovery completion", e)
		}
	}
	count(1)
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
