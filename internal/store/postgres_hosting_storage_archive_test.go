package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

type archiveControlFixture struct {
	pg                        *store.PostgresStore
	db                        *pgxpool.Pool
	service                   *app.Service
	ctx, owner, runnerContext context.Context
	runner                    string
	resource                  domain.Resource
	storage                   domain.HostingStorage
	source                    domain.HostingOperation
	helper                    domain.HostingStorageArchiveHelper
}

func newArchiveControlFixture(t *testing.T) archiveControlFixture {
	pg, db, resource, _ := hostingStorageFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	runner := "runner_owned_storage"
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=ARRAY['compose_deploy','image_deploy','image_release_ports_v1','hosting_owned_storage_v1','storage_archive','hosting_storage_archive_v1'] WHERE id=$1`, runner); err != nil {
		t.Fatal(err)
	}
	helper := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: "sha256:" + strings.Repeat("d", 64), ExecutableSHA256: "sha256:" + strings.Repeat("e", 64), SourceCommit: strings.Repeat("f", 40), OS: "linux", Architecture: "arm64", DaemonID: "daemon_archive_fixture"}
	helper.Hash = domain.HostingDocumentHash(helper)
	limits := domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 19, FileBytes: 1 << 18, Members: 1000, Depth: 32, PathBytes: 255}
	if err := pg.ConfigureStorageArchive(&helper, limits); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		changed, err := func() (bool, error) {
			tx, err := db.Begin(ctx)
			if err != nil {
				return false, err
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if e := tx.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error("queue migration rollback failed")
				}
			}()
			changed, err := hostingjobs.MigrateNextTx(ctx, tx)
			if err != nil {
				return false, err
			}
			return changed, tx.Commit(ctx)
		}()
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
		if i == 99 {
			t.Fatal("queue migration did not terminate")
		}
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = db.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stop(cleanup) != nil {
			t.Error("archive queue did not stop")
		}
		closePostgresStore(t, pg)
		db.Close()
		if !strings.HasPrefix(schema, "nerocd_owned_storage_") {
			t.Error("archive cleanup schema ownership mismatch")
			return
		}
		admin, e := pgx.Connect(cleanup, os.Getenv("NEROCD_TEST_DATABASE_URL"))
		if e != nil {
			t.Error("archive schema cleanup connection unavailable")
			return
		}
		defer func() {
			fresh, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if admin.Close(fresh) != nil {
				t.Error("archive schema cleanup close failed")
			}
		}()
		if _, e = admin.Exec(cleanup, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); e != nil {
			t.Error("owned archive schema cleanup failed")
			return
		}
		var absent bool
		if admin.QueryRow(cleanup, `SELECT to_regnamespace($1) IS NULL`, schema).Scan(&absent) != nil || !absent {
			t.Error("owned archive schema absence not proven")
		}
	})
	storage := saveHostingStorage(t, pg, resource.ID, hostingStorageInput())
	input := hostingDeploymentRequest()
	input.HostPort = 0
	input.IdempotencyKey = "archive_source"
	op, err := pg.RequestHostingDeployment(ctx, "hop_archive_source", resource.ID, "usr_bootstrap", input, destinationEvent("archive_source"))
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, runner, a); err != nil {
		t.Fatal("source plan authority failed")
	}
	r := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: helper.DaemonID}
	r.Hash = domain.HostingDocumentHash(r)
	op, err = pg.HostingPrepared(ctx, runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "source_prepared", Release: r})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := pg.HostingStorageCreateBegin(ctx, runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: storage.Reference(), CreationNonce: op.Plan.Storage.CreationNonce})
	if err != nil {
		t.Fatal(err)
	}
	proof := archiveMaterializedProof(a, op.Plan.Hash, storage, ack.Materialization, "source_materialized")
	if _, err = pg.HostingStorageMaterialized(ctx, runner, proof); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = pg.HostingComplete(ctx, runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "source_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: r.ImageID, DaemonID: r.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	service := hostingDatabaseApplication(t, pg)
	owner := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	runnerCtx := auth.WithPrincipal(ctx, auth.Principal{ID: runner, Provider: domain.PrincipalRunner})
	return archiveControlFixture{pg: pg, db: db, service: service, ctx: ctx, owner: owner, runnerContext: runnerCtx, runner: runner, resource: resource, storage: storage, source: op, helper: helper}
}

// settleImage exercises the ordinary post-restore admission/receipt path. The
// runner evidence is simulated here; this is a controller test, not Docker proof.
func (f archiveControlFixture) settleImage(t *testing.T, op domain.HostingOperation, storage domain.HostingStorage, release domain.HostingRelease, container string) domain.HostingOperation {
	t.Helper()
	if err := f.pg.PrepareDeployment(f.ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("ordinary image claim did not match operation")
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingPrepared(f.ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: release})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: storage.Reference(), CreationNonce: op.Plan.Storage.CreationNonce})
	if err != nil || ack.CreatePermitted || ack.Materialization.CreationOperationID == op.ID {
		t.Fatal("ordinary image deployment replaced the immutable volume origin")
	}
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, archiveMaterializedProof(a, op.Plan.Hash, storage, ack.Materialization, "materialized_"+op.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingComplete(f.ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: container, ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func archiveMaterializedProof(a domain.HostingAuthority, hash string, st domain.HostingStorage, m domain.HostingStorageMaterialization, receipt string) domain.HostingStorageMaterializedRequest {
	return domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: receipt, PlanHash: hash, StorageRef: st.Reference(), CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: st.VolumeName, Driver: "local", Scope: "local", Labels: maps.Clone(m.Labels), Options: map[string]string{}}
}

func (f archiveControlFixture) backup(t *testing.T) domain.HostingStorageBackup {
	t.Helper()
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("1", 32), ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (f archiveControlFixture) claim(t *testing.T, id, mode string) (domain.HostingOperation, domain.HostingAuthority) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		op, err := f.pg.GetHostingOperation(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.RunID != "" {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("archive dispatch context ended")
		case <-deadline.C:
			t.Fatal("real River archive dispatch timed out")
		case <-ticker.C:
		}
	}
	claim, err := f.service.ClaimRun(f.runnerContext)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.RunSpec.Type != domain.RunTypeStorageArchive || claim.Run.RunSpec.HostingOperationID != id || claim.Run.RunSpec.HostingMode != mode || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 {
		t.Fatal("archive claim escaped dedicated primitive authority")
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err := f.service.RunnerStorageArchivePlan(f.runnerContext, a)
	if err != nil {
		t.Fatal(err)
	}
	if op.Snapshot != (domain.HostingSnapshot{}) || op.Plan != nil || op.Release != nil || op.Result != nil || op.StorageArchive == nil || op.TaskMode != mode {
		t.Fatal("archive response mixed application family")
	}
	op, err = f.service.RunnerStorageArchivePrepared(f.runnerContext, domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + id, PlanHash: op.StorageArchive.Plan.Hash, Helper: f.helper})
	if err != nil {
		t.Fatal(err)
	}
	prepared := domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + id, PlanHash: op.StorageArchive.Plan.Hash, Helper: f.helper}
	again, err := f.service.RunnerStorageArchivePrepared(f.runnerContext, prepared)
	if err != nil || again.StorageArchive.PreparedReceipt == nil || again.StorageArchive.PreparedReceipt.BodyHash != domain.HostingDocumentHash(prepared) {
		t.Fatal("exact prepared receipt replay lost full body identity")
	}
	changed := prepared
	changed.Helper.ImageID = "sha256:" + strings.Repeat("1", 64)
	changed.Helper.Hash = ""
	changed.Helper.Hash = domain.HostingDocumentHash(changed.Helper)
	if _, err = f.service.RunnerStorageArchivePrepared(f.runnerContext, changed); err == nil {
		t.Fatal("changed helper reused preparation approval")
	}
	return op, a
}
func (f archiveControlFixture) begin(t *testing.T, op domain.HostingOperation, a domain.HostingAuthority) domain.HostingOperation {
	t.Helper()
	result, err := f.service.RunnerStorageArchiveApplyBegin(f.runnerContext, domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "begin_" + op.ID, PlanHash: op.StorageArchive.Plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func archiveStates(p domain.HostingStorageArchivePlan, running bool) []domain.HostingStorageArchiveWriterState {
	var out []domain.HostingStorageArchiveWriterState
	for _, h := range p.Writers {
		out = append(out, domain.HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: running})
	}
	return out
}
func (f archiveControlFixture) progress(t *testing.T, op domain.HostingOperation, a domain.HostingAuthority, phase string, writers []domain.HostingStorageArchiveWriterState, blob *domain.HostingStorageArchiveBlob) domain.HostingOperation {
	t.Helper()
	in := domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "progress_" + phase + "_" + op.ID, PlanHash: op.StorageArchive.Plan.Hash, Phase: phase, Writers: writers, Blob: blob, RestoreVerified: phase == "restore_verified"}
	if phase == "helper_identified" {
		in.HelperContainerID = strings.Repeat("a", 64)
	}
	result, err := f.service.RunnerStorageArchiveProgress(f.runnerContext, in)
	if err != nil {
		t.Fatalf("phase %s rejected: %v", phase, err)
	}
	matched := false
	for _, e := range result.StorageArchive.Progress {
		if e.Phase == phase {
			matched = e.Receipt.BodyHash == domain.HostingDocumentHash(in)
		}
	}
	if !matched {
		t.Fatal("progress receipt omitted full request identity")
	}
	if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, in); err != nil {
		t.Fatal("exact progress response replay refused")
	}
	return result
}
func (f archiveControlFixture) finishBackup(t *testing.T, b domain.HostingStorageBackup) domain.HostingStorageBackup {
	t.Helper()
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "resume_intent", "resumed"} {
		var states []domain.HostingStorageArchiveWriterState
		var blob *domain.HostingStorageArchiveBlob
		switch phase {
		case "writer_snapshot", "stop_intent", "resume_intent", "resumed":
			states = archiveStates(p, true)
		case "stopped":
			states = archiveStates(p, false)
		}
		if phase == "archive_published" {
			blob = &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("9", 64), SizeBytes: 1536, Format: domain.HostingStorageArchiveFormat}
		}
		op = f.progress(t, op, a, phase, states, blob)
	}
	var blob *domain.HostingStorageArchiveBlob
	for _, phase := range op.StorageArchive.Progress {
		if phase.Phase == "archive_published" {
			blob = phase.Blob
		}
	}
	complete := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed", Blob: blob}}
	settled, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, complete)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != "succeeded" || settled.MutationHold || settled.Result != nil || settled.CompleteReceipt == nil {
		t.Fatal("backup did not settle independent safe outcome")
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal("historical lease expiry setup failed")
	}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, complete); err != nil {
		t.Fatal("historical completion ACK incorrectly required a live lease")
	}
	if settled.CompleteReceipt.BodyHash != domain.HostingDocumentHash(complete) {
		t.Fatal("completion receipt omitted full request identity")
	}
	if _, err = f.service.RunnerStorageArchivePlan(f.runnerContext, a); err == nil {
		t.Fatal("historical completion ACK revived live effect authority")
	}
	result, err := f.service.GetStorageBackup(f.owner, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPostgresStorageArchiveAdmissionAndDispatch(t *testing.T) {
	f := newArchiveControlFixture(t)
	for _, item := range []struct{ table, trigger string }{
		{"hosting_storage_restore_origins", "hosting_storage_archive_origin_exclusive"},
		{"hosting_storage_restore_use_receipts", "hosting_storage_restore_use_receipts_immutable"},
	} {
		tx, err := f.db.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if e := tx.Rollback(cleanup); e != nil {
					t.Error("schema guard rollback failed")
				}
			}()
			if _, err = tx.Exec(f.ctx, `ALTER TABLE `+pgx.Identifier{item.table}.Sanitize()+` DISABLE TRIGGER `+pgx.Identifier{item.trigger}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			var compatible bool
			if tx.QueryRow(f.ctx, `SELECT nerocd_storage_archive_schema_compatible()`).Scan(&compatible) != nil || compatible {
				t.Fatal("disabled archive safety guard remained schema compatible")
			}
		}()
	}
	b := f.backup(t)
	replayed := f.backup(t)
	if replayed.ID != b.ID || replayed.OperationID != b.OperationID {
		t.Fatal("backup retry changed identity")
	}
	if _, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("1", 32), ExpectedResourceGeneration: 2}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed body reused backup request")
	}
	if _, err := f.pg.UpsertProjectMember(f.ctx, domain.ProjectMember{ID: "archive_viewer", ProjectID: f.resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(f.ctx, auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	other, err := f.service.CreateProject(f.owner, app.ProjectInput{Name: "archive other project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.UpsertProjectMember(f.ctx, domain.ProjectMember{ID: "archive_other_owner", ProjectID: other.ID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetStorageBackup(viewer, b.ID); err != nil {
		t.Fatal("viewer metadata read refused")
	}
	if _, err := f.service.RequestStorageBackup(viewer, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("2", 32), ExpectedResourceGeneration: 1}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer or owner of another project admitted backup")
	}
	b = f.finishBackup(t, b)
	if b.Blob == nil || b.State != "succeeded" {
		t.Fatal("published backup metadata missing")
	}
	op, err := f.service.HostingOperation(viewer, b.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"lease_id"`, `"fence"`, `"attempt"`} {
		if strings.Contains(string(raw), field) {
			t.Fatal("public archive operation exposed runner authority")
		}
	}
	input := hostingDeploymentRequest()
	input.HostPort = 0
	input.IdempotencyKey = "archive_source_successor"
	next, err := f.pg.RequestHostingDeployment(f.ctx, "hop_archive_successor", f.resource.ID, "usr_bootstrap", input, destinationEvent("archive_successor"))
	if err != nil {
		t.Fatal(err)
	}
	release := *f.source.Release
	release.CanonicalImage = "docker.io/library/nginx@sha256:" + strings.Repeat("6", 64)
	release.ImageID = "sha256:" + strings.Repeat("7", 64)
	release.Hash = ""
	release.Hash = domain.HostingDocumentHash(release)
	next = f.settleImage(t, next, f.storage, release, strings.Repeat("8", 64))
	var retained domain.HostingHealthy
	var body []byte
	var state string
	if err = f.db.QueryRow(f.ctx, `SELECT body,state FROM hosting_retained_releases WHERE operation_id=$1`, f.source.ID).Scan(&body, &state); err != nil || json.Unmarshal(body, &retained) != nil || state != "retained" {
		t.Fatal("cleanup regression did not establish a retired source release")
	}
	if _, err = domain.CompileHostingCleanupPlan(retained, next.ID); err != nil {
		t.Fatal("cleanup target would be ineligible independently of backup retention")
	}
	if _, err = f.pg.RequestHostingReleaseCleanup(f.ctx, "hop_archive_pinned_cleanup", f.resource.ID, f.source.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: f.resource.Generation, IdempotencyKey: "archive_pinned_cleanup"}, destinationEvent("archive_cleanup")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("retained backup did not pin its recoverable source image")
	}
	var absent bool
	if f.db.QueryRow(f.ctx, `SELECT NOT EXISTS(SELECT 1 FROM hosting_operations WHERE id='hop_archive_pinned_cleanup')`).Scan(&absent) != nil || !absent {
		t.Fatal("denied cleanup left mutation authority")
	}
}

