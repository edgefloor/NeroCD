package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"testing"
	"time"
)

// This fixture proves backend admission with canonical persisted build evidence.
// It does not claim that the separate local-image runtime adapter has executed.
func successfulLocalBuildFixture(t *testing.T) (buildStoreFixture, domain.HostingBuild) {
	t.Helper()
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "local-build")
	b, err := f.pg.CommitHostingBuildSource(t.Context(), f.runner, a, "local-source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(t.Context(), f.runner, a, b.Revision, "local-daemon")
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	b, err = f.pg.CompleteHostingBuild(t.Context(), f.runner, a, "local-artifact", b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,'local_build_deploy_v1') WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	return f, b
}
func localBuildDeploymentInput(build string) domain.HostingBuildDeploymentInput {
	return domain.HostingBuildDeploymentInput{BuildID: build, ExpectedGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: "deploy-local", ContainerPort: 80, HostPort: 21001, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}
}
func TestPostgresHostingBuildDeploymentAdmissionAndTypedClaim(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	in := localBuildDeploymentInput(b.ID)
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "local-operation", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("local-deploy"))
	if err != nil {
		t.Fatal(err)
	}
	if op.Snapshot.LocalBuild.Artifact != *b.Artifact || op.Snapshot.ImageReference != "" || op.Snapshot.LocalBuild.ArtifactHash != domain.HostingDocumentHash(*b.Artifact) {
		t.Fatalf("artifact changed %+v", op)
	}
	replay, err := f.pg.RequestHostingBuildDeployment(ctx, "ignored-operation", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("local-replay"))
	if err != nil || replay.ID != op.ID {
		t.Fatal("exact replay", replay.ID, err)
	}
	changed := in
	changed.ContainerPort++
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "changed", f.src.ResourceID, "usr_bootstrap", changed, destinationEvent("changed")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	// An old image-only agent cannot claim the canonical local deployment task.
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,'local_build_deploy_v1') WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old agent claimed local artifact", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'local_build_deploy_v1') WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.RunSpec.Type != domain.RunTypeLocalBuildDeploy {
		t.Fatalf("fallback task %+v", claim.Run.RunSpec)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err = f.pg.HostingRunnerPlan(ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	release, err := domain.NewHostingLocalBuildRelease(*b.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	bad := release
	bad.ImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "bad-release", Release: bad}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("substitution", err)
	}
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "local-prepared", Release: release})
	if err != nil {
		t.Fatal(err)
	}
	if op.Plan.CompilerVersion != domain.HostingLocalBuildCompilerVersion {
		t.Fatal("wrong compiler")
	}
	op, err = f.pg.HostingApplyBegin(ctx, f.runner, a)
	if err != nil || !op.MutationHold {
		t.Fatal("local apply admission", err)
	}
	result := domain.HostingResult{Outcome: "succeeded", ContainerID: "backend-only-container-evidence", ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash, Ready: true}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "local-complete", Result: result})
	if err != nil || op.State != "succeeded" || op.MutationHold {
		t.Fatal("backend settlement", op.State, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "local-complete", Result: result}); err != nil {
		t.Fatal("historical acknowledgment", err)
	}
}

