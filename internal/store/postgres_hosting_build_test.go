package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/source"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

type buildStoreFixture struct {
	pg     *store.PostgresStore
	db     *pgxpool.Pool
	src    domain.HostingGitSource
	runner string
}

func newBuildStoreFixture(t *testing.T) buildStoreFixture {
	t.Helper()
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "build_store")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	resource, src := createBuildGitResource(t, pg, "build_web", "proj_platform", domain.HostingGitSource{})
	now := time.Now().UTC()
	credential := fmt.Sprintf("%x", sha256.Sum256([]byte("build-fixture-token")))
	enrollment, err := pg.CreateRunnerEnrollment(ctx, domain.RunnerEnrollment{ID: "enroll_build", TokenHash: "enrollhash_build", RunnerID: "runner_build", RunnerName: "build", Tags: []string{"build"}, Capabilities: []string{domain.RunTypeImageDeploy}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, destinationEvent("enroll_build"))
	if err != nil {
		t.Fatalf("fixture step 1: %v", err)
	}
	runner, err := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "consume_build", CredentialHash: credential}, destinationEvent("consume_build"))
	if err != nil {
		t.Fatalf("fixture step 2: %v", err)
	}
	// No public capability registry/executor is installed by this persistence slice.
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=ARRAY['compose_deploy','image_deploy','hosting_build_v1']::text[] WHERE id=$1`, runner.ID); err != nil {
		t.Fatalf("fixture step 3: %v", err)
	}
	_, dest, err := pg.CreateServerWithDestination(ctx, domain.Server{ID: "server_build", RunnerID: runner.ID, Name: "build", CreatedBy: "usr_bootstrap", CreatedAt: now}, domain.Destination{ID: "dest_build", ServerID: "server_build", Name: "default", RuntimeKind: "docker_compose", CreatedAt: now}, destinationEvent("server_build"))
	if err != nil {
		t.Fatalf("fixture step 4: %v", err)
	}
	grantDestination(t, pg, dest.ID, resource.ProjectID, "grant_build")
	if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_build", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: "usr_bootstrap", CreatedAt: now}, destinationEvent("bind_build")); err != nil {
		t.Fatalf("fixture step 5: %v", err)
	}
	return buildStoreFixture{pg, db, src, runner.ID}
}
func buildPolicy() domain.HostingBuildPolicy {
	d := "sha256:" + strings.Repeat("a", 64)
	return domain.HostingBuildPolicy{Identity: d, ToolchainIdentity: d, Protocol: domain.HostingBuildProtocol, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 10 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Platform: "linux/amd64", Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 10 << 20, UnpackedBytes: 10 << 20, UsageBytes: 20 << 20, ReserveBytes: 10 << 20, PollInterval: time.Second, DiskMode: "monitored"}}
}
func buildInput(key string) domain.HostingBuildInput {
	return domain.HostingBuildInput{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: key}
}
func buildEnqueue(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
	_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS fixture_build_queue(build_id text PRIMARY KEY REFERENCES hosting_builds(id))`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO fixture_build_queue(build_id) VALUES($1)`, b.ID)
	return err
}
func buildDispatch(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) (string, error) {
	id := "build_run_" + b.ID
	spec, _ := json.Marshal(map[string]string{"type": domain.HostingBuildTaskType, "hosting_build_id": b.ID})
	_, err := tx.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at) VALUES($1,$2,$3,'{}','{}','{}','queued',$4,clock_timestamp(),clock_timestamp())`, id, b.ProjectID, spec, b.CreatedBy)
	return id, err
}
func (f buildStoreFixture) request(t *testing.T, id string) domain.HostingBuild {
	t.Helper()
	b, err := f.pg.RequestHostingBuild(t.Context(), id, f.src.ResourceID, "usr_bootstrap", buildInput(id), buildPolicy(), buildEnqueue, destinationEvent("request_"+id))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (f buildStoreFixture) start(t *testing.T, id string) (domain.HostingBuild, domain.HostingBuildAuthority) {
	t.Helper()
	f.request(t, id)
	return f.activate(t, id)
}
func (f buildStoreFixture) activate(t *testing.T, id string) (domain.HostingBuild, domain.HostingBuildAuthority) {
	t.Helper()
	ctx := t.Context()
	b, err := f.pg.DispatchHostingBuild(ctx, id, buildDispatch, destinationEvent("dispatch_"+id))
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingBuildAuthority{BuildID: id, RunID: b.RunID, LeaseID: "lease_" + id, Attempt: 1, Fence: "fence_" + id}
	if _, err = f.db.Exec(ctx, `UPDATE task_runs SET status='running',runner_id=$2 WHERE id=$1`, b.RunID, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),$4,$5)`, a.LeaseID, a.RunID, f.runner, a.Attempt, a.Fence); err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, a, b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return b, a
}
func sourceBuildReceipt(b domain.HostingBuild) domain.HostingBuildSourceReceipt {
	return domain.HostingBuildSourceReceipt{BuildID: b.ID, SourceID: b.Snapshot.Source.ID, InputID: b.Snapshot.InputID, Commit: strings.Repeat("b", 40), PolicyHash: domain.HostingDocumentHash(b.Snapshot.Source.Policy), TreeDigest: "sha256:" + strings.Repeat("b", 64)}
}
func buildArtifact(b domain.HostingBuild) domain.HostingBuildArtifact {
	d := "sha256:" + strings.Repeat("c", 64)
	return domain.HostingBuildArtifact{Origin: "local_build", BuildID: b.ID, SourceID: b.Snapshot.Source.ID, InputID: b.Snapshot.InputID, SourceReceiptHash: domain.HostingDocumentHash(b.SourceReceipt), ContextDigest: d, RequestDigest: d, DaemonID: b.DaemonID, ImageID: d, IndexDigest: d, ManifestDigest: d, ConfigDigest: d, Platform: b.Snapshot.Policy.Platform}
}

func TestPostgresHostingBuildMigrationAndIdempotency(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	calls := 0
	enqueue := func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) error {
		calls++
		return buildEnqueue(ctx, tx, b)
	}
	if _, err := f.pg.RequestHostingBuild(ctx, "absent", f.src.ResourceID, "usr_bootstrap", buildInput("absent"), buildPolicy(), nil, destinationEvent("absent")); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	b, err := f.pg.RequestHostingBuild(ctx, "first", f.src.ResourceID, "usr_bootstrap", buildInput("key"), buildPolicy(), enqueue, destinationEvent("first"))
	if err != nil {
		t.Fatal(err)
	}
	if b.DaemonID != "" || b.RunID != "" || b.Snapshot.ExpectedDaemonID != "" || b.State != "queued" {
		t.Fatalf("invented preparation: %+v", b)
	}
	replay, err := f.pg.RequestHostingBuild(ctx, "other", f.src.ResourceID, "usr_bootstrap", buildInput("key"), buildPolicy(), enqueue, destinationEvent("replay"))
	if err != nil || replay.ID != b.ID || calls != 1 {
		t.Fatalf("idempotency: %+v %v calls=%d", replay, err, calls)
	}
	changed := buildPolicy()
	changed.Platform = "linux/arm64"
	if _, err = f.pg.RequestHostingBuild(ctx, "changed", f.src.ResourceID, "usr_bootstrap", buildInput("key"), changed, enqueue, destinationEvent("changed")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	failure := errors.New("fixture enqueue failure")
	if _, err = f.pg.RequestHostingBuild(ctx, "rollback", f.src.ResourceID, "usr_bootstrap", buildInput("rollback"), buildPolicy(), func(context.Context, pgx.Tx, domain.HostingBuild) error { return failure }, destinationEvent("rollback")); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var n int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds WHERE id='rollback'`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	src := f.src
	src.Generation = 2
	src.Settings.ContextDir = "apps/web"
	if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("source_v2")); err != nil {
		t.Fatal(err)
	}
	if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("source_stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_source_versions SET body='{}' WHERE source_id=$1`, src.ID); err == nil {
		t.Fatal("mutable source history")
	}
	var generation int
	if err = f.db.QueryRow(ctx, `SELECT generation FROM resources WHERE id=$1`, src.ResourceID).Scan(&generation); err != nil || generation != 2 {
		t.Fatal("atomic source change did not advance resource", generation, err)
	}
	if _, err = f.pg.CancelHostingBuild(ctx, "usr_bootstrap", b.ID, b.Revision, destinationEvent("cancel_first")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingBuildFencedSourceDaemonArtifact(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "fenced")
	r := sourceBuildReceipt(b)
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "receipt_source", r)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "receipt_source", r)
	if err != nil || replay.Revision != b.Revision {
		t.Fatal("source replay", err)
	}
	bad := r
	bad.Commit = strings.Repeat("d", 40)
	if _, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "receipt_source", bad); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed source receipt", err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "observed-daemon")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != "building" || b.DaemonID != "observed-daemon" {
		t.Fatal("daemon unprepared")
	}
	if _, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "other-daemon"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("daemon mutation", err)
	}
	retained, prepareErr := f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, b.DaemonID)
	if prepareErr != nil || retained.Revision != b.Revision {
		t.Fatal("retained daemon pin revalidation", prepareErr)
	}
	artifact := buildArtifact(b)
	wrong := artifact
	wrong.BuildID = "another"
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "receipt_bad", b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &wrong}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unbound artifact", err)
	}
	completion := store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact}
	// Force the final audit INSERT to fail after artifact and receipt writes.
	auditID := "aud_build_" + domain.HostingDocumentHash(struct{ Build, Phase, Receipt string }{b.ID, "complete", "receipt_complete"})[7:]
	if _, err = f.db.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap','fixture','fixture','{}',clock_timestamp())`, auditID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "receipt_complete", b.Revision, completion); err == nil {
		t.Fatal("audit failure accepted")
	}
	var n int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_artifacts WHERE build_id=$1`, b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("non-atomic artifact", n, err)
	}
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "receipt_success", b.Revision, completion)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != "succeeded" || b.Artifact == nil {
		t.Fatalf("missing durable artifact %+v", b)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET status='expired',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "receipt_success", 1, completion); err != nil {
		t.Fatal("lost acknowledgment replay requires live lease", err)
	}
	stale := a
	stale.Fence = "older"
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, stale, "receipt_success", 1, completion); !errors.Is(err, store.ErrConflict) {
		t.Fatal("different replay tuple", err)
	}
	if _, err = f.pg.ResolveCurrentHostingBuildArtifact(ctx, "usr_bootstrap", f.src.ResourceID, b.ID); err != nil {
		t.Fatal(err)
	}
	next := f.src
	next.Generation = 2
	next.RequestedRef = "release"
	if _, err = configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, next, destinationEvent("source_next")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ResolveCurrentHostingBuildArtifact(ctx, "usr_bootstrap", f.src.ResourceID, b.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old generation became desired", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_artifacts SET body='{}' WHERE build_id=$1`, b.ID); err == nil {
		t.Fatal("artifact mutable")
	}
}

