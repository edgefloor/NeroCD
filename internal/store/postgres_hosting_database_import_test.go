package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"strings"
	"sync"
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
	concurrent := func(left, right func(context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
		defer cancel()
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, action := range []func(context.Context) error{left, right} {
			go func() { <-start; results <- action(ctx) }()
		}
		close(start)
		for range 2 {
			if e := <-results; e != nil {
				t.Fatal("concurrent replay/authority failed", e)
			}
		}
	}
	stageReplay := func(ctx context.Context) error {
		_, e := f.service.RequestDatabaseImportStage(ctx, f.target.Resource.ID, in)
		return e
	}
	concurrent(stageReplay, func(ctx context.Context) error { _, e := f.pg.DatabaseImportStagePlan(ctx, runner, a); return e })
	intent := domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "stage_intent", PlanHash: p.Hash, Phase: "stage_intent", DaemonID: p.Target.Release.DaemonID}
	if intent.Validate() != nil {
		t.Fatal("invalid stage intent fixture")
	}
	concurrent(stageReplay, func(ctx context.Context) error { _, e := f.pg.DatabaseImportMutation(ctx, runner, intent); return e })
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
	importReplay := func(ctx context.Context) error {
		_, e := f.service.RequestDatabaseImport(ctx, f.target.Resource.ID, confirm)
		return e
	}
	concurrent(importReplay, func(ctx context.Context) error { _, e := f.pg.DatabaseImportPlan(ctx, runner, a); return e })
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
	concurrent(importReplay, func(ctx context.Context) error { _, e := f.pg.DatabaseImportMutation(ctx, runner, mutation); return e })
	mutation.Phase = "complete"
	mutation.ReceiptID = "import_complete"
	mutation.WritersResumed = true
	var result domain.HostingDatabaseImportReceiptResult
	concurrent(func(ctx context.Context) error {
		var e error
		result, e = f.pg.DatabaseImportMutation(ctx, runner, mutation)
		return e
	}, func(ctx context.Context) error { return f.pg.ExpireLeases(ctx, time.Now()) })
	err = nil
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

