package store_test

import (
	"errors"
	"fmt"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

func storageRetentionFixture(t *testing.T) archiveControlFixture {
	t.Helper()
	f := newArchiveControlFixture(t)
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeStorageBackupRetention, domain.HostingStorageBackupRetentionCapability); e != nil {
		t.Fatal(e)
	}
	return f
}
func storageRetentionBackup(t *testing.T, f archiveControlFixture, index int, owned bool) domain.HostingStorageBackup {
	return storageRetentionBackupTarget(t, f, index, owned, nil)
}
func storageRetentionBackupTarget(t *testing.T, f archiveControlFixture, index int, owned bool, target *domain.BackupTargetSelection) domain.HostingStorageBackup {
	t.Helper()
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: fmt.Sprintf("%032x", index+100), ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	blob := &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: fmt.Sprintf("sha256:%064x", index+1), SizeBytes: int64(1536 + index), Format: domain.HostingStorageArchiveFormat}
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "resume_intent", "resumed"} {
		var states []domain.HostingStorageArchiveWriterState
		switch phase {
		case "writer_snapshot", "stop_intent", "resume_intent", "resumed":
			states = archiveStates(p, true)
		case "stopped":
			states = archiveStates(p, false)
		}
		if phase == "archive_published" {
			in := domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "publish_" + op.ID, PlanHash: p.Hash, Phase: phase, Blob: blob}
			if owned {
				in.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: uint64(index + 1), Bytes: blob.SizeBytes, Created: true}
			}
			var e error
			op, e = f.service.RunnerStorageArchiveProgress(f.runnerContext, in)
			if e != nil {
				t.Fatal("publish", e)
			}
		} else {
			op = f.progress(t, op, a, phase, states, nil)
		}
	}
	in := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed", Blob: blob}}
	if _, e := f.service.RunnerStorageArchiveComplete(f.runnerContext, in); e != nil {
		t.Fatal("complete", e)
	}
	out, e := f.service.GetStorageBackup(f.owner, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestPostgresStorageRetentionExactPreviewReplayAndAvailability(t *testing.T) {
	f := storageRetentionFixture(t)
	backups := []domain.HostingStorageBackup{}
	for i := range 3 {
		backups = append(backups, storageRetentionBackup(t, f, i, true))
	}
	in := domain.HostingStorageBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}
	p, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, in)
	if e != nil {
		t.Fatal("policy", e)
	}
	again, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, in)
	if e != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("policy replay", e)
	}
	name := "ordinary application configuration change"
	current, e := f.service.UpdateResource(f.owner, f.resource.ID, app.ResourcePatch{ExpectedGeneration: f.resource.Generation, Name: &name})
	if e != nil || current.Generation <= f.resource.Generation {
		t.Fatal("generation advance", e)
	}
	input := domain.HostingStorageBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: p.Revision}
	preview, e := f.service.PreviewStorageBackupRetention(f.owner, f.resource.ID, input)
	if e != nil {
		t.Fatal("preview", e)
	}
	selected := 0
	for _, c := range preview.Items {
		if c.Selected {
			selected++
		}
	}
	if selected != 2 || preview.EvaluatedAt.IsZero() {
		t.Fatalf("preview selection %+v", preview)
	}
	repeated, e := f.service.PreviewStorageBackupRetention(f.owner, f.resource.ID, input)
	if e != nil || !reflect.DeepEqual(preview, repeated) {
		t.Fatal("preview replay changed", e)
	}
	execute := domain.HostingStorageBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: p.Revision, ExpectedPlanHash: preview.PlanHash}
	execution, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, execute)
	if e != nil {
		t.Fatal("execute", e)
	}
	replay, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, execute)
	if e != nil || !reflect.DeepEqual(replay, execution) {
		t.Fatal("execution replay", e)
	}
	if _, e = f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("e", 32), ExpectedResourceGeneration: f.resource.Generation}); e == nil {
		t.Fatal("reserved pool allowed producer")
	}
	if e = f.pg.PrepareStorageBackupRetention(f.ctx, execution.ID); e != nil {
		t.Fatal("prepare", e)
	}
	claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal("claim", e)
	}
	a := domain.HostingAuthority{OperationID: execution.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, e := f.service.RunnerStorageBackupRetentionPlan(f.runnerContext, a)
	if e != nil {
		t.Fatal("plan", e)
	}
	for _, copy := range plan.Copies {
		m := domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent_" + copy.Item.CopyID, PlanHash: plan.Hash, CopyID: copy.Item.CopyID, Phase: "intent"}
		if _, e = f.service.RunnerStorageBackupRetentionMutation(f.runnerContext, m); e != nil {
			t.Fatal("intent", e)
		}
		m.Phase = "deleted"
		m.ReceiptID = "deleted_" + copy.Item.CopyID
		if _, e = f.service.RunnerStorageBackupRetentionMutation(f.runnerContext, m); e != nil {
			t.Fatal("deleted", e)
		}
	}
	result, e := f.service.GetStorageBackupRetentionExecution(f.owner, f.resource.ID, execution.ID)
	if e != nil || result.State != "succeeded" || result.ReservationHeld {
		t.Fatal("settlement", e, result)
	}
	for _, b := range backups {
		got, e := f.service.GetStorageBackup(f.owner, b.ID)
		if e != nil || got.State != "succeeded" || got.Blob == nil {
			t.Fatal("logical history changed", e)
		}
	}
	if _, e = f.service.RequestStorageRestore(f.owner, backups[0].ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("f", 32), Name: "retention deleted"}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("deleted local copy restore admitted", e)
	}
	// An exact old request still returns its original immutable response, never another batch.
	replay, e = f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, execute)
	if e != nil || replay.ID != execution.ID {
		t.Fatal("old request picked fresh batch", e)
	}
}
func TestPostgresStorageRetentionLegacyUnprovenAndFixedAge(t *testing.T) {
	f := storageRetentionFixture(t)
	storageRetentionBackup(t, f, 9, false)
	p, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepDays: 1}})
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.service.PreviewStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: p.Revision})
	if e != nil {
		t.Fatal(e)
	}
	if len(preview.Items) != 1 || preview.Items[0].Selected || preview.Items[0].Availability != "unproven" {
		t.Fatal("legacy deletion rights inferred", preview)
	}
}

