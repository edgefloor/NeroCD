package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func deletionCapability(t *testing.T, f archiveControlFixture) {
	t.Helper()
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3,$4]::text[] WHERE id=$1`, f.runner, domain.RunTypeStorageBackupRetention, domain.HostingStorageBackupRetentionCapability, domain.HostingStorageBackupDeletionCapability); e != nil {
		t.Fatal(e)
	}
}
func deletionPreview(t *testing.T, f archiveControlFixture, b domain.HostingStorageBackup, ids []string) domain.HostingStorageBackupRetentionPreview {
	t.Helper()
	p, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionPreviewInput{RequestID: "preview", ExpectedResourceGeneration: f.resource.Generation, CopyIDs: ids})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func deletionAdmit(t *testing.T, f archiveControlFixture, b domain.HostingStorageBackup, p domain.HostingStorageBackupRetentionPreview) domain.HostingStorageBackupRetentionExecution {
	t.Helper()
	v, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPlanHash: p.PlanHash, ConfirmFinalCopy: p.FinalCopy})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func deletionClaim(t *testing.T, f archiveControlFixture, v domain.HostingStorageBackupRetentionExecution) (domain.HostingAuthority, domain.HostingStorageBackupRetentionPlan) {
	t.Helper()
	if e := f.pg.PrepareStorageBackupRetention(f.ctx, v.ID); e != nil {
		t.Fatal(e)
	}
	c, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := domain.HostingAuthority{OperationID: v.ID, RunID: c.Run.ID, LeaseID: c.Lease.ID, Attempt: c.Lease.Attempt, Fence: c.Lease.Fence}
	p, e := f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, a)
	if e != nil {
		t.Fatal(e)
	}
	return a, p
}
func deletionRemoteBackup(t *testing.T) (archiveControlFixture, domain.HostingStorageBackup, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	f, target := storageRemoteControlFixture(t)
	deletionCapability(t, f)
	b := storageRetentionBackupTarget(t, f, 82, true, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
	ts, e := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if e != nil || len(ts) != 1 {
		t.Fatal(e)
	}
	a, p := claimStorageRemote(t, f, ts[0].ID)
	m := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "upload_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
	if _, e = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); e != nil {
		t.Fatal(e)
	}
	m.Phase = "complete"
	m.ReceiptID = "upload_complete"
	m.RemoteCopy = &domain.BackupRemoteCopyProof{ETag: `"owned-object"`, Created: true}
	if _, e = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); e != nil {
		t.Fatal(e)
	}
	b, e = f.service.GetStorageBackup(f.owner, b.ID)
	if e != nil || len(b.Copies) != 2 {
		t.Fatal(e, b.Copies)
	}
	return f, b, target
}
func TestPostgresStorageBackupDeletionManualDeleteSelectedBackupLocalKeepsRemoteAndHistory(t *testing.T) {
	f, b, _ := deletionRemoteBackup(t)
	p := deletionPreview(t, f, b, []string{b.Copies[0].CopyID})
	if p.FinalCopy || p.Origin != domain.StorageBackupManualSelection || p.PolicyRevision != 0 {
		t.Fatal(p)
	}
	v := deletionAdmit(t, f, b, p)
	a, plan := deletionClaim(t, f, v)
	for _, phase := range []string{"intent", "deleted"} {
		_, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: phase, PlanHash: plan.Hash, CopyID: p.Items[0].CopyID, Phase: phase})
		if e != nil {
			t.Fatal(e)
		}
	}
	got, e := f.service.GetStorageBackup(f.owner, b.ID)
	if e != nil || got.State != "succeeded" || got.Blob == nil || len(got.Copies) != 2 {
		t.Fatal(e, got)
	}
	available := 0
	for _, c := range got.Copies {
		if c.Availability == "available" {
			available++
			if c.Location != "remote" {
				t.Fatal("wrong copy survives")
			}
		}
	}
	if available != 1 {
		t.Fatal(got.Copies)
	}
	history, e := f.service.ListStorageBackupDeletions(f.owner, f.resource.ID, b.ID)
	if e != nil || len(history) != 1 || history[0].State != "succeeded" {
		t.Fatal(e, history)
	}
	policy, e := f.service.GetStorageBackupRetention(f.owner, f.resource.ID)
	if e != nil || policy != nil {
		t.Fatal("manual created policy", e)
	}
	old, e := f.service.ListStorageBackupRetentionExecutions(f.owner, f.resource.ID)
	if e != nil || len(old) != 0 {
		t.Fatal("manual leaked into policy workflow", e)
	}
}
func TestPostgresStorageBackupDeletionCommittedResponseLossRetriesExactSelection(t *testing.T) {
	f := storageRetentionFixture(t)
	deletionCapability(t, f)
	b := storageRetentionBackup(t, f, 42, true)
	p := deletionPreview(t, f, b, []string{b.Copies[0].CopyID})
	if !p.FinalCopy {
		t.Fatal(p)
	}
	in := domain.HostingStorageBackupDeletionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPlanHash: p.PlanHash}
	if _, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in); !errors.Is(e, store.ErrConflict) {
		t.Fatal("final confirmation missing", e)
	}
	in.ConfirmFinalCopy = true
	v, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in)
	if e != nil || !reflect.DeepEqual(v, again) {
		t.Fatal("response replay", e)
	}
	if _, e = f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, "foreign", in); e == nil {
		t.Fatal("foreign scope replay")
	}
	in.ConfirmFinalCopy = false
	if _, e = f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed request replay", e)
	}
}
func TestPostgresStorageBackupDeletionRestartAfterIntent(t *testing.T) {
	f := storageRetentionFixture(t)
	deletionCapability(t, f)
	b := storageRetentionBackup(t, f, 43, true)
	p := deletionPreview(t, f, b, []string{b.Copies[0].CopyID})
	v := deletionAdmit(t, f, b, p)
	old, plan := deletionClaim(t, f, v)
	m := domain.HostingStorageBackupRetentionMutation{HostingAuthority: old, ReceiptID: "intent", PlanHash: plan.Hash, CopyID: p.Items[0].CopyID, Phase: "intent"}
	if _, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, m); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, old.LeaseID); e != nil {
		t.Fatal(e)
	}
	resumed, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionInput{RequestID: "resume", PreviewID: p.ID, ExpectedPlanHash: p.PlanHash, ConfirmFinalCopy: true})
	if e != nil || resumed.ID != v.ID {
		t.Fatal(e)
	}
	c, e := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if e != nil || c.Lease.Attempt != 2 || c.Lease.Fence == old.Fence {
		t.Fatal(e)
	}
	fresh := domain.HostingAuthority{OperationID: v.ID, RunID: c.Run.ID, LeaseID: c.Lease.ID, Attempt: c.Lease.Attempt, Fence: c.Lease.Fence}
	next, e := f.pg.StorageBackupRetentionPlan(f.ctx, f.runner, fresh)
	if e != nil || next.Hash != plan.Hash || next.Progress[0].State != "intent" {
		t.Fatal(e, next)
	}
	m.Phase = "deleted"
	m.ReceiptID = "stale_deleted"
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, m); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale fence admitted", e)
	}
	m.HostingAuthority = fresh
	m.ReceiptID = "fresh_deleted"
	if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, m); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresStorageBackupDeletionRejectsStaleAuthorityAndProtectedCopies(t *testing.T) {
	for _, kind := range []string{"unproven", "foreign", "generation", "capability", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f := storageRetentionFixture(t)
			deletionCapability(t, f)
			b := storageRetentionBackup(t, f, 44, kind != "unproven")
			in := domain.HostingStorageBackupDeletionPreviewInput{RequestID: "preview", ExpectedResourceGeneration: f.resource.Generation, CopyIDs: []string{b.Copies[0].CopyID}}
			if kind == "unproven" {
				if _, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in); e == nil {
					t.Fatal("unproven selected")
				}
				return
			}
			if kind == "foreign" {
				in.CopyIDs = []string{"sbc_foreign"}
				if _, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in); e == nil {
					t.Fatal("foreign selected")
				}
				return
			}
			if kind == "generation" {
				in.ExpectedResourceGeneration++
				if _, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in); e == nil {
					t.Fatal("stale generation")
				}
				return
			}
			p := deletionPreview(t, f, b, in.CopyIDs)
			if kind == "capability" {
				if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingStorageBackupDeletionCapability); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "audit" {
				if _, e := f.db.Exec(f.ctx, `CREATE FUNCTION reject_deletion_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit rejection'; END $$; CREATE TRIGGER reject_deletion_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_deletion_audit()`); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionInput{RequestID: "execute", PreviewID: p.ID, ExpectedPlanHash: p.PlanHash, ConfirmFinalCopy: true}); e == nil {
				t.Fatal("denied admission")
			}
			var n int
			if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_retention_executions`).Scan(&n); e != nil || n != 0 {
				t.Fatal("failed admission persisted", e, n)
			}
		})
	}
}
func TestPostgresStorageBackupDeletionRemoteUsesRecordedTargetBothCopiesReportsPartialOutcome(t *testing.T) {
	f, b, target := deletionRemoteBackup(t)
	ids := []string{}
	for _, c := range b.Copies {
		ids = append(ids, c.CopyID)
	}
	p := deletionPreview(t, f, b, ids)
	v := deletionAdmit(t, f, b, p)
	a, plan := deletionClaim(t, f, v)
	for _, c := range plan.Copies {
		if c.Target != nil && (c.Target.Target.ID != target.ID || c.Target.Target.Endpoint != "https://backup.example") {
			t.Fatal("target redirected")
		}
		m := domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent_" + c.Item.CopyID, PlanHash: plan.Hash, CopyID: c.Item.CopyID, Phase: "intent"}
		if _, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, m); e != nil {
			t.Fatal(e)
		}
		m.ReceiptID = "result_" + c.Item.CopyID
		m.Phase = "deleted"
		if c.Item.Location == "remote" {
			m.Phase = "failed"
			m.FailureCode = "remote_unavailable"
		}
		if _, e := f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, m); e != nil {
			t.Fatal(e)
		}
	}
	result, e := f.service.GetStorageBackupDeletion(f.owner, f.resource.ID, b.ID, v.ID)
	if e != nil || result.LocalState != "succeeded" || result.RemoteState != "failed" || result.State != "failed" {
		t.Fatal(e, result)
	}
	if _, e = f.service.GetStorageBackupDeletion(f.owner, f.resource.ID, strings.Repeat("f", 32), v.ID); e == nil {
		t.Fatal("foreign backup disclosed history")
	}
}
func TestPostgresStorageBackupDeletionRechecksFinalCopyAtExecution(t *testing.T) {
	f, b, _ := deletionRemoteBackup(t)
	var local, remote string
	for _, c := range b.Copies {
		if c.Location == "local" {
			local = c.CopyID
		} else {
			remote = c.CopyID
		}
	}
	localPreview := deletionPreview(t, f, b, []string{local})
	rp, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionPreviewInput{RequestID: "remote_preview", ExpectedResourceGeneration: f.resource.Generation, CopyIDs: []string{remote}})
	if e != nil || rp.FinalCopy {
		t.Fatal(e)
	}
	rv := deletionAdmit(t, f, b, rp)
	a, plan := deletionClaim(t, f, rv)
	for _, phase := range []string{"intent", "deleted"} {
		if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: phase, PlanHash: plan.Hash, CopyID: remote, Phase: phase}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionInput{RequestID: "local_execute", PreviewID: localPreview.ID, ExpectedPlanHash: localPreview.PlanHash}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("unreviewed final-copy loss admitted", e)
	}
}
func TestPostgresStorageBackupDeletionRevocationAfterAdmission(t *testing.T) {
	for _, kind := range []string{"owner", "grant", "runner", "retention_capability", "deletion_capability", "generation", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f := storageRetentionFixture(t)
			deletionCapability(t, f)
			b := storageRetentionBackup(t, f, 45, true)
			preview := deletionPreview(t, f, b, []string{b.Copies[0].CopyID})
			v := deletionAdmit(t, f, b, preview)
			a, p := deletionClaim(t, f, v)
			var e error
			switch kind {
			case "owner":
				_, e = f.db.Exec(f.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, p.ActorID)
			case "grant":
				_, e = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, f.storage.DestinationID, v.ProjectID)
			case "runner":
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner)
			case "retention_capability":
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingStorageBackupRetentionCapability)
			case "deletion_capability":
				_, e = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingStorageBackupDeletionCapability)
			case "generation":
				_, e = f.db.Exec(f.ctx, `UPDATE resources SET generation=generation+1 WHERE id=$1`, f.resource.ID)
			case "audit":
				_, e = f.db.Exec(f.ctx, `CREATE FUNCTION reject_manual_intent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit rejection'; END $$; CREATE TRIGGER reject_manual_intent_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_manual_intent_audit()`)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pg.StorageBackupRetentionMutation(f.ctx, f.runner, domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "denied_intent", PlanHash: p.Hash, CopyID: p.Copies[0].Item.CopyID, Phase: "intent"}); e == nil {
				t.Fatal("revoked manual effect admitted")
			}
			var n int
			if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_retention_receipts WHERE execution_id=$1`, v.ID).Scan(&n); e != nil || n != 0 {
				t.Fatal("denied intent persisted", e, n)
			}
		})
	}
}
func TestPostgresStorageBackupDeletionDisabledPolicyDoesNotSelectCopies(t *testing.T) {
	f := storageRetentionFixture(t)
	deletionCapability(t, f)
	b := storageRetentionBackup(t, f, 46, true)
	policy, e := f.service.SaveStorageBackupRetention(f.owner, f.resource.ID, domain.HostingStorageBackupRetentionInput{RequestID: "disabled_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: false, Local: domain.BackupRetentionLimits{KeepCount: 99}, Remote: domain.BackupRetentionLimits{KeepCount: 88}})
	if e != nil {
		t.Fatal(e)
	}
	in := domain.HostingStorageBackupDeletionPreviewInput{RequestID: "manual_preview", ExpectedResourceGeneration: f.resource.Generation, CopyIDs: []string{b.Copies[0].CopyID}}
	p, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in)
	if e != nil || len(p.Items) != 1 || p.PolicyRevision != 0 {
		t.Fatal("disabled policy blocked explicit selection", e, p)
	}
	replay, e := f.service.PreviewStorageBackupDeletion(f.owner, f.resource.ID, b.ID, in)
	if e != nil || !reflect.DeepEqual(p, replay) {
		t.Fatal("manual preview reselected", e)
	}
	deletionAdmit(t, f, b, p)
	got, e := f.service.GetStorageBackupRetention(f.owner, f.resource.ID)
	if e != nil || got == nil || !reflect.DeepEqual(*got, policy) {
		t.Fatal("manual changed disabled policy", e)
	}
}
