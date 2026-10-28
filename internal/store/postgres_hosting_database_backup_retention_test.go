package store_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func retentionFixture(t *testing.T) networkCaptureFixture {
	t.Helper()
	f := backupFixture(t)
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability); e != nil {
		t.Fatal(e)
	}
	return f
}
func ownedRetentionBackup(t *testing.T, f networkCaptureFixture, index int) domain.HostingDatabaseBackupOperation {
	return ownedRetentionBackupTarget(t, f, index, nil)
}
func ownedRetentionBackupTarget(t *testing.T, f networkCaptureFixture, index int, target *domain.BackupTargetSelection) domain.HostingDatabaseBackupOperation {
	t.Helper()
	v, e := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: fmt.Sprintf("retention_backup_%d", index), ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: target})
	if e != nil {
		t.Fatal(e)
	}
	a, p := claimBackup(t, f, v.ID)
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "retention_intent_" + v.ID, PlanHash: p.Hash, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); e != nil {
		t.Fatal(e)
	}
	in.Phase = "complete"
	in.ReceiptID = "retention_complete_" + v.ID
	in.Blob = &domain.HostingDatabaseBackupBlob{Digest: fmt.Sprintf("sha256:%064x", index+1), Bytes: 65537, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
	in.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: uint64(index + 1), Bytes: 65537, Created: true}
	out, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
	if e != nil {
		t.Fatal("complete owned copy", e)
	}
	replay, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
	if e != nil || !reflect.DeepEqual(out, replay) {
		t.Fatal("backup receipt replay changed", e)
	}
	return out
}
func TestPostgresBackupRetentionPolicyPreviewExecutionReplay(t *testing.T) {
	testBackupRetentionPolicyPreviewExecutionReplay(t, retentionFixture(t), domain.HostingDatabaseBackupRetentionVersion)
}

func testBackupRetentionPolicyPreviewExecutionReplay(t *testing.T, f networkCaptureFixture, version string) {
	t.Helper()
	backups := []domain.HostingDatabaseBackupOperation{}
	for i := range 3 {
		backups = append(backups, ownedRetentionBackup(t, f, i))
	}
	in := domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}
	policy, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, in)
	if e != nil || policy.Version != version {
		t.Fatal("policy", e)
	}
	again, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, in)
	if e != nil || !reflect.DeepEqual(again, policy) {
		t.Fatal("policy replay", e)
	}
	if _, e = f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "stale", ExpectedResourceGeneration: f.resource.Generation, Enabled: true}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale revision", e)
	}
	preview, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal("preview", e)
	}
	selected := 0
	for _, c := range preview.Items {
		if c.Selected {
			selected++
			if c.BackupID == backups[2].ID {
				t.Fatal("newest selected")
			}
		}
	}
	if selected != 2 {
		t.Fatalf("selected=%d items=%+v", selected, preview.Items)
	}
	request := domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: preview.PlanHash}
	execution, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, request)
	if e != nil {
		t.Fatal("admit", e)
	}
	replay, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, request)
	if e != nil || !reflect.DeepEqual(execution, replay) {
		t.Fatal("execution replay", e)
	}
	// New archive admission cannot race the held immutable batch.
	if _, e = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "reserved_producer", ExpectedResourceGeneration: f.resource.Generation}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("pool producer admitted during reservation", e)
	}
	if e = f.pg.PrepareDatabaseBackupRetention(f.ctx, execution.ID); e != nil {
		t.Fatal("dispatch", e)
	}
	runnerContext := auth.WithPrincipal(f.ctx, auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	claim, e := f.service.ClaimRun(runnerContext)
	if e != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseBackupRetention {
		t.Fatal("retention claim", claim.Run.ID, e)
	}
	if !reflect.DeepEqual(claim.PrimitivePlan, domain.RunnerPrimitivePlan{RunID: claim.Run.ID}) {
		t.Fatal("retention claim acquired an unscoped primitive")
	}
	a := domain.HostingAuthority{OperationID: execution.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a)
	if e != nil || plan.Validate() != nil {
		t.Fatal("plan", e)
	}
	for _, copy := range plan.Copies {
		mutation := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent_" + copy.Item.CopyID, PlanHash: plan.Hash, CopyID: copy.Item.CopyID, Phase: "intent"}
		bad := mutation
		bad.Fence = "foreign"
		if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, bad); e == nil {
			t.Fatal("foreign fence accepted")
		}
		if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation); e != nil {
			t.Fatal("intent", e)
		}
		mutation.Phase = "deleted"
		mutation.ReceiptID = "deleted_" + copy.Item.CopyID
		result, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation)
		if e != nil {
			t.Fatal("delete receipt", e)
		}
		same, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation)
		if e != nil || !reflect.DeepEqual(result, same) {
			t.Fatal("lost ACK changed", e)
		}
	}
	final, e := f.service.GetDatabaseBackupRetentionExecution(f.ctx, f.resource.ID, execution.ID)
	if e != nil || final.State != "succeeded" || final.ReservationHeld || final.LocalState != "succeeded" {
		t.Fatal("settlement", final, e)
	}
	// Original backup history remains successful and unchanged.
	for _, b := range backups {
		history, e := f.service.GetDatabaseBackup(f.ctx, b.ID)
		if e != nil || !reflect.DeepEqual(history, b) {
			t.Fatal("retention changed immutable history", e)
		}
	}
	next, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "next", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range next.Items {
		if c.Selected {
			t.Fatal("same copies selected again")
		}
	}
}

