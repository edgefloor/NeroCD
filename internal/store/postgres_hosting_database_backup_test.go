package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

func backupFixture(t *testing.T) networkCaptureFixture {
	t.Helper()
	f := newNetworkCaptureFixture(t, "postgresql")
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability); err != nil {
		t.Fatal(err)
	}
	return f
}
func claimBackup(t *testing.T, f networkCaptureFixture, id string) (domain.HostingAuthority, domain.HostingDatabaseBackupPlan) {
	t.Helper()
	if err := f.pg.PrepareDatabaseBackup(f.ctx, id); err != nil {
		t.Fatal("backup dispatch", err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseBackup || claim.Run.RunSpec.HostingOperationID != id {
		t.Fatalf("backup claim %s: %v", claim.Run.ID, err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, err := f.pg.DatabaseBackupPlan(f.ctx, f.runner, a)
	if err != nil || p.Validate() != nil {
		t.Fatal("backup plan", err)
	}
	return a, p
}
func completeBackup(t *testing.T, f networkCaptureFixture, id string) domain.HostingDatabaseBackupOperation {
	t.Helper()
	a, p := claimBackup(t, f, id)
	return completeClaimedBackup(t, f, a, p)
}

func completeClaimedBackup(t *testing.T, f networkCaptureFixture, a domain.HostingAuthority, p domain.HostingDatabaseBackupPlan) domain.HostingDatabaseBackupOperation {
	t.Helper()
	id := p.Operation.ID
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent_" + id, PlanHash: p.Hash, Phase: "intent"}
	if p.Component != nil {
		in.ComponentVersion = p.Operation.Version
		in.WritersResumed = true
		if p.Target != nil {
			for _, c := range p.Target.Stack.Components {
				if c.Component != p.Component.Target {
					in.Writers = append(in.Writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ContainerID, OperationID: c.OperationID, ImageID: c.ImageID, PlanHash: p.Target.PlanHash, ReleaseHash: p.Target.Release.Hash, Running: c.Running})
				}
			}
		}
	}
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal("intent", err)
	}
	if _, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal("intent replay", err)
	}
	in.Phase = "complete"
	in.ReceiptID = "complete_" + id
	in.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: domain.HostingDatabaseBackupFormatFor(p.Operation.Version)}
	out, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
	if err != nil || out.State != "succeeded" || out.OwnershipHeld {
		t.Fatal("backup settle", err)
	}
	replay, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
	if err != nil || !reflect.DeepEqual(out, replay) {
		t.Fatal("lost ACK replay changed", err)
	}
	return out
}