func TestPostgresHostingBuildDispatchAdmissionAndCancellation(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b := f.request(t, "dispatch")
	if _, err := f.pg.DispatchHostingBuild(ctx, b.ID, nil, destinationEvent("nil_dispatch")); !errors.Is(err, store.ErrHostingBuildUnavailable) {
		t.Fatal(err)
	}
	mismatch := func(ctx context.Context, tx pgx.Tx, b domain.HostingBuild) (string, error) {
		_, err := buildDispatch(ctx, tx, b)
		return "public_run", err
	}
	if _, err := f.pg.DispatchHostingBuild(ctx, b.ID, mismatch, destinationEvent("mismatch")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM task_runs WHERE id=$1`, "build_run_"+b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("dispatch not atomic", n, err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := buildDispatch(ctx, tx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DispatchHostingBuild(ctx, b.ID, func(context.Context, pgx.Tx, domain.HostingBuild) (string, error) { return run, nil }, destinationEvent("preexisting")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("accepted preexisting public task", err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES('raw_lease',$1,$2,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),1,'fence')`, run, f.runner); err == nil {
		t.Fatal("unbound internal task admitted")
	}
	if _, err = f.db.Exec(ctx, `DELETE FROM task_runs WHERE id=$1`, run); err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, destinationEvent("valid_dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.CancelHostingBuild(ctx, "usr_bootstrap", b.ID, b.Revision-1, destinationEvent("stale_cancel")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	b, err = f.pg.CancelHostingBuild(ctx, "usr_bootstrap", b.ID, b.Revision, destinationEvent("cancel_dispatch"))
	if err != nil || b.State != "canceled" {
		t.Fatal(b.State, err)
	}
	var holds int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_binding_state WHERE hold_operation_id IS NOT NULL`).Scan(&holds); err != nil || holds != 0 {
		t.Fatal("build took workload mutation hold", holds, err)
	}
}

func TestPostgresHostingBuildLeaseExpiresWhileBlocked(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "expiry")
	r := sourceBuildReceipt(b)
	// An independent transaction holds the owned build row. The receipt call has
	// already locked its task/lease before it waits here. Expiry must be checked
	// after this wait, including after receipt/audit writes, against current time.
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '400 milliseconds' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = blocker.Exec(ctx, `SELECT id FROM hosting_builds WHERE id=$1 FOR UPDATE`, b.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, callErr := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "expired_source", r)
		done <- callErr
	}()
	joined := false
	defer func() {
		if !joined {
			_ = blocker.Rollback(context.Background())
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("receipt goroutine failed to join")
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SELECT revision FROM hosting_builds%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("receipt never reached owned build lock")
	}
	var expired bool
	for !expired && time.Now().Before(deadline) {
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, a.LeaseID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if !expired {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !expired {
		t.Fatal("lease did not expire")
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		joined = true
		if !errors.Is(err, store.ErrConflict) {
			t.Fatal("expired mutation accepted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked receipt failed to join")
	}
	var n int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_receipts WHERE build_id=$1`, b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("expired receipt persisted", n, err)
	}
}

