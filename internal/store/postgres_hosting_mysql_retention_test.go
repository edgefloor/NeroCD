package store_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func mysqlRetentionFixture(t *testing.T) networkCaptureFixture {
	t.Helper()
	f := scheduledBackupFixtureFor(t, "mysql")
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPostgresMySQLRetentionPolicyPreviewExecutionReplay(t *testing.T) {
	testBackupRetentionPolicyPreviewExecutionReplay(t, mysqlRetentionFixture(t), domain.HostingDatabaseMySQLBackupRetentionVersion)
}
func TestPostgresMySQLRetentionUnknownNonversionedDeleteRemainsReserved(t *testing.T) {
	testBackupRetentionUnknownNonversionedDeleteRemainsReserved(t, true)
}

func TestPostgresMySQLRetentionPreservesPreSupportCopyEvidence(t *testing.T) {
	f := mysqlRetentionFixture(t)
	// Existing MySQL backup completion already records ownership without a retention policy.
	if _, err := f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_backup_retention_schema_compatible() RENAME TO unavailable_mysql_retention_schema`); err != nil {
		t.Fatal(err)
	}
	first := ownedRetentionBackup(t, f, 0)
	ownedRetentionBackup(t, f, 1)
	legacy, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "legacy_unproven", ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal(err)
	}
	legacy = completeBackup(t, f, legacy.ID)
	newest := ownedRetentionBackup(t, f, 2)
	var before []byte
	if err = f.db.QueryRow(f.ctx, `SELECT body FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+first.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `ALTER FUNCTION unavailable_mysql_retention_schema RENAME TO nerocd_mysql_backup_retention_schema_compatible`); err != nil {
		t.Fatal(err)
	}
	policy, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "existing_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
	if err != nil || policy.Version != domain.HostingDatabaseMySQLBackupRetentionVersion {
		t.Fatal(err)
	}
	preview, err := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "existing_preview", ExpectedPolicyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	foundOwned, foundLegacy, foundNewest := false, false, false
	for _, item := range preview.Items {
		switch item.BackupID {
		case first.ID:
			foundOwned = true
			if !item.Selected || item.Availability != "available" {
				t.Fatal("pre-support creation proof lost")
			}
		case legacy.ID:
			foundLegacy = true
			if item.Selected || item.Availability != "unproven" || len(item.ProtectedReasons) == 0 {
				t.Fatal("legacy copy gained deletion authority")
			}
		case newest.ID:
			foundNewest = true
			if item.Selected {
				t.Fatal("newest recovery copy selected")
			}
		}
	}
	var after []byte
	if err = f.db.QueryRow(f.ctx, `SELECT body FROM hosting_database_backup_copies WHERE id=$1`, "dbc_local_"+first.ID).Scan(&after); err != nil || !bytes.Equal(before, after) || !foundOwned || !foundLegacy || !foundNewest {
		t.Fatal("inventory rewrote/omitted immutable evidence", err)
	}
}

func TestPostgresMySQLRetentionSchemaAndCapabilityDenial(t *testing.T) {
	for _, scenario := range []string{"schema", "capability", "claim"} {
		t.Run(scenario, func(t *testing.T) {
			f := mysqlRetentionFixture(t)
			for i := range 3 {
				ownedRetentionBackup(t, f, i)
			}
			if scenario == "schema" {
				if _, err := f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_backup_retention_schema_compatible() RENAME TO unavailable_mysql_retention_schema`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "denied_policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true}); !errors.Is(err, store.ErrConflict) {
					t.Fatal("old schema admitted MySQL policy", err)
				}
				return
			}
			if _, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "policy", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}); err != nil {
				t.Fatal(err)
			}
			preview, err := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "preview", ExpectedPolicyRevision: 1})
			if err != nil {
				t.Fatal(err)
			}
			input := domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "execute", PreviewID: preview.ID, ExpectedPolicyRevision: 1, ExpectedPlanHash: preview.PlanHash}
			if scenario == "capability" {
				if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
					t.Fatal(err)
				}
				if _, err = f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, input); err == nil {
					t.Fatal("old runner admitted MySQL deletion")
				}
				var count int
				if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_retention_executions`).Scan(&count); err != nil || count != 0 {
					t.Fatal("denied deletion retained work", err)
				}
				return
			}
			execution, err := f.service.ExecuteDatabaseBackupRetention(f.ctx, f.resource.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDatabaseBackupRetention(f.ctx, execution.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("old runner claimed MySQL deletion", err)
			}
		})
	}
}