func TestPostgresDatabaseBackupRestoredTargetApplicationConnection(t *testing.T) {
	source := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t).ready(t).complete(t)
	f := source.networkCaptureFixture
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability); err != nil {
		t.Fatal(err)
	}
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "application_restore_source", ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal("source backup admission", err)
	}
	backup = completeBackup(t, f, backup.ID)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "application_restore_target", Name: "Restored application database"})
	if err != nil {
		t.Fatal("restore admission", err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal("held target read", err)
	}
	child, storage := prepareBackupRestoreChild(t, f, restored, target)
	restored = completeBackup(t, f, restored.ID)
	if restored.State != "succeeded" || restored.OwnershipHeld || target.Resource.ID == f.resource.ID || target.Binding.ID == f.op.BindingID {
		t.Fatal("restore did not settle an independent target")
	}
	// Continue with the real controller using the restored target's persisted
	// healthy deployment and volume, rather than a newly seeded source database.
	f.resource, f.storage, f.op = target.Resource, storage, child
	capabilities := []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingDatabaseUpgradeCapability, domain.HostingApplicationDatabaseCapability, domain.HostingDatabaseMaintenanceCapability}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, capabilities); err != nil {
		t.Fatal(err)
	}
	application, err := f.service.CreateResource(f.ctx, app.ResourceInput{ProjectID: target.Resource.ProjectID, ProjectEnvironmentID: target.Resource.ProjectEnvironmentID, Name: "Restored database consumer", Kind: "application", Source: source.application.Source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.BindResourceDestination(f.ctx, application.ID, app.ResourceBindingInput{DestinationID: child.Snapshot.DestinationID}); err != nil {
		t.Fatal(err)
	}
	runnerContext, err := store.BindHostingDatabaseConnectionRunnerContext(f.ctx, f.runner, f.credential)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: target.Resource.ID, ExpectedResourceGeneration: application.Generation, ExpectedDatabaseGeneration: target.Resource.Generation, RequestID: strings.Repeat("f", 32)}, Deployment: hostingDeploymentRequest()}
	input.Deployment.IdempotencyKey = "restored_application_initial"
	input.Deployment.HostPort = 0 // A distinct consumer receives its own managed port.
	connected := (connectionFixture{networkCaptureFixture: f, application: application, input: input, runnerContext: runnerContext}).connectWithCaptureReceipt(t, "origin_"+domain.HostingDocumentHash(child.ID)[7:39])
	if connected.connection.CaptureID != domain.HostingDatabaseNetworkCaptureID(child.ID) || connected.appOperation.ApplicationDatabase == nil || connected.appOperation.ApplicationDatabase.Binding.DatabaseResourceID != target.Resource.ID {
		t.Fatal("restored application used foreign capture or database evidence")
	}
	var sourceAudit, targetAudit string
	if err = f.db.QueryRow(f.ctx, `SELECT audit_id FROM hosting_database_network_upgrades WHERE operation_id=$1`, source.connection.UpgradeOperationID).Scan(&sourceAudit); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(f.ctx, `SELECT audit_id FROM hosting_database_network_upgrades WHERE operation_id=$1`, connected.connection.UpgradeOperationID).Scan(&targetAudit); err != nil || sourceAudit != "aud_connection_upgrade_"+source.connection.UpgradeOperationID || targetAudit != "aud_connection_upgrade_"+connected.connection.UpgradeOperationID || sourceAudit == targetAudit {
		t.Fatal("connection upgrades did not retain distinct audit identities", err)
	}
	before := connectionCounts(t, connected)
	replay, err := f.service.ConnectHostingApplicationDatabase(f.ctx, application.ID, input)
	if err != nil || replay.ID != connected.connection.ID || replay.UpgradeOperationID != connected.connection.UpgradeOperationID || replay.ApplicationOperationID != connected.appOperation.ID || connectionCounts(t, connected) != before {
		t.Fatal("connection replay changed restored-target admission", err)
	}
}