func TestPostgresHostingBuildSourceAndGrantAuthorization(t *testing.T) {
	f := newBuildStoreFixture(t)
	setPrivateGitCapability(t, f, true)
	ctx := t.Context()
	for _, entry := range []struct{ id, project, role string }{{"build_viewer", "proj_platform", "viewer"}, {"build_maint", "proj_platform", "maintainer"}, {"build_owner", "proj_platform", "owner"}, {"build_cross", "proj_security", "owner"}} {
		if _, err := f.db.Exec(ctx, `INSERT INTO users(id,email,name,status,global_role) VALUES($1,$1||'@fixture.local',$1,'active','user')`, entry.id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES($1,$2,$1,$3)`, entry.id, entry.project, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	event := func(actor, id string) domain.AuditEvent { e := destinationEvent(id); e.ActorID = actor; return e }
	v2 := f.src
	v2.Generation = 2
	v2.CredentialKind = "https_basic"
	for _, actor := range []string{"build_viewer", "build_cross"} {
		if _, err := configureBuildFixtureSource(f.pg, ctx, actor, 1, v2, event(actor, "deny_source_"+actor)); err == nil {
			t.Fatalf("unauthorized source %s", actor)
		}
	}
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_maint", 1, v2, event("build_maint", "maint_source")); err != nil {
		t.Fatal(err)
	}
	f.src = v2
	item, record, audit := managedFixture(t, "build_secret", 1)
	if _, err := f.pg.CreateManagedSecret(ctx, item, record, audit); err != nil {
		t.Fatal(err)
	}
	grant := domain.HostingSourceGrant{ID: "git_grant", Version: 1, SourceID: v2.ID, ResourceID: v2.ResourceID, ProjectID: v2.ProjectID, RepositoryURL: v2.RepositoryURL, PolicyHash: domain.HostingDocumentHash(v2.Policy), SecretID: item.ID, SecretVersion: 1, Kind: "https_basic"}
	for _, actor := range []string{"build_viewer", "build_maint", "build_cross"} {
		if _, err := f.pg.CreateHostingSourceGrant(ctx, actor, grant, event(actor, "deny_grant_"+actor)); err == nil {
			t.Fatalf("unauthorized grant %s", actor)
		}
	}
	wrong := grant
	wrong.RepositoryURL = "https://example.com/other"
	if _, err := f.pg.CreateHostingSourceGrant(ctx, "build_owner", wrong, event("build_owner", "bad_repo")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	granted, err := f.pg.CreateHostingSourceGrant(ctx, "build_owner", grant, event("build_owner", "grant_owner"))
	if err != nil || granted.CreatedBy != "build_owner" {
		t.Fatal(granted, err)
	}
	input := buildInput("private")
	input.ExpectedSourceGeneration = 2
	input.ExpectedResourceGeneration = 2
	input.GrantID = grant.ID
	b, err := f.pg.RequestHostingBuild(ctx, "private", v2.ResourceID, "build_maint", input, buildPolicy(), buildEnqueue, event("build_maint", "private_request"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Snapshot.Grant == nil || b.Snapshot.Grant.SecretVersion != 1 {
		t.Fatal("grant not pinned")
	}
	serialized, _ := json.Marshal(b)
	if strings.Contains(string(serialized), "private-value") || strings.Contains(string(serialized), "ciphertext") {
		t.Fatal("secret in metadata")
	}
	// Independent cross-project source/grant check, without placement requirements.
	crossResource, crossInitial := createBuildGitResource(t, f.pg, "build_cross_secret", "proj_security", v2)
	_ = crossInitial
	cross := v2
	cross.ID = crossResource.Source.Git.SourceID
	cross.ResourceID = crossResource.ID
	cross.ProjectID = crossResource.ProjectID
	cross.Generation = 1

	crossGrant := grant
	crossGrant.ID = "cross_grant"
	crossGrant.SourceID = cross.ID
	crossGrant.ResourceID = cross.ResourceID
	crossGrant.ProjectID = cross.ProjectID
	if _, err = f.pg.CreateHostingSourceGrant(ctx, "build_cross", crossGrant, event("build_cross", "cross_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("cross-project secret accepted", err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE user_id='build_maint'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, event("build_maint", "revoked_role")); err == nil {
		t.Fatal("revoked maintainer dispatched")
	}
	if _, err = f.db.Exec(ctx, `UPDATE project_members SET role='maintainer' WHERE user_id='build_maint'`); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.RevokeHostingSourceGrant(ctx, "build_owner", grant.ID, event("build_owner", "grant_revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, event("build_maint", "revoked_grant_dispatch")); err == nil {
		t.Fatal("revoked grant dispatched")
	}
	input.IdempotencyKey = "revoked_grant"
	if _, err = f.pg.RequestHostingBuild(ctx, "revoked-grant", v2.ResourceID, "build_maint", input, buildPolicy(), buildEnqueue, event("build_maint", "revoked_grant_request")); err == nil {
		t.Fatal("revoked grant queued")
	}
	// A distinct immutable grant still cannot bypass secret revocation.
	grant.ID = "git_grant_second"
	if _, err = f.pg.CreateHostingSourceGrant(ctx, "build_owner", grant, event("build_owner", "grant_second")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	input.GrantID = grant.ID
	input.IdempotencyKey = "revoked_secret"
	if _, err = f.pg.RequestHostingBuild(ctx, "revoked-secret", v2.ResourceID, "build_maint", input, buildPolicy(), buildEnqueue, event("build_maint", "revoked_secret")); err == nil {
		t.Fatal("revoked secret queued")
	}
}

func TestPostgresHostingBuildServiceAccountAndLegacyToken(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	for _, entry := range []struct{ id, project string }{{"build_bot", "proj_platform"}, {"cross_bot", "proj_security"}} {
		if _, err := f.db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES($1,$2,'usr_bootstrap',$1,'maintainer','active')`, entry.id, entry.project); err != nil {
			t.Fatal(err)
		}
	}
	event := func(actor, id string) domain.AuditEvent { e := destinationEvent(id); e.ActorID = actor; return e }
	src := f.src
	src.Generation = 2
	src.RequestedRef = "account-v2"
	if _, err := configureBuildFixtureSource(f.pg, ctx, "cross_bot", 1, src, event("cross_bot", "cross_bot")); err == nil {
		t.Fatal("cross project service account")
	}
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_bot", 1, src, event("build_bot", "bot_source")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE service_accounts SET role='viewer' WHERE id='build_bot'`); err != nil {
		t.Fatal(err)
	}
	src.Generation = 3
	src.RequestedRef = "account-v3"
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_bot", 2, src, event("build_bot", "bot_viewer")); err == nil {
		t.Fatal("current account role ignored")
	}
	for _, entry := range []struct{ id, role string }{{"build_legacy_admin", "system_admin"}, {"build_legacy_user", "viewer"}} {
		if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES($1,$1,'service_account',$1,ARRAY[$2]::text[],'active','usr_bootstrap')`, entry.id, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_legacy_user", 2, src, event("build_legacy_user", "legacy_user")); err == nil {
		t.Fatal("legacy nonadmin elevated")
	}
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_legacy_admin", 2, src, event("build_legacy_admin", "legacy_admin")); err != nil {
		t.Fatal("legacy global admin changed", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id='build_legacy_admin'`); err != nil {
		t.Fatal(err)
	}
	src.Generation = 4
	if _, err := configureBuildFixtureSource(f.pg, ctx, "build_legacy_admin", 3, src, event("build_legacy_admin", "legacy_expired")); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestPostgresHostingBuildSelectedSecretVersionAndArtifactRevocation(t *testing.T) {
	f := newBuildStoreFixture(t)
	setPrivateGitCapability(t, f, true)
	ctx := t.Context()
	src := f.src
	src.Generation = 2
	src.CredentialKind = "https_basic"
	if _, err := configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", 1, src, destinationEvent("version_private")); err != nil {
		t.Fatal(err)
	}
	item, record, event := managedFixture(t, "version_secret", 1)
	if _, err := f.pg.CreateManagedSecret(ctx, item, record, event); err != nil {
		t.Fatal(err)
	}
	g := domain.HostingSourceGrant{ID: "version_grant", Version: 1, SourceID: src.ID, ResourceID: src.ResourceID, ProjectID: src.ProjectID, RepositoryURL: src.RepositoryURL, PolicyHash: domain.HostingDocumentHash(src.Policy), SecretID: item.ID, SecretVersion: 1, Kind: "https_basic"}
	if _, err := f.pg.CreateHostingSourceGrant(ctx, "usr_bootstrap", g, destinationEvent("version_grant")); err != nil {
		t.Fatal(err)
	}
	input := buildInput("version")
	input.ExpectedSourceGeneration = 2
	input.ExpectedResourceGeneration = 2
	input.GrantID = g.ID
	if _, err := f.pg.RequestHostingBuild(ctx, "version", src.ResourceID, "usr_bootstrap", input, buildPolicy(), buildEnqueue, destinationEvent("version_request")); err != nil {
		t.Fatal(err)
	}
	_, next, rotation := managedFixture(t, item.ID, 2)
	if _, err := f.pg.RotateManagedSecret(ctx, item.ID, 1, next, rotation); err != nil {
		t.Fatal(err)
	}
	b, a := f.activate(t, "version")
	if b.Snapshot.Grant.SecretVersion != 1 {
		t.Fatal("rotation silently changed version")
	}
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "version_source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "version-daemon")
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "version_artifact", b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ResolveCurrentHostingBuildArtifact(ctx, "usr_bootstrap", src.ResourceID, b.ID); err != nil {
		t.Fatal("active older version denied", err)
	}
	if err = f.pg.RevokeHostingSourceGrant(ctx, "usr_bootstrap", g.ID, destinationEvent("version_revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ResolveCurrentHostingBuildArtifact(ctx, "usr_bootstrap", src.ResourceID, b.ID); err == nil {
		t.Fatal("revoked grant resolved current artifact")
	}
	if _, err = f.pg.GetHostingBuild(ctx, "usr_bootstrap", b.ID); err != nil {
		t.Fatal("revocation erased history", err)
	}
}