func TestPostgresHostingBuildDeploymentRollbackAndCurrentGenerations(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	in := localBuildDeploymentInput(b.ID)
	if _, err := f.pg.RequestHostingBuildDeployment(ctx, "viewer", f.src.ResourceID, "usr_viewer", in, destinationEvent("viewer")); err == nil {
		t.Fatal("viewer admitted")
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES('duplicate-local-audit','usr_bootstrap','fixture','fixture','{}',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("duplicate-local-audit")
	event.ID = "duplicate-local-audit"
	if _, err := f.pg.RequestHostingBuildDeployment(ctx, "rollback-local", f.src.ResourceID, "usr_bootstrap", in, event); err == nil {
		t.Fatal("audit failure accepted")
	}
	var operations, associations, ports, jobs int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_operation_build_artifacts),(SELECT count(*) FROM hosting_release_slots),(SELECT count(*) FROM river_job WHERE args->>'operation_id'='rollback-local')`).Scan(&operations, &associations, &ports, &jobs); err != nil || operations != 0 || associations != 0 || ports != 0 || jobs != 0 {
		t.Fatalf("partial request %d %d %d %d %v", operations, associations, ports, jobs, err)
	}
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "current-local", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("current-local"))
	if err != nil {
		t.Fatal(err)
	}
	next := f.src
	next.Generation = 2
	next.Settings.ContextDir = "changed"
	if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, next, destinationEvent("local-source-changed")); err != nil {
		t.Fatal(err)
	}
	replayed, replayErr := f.pg.RequestHostingBuildDeployment(ctx, "replay-stale", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("stale"))
	if replayErr != nil || replayed.ID != op.ID || replayed.Snapshot != op.Snapshot {
		t.Fatal("exact replay replaced historical source", replayed, replayErr)
	}
	changed := in
	changed.IdempotencyKey = "fresh-stale-source"
	if _, err = f.pg.RequestHostingBuildDeployment(ctx, "fresh-stale", f.src.ResourceID, "usr_bootstrap", changed, destinationEvent("fresh-stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("fresh admission bypassed source generation", err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	after, err := f.pg.GetHostingOperation(context.Background(), op.ID)
	if err != nil || after.State != "failed" {
		t.Fatal("stale build dispatched", after.State, err)
	}
}

func TestPostgresHostingBuildDeploymentCurrentAuthorityDenials(t *testing.T) {
	for _, tt := range []struct {
		name, sql string
		args      func(buildStoreFixture, domain.HostingBuild) []any
	}{
		{"grant revoked", `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id='dest_build'`, nil},
		{"runner revoked", `UPDATE runners SET status='revoked' WHERE id='runner_build'`, nil},
		{"enrollment revoked", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id='runner_build'`, nil},
		{"wrong daemon", `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,daemon_id) SELECT id,destination_id,21002,'other-daemon' FROM resource_bindings WHERE resource_id=$1`, func(f buildStoreFixture, b domain.HostingBuild) []any { return []any{f.src.ResourceID} }},
		{"old capability", `UPDATE runners SET capabilities=array_remove(capabilities,'local_build_deploy_v1') WHERE id='runner_build'`, nil},
		{"archived project", `UPDATE projects SET archived_at=clock_timestamp() WHERE id='proj_platform'`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, b := successfulLocalBuildFixture(t)
			startBuildController(t, f)
			args := []any{}
			if tt.args != nil {
				args = tt.args(f, b)
			}
			if _, err := f.db.Exec(t.Context(), tt.sql, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pg.RequestHostingBuildDeployment(t.Context(), "denied-local", f.src.ResourceID, "usr_bootstrap", localBuildDeploymentInput(b.ID), destinationEvent("denied-local")); err == nil {
				t.Fatal("current authority denied condition accepted")
			}
			var n int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_operations`).Scan(&n); err != nil || n != 0 {
				t.Fatal("denial wrote operation", n, err)
			}
		})
	}
}

func TestPostgresHostingBuildDeploymentSchemaAndRawTaskDenial(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	ctx := t.Context()
	var compatible bool
	if err := f.db.QueryRow(ctx, `SELECT nerocd_hosting_build_deployment_schema_compatible()`).Scan(&compatible); err != nil || !compatible {
		t.Fatal("0050 readiness", compatible, err)
	}
	if _, err := f.db.Exec(ctx, `ALTER TABLE hosting_snapshots DISABLE TRIGGER hosting_snapshot_build_consistency`); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatal("disabled consistency ready", ok, err)
	}
	if _, err := f.db.Exec(ctx, `ALTER TABLE hosting_snapshots ENABLE TRIGGER hosting_snapshot_build_consistency`); err != nil {
		t.Fatal(err)
	}
	// Targetless persisted input must never regain generic mutation authority.
	if _, err := f.db.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at) VALUES('raw-local','proj_platform','{"type":"local_build_deploy_v1","hosting_operation_id":"absent","hosting_mode":"deploy"}','{}','{}','{}','queued','usr_bootstrap',clock_timestamp(),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.UpdateRunStatus(ctx, "raw-local", domain.RunSucceeded, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatal("generic local completion", err)
	}
	if _, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("raw local claim", err)
	}
	startBuildController(t, f)
	op, err := f.pg.RequestHostingBuildDeployment(ctx, "immutable-local", f.src.ResourceID, "usr_bootstrap", localBuildDeploymentInput(b.ID), destinationEvent("immutable-local"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_operation_build_artifacts SET artifact_hash='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE operation_id=$1`, op.ID); err == nil {
		t.Fatal("artifact association mutable")
	}
}

func TestPostgresHostingBuildDeploymentExpiryAfterAuditWait(t *testing.T) {
	for _, phase := range []string{"request", "claim"} {
		t.Run(phase, func(t *testing.T) {
			f, b := successfulLocalBuildFixture(t)
			startBuildController(t, f)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			actor := "local-expiring-actor"
			if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES($1,$1,'service_account',$1,ARRAY['system_admin']::text[],'active','usr_bootstrap')`, actor); err != nil {
				t.Fatal(err)
			}
			in := localBuildDeploymentInput(b.ID)
			opID := "audit-expiry-local"
			if phase == "claim" {
				if _, err := f.pg.RequestHostingBuildDeployment(ctx, opID, f.src.ResourceID, actor, in, destinationEvent("request-before-expiry")); err != nil {
					t.Fatal(err)
				}
				if err := f.pg.PrepareDeployment(ctx, opID); err != nil {
					t.Fatal(err)
				}
			}
			event := destinationEvent("local-expiry-" + phase)
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap','fixture','fixture','{}',clock_timestamp())`, event.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, actor); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if phase == "request" {
					_, e := f.pg.RequestHostingBuildDeployment(ctx, opID, f.src.ResourceID, actor, in, event)
					done <- e
				} else {
					_, e := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute, store.WithAudit(event))
					done <- e
				}
			}()
			joined := false
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				if !joined {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("worker did not join")
					}
				}
			}()
			wait := func(query string, args ...any) {
				t.Helper()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					var ready bool
					if err := f.db.QueryRow(ctx, query, args...).Scan(&ready); err != nil {
						t.Fatal(err)
					}
					if ready {
						return
					}
					select {
					case e := <-done:
						joined = true
						t.Fatalf("did not block: %v", e)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-tick.C:
					}
				}
			}
			wait(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid)
			wait(`SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`, actor)
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				joined = true
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !errors.Is(err, store.ErrConflict) && !errors.Is(err, auth.ErrForbidden) && phase == "claim" {
				t.Fatalf("expired creator claim: %v", err)
			}
			if err == nil {
				t.Fatal("expired actor accepted after blocking audit")
			}
			var leases, audits int
			if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_leases l JOIN hosting_task_targets t ON t.run_id=l.run_id WHERE t.operation_id=$1),(SELECT count(*) FROM audit_events WHERE id=$2)`, opID, event.ID).Scan(&leases, &audits); err != nil || leases != 0 || audits != 0 {
				t.Fatalf("partial expiry commit %d %d %v", leases, audits, err)
			}
			if phase == "request" {
				var n int
				if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_operations WHERE id=$1`, opID).Scan(&n); err != nil || n != 0 {
					t.Fatal("expired request persisted", n, err)
				}
			}
		})
	}
}

