package store_test

import (
	"context"
	"errors"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"strings"
	"testing"
	"time"
)

func databaseDownloadFixture(t *testing.T, kind string) (networkCaptureFixture, domain.HostingDatabaseBackupInput) {
	t.Helper()
	var f networkCaptureFixture
	component := ""
	switch kind {
	case "native_pg":
		f = retentionFixture(t)
	case "native_mysql":
		f = scheduledBackupFixtureFor(t, "mysql")
	case "compose_mysql":
		f = composeMySQLRetentionFixture(t)
		component = "mysql"
	case "compose_pg":
		pg, db, base, server, destination := hostingExecutionFixture(t)
		caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseBackupComponentCapability, domain.HostingStackLifecycleCapability, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability}
		if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, server.RunnerID, caps); err != nil {
			t.Fatal(err)
		}
		resource, op := deployBackupComponentStack(t, pg, base, server, destination, "a")
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
		f = networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: pg, db: db, resource: resource, runner: server.RunnerID, op: op}, service: hostingDatabaseApplication(t, pg), ctx: ctx}
		component = "postgresql"
	}
	caps := []string{domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupDownloadCapability, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability, domain.HostingDatabaseMySQLBackupRetentionCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, caps); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseBackupInput{RequestID: "download_original", ExpectedResourceGeneration: f.resource.Generation, Component: component}
	if component != "" {
		in.ExpectedDeployedOperationID = f.op.ID
	}
	return f, in
}

func TestPostgresDatabaseDownloadHistoricalCopyAuthorityAndRetention(t *testing.T) {
	for _, kind := range []string{"native_pg", "native_mysql", "compose_pg", "compose_mysql"} {
		t.Run(kind, func(t *testing.T) {
			f, input := databaseDownloadFixture(t, kind)
			publish := func(request string, owned bool) domain.HostingDatabaseBackupOperation {
				in := input
				in.RequestID = request
				b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, in)
				if err != nil {
					t.Fatal(err)
				}
				a, p := claimBackup(t, f, b.ID)
				m := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent_" + b.ID, PlanHash: p.Hash, Phase: "intent"}
				if p.Component != nil {
					m.ComponentVersion = p.Operation.Version
					m.WritersResumed = true
				}
				if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, m); err != nil {
					t.Fatal(err)
				}
				m.Phase = "complete"
				m.ReceiptID = "complete_" + b.ID
				m.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
				if !owned {
					m.Blob.Digest = "sha256:" + strings.Repeat("c", 64)
				}
				if owned {
					m.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("b", 64), Device: 1, Inode: 2, Bytes: 65537, Created: true}
				}
				out, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, m)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			backup := publish("download_original", true)
			unproven := publish("download_unproven", false)
			out, err := f.service.DatabaseBackupDownloadOptions(f.ctx, unproven.ID)
			if err != nil || out.Available || out.ReasonCode != "local_copy_unavailable" {
				t.Fatal("unproven copy admitted", err)
			}
			out, err = f.service.DatabaseBackupDownloadOptions(f.ctx, backup.ID)
			if err != nil || !out.Available || out.Blob == nil || *out.Blob != *backup.Blob {
				t.Fatal("historical local copy unavailable", err)
			}
			if _, err = f.pg.DatabaseBackupDownloadOptions(f.ctx, "unknown_owner", backup.ID); !errors.Is(err, auth.ErrForbidden) {
				t.Fatal("nonowner read", err)
			}
			policy, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "download_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "download_preview", ExpectedPolicyRevision: policy.Revision})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.service.CreateDatabaseBackupDownload(f.ctx, "download_one", backup.ID, time.Now().Add(time.Minute))
			if err != nil || plan.Validate() != nil || plan.BackupVersion != backup.Version {
				t.Fatal("admission", err)
			}
			_, err = f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "delete_during_read", PreviewID: preview.ID, ExpectedPlanHash: preview.PlanHash, ExpectedPolicyRevision: policy.Revision})
			if !errors.Is(err, store.ErrConflict) {
				t.Fatal("retention ignored active reader", err)
			}
			_, err = f.service.RotateRunnerToken(f.ctx, app.RunnerTokenInput{RunnerID: f.runner})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runner_enrollments SET credential_hash=(SELECT token_hash FROM runners WHERE id=$1) WHERE runner_id=$1 AND revoked_at IS NULL`, f.runner); err != nil {
				t.Fatal(err)
			}
			var hash string
			if err = f.db.QueryRow(f.ctx, `SELECT token_hash FROM runners WHERE id=$1`, f.runner).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			ctx, err := store.BindHostingDatabaseConnectionRunnerContext(auth.WithPrincipal(context.Background(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner}), f.runner, hash)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := f.service.ClaimRun(ctx)
			if err != nil || claim.Run.ID != "db_transfer_download_one" {
				t.Fatal("claim", err)
			}
			a := domain.HostingAuthority{OperationID: plan.SessionID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			actual, err := f.service.RunnerDatabaseBackupDownloadPlan(ctx, a)
			if err != nil || actual != plan {
				t.Fatal("full current authority", err)
			}
			stale := a
			stale.Fence = "wrong"
			if _, err = f.service.RunnerDatabaseBackupDownloadPlan(ctx, stale); err == nil {
				t.Fatal("stale authority")
			}
			in := domain.HostingDatabaseBackupDownloadStream{HostingAuthority: a, PlanHash: plan.Hash}
			if _, err = f.service.BeginDatabaseBackupDownload(ctx, in); err != nil {
				t.Fatal("begin", err)
			}
			if _, err = f.service.BeginDatabaseBackupDownload(ctx, in); !errors.Is(err, store.ErrConflict) {
				t.Fatal("second stream attempt", err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, plan.DestinationID, plan.ProjectID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.RunnerDatabaseBackupDownloadPlan(ctx, a); !errors.Is(err, auth.ErrForbidden) {
				t.Fatal("revoked authority", err)
			}
			if err = f.service.CloseDatabaseBackupDownload(f.ctx, plan.SessionID, false); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.RunnerDatabaseBackupDownloadPlan(ctx, a); err == nil {
				t.Fatal("closed stream replay")
			}
			now := time.Now().UTC()
			for range 2 {
				if _, err = f.pg.CompleteLeaseRequest(ctx, a.LeaseID, f.runner, domain.RunFailed, a.Attempt, a.Fence, "download_closed", now, domain.RunFailed, &now, nil, nil, domain.AuditEvent{}); err != nil {
					t.Fatal("metadata completion/replay", err)
				}
			}
			var active bool
			var transfers int
			if err = f.db.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM hosting_database_backup_downloads WHERE runner_id=$1 AND state IN ('waiting','streaming') AND expires_at>clock_timestamp()),(SELECT count(*) FROM hosting_database_backup_transfers)`, f.runner).Scan(&active, &transfers); err != nil || active || transfers != 0 {
				t.Fatal("read hold or remote effects remained", err)
			}

		})
	}
}