func TestPostgresBackupRetentionInterruptedBatchFreshFence(t *testing.T) {
	testBackupRetentionInterruptedBatchFreshFence(t, retentionFixture(t))
}
func testBackupRetentionInterruptedBatchFreshFence(t *testing.T, f networkCaptureFixture) {
	t.Helper()
	for i := range 3 {
		ownedRetentionBackup(t, f, i)
	}
	policy, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: policy.Revision})
	if e != nil {
		t.Fatal(e)
	}
	input := domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: policy.Revision, ExpectedPlanHash: preview.PlanHash}
	execution, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pg.PrepareDatabaseBackupRetention(f.ctx, execution.ID); e != nil {
		t.Fatal(e)
	}
	first, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	old := domain.HostingAuthority{OperationID: execution.ID, RunID: first.Run.ID, LeaseID: first.Lease.ID, Attempt: first.Lease.Attempt, Fence: first.Lease.Fence}
	plan, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, old)
	if e != nil {
		t.Fatal(e)
	}
	mutation := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: old, ReceiptID: "first_intent", PlanHash: plan.Hash, CopyID: plan.Copies[0].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation); e != nil {
		t.Fatal(e)
	}
	mutation.Phase = "deleted"
	mutation.ReceiptID = "first_deleted"
	committed, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil {
		t.Fatal(e)
	}
	// The controller ACK is lost: the original exact receipt remains immutable.
	if _, e = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, old.LeaseID); e != nil {
		t.Fatal(e)
	}
	staleBeforeResume := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: old, ReceiptID: "expired_new_intent", PlanHash: plan.Hash, CopyID: plan.Copies[1].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, staleBeforeResume); !errors.Is(e, store.ErrConflict) {
		t.Fatal("known expired authority must conflict", e)
	}
	for _, unknown := range []domain.HostingAuthority{func() domain.HostingAuthority { v := old; v.Fence = "foreign_fence"; return v }(), func() domain.HostingAuthority { v := old; v.LeaseID = "unknown_lease"; return v }()} {
		bad := staleBeforeResume
		bad.HostingAuthority = unknown
		if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, bad); !errors.Is(e, store.ErrNotFound) {
			t.Fatal("unknown tuple disclosed execution", e)
		}
	}
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, "foreign_runner", staleBeforeResume); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("foreign runner disclosed execution", e)
	}
	beforeResume, e := f.service.GetDatabaseBackupRetentionExecution(f.ctx, f.resource.ID, execution.ID)
	if e != nil || !reflect.DeepEqual(beforeResume, committed) {
		t.Fatal("stale denial changed execution", e)
	}
	oldReceipt, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(oldReceipt, committed) {
		t.Fatal("expired committed receipt did not replay", e)
	}
	input.RequestID = "retry_after_reload"
	resumed, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, input)
	if e != nil || resumed.ID != execution.ID {
		t.Fatal("same-batch resume", e)
	}
	receipt, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(receipt, committed) {
		t.Fatal("committed old receipt changed", e)
	}
	retirement, e := f.pg.DatabaseBackupRetentionRetirement(f.ctx, f.runner, domain.HostingDatabaseBackupRetentionRetirementInput{HostingAuthority: old, PlanHash: plan.Hash})
	if e != nil || retirement.NextAttempt != 2 || !retirement.ReplayOnly {
		t.Fatal("protected supersession proof", e)
	}
	second, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil || second.Run.ID != first.Run.ID || second.Lease.Attempt != 2 || second.Lease.Fence == old.Fence {
		t.Fatal("fresh full fence", e)
	}
	fresh := domain.HostingAuthority{OperationID: execution.ID, RunID: second.Run.ID, LeaseID: second.Lease.ID, Attempt: second.Lease.Attempt, Fence: second.Lease.Fence}
	p, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, fresh)
	if e != nil || p.Hash != plan.Hash || p.Progress[0].State != "deleted" || p.Progress[1].State != "pending" {
		t.Fatal("resume reselected or lost progress", e)
	}
	stale := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: old, ReceiptID: "stale_new_intent", PlanHash: p.Hash, CopyID: p.Copies[1].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, stale); !errors.Is(e, store.ErrConflict) {
		t.Fatal("old authority must conflict for new mutation", e)
	}
	stale.HostingAuthority = fresh
	stale.ReceiptID = "fresh_intent"
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, stale); e != nil {
		t.Fatal(e)
	}
	stale.Phase = "deleted"
	stale.ReceiptID = "fresh_deleted"
	final, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, stale)
	if e != nil || final.State != "succeeded" || final.ReservationHeld {
		t.Fatal("continued same batch", e)
	}
	input.RequestID = "completed_replay"
	again, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, input)
	if e != nil || again.ID != execution.ID || again.State != "succeeded" {
		t.Fatal("terminal replay enqueued work", e)
	}
	var executions, resumes int
	if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_retention_executions),(SELECT count(*) FROM hosting_database_backup_retention_resumes)`).Scan(&executions, &resumes); e != nil || executions != 1 || resumes != 1 {
		t.Fatal("resume duplicated selection", executions, resumes, e)
	}
}

func completeOwnedRetentionUpload(t *testing.T, f networkCaptureFixture, backup string) {
	t.Helper()
	transfers, e := f.service.ListDatabaseBackupTransfers(f.ctx, backup, false)
	if e != nil || len(transfers) != 1 {
		t.Fatal(e)
	}
	a, p := claimRemoteTransfer(t, f, transfers[0].ID)
	in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "ret_remote_intent_" + a.OperationID, PlanHash: p.Hash, Phase: "intent", DaemonID: f.op.Release.DaemonID}
	if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in); e != nil {
		t.Fatal(e)
	}
	in.Phase = "complete"
	in.ReceiptID = "ret_remote_complete_" + a.OperationID
	in.RemoteCopy = &domain.BackupRemoteCopyProof{Created: true, ETag: `"owned-` + backup + `"`}
	result, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, in)
	if e != nil || !reflect.DeepEqual(result, replay) {
		t.Fatal("remote receipt replay changed", e)
	}
}
func TestPostgresBackupRetentionAutomaticPublicationOrdering(t *testing.T) {
	testBackupRetentionAutomaticPublicationOrdering(t, false)
}
func testBackupRetentionAutomaticPublicationOrdering(t *testing.T, mysql bool) {
	t.Helper()
	var f networkCaptureFixture
	var target domain.HostingDatabaseBackupTarget
	capability := domain.HostingDatabaseBackupRetentionCapability
	if mysql {
		f, target = mysqlRemoteFixture(t)
		capability = domain.HostingDatabaseMySQLBackupRetentionCapability
	} else {
		f, target = remoteBackupFixture(t)
	}
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupRetention, capability); e != nil {
		t.Fatal(e)
	}
	selection := &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}
	for i := range 3 {
		b := ownedRetentionBackupTarget(t, f, i, selection)
		completeOwnedRetentionUpload(t, f, b.ID)
	}
	if _, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "auto_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}, Remote: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	newest := ownedRetentionBackupTarget(t, f, 3, selection)
	var events, uploads int
	if e := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_retention_publications),(SELECT count(*) FROM hosting_database_backup_transfers WHERE backup_id=$1 AND mode='upload')`, newest.ID).Scan(&events, &uploads); e != nil || events != 1 || uploads != 1 {
		t.Fatal("publication/upload not atomic", events, uploads, e)
	}
	preview, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "during_upload", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	protected := false
	for _, i := range preview.Items {
		if i.BackupID == newest.ID && i.Location == "local" {
			for _, reason := range i.ProtectedReasons {
				protected = protected || reason == "upload_pending"
			}
			if i.Selected {
				t.Fatal("pending upload local copy selected")
			}
		}
	}
	if !protected {
		t.Fatal("missing pending upload protection")
	}
	if e = f.pg.ReconcileDatabaseBackupRetention(f.ctx); e != nil {
		t.Fatal(e)
	}
	var executions int
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_retention_executions`).Scan(&executions); e != nil || executions != 0 {
		t.Fatal("cleanup raced queued upload", e)
	}
	completeOwnedRetentionUpload(t, f, newest.ID)
	for range 2 {
		if e = f.pg.ReconcileDatabaseBackupRetention(f.ctx); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_retention_executions`).Scan(&executions); e != nil || executions != 1 {
		t.Fatal("automatic publication duplicate/missing", executions, e)
	}
	local, e := f.service.GetDatabaseBackup(f.ctx, newest.ID)
	if e != nil || !reflect.DeepEqual(local, newest) {
		t.Fatal("retention discovery changed local success", e)
	}
	transfers, e := f.service.ListDatabaseBackupTransfers(f.ctx, newest.ID, false)
	if e != nil || len(transfers) != 1 || transfers[0].State != "succeeded" {
		t.Fatal("retention discovery changed upload", e)
	}
	list, e := f.service.ListDatabaseBackupRetentionExecutions(f.ctx, f.resource.ID)
	if e != nil || len(list) != 1 || len(list[0].Items) != 6 {
		t.Fatal("independent global local/remote selection", len(list), e)
	}
}