func TestPostgresDatabaseConnectionUpgradeHistoricalEmptyAuditReplay(t *testing.T) {
	f, input := upgradeFixture(t, "postgresql")
	const id = "historical_connection_upgrade"
	// Older controller versions supplied an empty ID. Admit that exact historical
	// shape through the existing store boundary, without rewriting stored rows.
	operation, err := f.pg.RequestHostingDatabaseNetworkUpgrade(f.ctx, id, f.resource.ID, f.op.CreatedBy, input, domain.AuditEvent{ActorID: f.op.CreatedBy, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal("historical admission", err)
	}
	f = settleUpgrade(t, prepareUpgrade(t, f, operation))
	before := upgradeCounts(t, f)
	replay, err := f.pg.RequestHostingDatabaseNetworkUpgrade(f.ctx, id, f.resource.ID, operation.CreatedBy, input, domain.AuditEvent{ID: "aud_connection_upgrade_" + id, ActorID: operation.CreatedBy, CreatedAt: time.Now().UTC()})
	if err != nil || replay.ID != id || replay.State != "succeeded" || replay.DatabaseUpgrade == nil || replay.DatabaseUpgrade.Hash != f.op.DatabaseUpgrade.Hash || upgradeCounts(t, f) != before {
		t.Fatal("historical replay changed accepted operation", err)
	}
	var auditID string
	if err = f.db.QueryRow(f.ctx, `SELECT audit_id FROM hosting_database_network_upgrades WHERE operation_id=$1`, id).Scan(&auditID); err != nil || auditID != "" {
		t.Fatal("historical empty audit identity was rewritten", err)
	}
}

func TestPostgresDatabaseBackupRestoreAtomicLifecycle(t *testing.T) {
	f := backupFixture(t)
	in := domain.HostingDatabaseBackupInput{RequestID: "backup_one", ExpectedResourceGeneration: 1}
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, in)
	if err != nil {
		t.Fatal("request", err)
	}
	replay, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, in)
	if err != nil || replay.ID != b.ID {
		t.Fatal("request replay", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("blocked_source")); err == nil {
		t.Fatal("source maintenance passed backup hold")
	}
	b = completeBackup(t, f, b.ID)
	raw, _ := json.Marshal(b)
	for _, secret := range []string{"source_plan", "location_id", "credentials", "password", "/var/", "runner_id", "fence"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("unsafe public projection", secret)
		}
	}
	ri := domain.HostingDatabaseBackupRestoreInput{RequestID: "restore_one", Name: "Restored database"}
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, b.ID, ri)
	if err != nil {
		t.Fatal("restore admission", err)
	}
	again, err := f.service.RequestDatabaseBackupRestore(f.ctx, b.ID, ri)
	if err != nil || again.ID != restored.ID || again.TargetResourceID != restored.TargetResourceID {
		t.Fatal("one restore target replay", err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.service.GetHostingDatabase(f.ctx, f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Resource.ID == source.Resource.ID || target.Binding.ID == source.Binding.ID || target.StorageRef == source.StorageRef {
		t.Fatal("restore reused source identity")
	}
	for i := range source.Credentials {
		if source.Credentials[i].SecretID == target.Credentials[i].SecretID {
			t.Fatal("restore reused credential reference")
		}
		a, _ := f.pg.GetManagedSecretRecord(f.ctx, source.Credentials[i].SecretID, 1)
		b, _ := f.pg.GetManagedSecretRecord(f.ctx, target.Credentials[i].SecretID, 1)
		if reflect.DeepEqual(a.Envelope, b.Envelope) {
			t.Fatal("restore reused encrypted credentials")
		}
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("blocked_target")); err == nil {
		t.Fatal("public deploy crossed target restore hold")
	}
	if err = f.pg.PrepareDatabaseBackup(f.ctx, restored.ID); !errors.Is(err, hostingBackupWaiting()) {
		t.Fatal("target child not awaited", err)
	}
	prepareBackupRestoreChild(t, f, restored, target)
	// Retain the actual pending restore lease before claiming independent work.
	// The generic claim queue may legitimately return this restore first.
	restoreAuthority, restorePlan := claimBackup(t, f, restored.ID)
	// Restoring an immutable artifact holds only its fresh target. A normal
	// source redeployment can be admitted independently while import is pending.
	sourceReplacement, err := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("source_replacement_during_restore"))
	if err != nil {
		t.Fatal("restore blocked source replacement", err)
	}
	sf := prepareDatabaseExecution(t, f.storageMaterializationFixture, sourceReplacement)
	origin, err := sf.pg.HostingStorageCreateBegin(f.ctx, f.runner, sf.request())
	if err != nil || origin.CreatePermitted {
		t.Fatal("source replacement origin", err)
	}
	if _, err = sf.pg.HostingStorageMaterialized(f.ctx, f.runner, sf.proof(origin)); err != nil {
		t.Fatal(err)
	}
	if _, err = sf.pg.HostingApplyBegin(f.ctx, f.runner, sf.authority); err != nil {
		t.Fatal(err)
	}
	if _, err = sf.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: sf.authority, ReceiptID: "source_replaced", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: sf.op.Release.ImageID, DaemonID: sf.op.Release.DaemonID, PlanHash: sf.op.Plan.Hash}}); err != nil {
		t.Fatal("source replacement completion", err)
	}
	completeClaimedBackup(t, f, restoreAuthority, restorePlan)
	history, err := f.service.ListDatabaseBackups(f.ctx, f.resource.ID)
	if err != nil || len(history) != 2 {
		t.Fatal("backup history", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("after_restore")); err != nil {
		t.Fatal("settled target remained held", err)
	}
}
func hostingBackupWaiting() error { return hostingjobs.ErrDatabaseBackupWaiting }
func TestPostgresDatabaseBackupConcurrentRequestAndAuthority(t *testing.T) {
	f := backupFixture(t)
	in := domain.HostingDatabaseBackupInput{RequestID: "concurrent", ExpectedResourceGeneration: 1}
	var wg sync.WaitGroup
	out := make(chan domain.HostingDatabaseBackupOperation, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, in)
			out <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for v := range out {
		if id != "" && v.ID != id {
			t.Fatal("concurrent request created two effects")
		}
		id = v.ID
	}
	a, p := claimBackup(t, f, id)
	bad := a
	bad.Fence += "_stale"
	if _, e := f.pg.DatabaseBackupPlan(f.ctx, f.runner, bad); e == nil {
		t.Fatal("stale fence accepted")
	}
	before := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err := f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, p.Source.Snapshot.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, before); e == nil {
		t.Fatal("revoked grant admitted effect")
	}
	var receipts int
	if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_receipts`).Scan(&receipts); e != nil || receipts != 0 {
		t.Fatal("denied effect left evidence", e)
	}
}
func TestPostgresDatabaseBackupUncertainHoldsAndNoNewAttempt(t *testing.T) {
	f := backupFixture(t)
	b, e := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "uncertain", ExpectedResourceGeneration: 1})
	if e != nil {
		t.Fatal(e)
	}
	a, p := claimBackup(t, f, b.ID)
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, Phase: "intent"}
	if _, e = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); e != nil {
		t.Fatal(e)
	}
	in.Phase = "uncertain"
	in.ReceiptID = "uncertain"
	in.FailureCode = "interrupted"
	out, e := f.pg.DatabaseBackupMutation(f.ctx, f.runner, in)
	if e != nil || !out.OwnershipHeld || out.State != "uncertain" {
		t.Fatal("uncertain settlement", e)
	}
	if _, e = f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("cannot_maintain")); e == nil {
		t.Fatal("uncertain hold released")
	}
	if e = f.pg.PrepareDatabaseBackup(f.ctx, b.ID); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM run_leases WHERE run_id=$1`, a.RunID).Scan(&n); e != nil || n != 1 {
		t.Fatal("uncertain effect generated retry attempt", e)
	}
}