func TestPostgresStorageArchiveReceiptAuthorityAndRecovery(t *testing.T) {
	for _, running := range []bool{true, false} {
		name := "originally_stopped"
		if running {
			name = "originally_running"
		}
		t.Run(name, func(t *testing.T) { testArchiveReceiptRecovery(t, running) })
	}
}

func testArchiveReceiptRecovery(t *testing.T, running bool) {
	t.Helper()
	f := newArchiveControlFixture(t)
	b := f.backup(t)
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	if _, err := f.pg.HostingApplyBegin(f.ctx, f.runner, a); err == nil {
		t.Fatal("generic image endpoint admitted archive")
	}
	if _, err := f.pg.StorageArchivePlan(f.ctx, "foreign_runner", a); err == nil {
		t.Fatal("foreign runner received archive authority")
	}
	op = f.progress(t, op, a, "writer_snapshot", archiveStates(op.StorageArchive.Plan, running), nil)
	op = f.progress(t, op, a, "stop_intent", archiveStates(op.StorageArchive.Plan, running), nil)
	canceled, err := f.service.CancelHostingOperation(f.owner, op.ID)
	if err != nil || !canceled.MutationHold || canceled.State != "cancel_requested" {
		t.Fatal("cancellation lost stopped-writer hold")
	}
	if again, e := f.service.CancelHostingOperation(f.owner, op.ID); e != nil || again.State != canceled.State {
		t.Fatal("repeated authorized cancellation was not idempotent")
	}

	if _, err = f.service.RunnerStorageArchivePlan(f.runnerContext, a); err == nil {
		t.Fatal("canceled lease remained executable")
	}
	if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "canceled_progress", PlanHash: op.StorageArchive.Plan.Hash, Phase: "stopped", Writers: archiveStates(op.StorageArchive.Plan, false)}); err == nil {
		t.Fatal("canceled lease created new progress")
	}
	recovery, err := f.service.RequestStorageArchiveRecovery(f.owner, op.ID, domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("3", 32), ExpectedPlanHash: op.StorageArchive.Plan.Hash})
	if err != nil {
		t.Fatal(err)
	}
	e := recovery.StorageArchive.Plan.RecoveryEvidence
	want := 0
	if running {
		want = 1
	}
	if e == nil || len(e.ResumeRequiredContainerIDs) != want || running && e.ResumeRequiredContainerIDs[0] != op.StorageArchive.Plan.SourceHealthy.ContainerID {
		t.Fatal("recovery lost exact durable running writer obligation")
	}
	recovery, ra := f.claim(t, recovery.ID, "recover")
	recovery = f.begin(t, recovery, ra)
	requestNext := func(request string) (domain.HostingOperation, error) {
		return f.service.RequestStorageArchiveRecovery(f.owner, op.ID, domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat(request, 32), ExpectedPlanHash: op.StorageArchive.Plan.Hash})
	}
	if _, err = requestNext("6"); err == nil {
		t.Fatal("live recovery was replaced with competing authority")
	}
	if _, err = f.service.CancelHostingOperation(f.owner, recovery.ID); err != nil {
		t.Fatal(err)
	}
	recovery, err = requestNext("6")
	if err != nil || len(recovery.StorageArchive.Plan.RecoveryEvidence.ResumeRequiredContainerIDs) != want {
		t.Fatal("first interrupted recovery stranded or changed writer obligations")
	}
	if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: ra, ReceiptID: "superseded_recovery", PlanHash: recovery.StorageArchive.Plan.Hash, Phase: "resume_intent", Writers: e.Writers}); err == nil {
		t.Fatal("superseded recovery retained effect authority")
	}
	recovery, ra = f.claim(t, recovery.ID, "recover")
	recovery = f.begin(t, recovery, ra)
	if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: ra, ReceiptID: "recovery_helper", PlanHash: recovery.StorageArchive.Plan.Hash, Phase: "helper_intent"}); err == nil {
		t.Fatal("recovery could repeat archive helper")
	}
	if running {
		recovery = f.progress(t, recovery, ra, "resume_intent", e.Writers, nil)
		recovery = f.progress(t, recovery, ra, "resumed", e.Writers, nil)
	} else if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: ra, ReceiptID: "stopped_writer_resume", PlanHash: recovery.StorageArchive.Plan.Hash, Phase: "resume_intent", Writers: e.Writers}); err == nil {
		t.Fatal("originally stopped writer received start authority")
	}
	// Lose the second recovery after its durable resume proof, before Complete.
	if _, err = f.service.CancelHostingOperation(f.owner, recovery.ID); err != nil {
		t.Fatal(err)
	}
	recovery, err = requestNext("7")
	if err != nil || len(recovery.StorageArchive.Plan.RecoveryEvidence.ResumeRequiredContainerIDs) != 0 {
		t.Fatal("second interrupted recovery reintroduced an already-resumed writer")
	}
	recovery, ra = f.claim(t, recovery.ID, "recover")
	recovery = f.begin(t, recovery, ra)
	if _, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: ra, ReceiptID: "duplicate_resume", PlanHash: recovery.StorageArchive.Plan.Hash, Phase: "resume_intent", Writers: e.Writers}); err == nil {
		t.Fatal("empty remaining set authorized another writer resume")
	}
	settled, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: ra, ReceiptID: "recovery_complete", PlanHash: recovery.StorageArchive.Plan.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "not_attempted", ResumeOutcome: "not_needed"}})
	if err != nil || settled.MutationHold || settled.State != "succeeded" {
		t.Fatal("recovery did not resolve original writer hold")
	}
}