func admittedRetentionBatch(t *testing.T) (networkCaptureFixture, domain.HostingDatabaseBackupRetentionExecution) {
	t.Helper()
	f := retentionFixture(t)
	for i := range 3 {
		ownedRetentionBackup(t, f, i)
	}
	if _, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	p, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: p.PlanHash})
	if e != nil {
		t.Fatal(e)
	}
	return f, v
}
func TestPostgresBackupRetentionCurrentAuthorityAndAuditRollback(t *testing.T) {
	for _, kind := range []string{"owner", "grant", "runner", "capability", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f, v := admittedRetentionBatch(t)
			if e := f.pg.PrepareDatabaseBackupRetention(f.ctx, v.ID); e != nil {
				t.Fatal(e)
			}
			claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			a := domain.HostingAuthority{OperationID: v.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			p, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a)
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
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseBackupRetentionCapability)
			case "audit":
				_, e = f.db.Exec(f.ctx, `CREATE FUNCTION reject_retention_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit rejection'; END $$; CREATE TRIGGER reject_retention_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_retention_audit()`)
			}
			if e != nil {
				t.Fatal(e)
			}
			in := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "denied", PlanHash: p.Hash, CopyID: p.Copies[0].Item.CopyID, Phase: "intent"}
			if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in); e == nil {
				t.Fatal("revoked authority or rejected audit admitted intent")
			}
			var receipts, changed int
			if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_retention_receipts WHERE execution_id=$1),(SELECT count(*) FROM hosting_database_backup_retention_items WHERE execution_id=$1 AND (state<>'pending' OR NOT reservation_held))`, v.ID).Scan(&receipts, &changed); e != nil || receipts != 0 || changed != 0 {
				t.Fatalf("failed mutation changed evidence receipts=%d items=%d err=%v", receipts, changed, e)
			}
			if kind == "audit" {
				if _, e = f.db.Exec(f.ctx, `DROP TRIGGER reject_retention_audit ON audit_events`); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in); e != nil {
					t.Fatal("retry after rollback", e)
				}
			} else if _, e = f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a); e == nil {
				t.Fatal("revoked current plan allowed")
			}
		})
	}
}

func remoteRetentionBatch(t *testing.T) (networkCaptureFixture, domain.HostingDatabaseBackupRetentionExecution, domain.HostingAuthority, domain.HostingDatabaseBackupRetentionPlan) {
	t.Helper()
	return remoteRetentionBatchFor(t, false)
}

func remoteRetentionBatchFor(t *testing.T, mysql bool) (networkCaptureFixture, domain.HostingDatabaseBackupRetentionExecution, domain.HostingAuthority, domain.HostingDatabaseBackupRetentionPlan) {
	t.Helper()
	var f networkCaptureFixture
	var target domain.HostingDatabaseBackupTarget
	capability := domain.HostingDatabaseBackupRetentionCapability
	if mysql {
		f, target = mysqlRemoteFixture(t)
		capability = domain.HostingDatabaseMySQLBackupRetentionCapability
	} else {
		f, target = remoteBackupFixture(t)
	}
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupRetention, capability); e != nil {
		t.Fatal(e)
	}
	for i := range 3 {
		b := ownedRetentionBackupTarget(t, f, i, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
		completeOwnedRetentionUpload(t, f, b.ID)
	}
	if _, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Remote: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	preview, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: preview.PlanHash})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pg.PrepareDatabaseBackupRetention(f.ctx, v.ID); e != nil {
		t.Fatal(e)
	}
	claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := domain.HostingAuthority{OperationID: v.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a)
	if e != nil || len(p.Copies) != 2 {
		t.Fatal("remote batch", e)
	}
	return f, v, a, p
}
func TestPostgresBackupRetentionScopedCredentialDelivery(t *testing.T) {
	f, _, a, p := remoteRetentionBatch(t)
	c := p.Copies[0]
	in := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "retention_credentials", PlanHash: p.Hash, TargetID: c.Target.Target.ID, TargetRevision: c.Target.Target.Revision, DaemonID: c.DaemonID, RetentionCopyID: c.Item.CopyID}
	grant, e := f.pg.AuthorizeBackupRetentionCredentials(f.ctx, f.runner, f.credential, in)
	if e != nil || grant.Metadata.Purpose != domain.HostingDatabaseBackupRetentionCredentialPurpose || grant.Metadata.Validate() != nil {
		t.Fatal("dedicated delivery", e)
	}
	again, e := f.pg.AuthorizeBackupRetentionCredentials(f.ctx, f.runner, f.credential, in)
	if e != nil || !reflect.DeepEqual(grant.Metadata, again.Metadata) {
		t.Fatal("delivery replay changed", e)
	}
	for _, bad := range []domain.BackupRemoteCredentialRequest{func() domain.BackupRemoteCredentialRequest { v := in; v.RetentionCopyID = "foreign"; return v }(), func() domain.BackupRemoteCredentialRequest { v := in; v.Authority.Fence = "foreign"; return v }(), func() domain.BackupRemoteCredentialRequest { v := in; v.DaemonID = "foreign"; return v }()} {
		if _, e = f.pg.AuthorizeBackupRetentionCredentials(f.ctx, f.runner, f.credential, bad); e == nil {
			t.Fatal("foreign delivery accepted")
		}
	}
	if _, e = f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, in); e == nil {
		t.Fatal("retention authority accepted by ordinary transfer delivery")
	}
	if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id=$1`, c.Target.Secret.SecretID); e != nil {
		t.Fatal(e)
	}
	if e = f.pg.RevalidateBackupRetentionCredentials(f.ctx, f.runner, f.credential, in, grant.Metadata); e == nil {
		t.Fatal("revoked key revalidated")
	}
	if _, e = f.pg.AuthorizeBackupRetentionCredentials(f.ctx, f.runner, f.credential, in); e == nil {
		t.Fatal("revoked delivery replayed")
	}
}
func TestPostgresBackupRetentionUnknownNonversionedDeleteRemainsReserved(t *testing.T) {
	testBackupRetentionUnknownNonversionedDeleteRemainsReserved(t, false)
}