func TestPostgresHostingBuildTargetAndStaleAttempt(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'shell') WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.RequestHostingBuild(ctx, "shell", f.src.ResourceID, "usr_bootstrap", buildInput("shell"), buildPolicy(), buildEnqueue, destinationEvent("shell_deny")); err == nil {
		t.Fatal("shell-capable host admitted")
	}
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,'shell') WHERE id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	b, a := f.start(t, "attempt")
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET status='expired',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	newer := a
	newer.LeaseID = "lease_attempt_two"
	newer.Attempt = 2
	newer.Fence = "new_fence"
	if _, err := f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),$4,$5)`, newer.LeaseID, newer.RunID, f.runner, newer.Attempt, newer.Fence); err != nil {
		t.Fatal(err)
	}
	receipt := sourceBuildReceipt(b)
	if _, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "stale_source", receipt); !errors.Is(err, store.ErrConflict) {
		t.Fatal("older attempt changed state", err)
	}
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, newer, "new_source", receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.PrepareHostingBuild(ctx, f.runner, newer, b.Revision, "daemon"); err == nil {
		t.Fatal("revoked enrollment prepared")
	}
}

func TestPostgresHostingBuildTrustedDaemonAndSchemaGuards(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	var compatible bool
	if err := f.db.QueryRow(ctx, `SELECT nerocd_hosting_build_schema_compatible()`).Scan(&compatible); err != nil || !compatible {
		t.Fatal("schema incompatible", compatible, err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port,daemon_id) VALUES('binding_build','dest_build',18081,'trusted-daemon')`); err != nil {
		t.Fatal(err)
	}
	b, a := f.start(t, "trusted")
	if b.Snapshot.ExpectedDaemonID != "trusted-daemon" || b.DaemonID != "" {
		t.Fatal("unexpected daemon snapshot")
	}
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "trusted_source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "unselected-daemon"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("trusted daemon constraint ignored", err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "trusted-daemon")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_builds SET snapshot=snapshot||'{"input_id":"changed"}'::jsonb,revision=revision+1 WHERE id=$1`, b.ID); err == nil {
		t.Fatal("mutable build snapshot")
	}
	wrong := buildArtifact(b)
	wrong.SourceID = "other-source"
	raw, _ := json.Marshal(wrong)
	if _, err = f.db.Exec(ctx, `INSERT INTO hosting_build_artifacts(build_id,body,body_hash) VALUES($1,$2,$3)`, b.ID, raw, domain.HostingDocumentHash(wrong)); err == nil {
		t.Fatal("DB accepted cross-source artifact")
	}
	if _, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id='dest_build' AND project_id='proj_platform'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingBuild(ctx, "revoked-target", f.src.ResourceID, "usr_bootstrap", buildInput("revoked-target"), buildPolicy(), buildEnqueue, destinationEvent("revoked_target")); err == nil {
		t.Fatal("revoked destination admitted")
	}
}

