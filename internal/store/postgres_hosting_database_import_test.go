package store_test

import (
	"encoding/json"
	"nerocd/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestPostgresExternalMySQLStageAndSingleConsumption(t *testing.T) {
	f := newComposeMySQLBackupFixture(t)
	runner := f.sourceOp.Snapshot.RunnerID
	caps := []string{domain.HostingDatabaseImportStageCapability, domain.HostingDatabaseImportCapability, domain.RunTypeDatabaseImportStage, domain.RunTypeDatabaseImport, domain.HostingDatabaseMySQLCapability, domain.HostingRuntimeEnvironmentCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$1::text[]`, caps); err != nil {
		t.Fatal(err)
	}
	_, target := remoteBackupFixtureFor(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: f.pg, db: f.db, resource: f.source.Resource, runner: runner, op: f.sourceOp}, service: f.service, ctx: f.ctx})
	in := domain.HostingDatabaseImportStageInput{RequestID: "external_inspect", Component: "mysql", ExpectedDeployedOperationID: f.targetOp.ID, ExpectedResourceGeneration: f.target.Resource.Generation, S3Source: domain.HostingDatabaseImportS3Source{TargetID: target.ID, Revision: target.Revision, ObjectKey: "nerocd/operator-import/plain.sql"}}
	if in.Validate() != nil {
		t.Fatal("invalid stage fixture")
	}
	stage, err := f.service.RequestDatabaseImportStage(f.ctx, f.target.Resource.ID, in)
	if err != nil {
		t.Fatal("external stage admission", err)
	}
	replay, err := f.service.RequestDatabaseImportStage(f.ctx, f.target.Resource.ID, in)
	if err != nil || replay.ID != stage.ID {
		t.Fatal("stage replay", err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseImportStage {
		t.Fatal("stage claim", err)
	}
	a := domain.HostingAuthority{OperationID: stage.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, err := f.pg.DatabaseImportStagePlan(f.ctx, runner, a)
	if err != nil || p.Validate() != nil {
		t.Fatal("stage plan", err)
	}
	intent := domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "stage_intent", PlanHash: p.Hash, Phase: "stage_intent", DaemonID: p.Target.Release.DaemonID}
	if intent.Validate() != nil {
		t.Fatal("invalid stage intent fixture")
	}
	if _, err = f.pg.DatabaseImportMutation(f.ctx, runner, intent); err != nil {
		t.Fatal("stage intent", err)
	}
	now := time.Now().UTC()
	r := domain.HostingDatabaseImportReadyReceipt{StageID: stage.ID, PlanHash: p.Hash, SourceBytes: 32, Compression: "plain", Blob: domain.HostingDatabaseImportBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 32, Format: "mysql_sql"}, File: domain.HostingDatabaseImportStageFile{Device: 1, Inode: 2, Bytes: 32}, ReadyAt: now, ExpiresAt: now.Add(time.Hour)}
	r.Hash = domain.HostingDocumentHash(r)
	if r.ValidateFor(p) != nil {
		t.Fatal("invalid ready fixture")
	}
	ready := intent
	ready.ReceiptID = "stage_ready"
	ready.Phase = "ready"
	ready.Ready = &r
	saved, err := f.pg.DatabaseImportMutation(f.ctx, runner, ready)
	if err != nil || saved.Stage == nil || saved.Stage.State != "ready" {
		t.Fatal("stage ready", err)
	}
	confirm := domain.HostingDatabaseImportInput{RequestID: "external_confirm", StageID: stage.ID, ExpectedStageHash: r.Hash, ComponentTarget: domain.HostingDatabaseBackupComponentTarget{ResourceID: f.target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: f.targetOp.ID, ExpectedResourceGeneration: f.target.Resource.Generation, AcknowledgeReplacement: true}}
	if confirm.Validate() != nil {
		t.Fatal("invalid confirmation fixture")
	}
	operation, err := f.service.RequestDatabaseImport(f.ctx, f.target.Resource.ID, confirm)
	if err != nil {
		t.Fatal("single stage consumption", err)
	}
	again, err := f.service.RequestDatabaseImport(f.ctx, f.target.Resource.ID, confirm)
	if err != nil || again.ID != operation.ID {
		t.Fatal("confirmed operation replay", err)
	}
	other := confirm
	other.RequestID = "second_confirmation"
	if _, err = f.service.RequestDatabaseImport(f.ctx, f.target.Resource.ID, other); err == nil {
		t.Fatal("stage consumed twice")
	}
	claim, err = f.pg.ClaimRun(f.ctx, runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseImport {
		t.Fatal("import claim", err)
	}
	a = domain.HostingAuthority{OperationID: operation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	ip, err := f.pg.DatabaseImportPlan(f.ctx, runner, a)
	if err != nil || ip.Validate() != nil {
		t.Fatal("import plan", err)
	}
	var writers []domain.HostingStorageArchiveWriterState
	for _, c := range ip.StagePlan.Target.Stack.Components {
		if c.Component != "mysql" {
			writers = append(writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ContainerID, OperationID: c.OperationID, ImageID: c.ImageID, PlanHash: ip.StagePlan.Target.PlanHash, ReleaseHash: ip.StagePlan.Target.Release.Hash, Running: true})
		}
	}
	mutation := domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "import_intent", PlanHash: ip.Hash, Phase: "import_intent", DaemonID: ip.StagePlan.Target.Release.DaemonID, Writers: writers}
	if mutation.Validate() != nil {
		t.Fatal("invalid import receipt fixture")
	}
	if _, err = f.pg.DatabaseImportMutation(f.ctx, runner, mutation); err != nil {
		t.Fatal("SQL intent", err)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "import_complete"
	mutation.WritersResumed = true
	result, err := f.pg.DatabaseImportMutation(f.ctx, runner, mutation)
	if err != nil || result.Import == nil || result.Import.State != "succeeded" || result.Import.OwnershipHeld {
		t.Fatal("SQL settlement", err)
	}
	// Returning committed facts never resolves credentials or reopens a stage.
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked'`); err != nil {
		t.Fatal(err)
	}
	if got, e := f.service.GetDatabaseImportStage(f.ctx, stage.ID); e != nil || got.State != "consumed" {
		t.Fatal("historical stage required live S3", e)
	}
	if got, e := f.service.GetDatabaseImport(f.ctx, operation.ID); e != nil || got.State != "succeeded" {
		t.Fatal("historical import required live S3", e)
	}
	if got, e := f.service.RequestDatabaseImport(f.ctx, f.target.Resource.ID, confirm); e != nil || got.ID != operation.ID {
		t.Fatal("committed replay required live S3", e)
	}
	if got, e := f.service.RequestDatabaseImportStage(f.ctx, f.target.Resource.ID, in); e != nil || got.ID != stage.ID {
		t.Fatal("committed inspection refetched", e)
	}
	var backups, imports, stages int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_imports),(SELECT count(*) FROM hosting_database_import_stages)`).Scan(&backups, &imports, &stages); err != nil || backups != 0 || imports != 1 || stages != 1 {
		t.Fatal("external input fabricated backup or duplicate work", err)
	}
	var raw []byte
	if err = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_imports WHERE id=$1`, operation.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored domain.HostingDatabaseImportPlan
	if json.Unmarshal(raw, &stored) != nil || stored.Hash != ip.Hash {
		t.Fatal("immutable import authority changed")
	}
}