func testBackupRetentionUnknownNonversionedDeleteRemainsReserved(t *testing.T, mysql bool) {
	t.Helper()
	f, v, a, p := remoteRetentionBatchFor(t, mysql)
	for i, c := range p.Copies {
		in := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: fmt.Sprintf("intent_%d", i), PlanHash: p.Hash, CopyID: c.Item.CopyID, Phase: "intent"}
		if _, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in); e != nil {
			t.Fatal(e)
		}
		in.ReceiptID = fmt.Sprintf("result_%d", i)
		in.Phase = "deleted"
		if i == 0 {
			in.Phase = "uncertain"
			in.FailureCode = "interrupted"
		}
		if _, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in); e != nil {
			t.Fatal(e)
		}
	}
	current, e := f.service.GetDatabaseBackupRetentionExecution(f.ctx, f.resource.ID, v.ID)
	if e != nil || current.State != "uncertain" || !current.ReservationHeld || current.RemoteState != "uncertain" {
		t.Fatal("unknown outcome released hold", e)
	}
	if _, e = f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "retry_after_reload", PreviewID: v.PreviewID, ExpectedPolicyRevision: v.PolicyRevision, ExpectedPlanHash: v.PlanHash}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("unknown nonversioned deletion retried", e)
	}
	if _, e = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "reuse_reserved_pool", ExpectedResourceGeneration: f.resource.Generation}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("reserved pool reused", e)
	}
	var holds, resumes int
	if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_retention_items WHERE execution_id=$1 AND reservation_held),(SELECT count(*) FROM hosting_database_backup_retention_resumes WHERE execution_id=$1)`, v.ID).Scan(&holds, &resumes); e != nil || holds != 1 || resumes != 0 {
		t.Fatal("unknown hold/resume", holds, resumes, e)
	}
}

func TestPostgresBackupRetentionAdoptionUsesOnlyExactKnownProof(t *testing.T) {
	f := retentionFixture(t)
	first := ownedRetentionBackup(t, f, 0)
	for _, changed := range []bool{false, true} {
		request := "adopt_exact"
		if changed {
			request = "adopt_foreign_inode"
		}
		b, e := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: request, ExpectedResourceGeneration: f.resource.Generation})
		if e != nil {
			t.Fatal(e)
		}
		a, p := claimBackup(t, f, b.ID)
		in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent_" + b.ID, PlanHash: p.Hash, Phase: "intent"}
		if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); e != nil {
			t.Fatal(e)
		}
		in.Phase = "complete"
		in.ReceiptID = "complete_" + b.ID
		in.Blob = first.Blob
		in.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 1, Bytes: first.Blob.Bytes, Created: false}
		if changed {
			in.LocalCopy.Inode = 999
		}
		out, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
		if e != nil {
			t.Fatal("adoption completion", e)
		}
		again, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
		if e != nil || !reflect.DeepEqual(out, again) {
			t.Fatal("adoption replay", e)
		}
		var owned bool
		var availability string
		if e = f.db.QueryRow(f.ctx, `SELECT body->'local'->>'created'='true',availability FROM hosting_database_backup_copies WHERE backup_id=$1`, b.ID).Scan(&owned, &availability); e != nil || owned == changed {
			t.Fatal("adoption proof authority", owned, availability, e)
		}
		if changed && availability != "unproven" {
			t.Fatal("foreign inode inherited proof")
		}
	}
}

func TestPostgresBackupRetentionSerializesProducerAndRestore(t *testing.T) {
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			f := retentionFixture(t)
			var first domain.HostingDatabaseBackupOperation
			for i := range 3 {
				b := ownedRetentionBackup(t, f, i)
				if i == 0 {
					first = b
				}
			}
			if _, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
				t.Fatal(e)
			}
			p, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
			if e != nil {
				t.Fatal(e)
			}
			start := make(chan struct{})
			result := make(chan error, 2)
			go func() {
				<-start
				_, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: p.PlanHash})
				result <- e
			}()
			go func() {
				<-start
				var e error
				if mode == "backup" {
					_, e = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "concurrent_producer", ExpectedResourceGeneration: f.resource.Generation})
				} else {
					_, e = f.service.RequestDatabaseBackupRestore(f.ctx, first.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "concurrent_restore", Name: "Restore racing retention"})
				}
				result <- e
			}()
			close(start)
			success := 0
			for range 2 {
				e := <-result
				if e == nil {
					success++
				} else if !errors.Is(e, store.ErrConflict) {
					t.Fatal("unexpected admission error", e)
				}
			}
			if success != 1 {
				t.Fatalf("concurrent admissions succeeded=%d", success)
			}
			var heldOperations, retentions int
			if e = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backups WHERE ownership_held),(SELECT count(*) FROM hosting_database_backup_retention_executions WHERE reservation_held)`).Scan(&heldOperations, &retentions); e != nil || heldOperations+retentions != 1 {
				t.Fatal("overlapping admitted effects", heldOperations, retentions, e)
			}
		})
	}
}