func historicalExternalStageFixture(t *testing.T, until time.Duration) (composeMySQLBackupFixture, domain.HostingDatabaseImportStagePlan, domain.HostingDatabaseImportReadyReceipt) {
	t.Helper()
	f := newComposeMySQLBackupFixture(t)
	runner := f.targetOp.Snapshot.RunnerID
	caps := []string{domain.HostingDatabaseImportStageCapability, domain.HostingDatabaseImportCapability, domain.RunTypeDatabaseImportStage, domain.RunTypeDatabaseImport, domain.HostingDatabaseMySQLCapability, domain.HostingRuntimeEnvironmentCapability}
	if _, e := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$1::text[]`, caps); e != nil {
		t.Fatal(e)
	}
	_, target := remoteBackupFixtureFor(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: f.pg, db: f.db, resource: f.source.Resource, runner: runner, op: f.sourceOp}, service: f.service, ctx: f.ctx})
	var healthy domain.HostingHealthy
	var snapshot domain.HostingDatabaseBackupTargetSnapshot
	if e := f.db.QueryRow(f.ctx, `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, f.targetOp.Snapshot.BindingID).Scan(&healthy); e != nil {
		t.Fatal(e)
	}
	if e := f.db.QueryRow(f.ctx, `SELECT snapshot FROM hosting_database_backup_target_versions WHERE target_id=$1 AND revision=$2`, target.ID, target.Revision).Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	expires := time.Now().UTC().Add(until)
	created := expires.Add(-2 * time.Hour)
	stage := domain.HostingDatabaseImportStage{ID: "historical_stage", Version: domain.HostingDatabaseImportStageVersion, TargetResourceID: f.target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: healthy.OperationID, ExpectedResourceGeneration: f.target.Resource.Generation, S3Source: domain.HostingDatabaseImportS3Source{TargetID: target.ID, Revision: target.Revision, ObjectKey: "literal.sql"}, State: "queued", CreatedAt: created, UpdatedAt: created}
	p := domain.HostingDatabaseImportStagePlan{Stage: stage, ActorID: "usr_bootstrap", ProjectID: f.target.Resource.ProjectID, RunnerID: runner, Target: healthy, TargetPlan: *f.targetOp.Plan, S3Target: snapshot, MaxBytes: domain.HostingDatabaseImportMaxBytes, TimeoutSeconds: 3600}
	p.Hash = domain.HostingDocumentHash(p)
	r := domain.HostingDatabaseImportReadyReceipt{StageID: stage.ID, PlanHash: p.Hash, SourceBytes: 32, Compression: "plain", Blob: domain.HostingDatabaseImportBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 32, Format: "mysql_sql"}, File: domain.HostingDatabaseImportStageFile{Device: 1, Inode: 2, Bytes: 32}, ReadyAt: expires.Add(-time.Hour), ExpiresAt: expires}
	r.Hash = domain.HostingDocumentHash(r)
	view, e := r.StageView(p)
	if e != nil {
		t.Fatal("historical receipt fixture", e)
	}
	raw := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	_, e = f.db.Exec(f.ctx, `INSERT INTO hosting_database_import_stages(id,project_id,target_resource_id,target_binding_id,actor_id,request_id,request_hash,runner_id,daemon_id,plan,plan_hash,body,ready_receipt,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, stage.ID, p.ProjectID, stage.TargetResourceID, healthy.Snapshot.BindingID, p.ActorID, "historical_request", "sha256:"+strings.Repeat("b", 64), runner, healthy.Release.DaemonID, raw(p), p.Hash, raw(view), raw(r), created, r.ReadyAt)
	if e != nil {
		t.Fatal("saved historical stage", e)
	}
	return f, p, r
}
func historicalImportConfirmation(p domain.HostingDatabaseImportStagePlan, r domain.HostingDatabaseImportReadyReceipt) domain.HostingDatabaseImportInput {
	return domain.HostingDatabaseImportInput{RequestID: "confirm_historical", StageID: p.Stage.ID, ExpectedStageHash: r.Hash, ComponentTarget: domain.HostingDatabaseBackupComponentTarget{ResourceID: p.Stage.TargetResourceID, Component: "mysql", ExpectedDeployedOperationID: p.Target.OperationID, ExpectedResourceGeneration: p.Stage.ExpectedResourceGeneration, AcknowledgeReplacement: true}}
}

func TestPostgresExternalStageConsumedReservationSurvivesExpiry(t *testing.T) {
	f, p, r := historicalExternalStageFixture(t, 3*time.Second)
	in := historicalImportConfirmation(p, r)
	operation, e := f.service.RequestDatabaseImport(f.ctx, p.Stage.TargetResourceID, in)
	if e != nil {
		t.Fatal("consume before deadline", e)
	}
	timer := time.NewTimer(time.Until(r.ExpiresAt) + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		t.Fatal("expiry fixture cancelled")
	}
	cleanup := domain.HostingDatabaseImportStageCleanup{StageID: p.Stage.ID, PlanHash: p.Hash, ReadyHash: r.Hash}
	decision, e := f.pg.AuthorizeDatabaseImportStageCleanup(f.ctx, p.RunnerID, cleanup)
	if e != nil || decision.Delete {
		t.Fatal("queued reservation lost to elapsed stage expiry", e)
	}
	claim, e := f.pg.ClaimRun(f.ctx, p.RunnerID, time.Now(), time.Minute)
	if e != nil || claim.Run.RunSpec.HostingOperationID != operation.ID {
		t.Fatal("first import claim after ready expiry", e)
	}
	a := domain.HostingAuthority{OperationID: operation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, e := f.pg.DatabaseImportPlan(f.ctx, p.RunnerID, a)
	if e != nil || plan.Validate() != nil || !plan.Receipt.ExpiresAt.Before(time.Now()) {
		t.Fatal("admitted immutable consumption expired in queue", e)
	}
	if _, e = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked'`); e != nil {
		t.Fatal(e)
	}
	if decision, e = f.pg.AuthorizeDatabaseImportStageCleanup(f.ctx, p.RunnerID, cleanup); e != nil || decision.Delete {
		t.Fatal("consumed preservation required live S3", e)
	}
}