func TestPostgresStorageRetentionInterruptedBatchFreshFence(t *testing.T) {
	f := storageRetentionFixture(t)
	for i := range 3 {
		storageRetentionBackup(t, f, i, true)
	}
	policy, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.service.PreviewStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: policy.Revision})
	if e != nil {
		t.Fatal(e)
	}
	input := domain.HostingStorageBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: policy.Revision, ExpectedPlanHash: preview.PlanHash}
	execution, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pg.PrepareStorageBackupRetention(f.ctx, execution.ID); e != nil {
		t.Fatal(e)
	}
	first, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	old := domain.HostingAuthority{OperationID: execution.ID, RunID: first.Run.ID, LeaseID: first.Lease.ID, Attempt: first.Lease.Attempt, Fence: first.Lease.Fence}
	plan, e := f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, old)
	if e != nil {
		t.Fatal(e)
	}
	mutation := domain.HostingStorageBackupRetentionMutation{HostingAuthority: old, ReceiptID: "first_intent", PlanHash: plan.Hash, CopyID: plan.Copies[0].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, mutation); e != nil {
		t.Fatal(e)
	}
	mutation.Phase = "deleted"
	mutation.ReceiptID = "first_deleted"
	committed, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil {
		t.Fatal(e)
	}
	// The controller ACK is lost: the original exact receipt remains immutable.
	if _, e = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, old.LeaseID); e != nil {
		t.Fatal(e)
	}
	staleBeforeResume := domain.HostingStorageBackupRetentionMutation{HostingAuthority: old, ReceiptID: "expired_new_intent", PlanHash: plan.Hash, CopyID: plan.Copies[1].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, staleBeforeResume); !errors.Is(e, store.ErrConflict) {
		t.Fatal("known expired authority must conflict", e)
	}
	for _, unknown := range []domain.HostingAuthority{func() domain.HostingAuthority { v := old; v.Fence = "foreign_fence"; return v }(), func() domain.HostingAuthority { v := old; v.LeaseID = "unknown_lease"; return v }()} {
		bad := staleBeforeResume
		bad.HostingAuthority = unknown
		if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, bad); !errors.Is(e, store.ErrNotFound) {
			t.Fatal("unknown tuple disclosed execution", e)
		}
	}
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, "foreign_runner", staleBeforeResume); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("foreign runner disclosed execution", e)
	}
	beforeResume, e := f.service.GetStorageBackupRetentionExecution(f.owner, f.resource.ID, execution.ID)
	if e != nil || !reflect.DeepEqual(beforeResume, committed) {
		t.Fatal("stale denial changed execution", e)
	}
	oldReceipt, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(oldReceipt, committed) {
		t.Fatal("expired committed receipt did not replay", e)
	}
	input.RequestID = "retry_after_reload"
	resumed, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, input)
	if e != nil || resumed.ID != execution.ID {
		t.Fatal("same-batch resume", e)
	}
	receipt, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(receipt, committed) {
		t.Fatal("committed old receipt changed", e)
	}
	retirement, e := f.pg.StorageBackupRetentionRetirement(f.ctx, f.runner, domain.HostingStorageBackupRetentionRetirementInput{HostingAuthority: old, PlanHash: plan.Hash})
	if e != nil || retirement.NextAttempt != 2 || !retirement.ReplayOnly {
		t.Fatal("protected supersession proof", e)
	}
	second, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil || second.Run.ID != first.Run.ID || second.Lease.Attempt != 2 || second.Lease.Fence == old.Fence {
		t.Fatal("fresh full fence", e)
	}
	fresh := domain.HostingAuthority{OperationID: execution.ID, RunID: second.Run.ID, LeaseID: second.Lease.ID, Attempt: second.Lease.Attempt, Fence: second.Lease.Fence}
	p, e := f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, fresh)
	if e != nil || p.Hash != plan.Hash || p.Progress[0].State != "deleted" || p.Progress[1].State != "pending" {
		t.Fatal("resume reselected or lost progress", e)
	}
	stale := domain.HostingStorageBackupRetentionMutation{HostingAuthority: old, ReceiptID: "stale_new_intent", PlanHash: p.Hash, CopyID: p.Copies[1].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, stale); !errors.Is(e, store.ErrConflict) {
		t.Fatal("old authority must conflict for new mutation", e)
	}
	stale.HostingAuthority = fresh
	stale.ReceiptID = "fresh_intent"
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, stale); e != nil {
		t.Fatal(e)
	}
	stale.Phase = "deleted"
	stale.ReceiptID = "fresh_deleted"
	final, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, stale)
	if e != nil || final.State != "succeeded" || final.ReservationHeld {
		t.Fatal("continued same batch", e)
	}
	input.RequestID = "completed_replay"
	again, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, input)
	if e != nil || again.ID != execution.ID || again.State != "succeeded" {
		t.Fatal("terminal replay enqueued work", e)
	}
	var executions, resumes int
	if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_backup_retention_executions),(SELECT count(*) FROM hosting_storage_backup_retention_resumes)`).Scan(&executions, &resumes); e != nil || executions != 1 || resumes != 1 {
		t.Fatal("resume duplicated selection", executions, resumes, e)
	}
}

func admittedStorageRetentionBatch(t *testing.T) (archiveControlFixture, domain.HostingStorageBackupRetentionExecution) {
	t.Helper()
	f := storageRetentionFixture(t)
	for i := range 3 {
		storageRetentionBackup(t, f, i, true)
	}
	if _, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	p, e := f.service.PreviewStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.service.ExecuteStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionExecutionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: p.PlanHash})
	if e != nil {
		t.Fatal(e)
	}
	return f, v
}
func TestPostgresStorageRetentionCurrentAuthorityAndAuditRollback(t *testing.T) {
	for _, kind := range []string{"owner", "grant", "runner", "capability", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f, v := admittedStorageRetentionBatch(t)
			if e := f.pg.PrepareStorageBackupRetention(f.ctx, v.ID); e != nil {
				t.Fatal(e)
			}
			claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			a := domain.HostingAuthority{OperationID: v.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			p, e := f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, a)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "owner":
				_, e = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, p.ActorID)
			case "grant":
				_, e = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, p.Copies[0].DestinationID, v.ProjectID)
			case "runner":
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner)
			case "capability":
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingStorageBackupRetentionCapability)
			case "audit":
				_, e = f.db.Exec(f.ctx, `CREATE FUNCTION reject_retention_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit rejection'; END $$; CREATE TRIGGER reject_retention_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_retention_audit()`)
			}
			if e != nil {
				t.Fatal(e)
			}
			in := domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "denied", PlanHash: p.Hash, CopyID: p.Copies[0].Item.CopyID, Phase: "intent"}
			if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, in); e == nil {
				t.Fatal("revoked authority or rejected audit admitted intent")
			}
			var receipts, changed int
			if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_backup_retention_receipts WHERE execution_id=$1),(SELECT count(*) FROM hosting_storage_backup_retention_items WHERE execution_id=$1 AND (state<>'pending' OR NOT reservation_held))`, v.ID).Scan(&receipts, &changed); e != nil || receipts != 0 || changed != 0 {
				t.Fatalf("failed mutation changed evidence receipts=%d items=%d err=%v", receipts, changed, e)
			}
			if kind == "audit" {
				if _, e = f.db.Exec(f.ctx, `DROP TRIGGER reject_retention_audit ON audit_events`); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, in); e != nil {
					t.Fatal("retry after rollback", e)
				}
			} else if _, e = f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, a); e == nil {
				t.Fatal("revoked current plan allowed")
			}
		})
	}
}

func TestPostgresStorageRetentionAutomaticPublicationDeduplicates(t *testing.T) {
	f := storageRetentionFixture(t)
	for i := range 2 {
		storageRetentionBackup(t, f, i, true)
	}
	if _, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionInput{RequestID: "automatic_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	var before int
	if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_retention_publications`).Scan(&before); e != nil || before != 0 {
		t.Fatal("enabled cleanup retroactively selected old publications", e, before)
	}
	latest := storageRetentionBackup(t, f, 2, true)
	if e := f.stopExecution(); e != nil {
		t.Fatal(e)
	}
	if e := f.pg.ReconcileStorageBackupRetention(f.ctx); e != nil {
		t.Fatal("automatic publication", e)
	}
	first, e := f.service.ListStorageBackupRetentionExecutions(f.owner, f.resource.ID)
	if e != nil || len(first) != 1 || len(first[0].Items) != 2 {
		t.Fatal("automatic batch", e, first)
	}
	if e = f.pg.ReconcileStorageBackupRetention(f.ctx); e != nil {
		t.Fatal(e)
	}
	again, e := f.service.ListStorageBackupRetentionExecutions(f.owner, f.resource.ID)
	if e != nil || len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatal("automatic publication picked another batch", e)
	}
	got, e := f.service.GetStorageBackup(f.owner, latest.ID)
	if e != nil || got.State != "succeeded" {
		t.Fatal("retention changed successful archive", e)
	}
}
func TestPostgresStorageRetentionDownloadGenerationReuseAndSupersession(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
	b := storageRetentionBackupTarget(t, f, 72, true, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
	transfers, e := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if e != nil || len(transfers) != 1 {
		t.Fatal(e)
	}
	a, p := claimStorageRemote(t, f, transfers[0].ID)
	m := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "owned_upload_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
	if _, e = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); e != nil {
		t.Fatal(e)
	}
	m.Phase = "complete"
	m.ReceiptID = "owned_upload_complete"
	m.RemoteCopy = &domain.BackupRemoteCopyProof{ETag: `"owned-storage-object"`, Created: true}
	uploaded, e := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.stopExecution(); e != nil {
		t.Fatal(e)
	}
	for i, proof := range []domain.BackupLocalCopyProof{{PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 73, Bytes: b.Blob.SizeBytes, Created: false}, {PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 123, Bytes: b.Blob.SizeBytes, Created: true}, {PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 123, Bytes: b.Blob.SizeBytes, Created: false}} {
		restored, e := f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: fmt.Sprintf("%032x", i+700), Name: fmt.Sprintf("retained generation %d", i), RemoteSource: &domain.BackupRemoteSource{TransferID: uploaded.ID, ExpectedTargetRevision: target.Revision}})
		if e != nil {
			t.Fatal("restore admission", e)
		}
		var download string
		if e = f.db.QueryRow(f.ctx, `SELECT id FROM hosting_storage_backup_transfers WHERE restore_operation_id=$1`, restored.Operation.ID).Scan(&download); e != nil {
			t.Fatal(e)
		}
		a, p = claimStorageRemote(t, f, download)
		m = domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "download_intent_" + download, PlanHash: p.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
		if _, e = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); e != nil {
			t.Fatal(e)
		}
		m.Phase = "complete"
		m.ReceiptID = "download_complete_" + download
		m.LocalCopy = &proof
		complete, e := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m)
		if e != nil {
			t.Fatal("download generation", i, e)
		}
		replay, e := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m)
		if e != nil || !reflect.DeepEqual(replay, complete) {
			t.Fatal("download replay", e)
		}
		var copies, current int
		if e = f.db.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE superseded_by_copy_id IS NULL) FROM hosting_storage_backup_copies WHERE backup_id=$1 AND location='local'`, b.ID).Scan(&copies, &current); e != nil {
			t.Fatal(e)
		}
		want := 2
		if i == 0 {
			want = 1
		}
		if copies != want || current != 1 {
			t.Fatalf("download %d generation count %d current %d", i, copies, current)
		}
	}
}