func TestPostgresBackupRetentionReceiptProofBoundary(t *testing.T) {
	f := retentionFixture(t)
	b := ownedRetentionBackup(t, f, 0)
	for _, patch := range []string{`{"local_copy":{"created":true,"inode":999}}`, `{"local_copy":{"extra":"forged"}}`, `{"extra":"forged"}`} {
		_, e := f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response) SELECT operation_id,phase,'forged_proof',authority,jsonb_set(body,'{receipt_id}','"forged_proof"'::jsonb)||$2::jsonb,body_hash,response FROM hosting_database_backup_receipts WHERE operation_id=$1 AND phase='complete'`, b.ID, patch)
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "23514" {
			t.Fatal("forged proof did not fail receipt trigger", e)
		}
	}
	var count int
	if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1`, b.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("forged receipt persisted", count, e)
	}
}

func TestPostgresBackupRetentionRemoteDownloadRepublishesDistinctLocalCopy(t *testing.T) {
	f, target := remoteBackupFixture(t)
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability); e != nil {
		t.Fatal(e)
	}
	var original domain.HostingDatabaseBackupOperation
	for i := range 2 {
		b := ownedRetentionBackupTarget(t, f, i, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
		completeOwnedRetentionUpload(t, f, b.ID)
		if i == 0 {
			original = b
		}
	}
	if _, e := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); e != nil {
		t.Fatal(e)
	}
	preview, e := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
	if e != nil {
		t.Fatal(e)
	}
	execution, e := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: preview.PlanHash})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pg.PrepareDatabaseBackupRetention(f.ctx, execution.ID); e != nil {
		t.Fatal(e)
	}
	claim, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := domain.HostingAuthority{OperationID: execution.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, e := f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a)
	if e != nil || len(plan.Copies) != 1 || plan.Copies[0].Item.BackupID != original.ID {
		t.Fatal("original selection", e)
	}
	in := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "delete_intent", PlanHash: plan.Hash, CopyID: plan.Copies[0].Item.CopyID, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in); e != nil {
		t.Fatal(e)
	}
	in.Phase = "deleted"
	in.ReceiptID = "delete_result"
	deleted, e := f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "local_before_republication", Name: "Absent local"}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("deleted local copy admitted", e)
	}
	transfers, e := f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil || len(transfers) != 1 {
		t.Fatal(e)
	}
	restored, e := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "remote_restore", Name: "Retained remote recovery", RemoteSource: &domain.BackupRemoteSource{TransferID: transfers[0].ID, ExpectedTargetRevision: target.Revision}})
	if e != nil {
		t.Fatal(e)
	}
	targetDB, e := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if e != nil {
		t.Fatal(e)
	}
	prepareBackupRestoreChild(t, f, restored, targetDB)
	transfers, e = f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	download := ""
	for _, v := range transfers {
		if v.Mode == "download" {
			download = v.ID
		}
	}
	if download == "" {
		t.Fatal("missing download")
	}
	da, dp := claimRemoteTransfer(t, f, download)
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: da, ReceiptID: "download_intent", PlanHash: dp.Hash, Phase: "intent", DaemonID: dp.DaemonID}
	if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); e != nil {
		t.Fatal(e)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "download_complete"
	mutation.LocalCopy = &domain.BackupLocalCopyProof{PoolID: plan.Copies[0].Local.PoolID, Device: 1, Inode: 9999, Bytes: original.Blob.Bytes, Created: true}
	result, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil {
		t.Fatal("republication", e)
	}
	again, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(result, again) {
		t.Fatal("download replay", e)
	}

	ra, rp := claimBackup(t, f, restored.ID)
	rm := domain.HostingDatabaseBackupMutation{HostingAuthority: ra, ReceiptID: "restore_intent", PlanHash: rp.Hash, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, rm); e != nil {
		t.Fatal(e)
	}
	rm.Phase = "complete"
	rm.ReceiptID = "restore_complete"
	rm.Blob = original.Blob
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, rm); e != nil {
		t.Fatal("restore original digest", e)
	}
	later, e := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "local_after_republication", Name: "Recreated local recovery"})
	if e != nil || later.ID == restored.ID {
		t.Fatal("recreated local copy unavailable", e)
	}
	var oldState, newState string
	var owned bool
	var copies int
	if e = f.db.QueryRow(f.ctx, `SELECT (SELECT availability FROM hosting_database_backup_copies WHERE id=$1),(SELECT availability FROM hosting_database_backup_copies WHERE id=$2),(SELECT body->'local'->>'created'='true' FROM hosting_database_backup_copies WHERE id=$2),(SELECT count(*) FROM hosting_database_backup_copies WHERE backup_id=$3 AND location='local')`, plan.Copies[0].Item.CopyID, "dbc_download_"+download, original.ID).Scan(&oldState, &newState, &owned, &copies); e != nil || oldState != "deleted" || newState != "available" || !owned || copies != 2 {
		t.Fatal("republication rewrote/duplicated history", oldState, newState, owned, copies, e)
	}
	current, e := f.service.GetDatabaseBackupRetentionExecution(f.ctx, f.resource.ID, execution.ID)
	if e != nil || !reflect.DeepEqual(current, deleted) {
		t.Fatal("download changed deletion receipt", e)
	}
}

