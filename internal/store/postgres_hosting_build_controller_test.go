package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func startBuildController(t *testing.T, f buildStoreFixture) func(context.Context) error {
	t.Helper()
	for {
		tx, err := f.db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		changed, err := hostingjobs.MigrateNextTx(t.Context(), tx)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	p := buildPolicy()
	if err := f.pg.ConfigureHostingBuild(&p); err != nil {
		t.Fatal(err)
	}
	p.Platform = "caller-mutated"
	stop, err := f.pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return stop
}
func publicBuildInput(key string) domain.HostingBuildRequest {
	return domain.HostingBuildRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: key}
}
func awaitBuildDispatch(t *testing.T, f buildStoreFixture, id string) domain.HostingBuild {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", id)
		if err != nil {
			t.Fatal(err)
		}
		if b.RunID != "" {
			return b
		}
		if b.State == "failed" {
			t.Fatalf("dispatch failed: %+v", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("real River did not dispatch")
	return domain.HostingBuild{}
}
func TestPostgresHostingBuildControllerRealRiverAndExactClaim(t *testing.T) {
	f := newBuildStoreFixture(t)
	startBuildController(t, f)
	ctx := t.Context()
	b, err := f.pg.RequestPublicHostingBuild(ctx, "controller", f.src.ResourceID, "usr_bootstrap", publicBuildInput("controller"), destinationEvent("controller"))
	if err != nil {
		t.Fatal(err)
	}
	b = awaitBuildDispatch(t, f, b.ID)
	if err = f.pg.PrepareBuild(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_tasks WHERE build_id=$1`, b.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dispatch receipt count=%d %v", count, err)
	}
	var raw []byte
	if err = f.db.QueryRow(ctx, `SELECT run_spec FROM task_runs WHERE id=$1`, b.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec map[string]string
	if json.Unmarshal(raw, &spec) != nil || len(spec) != 2 || spec["type"] != domain.HostingBuildTaskType || spec["hosting_build_id"] != b.ID {
		t.Fatalf("noncanonical %s", raw)
	}
	// Identical tags and capability never substitute for immutable target identity.
	if _, err = f.db.Exec(ctx, `INSERT INTO runners(id,name,tags,capabilities,status,token_hash,registered_at,last_heartbeat_at) SELECT 'wrong_build_runner','wrong',tags,capabilities,'active','wrong',clock_timestamp(),clock_timestamp() FROM runners WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	wrong, err := f.pg.ClaimRun(ctx, "wrong_build_runner", time.Now(), time.Minute)
	if err == nil && wrong.Run.ID == b.RunID {
		t.Fatal("same-tag wrong runner claimed build")
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != b.RunID {
		t.Fatalf("exact claim=%+v %v", claim, err)
	}
	a := domain.HostingBuildAuthority{BuildID: b.ID, RunID: b.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || !status.MayExecute || len(status.Receipts) != 0 {
		t.Fatalf("live status=%+v %v", status, err)
	}
	if _, err = f.pg.UpdateRunStatus(ctx, b.RunID, domain.RunSucceeded, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generic status %v", err)
	}
	if _, err = f.pg.ApproveRun(ctx, b.RunID, "usr_bootstrap", time.Now()); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generic approval %v", err)
	}
}
func TestPostgresHostingBuildControllerRollbackReplayAndRecovery(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	if _, err := f.pg.RequestPublicHostingBuild(ctx, "disabled", f.src.ResourceID, "usr_bootstrap", publicBuildInput("disabled"), destinationEvent("disabled")); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	// A startup-disabled controller registers no build worker; enabling after start is rejected.
	startBuildController(t, f)
	_, err := f.pg.RequestPublicHostingBuild(ctx, "rollback", f.src.ResourceID, "usr_bootstrap", publicBuildInput("rollback"), domain.AuditEvent{ID: "bad_actor", ActorID: "usr_viewer"})
	if err == nil {
		t.Fatal("audit failure committed")
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds WHERE id='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback %d %v", count, err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'build_id'='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("queue rollback %d %v", count, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	ids := make(chan string, 2)
	for _, id := range []string{"replay-a", "replay-b"} {
		wg.Go(func() {
			b, e := f.pg.RequestPublicHostingBuild(ctx, id, f.src.ResourceID, "usr_bootstrap", publicBuildInput("same"), destinationEvent(id))
			errs <- e
			ids <- b.ID
		})
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	retained := ""
	for id := range ids {
		if retained != "" && id != retained {
			t.Fatal("idempotency created duplicate")
		}
		retained = id
	}
	awaitBuildDispatch(t, f, retained)
	// Simulate an accepted operation whose finite queue history was lost/cleaned.
	// The existing recovery loop must rediscover it independently of River status.
	b, err := f.pg.RequestHostingBuild(ctx, "recover", f.src.ResourceID, "usr_bootstrap", buildInput("recover"), buildPolicy(), buildEnqueue, destinationEvent("recover"))
	if err != nil {
		t.Fatal(err)
	}
	awaitBuildDispatch(t, f, b.ID)
}
func TestPostgresHostingBuildStatusHistoricalProofAndTerminalSettlement(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "historical")
	receipt := sourceBuildReceipt(b)
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "historical-source", receipt)
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "daemon-build")
	if err != nil {
		t.Fatal(err)
	}
	c := store.HostingBuildCompletion{State: "succeeded", Artifact: ptrBuildArtifact(buildArtifact(b))}
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "historical-complete", b.Revision, c)
	if err != nil {
		t.Fatal(err)
	}
	var runState, leaseState string
	if err = f.db.QueryRow(ctx, `SELECT r.status,l.status FROM task_runs r JOIN run_leases l ON l.run_id=r.id WHERE l.id=$1`, a.LeaseID).Scan(&runState, &leaseState); err != nil || runState != "succeeded" || leaseState != "succeeded" {
		t.Fatalf("terminal=%s/%s %v", runState, leaseState, err)
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "historical-complete", b.Revision, c); err != nil {
		t.Fatalf("committed receipt replay=%v", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute || len(status.Receipts) != 2 {
		t.Fatalf("historical=%+v %v", status, err)
	}
	for _, ack := range status.Receipts {
		if ack.Authority != a {
			t.Fatal("cross-tuple receipt")
		}
	}
	forged := a
	forged.Fence = "other"
	if _, err = f.pg.HostingBuildStatus(ctx, f.runner, forged); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("forged historical %v", err)
	}
}
func ptrBuildArtifact(a domain.HostingBuildArtifact) *domain.HostingBuildArtifact { return &a }
func TestPostgresHostingBuildExpiryRetainsCleanupAndFreshReceiptDenied(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "expired-controller")
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.ExpireLeases(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute || status.Build.State != "cleanup_required" || status.Build.FailureCode != "authority_lost" {
		t.Fatalf("expired=%+v %v", status, err)
	}
	_, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "stale-complete", status.Build.Revision, store.HostingBuildCompletion{State: "canceled", FailureCode: "canceled"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("fresh stale completion %v", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_receipts WHERE build_id=$1`, b.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipt written %d %v", count, err)
	}
	var state string
	if err = f.db.QueryRow(ctx, `SELECT status FROM task_runs WHERE id=$1`, a.RunID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("generic requeue=%s %v", state, err)
	}
	if strings.Contains(status.Build.FailureCode, " ") {
		t.Fatal("raw diagnostic persisted")
	}
}
func TestHostingBuildProtocolCompletionHashCompatibility(t *testing.T) {
	c := hostingbuildprotocol.Completion{State: "canceled", FailureCode: "canceled"}
	if hostingbuildprotocol.CompletionHash(c) != domain.HostingDocumentHash(store.HostingBuildCompletion{State: c.State, FailureCode: c.FailureCode}) {
		t.Fatal("protocol body hash differs from store")
	}
}

func TestPostgresHostingBuildStatusCurrentRevocationRenewalAndBlockingClock(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "status-current")
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute {
		t.Fatalf("expired historical=%+v %v", status, err)
	}
	// The initial tuple expiry is not immutable execution authority. A current
	// renewed live lease with the same tuple is authoritative.
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	status, err = f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || !status.MayExecute {
		t.Fatalf("renewed=%+v %v", status, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, b.Snapshot.DestinationID, b.ProjectID); err != nil {
		t.Fatal(err)
	}
	status, err = f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute {
		t.Fatalf("revoked grant=%+v %v", status, err)
	}
}

func TestPostgresHostingBuildStatusExpiresAfterBlockingResourceLock(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "status-blocked")
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, b.ResourceID); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status hostingbuildprotocol.StatusResponse
		err    error
	}
	done := make(chan answer, 1)
	go func() { status, err := f.pg.HostingBuildStatus(ctx, f.runner, a); done <- answer{status, err} }()
	deadline := time.Now().Add(5 * time.Second)
	expired := false
	for !expired && time.Now().Before(deadline) {
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, a.LeaseID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !expired {
		t.Fatal("lease never expired")
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.status.MayExecute {
			t.Fatalf("post-lock status=%+v %v", got.status, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("status did not join")
	}
}

func TestPostgresHostingBuildRecoversExhaustedRealRiverJob(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	stop := startBuildController(t, f)
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := f.pg.RequestPublicHostingBuild(ctx, "last-attempt", f.src.ResourceID, "usr_bootstrap", publicBuildInput("last-attempt"), destinationEvent("last_attempt"))
	if err != nil {
		t.Fatal(err)
	}
	// No consumer is running. Simulate finite retry exhaustion and retained job
	// cleanup before restart. Recovery must discover the business row itself.
	if _, err = f.db.Exec(ctx, `UPDATE river_job SET state='discarded',attempt=max_attempts,finalized_at=clock_timestamp() WHERE args->>'build_id'=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	restartStop, err := f.pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := restartStop(c); e != nil {
			t.Error(e)
		}
	})
	dispatched := awaitBuildDispatch(t, f, b.ID)
	if dispatched.RunID != "build_run_"+b.ID {
		t.Fatal("noncanonical recovery")
	}
	if err = f.pg.PrepareBuild(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_tasks WHERE build_id=$1`, b.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate dispatch %d %v", count, err)
	}
}

func TestPostgresHostingBuildControllerPreflightFailureIsDurable(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	f.request(t, "stale-config")
	src := f.src
	src.Generation = 2
	src.Settings.ContextDir = "changed"
	if _, err := configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("stale_config")); err != nil {
		t.Fatal(err)
	}
	startBuildController(t, f)
	if err := f.pg.PrepareBuild(ctx, "stale-config"); err != nil {
		t.Fatal(err)
	}
	b, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", "stale-config")
	if err != nil || b.State != "failed" || b.RunID != "" || b.FailureCode != "authority_lost" {
		t.Fatalf("preflight=%+v %v", b, err)
	}
	var tasks int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_tasks WHERE build_id=$1`, b.ID).Scan(&tasks); err != nil || tasks != 0 {
		t.Fatalf("stale config dispatched=%d %v", tasks, err)
	}
}