func TestPostgresMySQLRetentionCredentialAndProbeBinding(t *testing.T) {
	f, _, a, p := remoteRetentionBatchFor(t, true)
	copy := p.Copies[0]
	if p.Operation.Version != domain.HostingDatabaseMySQLBackupRetentionVersion || !strings.HasPrefix(copy.ObjectKey, "nerocd/mysql/") {
		t.Fatal("retention lost engine namespace")
	}
	input := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "mysql_retention_delivery", PlanHash: p.Hash, TargetID: copy.Target.Target.ID, TargetRevision: copy.Target.Target.Revision, DaemonID: copy.DaemonID, RetentionCopyID: copy.Item.CopyID}
	grant, err := f.pg.AuthorizeBackupRetentionCredentials(f.ctx, f.runner, f.credential, input)
	if err != nil || grant.Metadata.Purpose != domain.HostingDatabaseMySQLBackupRetentionCredentialPurpose {
		t.Fatal("MySQL retention credential purpose", err)
	}
	wrong := grant.Metadata
	wrong.Purpose = domain.HostingDatabaseBackupRetentionCredentialPurpose
	if err = f.pg.RevalidateBackupRetentionCredentials(f.ctx, f.runner, f.credential, input, wrong); err == nil {
		t.Fatal("PostgreSQL purpose crossed MySQL retention")
	}
	ctx := auth.WithPrincipal(f.ctx, auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	credentials, err := f.service.RunnerBackupRetentionCredentials(ctx, input, "image-hosting-fixture-token")
	if err != nil {
		t.Fatal("authenticated retention delivery", err)
	}
	if credentials.Metadata().Purpose != domain.HostingDatabaseMySQLBackupRetentionCredentialPurpose {
		t.Fatal("service lost retention purpose")
	}
	credentials.Close()
	probe := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "mysql_probe", PlanHash: p.Hash, CopyID: copy.Item.CopyID, Phase: "intent", ProbeOnly: true, ProbeKey: "nerocd/postgresql/retention-probes/" + a.OperationID + "/" + strings.Repeat("a", 32)}
	if _, err = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, probe); err == nil {
		t.Fatal("foreign engine probe accepted")
	}
	probe.ProbeKey = domain.HostingDatabaseBackupRetentionProbeNamespace(p.Operation.Version) + a.OperationID + "/" + strings.Repeat("a", 32)
	if _, err = f.pg.DatabaseBackupRetentionMutation(f.ctx, f.runner, probe); err != nil {
		t.Fatal("exact MySQL probe", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a); err == nil {
		t.Fatal("revoked adapter retained deletion authority")
	}
	if got, e := f.service.RunnerBackupRetentionCredentials(ctx, input, "image-hosting-fixture-token"); e == nil || got != nil {
		t.Fatal("revoked adapter disclosed retention keys")
	}
	// The already accepted intent cannot change the successful source histories.
	original, err := f.service.GetDatabaseBackup(f.ctx, copy.Item.BackupID)
	if err != nil || original.State != "succeeded" || original.Blob == nil || original.Blob.Digest != copy.Item.Digest {
		t.Fatal("retention changed source history", err)
	}
}

func TestPostgresMySQLRetentionInterruptedBatchFreshFence(t *testing.T) {
	testBackupRetentionInterruptedBatchFreshFence(t, mysqlRetentionFixture(t))
}
func TestPostgresMySQLRetentionAutomaticPublicationOrdering(t *testing.T) {
	testBackupRetentionAutomaticPublicationOrdering(t, true)
}