func TestPostgresDatabaseBackupLegacyLeaseAndImmutableArtifact(t *testing.T) {
	f := backupFixture(t)
	// A historical task without a type remains outside this new lease family.
	if _, err := f.db.Exec(f.ctx, `INSERT INTO task_runs(id,project_id,run_spec,status,requested_by,runner_id) VALUES('legacy_backup_test',$1,'{}','running','usr_bootstrap',$2)`, f.resource.ProjectID, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(f.ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,attempt,fence) VALUES('legacy_backup_lease','legacy_backup_test',$1,'active',clock_timestamp()+interval '1 minute',1,'legacy_fence')`, f.runner); err != nil {
		t.Fatal("new trigger rejected unrelated legacy lease", err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE run_leases SET status='succeeded',completed_at=clock_timestamp() WHERE id='legacy_backup_lease'`); err != nil {
		t.Fatal(err)
	}
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "immutable", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, b.ID)
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backups SET blob=jsonb_set(blob,'{bytes}','42') WHERE id=$1`, b.ID); err == nil {
		t.Fatal("completed archive metadata changed")
	}
	if _, err = f.db.Exec(f.ctx, `DELETE FROM hosting_database_backup_receipts WHERE operation_id=$1`, b.ID); err == nil {
		t.Fatal("receipt deleted")
	}
}

func TestPostgresDatabaseBackupConnectedSourcePlan(t *testing.T) {
	f := newConnectionFixture(t, "postgresql").connect(t).prepareApp(t).ready(t).complete(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability); err != nil {
		t.Fatal(err)
	}
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "connected_backup", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal("connected source backup", err)
	}
	a, p := claimBackup(t, f.networkCaptureFixture, b.ID)
	if p.SourcePlan.Database.Network == nil || p.SourcePlan.CompilerVersion != domain.HostingDatabaseNetworkCompilerVersion {
		t.Fatal("connected source lost private network plan")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded domain.HostingDatabaseBackupPlan
	if domain.DecodeHostingDatabaseBackupJSON(raw, &decoded) != nil || decoded.Validate() != nil || decoded.Hash != p.Hash {
		t.Fatal("private connected backup plan cannot survive transport")
	}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "connected_intent", PlanHash: p.Hash, Phase: "intent"}); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresDatabaseBackupRejectsRedis(t *testing.T) {
	f := newNetworkCaptureFixture(t, "redis")
	if _, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "redis_backup", ExpectedResourceGeneration: 1}); err == nil {
		t.Fatal("unsupported engine accepted")
	}
	var count int
	if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unsupported backup persisted", err)
	}
}

func prepareBackupRestoreChild(t *testing.T, f networkCaptureFixture, restored domain.HostingDatabaseBackupOperation, target domain.HostingDatabase) (domain.HostingOperation, domain.HostingStorage) {
	t.Helper()
	var err error
	var child string
	if err = f.db.QueryRow(f.ctx, `SELECT deployment_operation_id FROM hosting_database_backups WHERE id=$1`, restored.ID).Scan(&child); err != nil {
		t.Fatal(err)
	}
	op, err := f.pg.GetHostingOperation(f.ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(f.db).ReadHostingStorage(f.ctx, target.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	tf := f.storageMaterializationFixture
	tf.resource = target.Resource
	tf.storage = domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	tf = prepareDatabaseExecution(t, tf, op)
	ack, err := tf.pg.HostingStorageCreateBegin(f.ctx, f.runner, tf.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tf.pg.HostingStorageMaterialized(f.ctx, f.runner, tf.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = tf.pg.HostingApplyBegin(f.ctx, f.runner, tf.authority); err != nil {
		t.Fatal(err)
	}
	result := domain.HostingCompleteRequest{HostingAuthority: tf.authority, ReceiptID: "restore_target_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("b", 64), ImageID: tf.op.Release.ImageID, DaemonID: tf.op.Release.DaemonID, PlanHash: tf.op.Plan.Hash}}
	settled, err := tf.pg.HostingComplete(f.ctx, f.runner, result)
	if err != nil {
		t.Fatal("private target child completion", err)
	}
	return settled, tf.storage
}

func TestPostgresDatabaseBackupUncertainRestoreDoesNotOwnSource(t *testing.T) {
	f := backupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "source_live", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	b = completeBackup(t, f, b.ID)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, b.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "held_restore", Name: "Held target"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	prepareBackupRestoreChild(t, f, restored, target)
	a, p := claimBackup(t, f, restored.ID)
	in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "restore_intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	in.Phase = "uncertain"
	in.ReceiptID = "restore_uncertain"
	in.FailureCode = "interrupted"
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("source_live_during_uncertain_restore")); err != nil {
		t.Fatal("uncertain restore blocked independent source maintenance", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("blocked_uncertain_target")); err == nil {
		t.Fatal("uncertain target allowed deployment")
	}
	application, err := f.pg.GetResource(f.ctx, "resource_image_web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.PutHostingDatabaseBinding(f.ctx, "usr_bootstrap", application.ID, domain.HostingDatabaseBindingInput{DatabaseResourceID: target.Resource.ID, ExpectedResourceGeneration: application.Generation, ExpectedDatabaseGeneration: 1, RequestID: strings.Repeat("f", 32)}, destinationEvent("blocked_uncertain_connection")); err == nil {
		t.Fatal("uncertain target allowed application connection")
	}
	var bindings int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_bindings WHERE database_resource_id=$1`, target.Resource.ID).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatal("denied connection left binding", err)
	}
}