func TestPostgresBackupRetentionRemoteDownloadReplacesAvailableGeneration(t *testing.T) {
	f, target := remoteBackupFixture(t)
	original := ownedRetentionBackupTarget(t, f, 0, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
	completeOwnedRetentionUpload(t, f, original.ID)
	var savedCopy []byte
	if e := f.db.QueryRow(f.ctx, `SELECT body FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID).Scan(&savedCopy); e != nil {
		t.Fatal(e)
	}
	transfers, e := f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil || len(transfers) != 1 {
		t.Fatal(e)
	}
	restored, e := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "remote_restore_with_available_inventory", Name: "Remote-only recovery", RemoteSource: &domain.BackupRemoteSource{TransferID: transfers[0].ID, ExpectedTargetRevision: target.Revision}})
	if e != nil {
		t.Fatal(e)
	}
	targetDB, e := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if e != nil {
		t.Fatal(e)
	}
	prepareBackupRestoreChild(t, f, restored, targetDB)
	transfers, e = f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	download := ""
	for _, v := range transfers {
		if v.Mode == "download" {
			download = v.ID
		}
	}
	if download == "" {
		t.Fatal("missing download")
	}
	a, p := claimRemoteTransfer(t, f, download)
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "available_download_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
	if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); e != nil {
		t.Fatal(e)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "available_download_complete"
	// The live fixture withholds the CAS file without changing controller metadata.
	// A verified, no-clobber download therefore creates a new inode at the same pool/key.
	mutation.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 9999, Bytes: original.Blob.Bytes, Created: true}
	for _, state := range []string{"reserved", "uncertain"} {
		if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_copies SET availability=$2 WHERE id=$1`, "dbc_local_"+original.ID, state); e != nil {
			t.Fatal(e)
		}
		if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); !errors.Is(e, store.ErrConflict) {
			t.Fatal("protected generation replaced", state, e)
		}
	}
	if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_copies SET availability='available' WHERE id=$1`, "dbc_local_"+original.ID); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"device", "bytes", "same_inode", "adopted"} {
		bad := mutation
		proof := *mutation.LocalCopy
		bad.LocalCopy = &proof
		switch name {
		case "device":
			proof.Device++
		case "bytes":
			proof.Bytes++
		case "same_inode":
			proof.Inode = 1
		case "adopted":
			proof.Created = false
		}
		if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, bad); !errors.Is(e, store.ErrConflict) {
			t.Fatal("mismatched or unowned generation admitted", name, e)
		}
	}
	// Direct SQL cannot manufacture supersession from a new inventory row
	// without the exact successful download completion receipt.
	tx, e := f.db.Begin(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback(f.ctx) })
	if _, e = tx.Exec(f.ctx, `SET CONSTRAINTS hosting_database_backup_copies_superseded_by_copy_id_fkey, hosting_database_backup_copy_supersession_receipt DEFERRED`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `UPDATE hosting_database_backup_copies SET superseded_by_copy_id=$2 WHERE id=$1`, "dbc_local_"+original.ID, "dbc_download_"+download); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(f.ctx, `INSERT INTO hosting_database_backup_copies(id,backup_id,resource_id,runner_id,location,identity_hash,location_hash,body,availability,created_at) SELECT $2,backup_id,resource_id,runner_id,location,identity_hash,location_hash,jsonb_set(body,'{local,inode}','9999'::jsonb),availability,created_at FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID, "dbc_download_"+download); e != nil {
		t.Fatal(e)
	}
	var constraint *pgconn.PgError
	if e = tx.Commit(f.ctx); !errors.As(e, &constraint) || constraint.Code != "23514" {
		t.Fatal("supersession without receipt accepted", e)
	}
	result, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil {
		t.Fatal("fresh remote download publication against available old generation", e)
	}
	replay, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(result, replay) {
		t.Fatal("download completion replay changed", e)
	}
	var after []byte
	if e = f.db.QueryRow(f.ctx, `SELECT body FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID).Scan(&after); e != nil || !reflect.DeepEqual(savedCopy, after) {
		t.Fatal("download rewrote old ownership evidence", e)
	}
	var oldState, successor string
	var copies int
	if e = f.db.QueryRow(f.ctx, `SELECT availability,superseded_by_copy_id,(SELECT count(*) FROM hosting_database_backup_copies WHERE backup_id=$2 AND location='local') FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID, original.ID).Scan(&oldState, &successor, &copies); e != nil || oldState != "available" || successor != "dbc_download_"+download || copies != 2 {
		t.Fatal("supersession lost historical state or duplicated copies", oldState, successor, copies, e)
	}
	if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_copies SET superseded_by_copy_id=NULL WHERE id=$1`, "dbc_local_"+original.ID); e == nil {
		t.Fatal("supersession link was mutable")
	}
	ra, rp := claimBackup(t, f, restored.ID)
	done := domain.HostingDatabaseBackupMutation{HostingAuthority: ra, ReceiptID: "available_restore_intent", PlanHash: rp.Hash, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, done); e != nil {
		t.Fatal(e)
	}
	done.Phase = "complete"
	done.ReceiptID = "available_restore_complete"
	done.Blob = original.Blob
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, done); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "local_after_available_generation_replaced", Name: "Republished local recovery"}); e != nil {
		t.Fatal("republished local restore unavailable", e)
	}
	// A later deletion of the current generation must not revive the historical
	// row's availability. This models inventory state, not a physical DELETE.
	if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_copies SET availability='deleted' WHERE id=$1`, "dbc_download_"+download); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "no_historical_generation_resurrection", Name: "Unavailable local recovery"}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("superseded generation restored current availability", e)
	}
}