func TestPostgresStorageArchiveRestoreHoldAndMaterialization(t *testing.T) {
	f := newArchiveControlFixture(t)
	b := f.finishBackup(t, f.backup(t))
	restored, err := f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("4", 32), Name: "restored uploads"})
	if err != nil {
		t.Fatal(err)
	}
	p := restored.Operation.StorageArchive.Plan
	if len(p.Writers) != 0 || restored.Storage.ID == f.storage.ID || restored.Storage.BindingID == f.storage.BindingID || restored.Storage.VolumeName == f.storage.VolumeName || !restored.Operation.RestoreOwnershipHeld {
		t.Fatal("restore reused source or failed to hold new target")
	}
	input := hostingDeploymentRequest()
	input.HostPort = 0
	if _, err = f.service.RequestHostingDeployment(f.owner, restored.Resource.ID, input); err == nil {
		t.Fatal("ordinary deploy bypassed restore target hold")
	}
	op, a := f.claim(t, restored.Operation.ID, "restore")
	if _, err = f.service.RunnerStorageArchiveApplyBegin(f.runnerContext, domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "premature_restore_begin", PlanHash: p.Hash}); err == nil {
		t.Fatal("restore began before materialization ACK")
	}
	create := domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: p.Hash, StorageRef: restored.Storage.Reference(), CreationNonce: p.Target.Plan.CreationNonce}
	wrongTarget := create
	wrongTarget.StorageRef = f.storage.Reference()
	if _, err = f.pg.HostingStorageCreateBegin(f.ctx, f.runner, wrongTarget); err == nil {
		t.Fatal("restore approval reused source storage as target")
	}
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, create)
	if err != nil || !ack.CreatePermitted {
		t.Fatal("fresh restore origin not admitted")
	}
	again, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, create)
	if err != nil || again.CreatePermitted || again.Materialization.CreationPlanHash != p.Hash {
		t.Fatal("restore retry regained volume create permission")
	}
	proof := archiveMaterializedProof(a, p.Hash, restored.Storage, ack.Materialization, "restore_materialized")
	wrong := proof
	wrong.Labels = maps.Clone(proof.Labels)
	wrong.Labels["foreign"] = "owned"
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, wrong); err == nil {
		t.Fatal("foreign materialization labels accepted")
	}
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, proof); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, proof); err != nil {
		t.Fatal("materialization response-loss retry refused")
	}
	op = f.begin(t, op, a)
	op = f.progress(t, op, a, "helper_intent", nil, nil)
	op = f.progress(t, op, a, "helper_identified", nil, nil)
	f.progress(t, op, a, "restore_verified", nil, p.Blob)
	op, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "restore_complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "not_needed", Blob: p.Blob, RestoreVerified: true}})
	if err != nil {
		t.Fatal(err)
	}
	if op.RestoreOwnershipHeld || op.MutationHold || op.Result != nil || op.Release != nil {
		t.Fatal("file restore fabricated application readiness or retained resolved hold")
	}
	var noHealthy bool
	if f.db.QueryRow(f.ctx, `SELECT healthy IS NULL FROM hosting_binding_state WHERE binding_id=$1`, restored.Storage.BindingID).Scan(&noHealthy) != nil || !noHealthy {
		t.Fatal("file restoration inserted application healthy state")
	}
	input.IdempotencyKey = "deploy_restored_target"
	deployment, err := f.service.RequestHostingDeployment(f.owner, restored.Resource.ID, input)
	if err != nil {
		t.Fatal("verified target cannot request ordinary image deployment")
	}
	deployment = f.settleImage(t, deployment, restored.Storage, *f.source.Release, strings.Repeat("5", 64))
	var healthy domain.HostingHealthy
	var healthyBody []byte
	if f.db.QueryRow(f.ctx, `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, restored.Storage.BindingID).Scan(&healthyBody) != nil || json.Unmarshal(healthyBody, &healthy) != nil || healthy.Storage == nil || healthy.Storage.CreationPlanHash != p.Hash || healthy.Storage.Plan.CreationOperationID != restored.Operation.ID {
		t.Fatal("ordinary restored deployment lost the archive materialization origin")
	}
	var admissions, receipts, imageOrigins int
	if f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_storage_restore_use_admissions WHERE operation_id=$1),(SELECT count(*) FROM hosting_storage_restore_use_receipts WHERE operation_id=$1),(SELECT count(*) FROM hosting_storage_materializations WHERE storage_id=$2)`, deployment.ID, restored.Storage.ID).Scan(&admissions, &receipts, &imageOrigins) != nil || admissions != 1 || receipts != 1 || imageOrigins != 0 {
		t.Fatal("restored volume did not retain separate one-shot origin and image use receipts")
	}
}