func TestPostgresDatabaseBackupFailedRestorePreflightRemainsPrivate(t *testing.T) {
	f := backupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "preflight_source", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	b = completeBackup(t, f, b.ID)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, b.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "preflight_failure", Name: "Failed private target"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	prepareBackupRestoreChild(t, f, restored, target)
	a, p := claimBackup(t, f, restored.ID)
	out, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "preflight_failed", PlanHash: p.Hash, Phase: "failed", FailureCode: "execution_failed"})
	if err != nil || out.State != "failed" || !out.OwnershipHeld {
		t.Fatal("failed restore exposed empty target", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("failed_target_is_private")); err == nil {
		t.Fatal("failed restore target admitted deployment")
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("failed_restore_source_is_free")); err != nil {
		t.Fatal("failed restore held source", err)
	}
}

func TestPostgresDatabaseBackupSavedLimitsSurviveNewOperatorPolicy(t *testing.T) {
	f := backupFixture(t)
	b, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "saved_limits", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b.MaxBytes != domain.HostingDatabaseBackupMaxBytes || b.TimeoutSeconds != 900 {
		t.Fatal("default policy not recorded")
	}
	if err = f.pg.ConfigureDatabaseBackupLimits(domain.HostingDatabaseBackupLimits{MaxBytes: 65537, TimeoutSeconds: 3}); err == nil {
		t.Fatal("live queue configuration changed")
	}
	replacement, err := store.OpenPostgres(f.ctx, f.db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if err = replacement.ConfigureDatabaseBackupLimits(domain.HostingDatabaseBackupLimits{MaxBytes: 65537, TimeoutSeconds: 3}); err != nil {
		t.Fatal(err)
	}
	read, err := replacement.GetDatabaseBackup(f.ctx, "usr_bootstrap", b.ID)
	if err != nil || read.MaxBytes != b.MaxBytes || read.TimeoutSeconds != b.TimeoutSeconds {
		t.Fatal("new configuration rewrote an admitted limit", err)
	}
}

func TestPostgresDatabaseBackupHeldRestoreTarget(t *testing.T) {
	for _, phase := range []string{"waiting_target", "applying", "failed", "uncertain", "succeeded"} {
		t.Run(phase, func(t *testing.T) {
			f := backupFixture(t)
			original, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "original", ExpectedResourceGeneration: 1})
			if err != nil {
				t.Fatal(err)
			}
			original = completeBackup(t, f, original.ID)
			restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", Name: "Held restore target"})
			if err != nil {
				t.Fatal(err)
			}
			target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
			if err != nil {
				t.Fatal(err)
			}
			prepareBackupRestoreChild(t, f, restored, target)
			if phase != "waiting_target" {
				a, p := claimBackup(t, f, restored.ID)
				in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "restore_intent", PlanHash: p.Hash, Phase: "intent"}
				if phase != "failed" {
					if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
						t.Fatal(err)
					}
				}
				switch phase {
				case "failed":
					in.Phase = "failed"
					in.FailureCode = "execution_failed"
				case "uncertain":
					in.Phase = "uncertain"
					in.FailureCode = "interrupted"
				case "succeeded":
					in.Phase = "complete"
					in.Blob = original.Blob
				}
				if phase != "applying" {
					in.ReceiptID = "restore_" + phase
					if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, in); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before int
			if f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&before) != nil {
				t.Fatal("count")
			}
			_, err = f.service.RequestDatabaseBackup(f.ctx, target.Resource.ID, domain.HostingDatabaseBackupInput{RequestID: "target_backup", ExpectedResourceGeneration: 1})
			if phase == "succeeded" {
				if err != nil {
					t.Fatal("settled restored database cannot be backed up", err)
				}
			} else {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("restore-held target backup accepted phase=%s err=%v", phase, err)
				}
				var after int
				if f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&after) != nil || after != before {
					t.Fatal("denied backup admitted rows")
				}
				replay, e := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "original", ExpectedResourceGeneration: 1})
				if e != nil || replay.ID != original.ID {
					t.Fatal("original accepted backup replay blocked", e)
				}
				_, e = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "live_source", ExpectedResourceGeneration: 1})
				if e != nil {
					t.Fatal("held restore incorrectly owns original source", e)
				}
			}
		})
	}
}