func TestPostgresBackupRetentionRemoteDownloadAdoptsExactCurrentGeneration(t *testing.T) {
	f, target := remoteBackupFixture(t)
	original := ownedRetentionBackupTarget(t, f, 0, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
	completeOwnedRetentionUpload(t, f, original.ID)
	var savedCopy []byte
	if e := f.db.QueryRow(f.ctx, `SELECT body FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID).Scan(&savedCopy); e != nil {
		t.Fatal(e)
	}
	transfers, e := f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil || len(transfers) != 1 {
		t.Fatal(e)
	}
	restored, e := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "remote_restore_existing_file", Name: "Existing local recovery", RemoteSource: &domain.BackupRemoteSource{TransferID: transfers[0].ID, ExpectedTargetRevision: target.Revision}})
	if e != nil {
		t.Fatal(e)
	}
	targetDB, e := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if e != nil {
		t.Fatal(e)
	}
	prepareBackupRestoreChild(t, f, restored, targetDB)
	transfers, e = f.service.ListDatabaseBackupTransfers(f.ctx, original.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	var download string
	for _, v := range transfers {
		if v.Mode == "download" {
			download = v.ID
		}
	}
	if download == "" {
		t.Fatal("missing download")
	}
	a, p := claimRemoteTransfer(t, f, download)
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "existing_download_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
	if _, e = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); e != nil {
		t.Fatal(e)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "existing_download_complete"
	mutation.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 1, Bytes: original.Blob.Bytes, Created: false}
	result, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil {
		t.Fatal("verified exact current inode adoption", e)
	}
	replay, e := f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation)
	if e != nil || !reflect.DeepEqual(result, replay) || mutation.LocalCopy.Created {
		t.Fatal("adoption replay or incoming proof changed", e)
	}
	var after []byte
	var copies int
	var successor *string
	if e = f.db.QueryRow(f.ctx, `SELECT body,superseded_by_copy_id,(SELECT count(*) FROM hosting_database_backup_copies WHERE backup_id=$2 AND location='local') FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+original.ID, original.ID).Scan(&after, &successor, &copies); e != nil || !reflect.DeepEqual(savedCopy, after) || successor != nil || copies != 1 {
		t.Fatal("adoption replaced or duplicated current ownership", copies, e)
	}
	ra, rp := claimBackup(t, f, restored.ID)
	done := domain.HostingDatabaseBackupMutation{HostingAuthority: ra, ReceiptID: "existing_restore_intent", PlanHash: rp.Hash, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, done); e != nil {
		t.Fatal(e)
	}
	done.Phase = "complete"
	done.ReceiptID = "existing_restore_complete"
	done.Blob = original.Blob
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, done); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "local_after_exact_adoption", Name: "Current local recovery"}); e != nil {
		t.Fatal("exact adopted copy unavailable for restore", e)
	}
}