func TestPostgresHostingBuildCleanupReceipts(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "cleanup")
	b, err := f.pg.CancelHostingBuild(ctx, "usr_bootstrap", b.ID, b.Revision, destinationEvent("cleanup_cancel"))
	if err != nil || b.State != "cleanup_required" {
		t.Fatal(b.State, err)
	}
	cleanup := store.HostingBuildCompletion{State: "cleanup_required", FailureCode: "cleanup_unconfirmed"}
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "cleanup_unconfirmed_receipt", b.Revision, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.CompleteHostingBuild(ctx, f.runner, a, "cleanup_unconfirmed_receipt", 1, cleanup)
	if err != nil || replay.Revision != b.Revision {
		t.Fatal("cleanup replay", err)
	}
	if _, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "bad_failure_code", b.Revision, store.HostingBuildCompletion{State: "failed", FailureCode: "raw_stderr_secret"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("arbitrary diagnostic admitted", err)
	}
	terminal := store.HostingBuildCompletion{State: "canceled", FailureCode: "canceled"}
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "cleanup_confirmed", b.Revision, terminal)
	if err != nil || b.State != "canceled" {
		t.Fatal(b.State, err)
	}
	var receipts int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_build_receipts WHERE build_id=$1`, b.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("cleanup history lost", receipts, err)
	}
}

// Stop the real image consumer after migration, retaining its transactional
// enqueuer so image admission itself executes without background dispatch races.
func enableBuildFixtureImageAdmission(t *testing.T, f buildStoreFixture) {
	t.Helper()
	ctx := t.Context()
	for {
		tx, err := f.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := hostingjobs.MigrateNextTx(ctx, tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	stop, err := f.pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := stop(cleanup); stopErr != nil {
			t.Error(stopErr)
		}
	})
	if err = stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingBuildImageAdmissionLockOrder(t *testing.T) {
	for _, phase := range []string{"request", "dispatch", "source_receipt", "artifact_resolution"} {
		t.Run(phase, func(t *testing.T) {
			f := newBuildStoreFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var action func(context.Context) error
			switch phase {
			case "request":
				action = func(ctx context.Context) error {
					_, err := f.pg.RequestHostingBuild(ctx, "concurrent", f.src.ResourceID, "usr_bootstrap", buildInput("concurrent"), buildPolicy(), buildEnqueue, destinationEvent("concurrent_request"))
					return err
				}
			case "dispatch":
				b := f.request(t, "concurrent")
				action = func(ctx context.Context) error {
					_, err := f.pg.DispatchHostingBuild(ctx, b.ID, buildDispatch, destinationEvent("concurrent_dispatch"))
					return err
				}
			case "source_receipt":
				b, a := f.start(t, "concurrent")
				action = func(ctx context.Context) error {
					_, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "concurrent_source", sourceBuildReceipt(b))
					return err
				}
			case "artifact_resolution":
				b, a := f.start(t, "concurrent")
				var err error
				b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "concurrent_source", sourceBuildReceipt(b))
				if err != nil {
					t.Fatal(err)
				}
				b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "concurrent-daemon")
				if err != nil {
					t.Fatal(err)
				}
				artifact := buildArtifact(b)
				b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "concurrent_artifact", b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
				if err != nil {
					t.Fatal(err)
				}
				action = func(ctx context.Context) error {
					_, err := f.pg.ResolveCurrentHostingBuildArtifact(ctx, "usr_bootstrap", f.src.ResourceID, b.ID)
					return err
				}
			}
			enableBuildFixtureImageAdmission(t, f)
			imageResource := createDestinationResource(t, f.pg, "image_lock_order", "proj_platform")
			if _, err := f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_image_lock", ResourceID: imageResource.ID, DestinationID: "dest_build", CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("bind_image_lock")); err != nil {
				t.Fatal(err)
			}
			barrier, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			imageDone, buildDone := make(chan error, 1), make(chan error, 1)
			imageStarted, buildStarted := false, false
			defer func() {
				cancel()
				_ = barrier.Rollback(context.Background())
				for _, pending := range []struct {
					started bool
					done    <-chan error
				}{{imageStarted, imageDone}, {buildStarted, buildDone}} {
					if pending.started {
						select {
						case <-pending.done:
						case <-time.After(5 * time.Second):
							t.Error("concurrent admission failed to join")
						}
					}
				}
			}()
			if err = sqlcgen.New(barrier).LockHostingExecutionDestination(ctx, "dest_build"); err != nil {
				t.Fatal(err)
			}
			var ownerPID int
			if err = barrier.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&ownerPID); err != nil {
				t.Fatal(err)
			}
			// Queue the real image request first at its destination advisory lock. Then
			// queue each build API. Before the repair the build owns the resource while
			// waiting behind the image, causing a real image/build cycle on release.
			imageStarted = true
			go func() {
				_, imageErr := f.pg.RequestHostingDeployment(ctx, "hop_concurrent_image", imageResource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("concurrent_image"))
				imageDone <- imageErr
			}()
			waiters := func(want int) {
				t.Helper()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					var count int
					if err = f.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock'`, ownerPID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count >= want {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-ticker.C:
					}
				}
			}
			waiters(1)
			buildStarted = true
			go func() { buildDone <- action(ctx) }()
			waiters(2)
			if err = barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			imageErr := <-imageDone
			imageStarted = false
			buildErr := <-buildDone
			buildStarted = false
			if imageErr != nil || buildErr != nil {
				t.Fatalf("concurrent image/build admission failed: image=%v build=%v", imageErr, buildErr)
			}
			image, err := f.pg.GetHostingOperation(ctx, "hop_concurrent_image")
			if err != nil || image.ResourceID != imageResource.ID {
				t.Fatalf("real image request not committed: %+v %v", image, err)
			}
		})
	}
}