func TestPostgresDatabaseBackupHeldTargetDatabaseBoundarySerializes(t *testing.T) {
	f := backupFixture(t)
	original, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "original", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	original = completeBackup(t, f, original.ID)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, original.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", Name: "Held target"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	child, storage := prepareBackupRestoreChild(t, f, restored, target)
	var raw []byte
	var healthy domain.HostingHealthy
	if f.db.QueryRow(f.ctx, `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, target.Binding.ID).Scan(&raw) != nil || json.Unmarshal(raw, &healthy) != nil {
		t.Fatal("exact target healthy")
	}
	now := time.Now().UTC()
	limits := domain.DefaultHostingDatabaseBackupLimits()
	p := domain.HostingDatabaseBackupPlan{Operation: domain.HostingDatabaseBackupOperation{ID: "direct_target_backup", Version: domain.HostingDatabaseBackupVersion, ProjectID: target.Resource.ProjectID, SourceResourceID: target.Resource.ID, SourceGeneration: 1, BackupID: "direct_target_backup", Mode: "backup", State: "queued", OwnershipHeld: true, Consistency: "logical_snapshot_source_writable", MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds, CreatedAt: now, UpdatedAt: now}, ActorID: target.Resource.CreatedBy, RunnerID: f.runner, Source: healthy, SourcePlan: *child.Plan, SourceStorage: storage, Credentials: child.Plan.Database.Credentials, LocationID: "direct_target_backup", MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid exact direct admission fixture")
	}
	body, _ := json.Marshal(p)
	insert := `INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,runner_id,plan,plan_hash) VALUES($1,$2,$3,$4,1,$1,'backup','direct_request',$5,$6,'queued',$7,$8,$9)`
	args := []any{p.Operation.ID, p.Operation.ProjectID, p.Operation.SourceResourceID, p.Source.Snapshot.BindingID, domain.HostingDocumentHash(struct {
		Actor string
		Input domain.HostingDatabaseBackupInput
	}{p.ActorID, domain.HostingDatabaseBackupInput{RequestID: "direct_request", ExpectedResourceGeneration: 1}}), p.ActorID, p.RunnerID, body, p.Hash}
	locker, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locker.Rollback(f.ctx) }()
	var pid int
	if locker.QueryRow(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid) != nil {
		t.Fatal("locker pid")
	}
	if _, err = locker.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, target.Binding.DestinationID); err != nil {
		t.Fatal(err)
	}
	joined := make(chan error, 1)
	insertCtx, stopInsert := context.WithTimeout(f.ctx, 15*time.Second)
	received := false
	defer func() {
		stopInsert()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = locker.Rollback(cleanup)
		if !received {
			select {
			case <-joined:
			case <-cleanup.Done():
				t.Error("direct admission goroutine did not join")
			}
		}
	}()
	go func() { _, e := f.db.Exec(insertCtx, insert, args...); joined <- e }()
	blocked := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !blocked {
		select {
		case e := <-joined:
			received = true
			t.Fatalf("DB admission bypassed destination serialization: %v", e)
		case <-deadline.C:
			t.Fatal("direct admission did not wait on stable destination lock")
		case <-tick.C:
			if f.db.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked) != nil {
				t.Fatal("lock observation")
			}
		}
	}
	if err = locker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-joined:
		received = true
	case <-time.After(5 * time.Second):
		t.Fatal("direct admission did not join")
	}
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" || !strings.Contains(pgerr.Message, "binding role already owned") {
		t.Fatalf("DB boundary accepted cross-role ownership: %v", err)
	}
	// The exact same complete input becomes legal only after the restore receipt
	// atomically releases its target. This distinguishes the role guard from a
	// malformed test plan or a generally unusable restored database.
	completeBackup(t, f, restored.ID)
	if _, err = f.db.Exec(f.ctx, insert, args...); err != nil {
		t.Fatal("settled target DB admission rejected", err)
	}
	a, plan := claimBackup(t, f, p.Operation.ID)
	// Controlled private-schema corruption models a row admitted by the prior
	// unqualified draft. Re-enable its guard before testing every execution read.
	tx, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err = tx.Exec(f.ctx, `ALTER TABLE hosting_database_backups DISABLE TRIGGER hosting_database_backup_admission`); err != nil {
		t.Fatal(err)
	}
	var parent domain.HostingDatabaseBackupPlan
	var parentRaw []byte
	if tx.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backups WHERE id=$1`, restored.ID).Scan(&parentRaw) != nil || json.Unmarshal(parentRaw, &parent) != nil {
		t.Fatal("parent plan")
	}
	parent.Operation.ID = "conflicting_restore"
	parent.Operation.State = "waiting_target"
	parent.Operation.OwnershipHeld = true
	parent.Hash = ""
	parent.Hash = domain.HostingDocumentHash(parent)
	parentRaw, _ = json.Marshal(parent)
	_, err = tx.Exec(f.ctx, `INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,target_resource_id,target_binding_id,deployment_operation_id,runner_id,plan,plan_hash,blob) SELECT 'conflicting_restore',project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,'conflict',request_hash,created_by,'waiting_target',target_resource_id,target_binding_id,deployment_operation_id,runner_id,$2,$3,blob FROM hosting_database_backups WHERE id=$1`, restored.ID, parentRaw, parent.Hash)
	if err != nil {
		t.Fatal("private conflicting fixture", err)
	}
	if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal("conflicting fixture integrity", err)
	}
	if _, err = tx.Exec(f.ctx, `ALTER TABLE hosting_database_backups ENABLE TRIGGER hosting_database_backup_admission`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	replay, e := f.service.RequestDatabaseBackup(f.ctx, target.Resource.ID, domain.HostingDatabaseBackupInput{RequestID: "direct_request", ExpectedResourceGeneration: 1})
	if e != nil || replay.ID != p.Operation.ID {
		t.Fatal("accepted request replay was changed by later ownership", e)
	}
	if _, err = f.pg.DatabaseBackupPlan(f.ctx, f.runner, a); !errors.Is(err, store.ErrConflict) {
		t.Fatal("current plan skipped target ownership", err)
	}
	intent := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "denied_intent", PlanHash: plan.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, intent); !errors.Is(err, store.ErrConflict) {
		t.Fatal("current intent skipped target ownership", err)
	}
	var receipts int
	if f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1`, a.OperationID).Scan(&receipts) != nil || receipts != 0 {
		t.Fatal("denied effect recorded a receipt")
	}
}
