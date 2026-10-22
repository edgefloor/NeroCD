package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/source"
	"nerocd/internal/store"
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
	resource := createDestinationResource(t, pg, "build_web", "proj_platform")
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
	src := domain.HostingGitSource{ID: "source_build", ResourceID: resource.ID, ProjectID: resource.ProjectID, Generation: 1, RepositoryURL: "https://example.com/repo.git", RequestedRef: "main", CredentialKind: "public", Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}}, Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}
	if _, err = pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 0, src, destinationEvent("source_build")); err != nil {
		t.Fatalf("fixture step 6: %v", err)
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
	if _, err = f.pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 1, src, destinationEvent("source_v2")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 1, src, destinationEvent("source_stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_build_source_versions SET body='{}' WHERE source_id=$1`, src.ID); err == nil {
		t.Fatal("mutable source history")
	}
	var generation int
	if err = f.db.QueryRow(ctx, `SELECT generation FROM resources WHERE id=$1`, src.ResourceID).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("sidecar changed resource", generation, err)
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
	if _, err = f.pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 1, next, destinationEvent("source_next")); err != nil {
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
	if err != nil || b.State != "cleanup_required" {
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
		if _, err := f.pg.ConfigureHostingBuildSource(ctx, actor, 1, v2, event(actor, "deny_source_"+actor)); err == nil {
			t.Fatalf("unauthorized source %s", actor)
		}
	}
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_maint", 1, v2, event("build_maint", "maint_source")); err != nil {
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
	crossResource := createDestinationResource(t, f.pg, "build_cross_secret", "proj_security")
	cross := v2
	cross.ID = "cross_source"
	cross.ResourceID = crossResource.ID
	cross.ProjectID = crossResource.ProjectID
	cross.Generation = 1
	if _, err = f.pg.ConfigureHostingBuildSource(ctx, "build_cross", 0, cross, event("build_cross", "cross_source")); err != nil {
		t.Fatal(err)
	}
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
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "cross_bot", 1, src, event("cross_bot", "cross_bot")); err == nil {
		t.Fatal("cross project service account")
	}
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_bot", 1, src, event("build_bot", "bot_source")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE service_accounts SET role='viewer' WHERE id='build_bot'`); err != nil {
		t.Fatal(err)
	}
	src.Generation = 3
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_bot", 2, src, event("build_bot", "bot_viewer")); err == nil {
		t.Fatal("current account role ignored")
	}
	for _, entry := range []struct{ id, role string }{{"build_legacy_admin", "system_admin"}, {"build_legacy_user", "viewer"}} {
		if _, err := f.db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by) VALUES($1,$1,'service_account',$1,ARRAY[$2]::text[],'active','usr_bootstrap')`, entry.id, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_legacy_user", 2, src, event("build_legacy_user", "legacy_user")); err == nil {
		t.Fatal("legacy nonadmin elevated")
	}
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_legacy_admin", 2, src, event("build_legacy_admin", "legacy_admin")); err != nil {
		t.Fatal("legacy global admin changed", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE id='build_legacy_admin'`); err != nil {
		t.Fatal(err)
	}
	src.Generation = 4
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "build_legacy_admin", 3, src, event("build_legacy_admin", "legacy_expired")); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestPostgresHostingBuildSelectedSecretVersionAndArtifactRevocation(t *testing.T) {
	f := newBuildStoreFixture(t)
	ctx := t.Context()
	src := f.src
	src.Generation = 2
	src.CredentialKind = "https_basic"
	if _, err := f.pg.ConfigureHostingBuildSource(ctx, "usr_bootstrap", 1, src, destinationEvent("version_private")); err != nil {
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