// Retain the independent security regression for authority expiry after writes.

func TestPostgresHostingBuildCompletionExpiresAtAuditWait(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	b, a := f.start(t, "audit-wait")
	b, err := f.pg.CommitHostingBuildSource(ctx, f.runner, a, "audit_source", sourceBuildReceipt(b))
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "audit-daemon")
	if err != nil {
		t.Fatal(err)
	}
	artifact := buildArtifact(b)
	completion := store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact}
	receiptID := "audit_success"
	auditID := "aud_build_" + domain.HostingDocumentHash(struct{ Build, Phase, Receipt string }{b.ID, "complete", receiptID})[7:]
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var blockerPID int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap','fixture','fixture','{}',clock_timestamp())`, auditID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	joined := false
	done := make(chan error, 1)
	go func() {
		_, e := f.pg.CompleteHostingBuild(ctx, f.runner, a, receiptID, b.Revision, completion)
		done <- e
	}()
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("completion failed to join")
			}
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case e := <-done:
			joined = true
			t.Fatalf("completion did not wait at audit insert: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	for {
		var expired bool
		if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, a.LeaseID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var reportErr error
	select {
	case reportErr = <-done:
		joined = true
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts, artifacts, audits int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_build_receipts WHERE build_id=$1 AND phase='complete'),(SELECT count(*) FROM hosting_build_artifacts WHERE build_id=$1),(SELECT count(*) FROM audit_events WHERE id=$2)`, b.ID, auditID).Scan(&receipts, &artifacts, &audits); err != nil {
		t.Fatal(err)
	}
	t.Logf("expired during audit wait: error=%v state=%s revision=%d expected_revision=%d complete_receipts=%d artifacts=%d audits=%d", reportErr, after.State, after.Revision, b.Revision, receipts, artifacts, audits)
	if !errors.Is(reportErr, store.ErrConflict) || after.State != "building" || after.Revision != b.Revision || receipts != 0 || artifacts != 0 || audits != 0 {
		t.Fatal("expired post-write completion escaped atomic rollback")
	}
}

func TestPostgresHostingBuildRejectsPlacementChangedDuringDestinationWait(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	server, dest := createDestinationTarget(t, f.pg, "replacement_build")
	if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_build_v1') WHERE id=$1`, server.RunnerID); err != nil {
		t.Fatal(err)
	}
	grantDestination(t, f.pg, dest.ID, f.src.ProjectID, "replacement_grant")
	// Placement is immutable in production. Disable only this isolated schema's
	// identity trigger to model a stale read during administrative restoration.
	if _, err := f.db.Exec(ctx, `ALTER TABLE resource_bindings DISABLE TRIGGER resource_bindings_immutable`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := f.db.Exec(cleanup, `ALTER TABLE resource_bindings ENABLE TRIGGER resource_bindings_immutable`); err != nil {
			t.Error(err)
		}
	})
	barrier, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	started := false
	defer func() {
		cancel()
		_ = barrier.Rollback(context.Background())
		if started {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("stale placement request failed to join")
			}
		}
	}()
	if err = sqlcgen.New(barrier).LockHostingExecutionDestination(ctx, "dest_build"); err != nil {
		t.Fatal(err)
	}
	var ownerPID int
	if err = barrier.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&ownerPID); err != nil {
		t.Fatal(err)
	}
	started = true
	go func() {
		_, requestErr := f.pg.RequestHostingBuild(ctx, "stale-placement", f.src.ResourceID, "usr_bootstrap", buildInput("stale-placement"), buildPolicy(), buildEnqueue, destinationEvent("stale_placement"))
		done <- requestErr
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, ownerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if _, err = f.db.Exec(ctx, `UPDATE resource_bindings SET destination_id=$1 WHERE id='binding_build'`, dest.ID); err != nil {
		t.Fatal(err)
	}
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requestErr := <-done
	started = false
	if !errors.Is(requestErr, store.ErrConflict) {
		t.Fatalf("stale placement authorized a new target: %v", requestErr)
	}
	var builds int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds WHERE id='stale-placement'`).Scan(&builds); err != nil || builds != 0 {
		t.Fatal("stale placement persisted", builds, err)
	}
}

func TestPostgresHostingBuildFunctionsUseOwningSchema(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	b, a := f.start(t, "schema-bound")
	other := enrollDestinationRunner(t, f.pg, "runner_schema_other", []string{domain.RunTypeComposeDeploy})
	var schema string
	if err := f.db.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	qualified := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	shadow := schema + "_shadow"
	shadowID := pgx.Identifier{shadow}.Sanitize()
	if _, err := f.db.Exec(ctx, "CREATE SCHEMA "+shadowID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.db.Exec(cleanup, "DROP TABLE IF EXISTS "+shadowID+".task_runs,"+shadowID+".hosting_build_tasks,"+shadowID+".hosting_builds; DROP FUNCTION IF EXISTS "+shadowID+".jsonb_build_object(text[]),"+shadowID+".to_regclass(text); DROP SCHEMA "+shadowID); err != nil {
			t.Error(err)
		}
	})
	// Explicit owned fixture shadow objects model an unrelated caller schema.
	for _, ddl := range []string{
		"CREATE TABLE " + shadowID + ".task_runs(id text,run_spec jsonb)",
		"CREATE TABLE " + shadowID + ".hosting_build_tasks(run_id text,build_id text,runner_id text)",
		"CREATE TABLE " + shadowID + ".hosting_builds(id text,state text,run_id text)",
		"CREATE FUNCTION " + shadowID + ".jsonb_build_object(VARIADIC text[]) RETURNS jsonb LANGUAGE sql IMMUTABLE AS 'SELECT ''{}''::jsonb'",
		"CREATE FUNCTION " + shadowID + ".to_regclass(text) RETURNS regclass LANGUAGE sql IMMUTABLE AS 'SELECT NULL::regclass'",
	} {
		if _, err := f.db.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(ctx, "INSERT INTO "+shadowID+".task_runs(id,run_spec) VALUES($1,'{\"type\":\"shell\"}')", b.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET status='expired' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at) VALUES('schema_shell','proj_platform','{"type":"shell"}','{}','{}','{}','queued','usr_bootstrap',clock_timestamp(),clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES('schema_shell_lease','schema_shell',$1,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),1,'shell_fence')`, f.runner); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ name, path string }{{"unrelated", "pg_catalog,pg_temp"}, {"shadow", shadowID + ",pg_catalog,pg_temp"}, {"temporary", "pg_temp,pg_catalog"}} {
		t.Run(entry.name, func(t *testing.T) {
			run := func(fn func(pgx.Tx) error) error {
				tx, err := f.db.Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(ctx) }()
				if _, err = tx.Exec(ctx, "SET LOCAL search_path="+entry.path); err != nil {
					return err
				}
				if entry.name == "temporary" {
					if _, err = tx.Exec(ctx, `CREATE TEMP TABLE task_runs(id text,run_spec jsonb) ON COMMIT DROP; CREATE TEMP TABLE hosting_build_tasks(run_id text,build_id text,runner_id text) ON COMMIT DROP; CREATE TEMP TABLE hosting_builds(id text,state text,run_id text) ON COMMIT DROP`); err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `INSERT INTO task_runs(id,run_spec) VALUES($1,'{"type":"shell"}')`, b.RunID); err != nil {
						return err
					}
				}
				return fn(tx)
			}
			t.Run("nonbuild_update", func(t *testing.T) {
				if err := run(func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, "UPDATE "+qualified("run_leases")+" SET expires_at=clock_timestamp()-interval '1 second' WHERE id='schema_shell_lease'")
					return err
				}); err != nil {
					t.Fatal("schema-qualified nonbuild update inherited caller path", err)
				}
			})
			insert := func(runner string) error {
				return run(func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, "INSERT INTO "+qualified("run_leases")+"(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES('schema_build_next',$1,$2,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),2,'schema_fence')", b.RunID, runner)
					return err
				})
			}
			t.Run("correct_build", func(t *testing.T) {
				if err := insert(f.runner); err != nil {
					t.Fatal("correct bound build denied", err)
				}
			})
			t.Run("wrong_target", func(t *testing.T) {
				err := insert(other.ID)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
					t.Fatalf("wrong-target lease not rejected by bound build predicate: %v", err)
				}
			})
			t.Run("compatibility", func(t *testing.T) {
				err := run(func(tx pgx.Tx) error {
					var ok bool
					if err := tx.QueryRow(ctx, "SELECT "+qualified("nerocd_hosting_build_schema_compatible")+"()").Scan(&ok); err != nil {
						return err
					}
					if !ok {
						return errors.New("owning schema compatibility resolved caller objects")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// removeHostingBuildMigrationLayer removes only the additive hosting/runtime
// dependencies above 0046 so older missing-layer tests retain their assertions.
func removeHostingBuildMigrationLayer(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `DROP TABLE hosting_proxy_recovery_resolutions,hosting_proxy_recovery_plans;
 DROP FUNCTION nerocd_hosting_proxy_recovery_consistent(),nerocd_hosting_proxy_metadata_recovery_schema_compatible();
 DROP TRIGGER hosting_proxy_switch_operation_consistent ON hosting_proxy_operations;
 DROP TABLE hosting_proxy_switch_prior_references;
 DROP FUNCTION nerocd_hosting_verified_switch_consistent(),nerocd_hosting_verified_route_switch_schema_compatible();
 ALTER TABLE hosting_proxy_release_references DROP CONSTRAINT hosting_proxy_release_reference_identity;
 DROP TABLE server_readiness; DROP FUNCTION nerocd_server_readiness_schema_compatible();`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `DROP TABLE hosting_auto_deploy_origins,hosting_auto_deploy_demands,hosting_auto_deploy_controls;
 DROP FUNCTION nerocd_hosting_auto_deploy_schema_compatible();`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `DROP TABLE hosting_github_push_commit_intents,hosting_github_push_deliveries,hosting_github_push_commands,hosting_github_push_policies; DROP FUNCTION nerocd_hosting_github_push_schema_compatible();
DROP TABLE hosting_runtime_log_events,hosting_runtime_log_sessions; DROP FUNCTION nerocd_runtime_log_identity(),nerocd_hosting_runtime_log_schema_compatible();
 DROP TABLE hosting_runtime_observations; DROP FUNCTION nerocd_hosting_observation_schema_compatible();
 DROP TRIGGER hosting_runtime_environment_lease ON run_leases;
 DROP FUNCTION nerocd_hosting_runtime_environment_lease(),nerocd_hosting_runtime_environment_schema_compatible();
 ALTER TABLE hosting_runtime_configurations DROP CONSTRAINT hosting_runtime_current_version_fk;
 DROP TABLE hosting_runtime_configuration_receipts,hosting_runtime_configuration_secret_refs,hosting_runtime_configuration_versions,hosting_runtime_configurations;
 DROP FUNCTION nerocd_hosting_runtime_document_valid(jsonb),nerocd_hosting_runtime_immutable(),nerocd_hosting_runtime_pointer_transition(),nerocd_hosting_runtime_ref_creation(),nerocd_hosting_runtime_version_consistent(),nerocd_hosting_runtime_configuration_schema_compatible();
 DROP TRIGGER hosting_build_log_dispatch ON hosting_build_tasks;
 DROP TABLE hosting_build_log_receipts,hosting_build_log_states;
 DROP FUNCTION nerocd_build_log_dispatch(),nerocd_build_log_immutable(),nerocd_hosting_build_log_schema_compatible();
 ALTER TABLE run_logs DROP COLUMN component,DROP COLUMN source_timestamp;
 DROP TRIGGER local_build_deployment_lease ON run_leases;
 DROP TRIGGER hosting_snapshot_build_consistency ON hosting_snapshots;
 DROP TRIGGER hosting_release_build_consistency ON hosting_releases;
 DROP TABLE hosting_operation_build_artifacts;
 DROP FUNCTION nerocd_hosting_build_deployment_consistent(),nerocd_local_build_deployment_lease(),nerocd_hosting_build_deployment_schema_compatible();
 DROP TRIGGER resources_git_reference ON resources;
 DROP TRIGGER resources_source_kind ON resources;
 DROP TRIGGER hosting_sources_resource_reference ON hosting_build_sources;
 DROP TRIGGER hosting_source_versions_resource_reference ON hosting_build_source_versions;
 DROP FUNCTION nerocd_hosting_git_reference_consistent(),nerocd_hosting_source_kind_immutable(),nerocd_hosting_git_resource_schema_compatible();
 DROP TRIGGER hosting_build_lease_admission ON run_leases;
 DROP TABLE hosting_build_artifacts,hosting_build_receipts,hosting_build_tasks,hosting_builds;
 ALTER TABLE hosting_build_sources DROP CONSTRAINT hosting_build_source_current_fk;
 DROP TABLE hosting_source_grants,hosting_build_source_versions,hosting_build_sources;
 DROP FUNCTION nerocd_hosting_build_source_update(),nerocd_hosting_source_grant_update(),nerocd_hosting_build_update(),nerocd_hosting_build_lease_target(),nerocd_hosting_build_source_body(),nerocd_hosting_build_artifact_body(),nerocd_hosting_build_schema_compatible();
 ALTER TABLE resources DROP CONSTRAINT resources_build_project_key;
 ALTER TABLE project_managed_secrets DROP CONSTRAINT managed_secrets_build_project_key;`); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingBuildSchemaBindingRestoreAndReplay(t *testing.T) {
	container := os.Getenv("NEROCD_TEST_PG_CONTAINER")
	if container == "" {
		t.Skip("set task-owned NEROCD_TEST_PG_CONTAINER for real pg_dump/pg_restore")
	}
	f := newBuildStoreFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	b, a := f.start(t, "schema-restore")
	other := enrollDestinationRunner(t, f.pg, "runner_restore_other", []string{domain.RunTypeComposeDeploy})
	var schema string
	if err := f.db.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if err != nil || parsed.User == nil {
		t.Fatal("invalid fixture DSN")
	}
	dump := exec.CommandContext(ctx, "docker", "exec", container, "pg_dump", "--username", parsed.User.Username(), "--dbname", strings.TrimPrefix(parsed.Path, "/"), "--schema", schema, "--format=custom", "--no-owner", "--no-privileges")
	archive, err := dump.Output()
	if err != nil {
		t.Fatal("owned schema dump failed", err)
	}
	targetName := fmt.Sprintf("nerocd_build_restore_%x", time.Now().UnixNano())
	targetID := pgx.Identifier{targetName}.Sanitize()
	if _, err = f.db.Exec(ctx, "CREATE DATABASE "+targetID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := f.db.Exec(cleanup, "DROP DATABASE "+targetID+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	restore := exec.CommandContext(ctx, "docker", "exec", "-i", container, "pg_restore", "--username", parsed.User.Username(), "--dbname", targetName, "--no-owner", "--no-privileges", "--exit-on-error")
	restore.Stdin = bytes.NewReader(archive)
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("owned schema restore failed: %v: %.1000s", err, output)
	}
	config := f.db.Config().Copy()
	config.ConnConfig.Database = targetName
	target, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(target.Close)
	qualified := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	for _, phase := range []string{"restored", "replayed"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "replayed" {
				if err := applyEmbeddedMigrations(ctx, target, "0047_hosting_builds.sql", "0048_hosting_build_schema_binding.sql"); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := target.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err = tx.Exec(ctx, `SET LOCAL search_path=pg_catalog,pg_temp`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "UPDATE "+qualified("run_leases")+" SET status='expired',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", a.LeaseID); err != nil {
				t.Fatal("restored lease lookup escaped owning schema", err)
			}
			var gitCompatible bool
			if err = tx.QueryRow(ctx, "SELECT "+qualified("nerocd_hosting_git_resource_schema_compatible")+"()").Scan(&gitCompatible); err != nil || !gitCompatible {
				t.Fatal("restored0049 schema binding", gitCompatible, err)
			}
			var exact bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+qualified("resources")+" r JOIN "+qualified("hosting_build_sources")+" h ON h.id=r.source->'git'->>'source_id' AND h.generation=(r.source->'git'->>'generation')::bigint WHERE r.id=$1)", f.src.ResourceID).Scan(&exact); err != nil || !exact {
				t.Fatal("restored reference lost exact source", exact, err)
			}
			var compatible bool
			if err = tx.QueryRow(ctx, "SELECT "+qualified("nerocd_hosting_build_schema_compatible")+"()").Scan(&compatible); err != nil || !compatible {
				t.Fatal("restored compatibility lost owning schema", compatible, err)
			}
			_, err = tx.Exec(ctx, "INSERT INTO "+qualified("run_leases")+"(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES('restored_wrong_target',$1,$2,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),2,'restored_fence')", b.RunID, other.ID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("restored wrong-target lease admitted: %v", err)
			}
		})
	}
}

func createBuildGitResource(t *testing.T, pg *store.PostgresStore, id, project string, config domain.HostingGitSource) (domain.Resource, domain.HostingGitSource) {
	t.Helper()
	now := time.Now().UTC()
	env, err := pg.CreateProjectEnvironment(t.Context(), domain.ProjectEnvironment{ID: "env_" + id, ProjectID: project, Name: id, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if config.RepositoryURL == "" {
		config = domain.HostingGitSource{RepositoryURL: "https://example.com/repo.git", RequestedRef: "main", CredentialKind: "public", Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}}, Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}
	}
	config.ID = "source_" + id
	config.ResourceID = "resource_" + id
	config.ProjectID = project
	config.Generation = 1
	item, err := pg.CreateResource(t.Context(), domain.Resource{ID: config.ResourceID, ProjectID: project, ProjectEnvironmentID: env.ID, Name: id, Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: config.ID, Generation: 1}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, store.WithHostingGitSource(config), store.WithAudit(destinationEvent("create_"+id)))
	if err != nil {
		t.Fatal(err)
	}
	return item, config
}
func configureBuildFixtureSource(pg *store.PostgresStore, ctx context.Context, actor string, expected int64, src domain.HostingGitSource, event domain.AuditEvent) (domain.HostingGitSource, error) {
	item, err := pg.GetResource(ctx, src.ResourceID)
	if err != nil {
		return src, err
	}
	if item.Source.Kind != "git" || item.Source.Git.Generation != expected || src.Generation != expected+1 {
		return src, store.ErrConflict
	}
	src.Generation = expected
	event.ActorID = actor
	item.UpdatedAt = time.Now().UTC()
	updated, err := pg.UpdateResource(ctx, item, item.Generation, store.WithHostingGitSource(src), store.WithAudit(event))
	if err == nil {
		src.Generation = updated.Source.Git.Generation
	}
	return src, err
}