func TestSecurityArchiveAdmissionRejectsExpiredActorAfterAuditWait(t *testing.T) {
	testArchiveExpiredActorAfterWait(t, "audit")
}

func TestPostgresStorageArchiveActorExpiryAfterFinalWait(t *testing.T) {
	for _, path := range []string{"deferred_constraint", "read", "list", "request_replay", "runner_plan", "receipt_replay"} {
		t.Run(path, func(t *testing.T) { testArchiveExpiredActorAfterWait(t, path) })
	}
}

func testArchiveExpiredActorAfterWait(t *testing.T, path string) {
	t.Helper()
	f := newArchiveControlFixture(t)
	principal, _ := proxyLegacyToken(t, f.pg, f.service, "tok_archive_security_wait", []string{domain.RoleSystemAdmin})
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	actor := auth.WithPrincipal(ctx, principal)
	input := domain.HostingStorageBackupInput{RequestID: strings.Repeat("9", 32), ExpectedResourceGeneration: f.resource.Generation}
	call := func() error { _, err := f.service.RequestStorageBackup(actor, f.resource.ID, input); return err }
	var authority domain.HostingAuthority
	if path != "audit" && path != "deferred_constraint" {
		backup, err := f.service.RequestStorageBackup(actor, f.resource.ID, input)
		if err != nil {
			t.Fatal("authorized setup backup failed")
		}
		switch path {
		case "read":
			call = func() error { _, err := f.service.GetStorageBackup(actor, backup.ID); return err }
		case "list":
			call = func() error { _, err := f.service.ListStorageBackups(actor, f.resource.ID, store.Page{}); return err }
		case "runner_plan", "receipt_replay":
			op, a := f.claim(t, backup.OperationID, "backup")
			authority = a
			if path == "runner_plan" {
				call = func() error { _, err := f.service.RunnerStorageArchivePlan(f.runnerContext, a); return err }
			} else {
				request := domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, PlanHash: op.StorageArchive.Plan.Hash, Helper: f.helper}
				call = func() error { _, err := f.service.RunnerStorageArchivePrepared(f.runnerContext, request); return err }
			}
		}
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal("blocker setup")
	}
	joined, started := false, false
	done := make(chan error, 1)
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Error("blocker cleanup")
		}
		if started && !joined {
			select {
			case <-done:
			case <-cleanup.Done():
				t.Error("request did not join")
			}
		}
	}()
	var key int64
	var pid int
	if blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':archive-security-audit',0),pg_backend_pid()`).Scan(&key, &pid) != nil {
		t.Fatal("blocker identity")
	}
	switch path {
	case "audit", "deferred_constraint":
		if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			t.Fatal("blocker lock")
		}
		ddl := `CREATE FUNCTION archive_security_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage_archive.requested' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':archive-security-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER archive_security_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION archive_security_audit_wait()`
		if path == "deferred_constraint" {
			ddl = `CREATE FUNCTION archive_security_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':archive-security-audit',0)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER archive_security_audit_wait AFTER INSERT ON hosting_storage_archive_plans DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION archive_security_audit_wait()`
		}
		if _, err = f.db.Exec(ctx, ddl); err != nil {
			t.Fatal("wait fixture setup")
		}
	case "read", "list":
		if _, err = blocker.Exec(ctx, `LOCK TABLE hosting_storage_archive_blobs IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal("read blocker")
		}
	case "request_replay":
		if _, err = blocker.Exec(ctx, `LOCK TABLE hosting_storage_backups IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal("replay blocker")
		}
	case "runner_plan", "receipt_replay":
		var locked string
		if blocker.QueryRow(ctx, `SELECT id FROM run_leases WHERE id=$1 FOR UPDATE`, authority.LeaseID).Scan(&locked) != nil {
			t.Fatal("authority blocker")
		}
	}
	var expires time.Time
	if f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '1500 milliseconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expires) != nil {
		t.Fatal("expiry setup")
	}
	started = true
	go func() { done <- call() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
			t.Fatal("wait observation")
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			joined = true
			t.Fatalf("did not block: error_present=%t", e != nil)
		case <-ctx.Done():
			t.Fatal("wait deadline")
		case <-ticker.C:
		}
	}
	for {
		var expired bool
		if f.db.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expires).Scan(&expired) != nil {
			t.Fatal("clock")
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("expiry deadline")
		case <-ticker.C:
		}
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal("release")
	}
	select {
	case err = <-done:
		joined = true
		if !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("expired actor was not denied: error_present=%t", err != nil)
		}
	case <-ctx.Done():
		t.Fatal("request completion deadline")
	}
	if path == "audit" || path == "deferred_constraint" {
		var effects int
		if f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_storage_backups WHERE created_by=$1)+(SELECT count(*) FROM hosting_operations WHERE created_by=$1)+(SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action LIKE 'hosting.storage_archive.%')`, principal.ID).Scan(&effects) != nil || effects != 0 {
			t.Fatal("expired admission retained transactional effects")
		}
		var held bool
		if f.db.QueryRow(ctx, `SELECT hold_operation_id IS NOT NULL FROM hosting_binding_state WHERE binding_id=$1`, f.storage.BindingID).Scan(&held) != nil || held {
			t.Fatal("expired admission retained mutation hold")
		}
	}
}
