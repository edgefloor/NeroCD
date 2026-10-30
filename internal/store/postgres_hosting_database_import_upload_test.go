package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"sync"
	"testing"
	"time"
)

func TestPostgresLocalImportMetadataAndAtomicBody(t *testing.T) {
	f := newComposeMySQLBackupFixture(t)
	runner := f.targetOp.Snapshot.RunnerID
	in := domain.HostingDatabaseImportUploadInput{RequestID: "local_metadata", Component: "mysql", ExpectedDeployedOperationID: f.targetOp.ID, ExpectedResourceGeneration: f.target.Resource.Generation, Filename: "display.sql", SourceBytes: 32}
	if in.Validate() != nil {
		t.Fatal("invalid local fixture")
	}
	if _, err := f.service.RequestDatabaseImportUploadStage(f.ctx, f.target.Resource.ID, in); err == nil {
		t.Fatal("missing upload capability admitted")
	}
	caps := []string{domain.HostingDatabaseImportUploadCapability, domain.HostingDatabaseImportCapability, domain.RunTypeDatabaseImportStage, domain.RunTypeDatabaseImport, domain.HostingDatabaseMySQLCapability, domain.HostingRuntimeEnvironmentCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$1::text[] WHERE id=$2`, caps, runner); err != nil {
		t.Fatal(err)
	}
	stage, err := f.service.RequestDatabaseImportUploadStage(f.ctx, f.target.Resource.ID, in)
	if err != nil {
		t.Fatal("local metadata without any S3 target", err)
	}
	replay, err := f.service.RequestDatabaseImportUploadStage(f.ctx, f.target.Resource.ID, in)
	if err != nil || replay.ID != stage.ID || replay.UploadHash != stage.UploadHash {
		t.Fatal("metadata replay", err)
	}
	var raw []byte
	var runID *string
	if err = f.db.QueryRow(f.ctx, `SELECT plan,run_id FROM hosting_database_import_stages WHERE id=$1`, stage.ID).Scan(&raw, &runID); err != nil {
		t.Fatal(err)
	}
	if runID != nil {
		t.Fatal("metadata prematurely dispatched")
	}
	var p domain.HostingDatabaseImportStagePlan
	if json.Unmarshal(raw, &p) != nil || p.Validate() != nil {
		t.Fatal("invalid local plan")
	}
	if _, _, _, err = f.pg.DatabaseImportUpload(f.ctx, p.ActorID, stage.ID, "sha256:wrong", false); !errors.Is(err, store.ErrDatabaseImportUploadHash) {
		t.Fatal("hash mismatch accepted", err)
	}
	changed := in
	changed.SourceBytes++
	if _, err = f.service.RequestDatabaseImportUploadStage(f.ctx, f.target.Resource.ID, changed); err == nil {
		t.Fatal("request identity changed")
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	failures := make(chan error, 2)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for range 2 {
		wg.Go(func() {
			_, _, attempted, e := f.pg.DatabaseImportUpload(ctx, p.ActorID, stage.ID, stage.UploadHash, true)
			results <- attempted
			failures <- e
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	first := 0
	for attempted := range results {
		if !attempted {
			first++
		}
	}
	for e := range failures {
		if e != nil {
			t.Fatal("concurrent body admission", e)
		}
	}
	if first != 1 {
		t.Fatal("more than one first body admitted")
	}
	var tasks int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM task_runs WHERE id=$1`, "db_import_stage_"+stage.ID).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatal("body dispatch count", err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != "db_import_stage_"+stage.ID {
		t.Fatal("local stage claim", err)
	}
	authority := domain.HostingAuthority{OperationID: stage.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	got, err := f.pg.DatabaseImportStagePlan(f.ctx, runner, authority)
	if err != nil || got.Hash != p.Hash || got.Stage.Version != domain.HostingDatabaseImportUploadVersion {
		t.Fatal("local stage authority", err)
	}
	if err = f.pg.RevalidateDatabaseImportUpload(f.ctx, p.ActorID, p); err != nil {
		t.Fatal("live local authority", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1) WHERE id=$2`, domain.HostingDatabaseImportUploadCapability, runner); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.RevalidateDatabaseImportUpload(f.ctx, p.ActorID, p); err == nil {
		t.Fatal("revoked capability survived active stream")
	}
	// An undispatched stage expires without acquiring any target mutation ownership.
	expired := p
	expired.Stage.ID = "expired_local_metadata"
	expired.Stage.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	expired.Stage.UpdatedAt = expired.Stage.CreatedAt
	past := expired.Stage.CreatedAt.Add(time.Hour)
	expired.Stage.UploadDeadline = &past
	expired.Stage.UploadHash = expired.UploadIdentityHash()
	expired.Hash = ""
	expired.Hash = domain.HostingDocumentHash(expired)
	if expired.Validate() != nil {
		t.Fatal("invalid expired metadata fixture")
	}
	planRaw, _ := json.Marshal(expired)
	viewRaw, _ := json.Marshal(expired.Stage)
	_, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_import_stages(id,project_id,target_resource_id,target_binding_id,actor_id,request_id,request_hash,runner_id,daemon_id,plan,plan_hash,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, expired.Stage.ID, expired.ProjectID, expired.Stage.TargetResourceID, expired.Target.Snapshot.BindingID, expired.ActorID, "expired_request", expired.Hash, runner, expired.Target.Release.DaemonID, planRaw, expired.Hash, viewRaw)
	if err != nil {
		t.Fatal("expired metadata insert", err)
	}
	view, err := f.pg.GetDatabaseImportStage(f.ctx, p.ActorID, expired.Stage.ID)
	if err != nil || view.State != "failed" || view.FailureCode != "stage_expired" {
		t.Fatal("undispatched expiry", err)
	}
	if _, _, _, err = f.pg.DatabaseImportUpload(f.ctx, p.ActorID, expired.Stage.ID, expired.Stage.UploadHash, true); err == nil {
		t.Fatal("expired metadata admitted bytes")
	}
	// The committed metadata remains readable even when current execution is unavailable.
	history, err := f.service.RequestDatabaseImportUploadStage(f.ctx, f.target.Resource.ID, in)
	if err != nil || history.ID != stage.ID {
		t.Fatal("historical metadata replay lost", err)
	}
}
