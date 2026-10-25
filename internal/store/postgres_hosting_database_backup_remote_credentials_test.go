package store_test

import (
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func TestPostgresBackupRemoteCredentialGrantUsesPersistedInstant(t *testing.T) {
	f := backupFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, []string{domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability}); err != nil {
		t.Fatal(err)
	}
	keys, err := app.NewBackupTargetCredentials([]byte("grant-access-canary"), []byte("grant-secret-canary"))
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	target, err := f.service.SaveDatabaseBackupTarget(f.ctx, f.resource.ProjectID, "", domain.HostingDatabaseBackupTargetInput{RequestID: "grant_target", Name: "Grant target", Endpoint: "https://backup.example", Bucket: "grant-fixture", Region: "us-east-1", Enabled: true}, keys)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := f.service.ValidateDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "metadata_validation", ExpectedRevision: 1, DestinationID: f.op.Snapshot.DestinationID})
	if err != nil {
		t.Fatal(err)
	}
	a, p := claimRemoteTransfer(t, f, transfer.ID)
	in := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "metadata_delivery", PlanHash: p.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: f.op.Release.DaemonID}
	grant, err := f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, in)
	if err != nil {
		t.Fatal("grant authorization failed", err)
	}
	equivalent := grant.Metadata
	// An intentionally uncached zone makes pointer/representation inequality
	// deterministic on both a UTC runner and a non-UTC developer machine.
	equivalent.UseUntil = equivalent.UseUntil.In(time.FixedZone("owned-fixture-offset", 17*60))
	if equivalent == grant.Metadata || !equivalent.UseUntil.Equal(grant.Metadata.UseUntil) {
		t.Fatal("fixture did not isolate timestamp representation")
	}
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, in, equivalent); err != nil {
		t.Fatal("same persisted grant instant rejected", err)
	}
	replay, err := f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, in)
	if err != nil || !replay.Metadata.UseUntil.Equal(grant.Metadata.UseUntil) {
		t.Fatal("replay changed grant deadline", err)
	}
	for _, field := range []string{"deadline", "daemon", "fence", "target_revision", "runner", "purpose"} {
		t.Run(field, func(t *testing.T) {
			changed := equivalent
			switch field {
			case "deadline":
				changed.UseUntil = changed.UseUntil.Add(time.Nanosecond)
			case "daemon":
				changed.Request.DaemonID += "_foreign"
			case "fence":
				changed.Request.Authority.Fence += "_foreign"
			case "target_revision":
				changed.Request.TargetRevision++
			case "runner":
				changed.RunnerID += "_foreign"
			case "purpose":
				changed.Purpose += "_foreign"
			}
			changed.RequestHash = domain.HostingDocumentHash(struct {
				Runner  string
				Request domain.BackupRemoteCredentialRequest
			}{changed.RunnerID, changed.Request})
			if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, in, changed); err == nil {
				t.Fatal("changed immutable grant field accepted")
			}
		})
	}
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, "wrong_credential_hash", in, equivalent); err == nil {
		t.Fatal("wrong bearer revalidated grant")
	}
	var count int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_remote_deliveries WHERE operation_id=$1`, transfer.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay or denied metadata duplicated delivery", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, in, equivalent); err == nil {
		t.Fatal("expired lease revalidated grant")
	}
}