func TestPostgresHostingBuildDeploymentDeferredAssociationAndConcurrentReplay(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	in := localBuildDeploymentInput(b.ID)
	type outcome struct {
		op  domain.HostingOperation
		err error
	}
	done := make(chan outcome, 2)
	for _, id := range []string{"concurrent-a", "concurrent-b"} {
		go func() {
			op, err := f.pg.RequestHostingBuildDeployment(ctx, id, f.src.ResourceID, "usr_bootstrap", in, destinationEvent(id))
			done <- outcome{op, err}
		}()
	}
	first, second := <-done, <-done
	if first.err != nil || second.err != nil || first.op.ID != second.op.ID {
		t.Fatalf("concurrent replay %+v %+v", first, second)
	}
	op := first.op
	for _, shape := range []string{"missing_association", "registry_association", "artifact_mismatch", "target_mismatch", "source_generation_mismatch", "release_substitution"} {
		t.Run(shape, func(t *testing.T) {
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			id := "deferred-" + shape
			if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT $2,project_id,resource_id,binding_id,$2,request_hash,'queued',created_by FROM hosting_operations WHERE id=$1`, op.ID, id); err != nil {
				t.Fatal(err)
			}
			snapshot := op.Snapshot
			if shape == "registry_association" {
				snapshot.LocalBuild = domain.HostingLocalBuildSource{}
				snapshot.ImageReference = "nginx:1.27"
				snapshot.PolicyVersion = domain.HostingPolicyVersion
			}
			if shape == "artifact_mismatch" {
				snapshot.LocalBuild.Artifact.InputID = "substituted"
			}
			if shape == "target_mismatch" {
				snapshot.RunnerID = "another-runner"
			}
			if shape == "source_generation_mismatch" {
				snapshot.LocalBuild.SourceGeneration++
			}

			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_snapshots(operation_id,body,body_hash) VALUES($1,$2,$3)`, id, raw, domain.HostingDocumentHash(snapshot)); err != nil {
				t.Fatal(err)
			}
			if shape != "missing_association" {
				if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_operation_build_artifacts(operation_id,build_id,artifact_hash) VALUES($1,$2,$3)`, id, b.ID, domain.HostingDocumentHash(*b.Artifact)); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "release_substitution" {
				release, err := domain.NewHostingLocalBuildRelease(*b.Artifact)
				if err != nil {
					t.Fatal(err)
				}
				release.DaemonID = "substituted"
				raw, _ = json.Marshal(release)
				if _, err = tx.Exec(t.Context(), `INSERT INTO hosting_releases(operation_id,body,body_hash) VALUES($1,$2,$3)`, id, raw, release.Hash); err != nil {
					t.Fatal(err)
				}
			}
			err = tx.Commit(t.Context())
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("deferred consistency accepted: %v", err)
			}
		})
	}
}

func settleLocalDeploymentMetadata(t *testing.T, f buildStoreFixture, b domain.HostingBuild, id string, port int) domain.HostingOperation {
	t.Helper()
	ctx := t.Context()
	in := localBuildDeploymentInput(b.ID)
	in.IdempotencyKey = id
	in.HostPort = port
	op, err := f.pg.RequestHostingBuildDeployment(ctx, id, f.src.ResourceID, "usr_bootstrap", in, destinationEvent(id))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(ctx, id); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	release, err := domain.NewHostingLocalBuildRelease(*b.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: id + "-prepared", Release: release})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: id + "-complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "metadata-" + id, ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestPostgresHostingBuildDeploymentRetainedCleanupAfterSourceChange(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	old := settleLocalDeploymentMetadata(t, f, b, "local-old", 22001)
	current := settleLocalDeploymentMetadata(t, f, b, "local-current", 22002)
	if !current.Plan.RetainPrevious || current.Plan.PreviousHealthy.OperationID != old.ID {
		t.Fatal("local retained ownership lost")
	}
	next := f.src
	next.Generation = 2
	next.Settings.ContextDir = "changed"
	if _, err := configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, next, destinationEvent("cleanup-source-change")); err != nil {
		t.Fatal(err)
	}
	op, err := f.pg.RequestHostingReleaseCleanup(ctx, "local-cleanup", f.src.ResourceID, old.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: 2, IdempotencyKey: "local-cleanup"}, destinationEvent("local-cleanup"))
	if err != nil {
		t.Fatal("historical cleanup denied", err)
	}
	if op.CleanupPlan.CompilerVersion != domain.HostingLocalBuildCompilerVersion || op.Snapshot.LocalBuild.Artifact != *b.Artifact {
		t.Fatal("cleanup downgraded local evidence")
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeLocalBuildDeploy {
		t.Fatal("cleanup claim", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err = f.pg.HostingRunnerPlan(ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	result := domain.HostingResult{Outcome: "succeeded", ContainerID: op.CleanupPlan.Target.ContainerID, ImageID: op.CleanupPlan.Target.Release.ImageID, DaemonID: op.CleanupPlan.Target.Release.DaemonID, PlanHash: op.CleanupPlan.Hash}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "local-cleanup-complete", Result: result})
	if err != nil || op.State != "succeeded" || op.MutationHold {
		t.Fatal("cleanup settlement", err)
	}
	retained, err := f.pg.ListHostingRetainedReleases(ctx, f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range retained {
		if r.Healthy.OperationID == old.ID && r.State != "removed" {
			t.Fatal("local target not removed")
		}
	}
}

func TestPostgresHostingBuildDeploymentProxyAndCleanupProtection(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	proxy, err := f.pg.ConfigureHostingProxy(ctx, "local-proxy", "server_build", "usr_bootstrap", domain.HostingProxyInput{ControlProjectID: f.src.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "local-proxy"}, destinationEvent("local-proxy"))
	if err != nil {
		t.Fatal(err)
	}
	var initial string
	if err = f.db.QueryRow(ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id=$1 AND generation=1`, proxy.ID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	settleProxyGeneration(t, f.pg, initial, f.runner, "local-daemon")
	if _, err = f.pg.GrantHostingHostname(ctx, proxy.ID, "local-hostname", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: f.src.ProjectID, Hostname: "apps.nerocd.invalid", Wildcard: true}, destinationEvent("local-hostname")); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingRouteInput{IdempotencyKey: "premature-route", Hostname: "local.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: "only-successful-build"}
	if _, err = f.pg.RequestHostingRoute(ctx, f.src.ResourceID, "", "premature-proxy-op", "usr_bootstrap", in, destinationEvent("premature-route")); err == nil {
		t.Fatal("successful build alone routed")
	}
	old := settleLocalDeploymentMetadata(t, f, b, "local-route-old", 23001)
	in.IdempotencyKey = "local-route"
	in.ExpectedHealthyOperationID = old.ID
	route, err := f.pg.RequestHostingRoute(ctx, f.src.ResourceID, "", "local-route-operation", "usr_bootstrap", in, destinationEvent("local-route"))
	if err != nil {
		t.Fatal("settled local release rejected", err)
	}
	settled := settleProxyGeneration(t, f.pg, route.OperationID, f.runner, "local-daemon")
	if len(settled.Snapshot.Routes) != 1 || settled.Snapshot.Routes[0].HostPort != 23001 {
		t.Fatal("local proxy target changed")
	}
	settleLocalDeploymentMetadata(t, f, b, "local-route-current", 23002)
	if _, err = f.pg.RequestHostingReleaseCleanup(ctx, "forbidden-route-cleanup", f.src.ResourceID, old.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: 1, IdempotencyKey: "forbidden-route-cleanup"}, destinationEvent("forbidden-route-cleanup")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("route-referenced local container cleanup", err)
	}
}

func TestPostgresHostingBuildDeploymentMissingMigrationAndSchemaBinding(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	var schema string
	if err := f.db.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL search_path=pg_catalog,pg_temp`); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if err = tx.QueryRow(ctx, "SELECT "+pgx.Identifier{schema, "nerocd_hosting_build_deployment_schema_compatible"}.Sanitize()+"()").Scan(&ok); err != nil || !ok {
		t.Fatal("schema-bound readiness", ok, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `DROP TRIGGER local_build_deployment_lease ON run_leases; DROP TRIGGER hosting_snapshot_build_consistency ON hosting_snapshots; DROP TRIGGER hosting_release_build_consistency ON hosting_releases; DROP TABLE hosting_operation_build_artifacts; ALTER TABLE hosting_build_artifacts DROP CONSTRAINT hosting_build_artifact_identity; DROP FUNCTION nerocd_hosting_build_deployment_consistent(),nerocd_local_build_deployment_lease(),nerocd_hosting_build_deployment_schema_compatible();`); err != nil {
		t.Fatal(err)
	}
	if ok, err = f.pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatal("missing0050 ready", ok, err)
	}
	if opened, err := store.OpenPostgres(ctx, f.db.Config().ConnConfig.ConnString()); err == nil {
		_ = opened.Close()
		t.Fatal("missing0050 startup accepted")
	}
	if err = applyEmbeddedMigrations(ctx, f.db, "0049_hosting_git_resources.sql", "0050_hosting_build_deployments.sql"); err != nil {
		t.Fatal(err)
	}
	if ok, err = f.pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatal("restored0050 readiness", ok, err)
	}
}

func TestPostgresHostingBuildDeploymentObservationKeepsRequester(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	op := settleLocalDeploymentMetadata(t, f, b, "observed-local", 24001)
	if _, err := f.pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("request-local-observation")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES('other-local-owner','owner','service_account','other-local-owner',ARRAY['system_admin']::text[],'active','usr_bootstrap')`); err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("other-owner-observation")
	event.ActorID = "other-local-owner"
	if _, err := f.pg.RequestHostingReconciliation(ctx, op.ID, event); !errors.Is(err, store.ErrConflict) {
		t.Fatal("observation attribution transferred", err)
	}
	var actor string
	if err := f.db.QueryRow(ctx, `SELECT requested_by FROM task_runs WHERE id=$1`, "observe_"+op.ID).Scan(&actor); err != nil || actor != "usr_bootstrap" {
		t.Fatal("original requester changed", actor, err)
	}
	in := localBuildDeploymentInput(b.ID)
	in.IdempotencyKey = "cancel-auth"
	in.HostPort = 24002
	next, err := f.pg.RequestHostingBuildDeployment(ctx, "cancel-auth-local", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("cancel-auth-request"))
	if err != nil {
		t.Fatal(err)
	}
	event = destinationEvent("viewer-cancel-local")
	event.ActorID = "usr_viewer"
	if _, err = f.pg.CancelHostingOperation(ctx, next.ID, event); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer canceled local operation", err)
	}
}