func TestPostgresExternalStageCleanupAndConfirmationSerialize(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "consume wins", true: "expiry wins"}[expired], func(t *testing.T) {
			until := time.Minute
			if expired {
				until = -time.Minute
			}
			f, p, r := historicalExternalStageFixture(t, until)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var operation domain.HostingDatabaseImportOperation
			var admission error
			var decision domain.HostingDatabaseImportStageCleanupResult
			var cleanup error
			go func() {
				defer wg.Done()
				<-start
				operation, admission = f.service.RequestDatabaseImport(ctx, p.Stage.TargetResourceID, historicalImportConfirmation(p, r))
			}()
			go func() {
				defer wg.Done()
				<-start
				decision, cleanup = f.pg.AuthorizeDatabaseImportStageCleanup(ctx, p.RunnerID, domain.HostingDatabaseImportStageCleanup{StageID: p.Stage.ID, PlanHash: p.Hash, ReadyHash: r.Hash})
			}()
			close(start)
			wg.Wait()
			if ctx.Err() != nil || cleanup != nil {
				t.Fatal("cleanup/confirmation lock or authority failure", cleanup)
			}
			if expired {
				if admission == nil || !decision.Delete {
					t.Fatal("expired input was consumed or not retired")
				}
			} else if admission != nil || operation.State != "queued" || decision.Delete {
				t.Fatal("unexpired consumption was deleted", admission)
			}
			again, e := f.pg.AuthorizeDatabaseImportStageCleanup(ctx, p.RunnerID, decision.HostingDatabaseImportStageCleanup)
			if e != nil || again.Delete != decision.Delete {
				t.Fatal("cleanup authorization replay changed", e)
			}
			mismatch := decision.HostingDatabaseImportStageCleanup
			mismatch.PlanHash = "sha256:" + strings.Repeat("f", 64)
			if wrong, e := f.pg.AuthorizeDatabaseImportStageCleanup(ctx, p.RunnerID, mismatch); e == nil || wrong.Delete {
				t.Fatal("foreign plan authorized deletion")
			}
		})
	}
}

func TestPostgresExternalImportExpiryAndReceiptSerialize(t *testing.T) {
	f, p, r := historicalExternalStageFixture(t, time.Minute)
	op, e := f.service.RequestDatabaseImport(f.ctx, p.Stage.TargetResourceID, historicalImportConfirmation(p, r))
	if e != nil {
		t.Fatal(e)
	}
	claim, e := f.pg.ClaimRun(f.ctx, p.RunnerID, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, e := f.pg.DatabaseImportPlan(f.ctx, p.RunnerID, a)
	if e != nil {
		t.Fatal(e)
	}
	var writers []domain.HostingStorageArchiveWriterState
	for _, c := range plan.StagePlan.Target.Stack.Components {
		if c.Component != "mysql" {
			writers = append(writers, domain.HostingStorageArchiveWriterState{ContainerID: c.ContainerID, OperationID: c.OperationID, ImageID: c.ImageID, PlanHash: plan.StagePlan.Target.PlanHash, ReleaseHash: plan.StagePlan.Target.Release.Hash, Running: true})
		}
	}
	receipt := domain.HostingDatabaseImportMutation{HostingAuthority: a, ReceiptID: "expired_intent", PlanHash: plan.Hash, Phase: "import_intent", DaemonID: plan.StagePlan.Target.Release.DaemonID, Writers: writers}
	if receipt.Validate() != nil {
		t.Fatal("invalid expiry receipt fixture")
	}
	if _, e = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- f.pg.ExpireLeases(ctx, time.Now()) }()
	go func() {
		<-start
		_, e := f.pg.DatabaseImportMutation(ctx, p.RunnerID, receipt)
		if !errors.Is(e, store.ErrNotFound) {
			results <- errors.New("expired receipt did not fail closed")
			return
		}
		results <- nil
	}()
	close(start)
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal("expiry/receipt lock failure", e)
		}
	}
	var count int
	if e = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_database_import_receipts WHERE operation_id=$1`, op.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired receipt persisted", e)
	}
}