func TestPostgresHostingBuildDeploymentLostAuthorityRetainsHold(t *testing.T) {
	for _, condition := range []string{"expired", "canceled", "source_changed", "local_capability_removed"} {
		t.Run(condition, func(t *testing.T) {
			f, b := successfulLocalBuildFixture(t)
			startBuildController(t, f)
			ctx := t.Context()
			op, err := f.pg.RequestHostingBuildDeployment(ctx, "held-local", f.src.ResourceID, "usr_bootstrap", localBuildDeploymentInput(b.ID), destinationEvent("held-local"))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
				t.Fatal(err)
			}
			release, err := domain.NewHostingLocalBuildRelease(*b.Artifact)
			if err != nil {
				t.Fatal(err)
			}
			op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "held-local-prepared", Release: release})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
				t.Fatal(err)
			}
			switch condition {
			case "expired":
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID)
			case "canceled":
				_, err = f.pg.CancelHostingOperation(ctx, op.ID, destinationEvent("cancel-held-local"))
			case "source_changed":
				next := f.src
				next.Generation = 2
				next.Settings.ContextDir = "changed"
				_, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, next, destinationEvent("change-held-source"))
			case "local_capability_removed":
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,'local_build_deploy_v1') WHERE id=$1`, f.runner)
			}
			if err != nil {
				t.Fatal(err)
			}
			result := domain.HostingResult{Outcome: "succeeded", ContainerID: "metadata-held", ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash, Ready: true}
			if _, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "held-local-complete", Result: result}); err == nil {
				t.Fatal("lost authority settled local mutation")
			}
			after, err := f.pg.GetHostingOperation(ctx, op.ID)
			if err != nil || !after.MutationHold || after.CompleteReceipt != nil || after.Result != nil {
				t.Fatal("lost authority discarded hold or recorded first receipt", after, err)
			}
		})
	}
}

func TestPostgresHostingBuildDeploymentCleanupExpiryRetainsUncertainty(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	old := settleLocalDeploymentMetadata(t, f, b, "cleanup-expiry-old", 25001)
	settleLocalDeploymentMetadata(t, f, b, "cleanup-expiry-current", 25002)
	op, err := f.pg.RequestHostingReleaseCleanup(ctx, "cleanup-expiry-local", f.src.ResourceID, old.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: 1, IdempotencyKey: "cleanup-expiry-local"}, destinationEvent("cleanup-expiry-local"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ExpireLeases(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := f.pg.GetHostingOperation(ctx, op.ID)
	if err != nil || !after.MutationHold || after.State != "reconciliation_required" {
		t.Fatal("cleanup uncertainty lost", after.State, after.MutationHold, err)
	}
	var status string
	if err = f.db.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, a.RunID).Scan(&status); err != nil || status != "failed" {
		t.Fatal("cleanup blindly requeued", status, err)
	}
	if _, err = f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("uncertain cleanup claimed", err)
	}
}

func TestPostgresHostingBuildDeploymentCannotCrossResourceOrProject(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	for _, project := range []string{"proj_platform", "proj_security"} {
		resource, _ := createBuildGitResource(t, f.pg, "other_"+project, project, domain.HostingGitSource{})
		if project != "proj_platform" {
			grantDestination(t, f.pg, "dest_build", project, "grant-local-other")
		}
		if _, err := f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_" + project, ResourceID: resource.ID, DestinationID: "dest_build", CreatedBy: "usr_bootstrap", CreatedAt: time.Now()}, destinationEvent("bind_"+project)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.RequestHostingBuildDeployment(ctx, "cross_"+project, resource.ID, "usr_bootstrap", localBuildDeploymentInput(b.ID), destinationEvent("cross_"+project)); !errors.Is(err, store.ErrConflict) {
			t.Fatal("cross-resource artifact admitted", project, err)
		}
	}
}

func waitLocalObservationAuditExpiry(t *testing.T, ctx context.Context, f buildStoreFixture, pid int, expiredQuery string, args ...any) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var ready bool
		if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	for {
		var ready bool
		if err := f.db.QueryRow(ctx, expiredQuery, args...).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
func TestPostgresLocalObservationRejectsExpiryDuringAudit(t *testing.T) {
	for _, expiry := range []string{"lease", "owner"} {
		t.Run(expiry, func(t *testing.T) {
			f, b := successfulLocalBuildFixture(t)
			startBuildController(t, f)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			op := settleLocalDeploymentMetadata(t, f, b, "security-observation", 26001)
			actor := "usr_bootstrap"
			if expiry == "owner" {
				actor = "security_observation_owner"
				if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES($1,$1,'service_account',$1,ARRAY['system_admin']::text[],'active','usr_bootstrap')`, actor); err != nil {
					t.Fatal(err)
				}
			}
			event := destinationEvent("security-observation-request")
			event.ActorID = actor
			if _, err := f.pg.RequestHostingReconciliation(ctx, op.ID, event); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			in := domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: op.Release.DaemonID, ContainerID: op.Result.ContainerID, ImageID: op.Release.ImageID, PlanHash: op.Plan.Hash, ReleaseHash: op.Release.Hash, Ready: true}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			audit := "aud_observed_" + a.LeaseID
			if _, err = blocker.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap','fixture','fixture','{}',clock_timestamp())`, audit); err != nil {
				t.Fatal(err)
			}
			q := `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`
			key := a.LeaseID
			if expiry == "lease" {
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, key)
			} else {
				q = `SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`
				key = actor
				_, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, key)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			joined := false
			go func() { _, e := f.pg.HostingReconciliation(ctx, f.runner, in); done <- e }()
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				if !joined {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("observation did not join")
					}
				}
			}()
			waitLocalObservationAuditExpiry(t, ctx, f, pid, q, key)
			if err = blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			joined = true
			var receipts, audits int
			var leaseState, taskState string
			var requested bool
			if e := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_reconciliation_receipts WHERE lease_id=$1),(SELECT count(*) FROM audit_events WHERE id=$2),(SELECT status FROM run_leases WHERE id=$1),(SELECT reconcile_requested FROM hosting_operations WHERE id=$3),(SELECT status FROM task_runs WHERE id=$4)`, a.LeaseID, audit, op.ID, a.RunID).Scan(&receipts, &audits, &leaseState, &requested, &taskState); e != nil {
				t.Fatal(e)
			}
			t.Logf("expiry=%s report_error=%v receipts=%d audits=%d lease=%s task=%s reconcile_requested=%v", expiry, err, receipts, audits, leaseState, taskState, requested)
			if err == nil || receipts != 0 || audits != 0 || leaseState != "active" || taskState != "running" || !requested {
				t.Fatal("first local observation settled after authority expired at audit wait")
			}
		})
	}
}
func TestPostgresLocalObservationClaimRejectsExpiryDuringAudit(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	op := settleLocalDeploymentMetadata(t, f, b, "security-observation-claim", 26001)
	old := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID}
	if err := f.db.QueryRow(ctx, `SELECT id,attempt,fence FROM run_leases WHERE run_id=$1`, op.RunID).Scan(&old.LeaseID, &old.Attempt, &old.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("security-observation-claim-request")); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `LOCK TABLE audit_events IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	joined := false
	go func() { _, e := f.pg.ClaimHostingObservation(ctx, f.runner, old, time.Second); done <- e }()
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("observation claim did not join")
			}
		}
	}()
	// The uncommitted lease is invisible; a database-clock two-second bound after
	// the observed audit block exceeds the one-second window already allocated.
	var bound time.Time
	if err = f.db.QueryRow(ctx, `SELECT clock_timestamp()+interval '3 seconds'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	waitLocalObservationAuditExpiry(t, ctx, f, pid, `SELECT clock_timestamp()>=$1::timestamptz`, bound)
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	joined = true
	var leases, audits, nextAttempt int
	var requested bool
	var state string
	if e := f.db.QueryRow(ctx, `SELECT status,next_attempt,(SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$2 AND action='hosting.observation_claimed'),(SELECT reconcile_requested FROM hosting_operations WHERE id=$2) FROM task_runs WHERE id=$1`, "observe_"+op.ID, op.ID).Scan(&state, &nextAttempt, &leases, &audits, &requested); e != nil {
		t.Fatal(e)
	}
	t.Logf("expired observation claim: error=%v leases=%d audits=%d next_attempt=%d task=%s requested=%v", err, leases, audits, nextAttempt, state, requested)
	if err == nil || leases != 0 || audits != 0 || nextAttempt != 1 || !requested || state != "queued" {
		t.Fatal("expired local observation lease committed after audit wait")
	}
}

func TestPostgresLocalObservationCommittedReplaySurvivesLostAuthority(t *testing.T) {
	f, b := successfulLocalBuildFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	op := settleLocalDeploymentMetadata(t, f, b, "local-observation-replay", 26002)
	const owner = "local-observation-replay-owner"
	if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES($1,$1,'service_account',$1,ARRAY['system_admin']::text[],'active','usr_bootstrap')`, owner); err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("local-observation-replay-request")
	event.ActorID = owner
	if _, err := f.pg.RequestHostingReconciliation(ctx, op.ID, event); err != nil {
		t.Fatal(err)
	}
	previous := domain.HostingAuthority{OperationID: op.ID, RunID: op.RunID}
	if err := f.db.QueryRow(ctx, `SELECT id,attempt,fence FROM run_leases WHERE run_id=$1`, op.RunID).Scan(&previous.LeaseID, &previous.Attempt, &previous.Fence); err != nil {
		t.Fatal(err)
	}
	observed, err := f.pg.ClaimHostingObservation(ctx, f.runner, previous, time.Minute)
	if err != nil || observed.ObservationLease == nil {
		t.Fatal("live observation claim", err)
	}
	lease := *observed.ObservationLease
	again, err := f.pg.ClaimHostingObservation(ctx, f.runner, previous, time.Minute)
	if err != nil || again.ObservationLease == nil || again.ObservationLease.ID != lease.ID {
		t.Fatal("active observation lease reuse", err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
	in := domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: op.Release.DaemonID, ContainerID: op.Result.ContainerID, ImageID: op.Release.ImageID, PlanHash: op.Plan.Hash, ReleaseHash: op.Release.Hash, Ready: true}
	settled, err := f.pg.HostingReconciliation(ctx, f.runner, in)
	if err != nil || settled.ReconcileRequested {
		t.Fatal("live first report rejected after tentative completion", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1;`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE api_tokens SET status='revoked',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id='dest_build'`); err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.HostingReconciliation(ctx, f.runner, in)
	if err != nil || replay.ID != op.ID {
		t.Fatal("committed acknowledgment required live owner, grant or lease", err)
	}
	changed := in
	changed.Ready = false
	if _, err = f.pg.HostingReconciliation(ctx, f.runner, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed historical report replay", err)
	}
	changed = in
	changed.Fence = "different-fence"
	if _, err = f.pg.HostingReconciliation(ctx, f.runner, changed); err == nil {
		t.Fatal("different historical tuple accepted")
	}
	var receipts, audits int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_reconciliation_receipts WHERE lease_id=$1),(SELECT count(*) FROM audit_events WHERE id=$2)`, a.LeaseID, "aud_observed_"+a.LeaseID).Scan(&receipts, &audits); err != nil || receipts != 1 || audits != 1 {
		t.Fatal("receipt replay duplicated writes", receipts, audits, err)
	}
}
