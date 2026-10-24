package runnerruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
)

// This opt-in fixture exercises the accepted API/service/PostgreSQL controller
// and River dispatcher. Its engine is a fake: it provides protocol evidence,
// not Docker, archive-byte, application, or physical workflow qualification.
type archiveControllerFixture struct {
	t            *testing.T
	ctx          context.Context
	pg           *store.PostgresStore
	pool         *pgxpool.Pool
	server       *httptest.Server
	client       runnerclient.Client
	owner        string
	resource     domain.Resource
	helper       domain.HostingStorageArchiveHelper
	mu           sync.Mutex
	losePhase    string
	loseComplete bool
	received     []domain.HostingStorageArchiveProgress
}

func archiveControllerMust[T any](value T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

func archiveControllerInventory(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var inventory string
	err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(datname ORDER BY datname) FROM pg_database),
 (SELECT jsonb_agg(nspname ORDER BY nspname) FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname <> 'information_schema'),
 (SELECT jsonb_agg(n.nspname||'.'||c.relname||':'||c.relkind::text ORDER BY n.nspname,c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'))::text`).Scan(&inventory)
	return inventory, err
}

func newArchiveControllerFixture(t *testing.T) *archiveControllerFixture {
	t.Helper()
	raw := os.Getenv("NEROCD_ARCHIVE_CONTROLLER_DATABASE_URL")
	if raw == "" {
		t.Skip("requires an explicitly authorized disposable archive controller PostgreSQL fixture")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "postgres" || parsed.Host != "127.0.0.1:32967" || parsed.Path != "/nerocd_auto_deploy" || parsed.User == nil || parsed.User.Username() != "nerocd" || parsed.Query().Get("sslmode") != "disable" || len(parsed.Query()) != 1 {
		t.Fatal("archive controller fixture database profile mismatch")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	config := archiveControllerMust(pgxpool.ParseConfig(raw))(t)
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_archive_protocol_admin"
	admin := archiveControllerMust(pgxpool.NewWithConfig(ctx, config))(t)
	t.Cleanup(admin.Close)
	var database, user string
	var other int
	if err := admin.QueryRow(ctx, `SELECT current_database(),current_user,(SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid())`).Scan(&database, &user, &other); err != nil || database != "nerocd_auto_deploy" || user != "nerocd" || other != 0 {
		t.Fatal("archive controller fixture is not the exclusive expected database")
	}
	inventory := archiveControllerMust(archiveControllerInventory(ctx, admin))(t)
	sum := sha256.Sum256([]byte(inventory))
	t.Logf("fixture identity database=%s user=%s other_clients=%d inventory_sha256=%x", database, user, other, sum)
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	schema := "nerocd_archive_protocol_" + hex.EncodeToString(nonce)
	quoted := pgx.Identifier{schema}.Sanitize()
	var pool *pgxpool.Pool
	var pg *store.PostgresStore
	var stopJobs func(context.Context) error
	var server *httptest.Server
	// Register cleanup before CREATE and before every fallible setup operation.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if server != nil {
			server.Close()
		}
		if stopJobs != nil {
			if err := stopJobs(cleanup); err != nil {
				t.Error("archive fixture queue did not join", err)
			}
		}
		if pg != nil {
			if err := pg.Close(); err != nil {
				t.Error("archive fixture store close", err)
			}
		}
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(cleanup, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); err != nil {
			t.Error("archive owned schema cleanup failed", err)
			return
		}
		var absent bool
		if err := admin.QueryRow(cleanup, `SELECT to_regnamespace($1) IS NULL`, schema).Scan(&absent); err != nil || !absent {
			t.Error("archive owned schema absence not proven")
		}
		after, err := archiveControllerInventory(cleanup, admin)
		if err != nil || after != inventory {
			t.Error("archive fixture foreign database/schema inventory changed")
		}
		t.Logf("fixture cleanup schema=%s absent=%v inventory_equal=%v", schema, absent, after == inventory)
	})
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	query.Set("application_name", "nerocd_archive_protocol_owned")
	parsed.RawQuery = query.Encode()
	pool = archiveControllerMust(pgxpool.New(ctx, parsed.String()))(t)
	var current string
	if err = pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&current); err != nil || current != schema {
		t.Fatal("archive owned schema not isolated")
	}
	entries := archiveControllerMust(db.Files.ReadDir("migrations"))(t)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		migration := archiveControllerMust(db.Files.ReadFile("migrations/" + entry.Name()))(t)
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("archive migration %s: %v", entry.Name(), err)
		}
	}
	seed := archiveControllerMust(os.ReadFile("../../db/seeds/dev.sql"))(t)
	if _, err = pool.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg = archiveControllerMust(store.OpenPostgres(ctx, parsed.String()))(t)
	helper := archiveRuntimePlan(t).Helper
	if err = pg.ConfigureStorageArchive(&helper, archiveRuntimePlan(t).Limits); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		tx := archiveControllerMust(pool.Begin(ctx))(t)
		changed, e := hostingjobs.MigrateNextTx(ctx, tx)
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
		if i == 99 {
			t.Fatal("queue migrations did not terminate")
		}
	}
	stopJobs = archiveControllerMust(pg.StartHostingExecution(ctx))(t)
	service := archiveControllerMust(app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg}))(t)
	f := &archiveControllerFixture{t: t, ctx: ctx, pg: pg, pool: pool, helper: helper}
	handler := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/storage-archive/progress" {
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var p domain.HostingStorageArchiveProgress
			if json.Unmarshal(raw, &p) != nil {
				t.Error("invalid runner progress")
				w.WriteHeader(400)
				return
			}
			f.mu.Lock()
			f.received = append(f.received, p)
			lose := f.losePhase == p.Phase
			if lose {
				f.losePhase = ""
			}
			f.mu.Unlock()
			if lose {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Errorf("receipt not committed before lost ACK: %d", rec.Code)
				}
				w.WriteHeader(503)
				return
			}
		}
		if r.URL.Path == "/api/v1/runners/hosting/storage-archive/complete" {
			f.mu.Lock()
			lose := f.loseComplete
			f.loseComplete = false
			f.mu.Unlock()
			if lose {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, r)
				if rec.Code != http.StatusOK {
					t.Errorf("completion not committed before lost ACK: %d", rec.Code)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	f.server = server
	var session struct {
		Token string `json:"token"`
	}
	f.json("POST", "/api/v1/sessions", "", map[string]string{"email": "admin@example.local", "password": "admin"}, 201, &session)
	f.owner = session.Token
	var enrolled app.CreatedRunnerEnrollment
	f.json("POST", "/api/v1/runner-enrollments", f.owner, app.RunnerEnrollmentInput{RunnerID: "runner_archive_protocol", RunnerName: "archive protocol", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingOwnedStorageCapability, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability}}, 201, &enrolled)
	token := "archive_protocol_fixture_only_credential"
	digest := sha256.Sum256([]byte(token))
	var consumed app.ConsumedRunnerEnrollment
	f.json("POST", "/api/v1/runner-enrollments/consume", enrolled.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(digest[:])}, 200, &consumed)
	f.client = runnerclient.New(server.URL, token)
	f.client.HTTP = server.Client()
	f.seedSource()
	return f
}

func (f *archiveControllerFixture) json(method, path, token string, input any, status int, out any) {
	f.t.Helper()
	raw := archiveControllerMust(json.Marshal(input))(f.t)
	request := archiveControllerMust(http.NewRequestWithContext(f.ctx, method, f.server.URL+path, bytes.NewReader(raw)))(f.t)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := archiveControllerMust(f.server.Client().Do(request))(f.t)
	defer func() {
		if err := response.Body.Close(); err != nil {
			f.t.Error(err)
		}
	}()
	if response.StatusCode != status {
		f.t.Fatalf("actual controller %s %s status=%d want=%d", method, path, response.StatusCode, status)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *archiveControllerFixture) seedSource() {
	t := f.t
	ctx := f.ctx
	now := time.Now().UTC()
	audit := func(id string) domain.AuditEvent {
		return domain.AuditEvent{ID: "aud_archive_fixture_" + id, ActorID: "usr_bootstrap", CreatedAt: now, Metadata: map[string]any{}}
	}
	_, dest, err := f.pg.CreateServerWithDestination(ctx, domain.Server{ID: "server_archive_protocol", Name: "archive protocol", RunnerID: "runner_archive_protocol", CreatedBy: "usr_bootstrap", CreatedAt: now}, domain.Destination{ID: "destination_archive_protocol", ServerID: "server_archive_protocol", Name: "archive protocol", RuntimeKind: "docker_compose", CreatedAt: now}, audit("server"))
	if err != nil {
		t.Fatal(err)
	}
	env := archiveControllerMust(f.pg.CreateProjectEnvironment(ctx, domain.ProjectEnvironment{ID: "env_archive_protocol", ProjectID: "proj_platform", Name: "archive protocol", CreatedAt: now}))(t)
	resource := archiveControllerMust(f.pg.CreateResource(ctx, domain.Resource{ID: "resource_archive_protocol", ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "archive protocol", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}))(t)
	_ = archiveControllerMust(f.pg.GrantDestinationProject(ctx, domain.DestinationProjectGrant{DestinationID: dest.ID, ProjectID: resource.ProjectID, GrantedBy: "usr_bootstrap", CreatedAt: now}, audit("grant")))(t)
	_ = archiveControllerMust(f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_archive_protocol", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: "usr_bootstrap", CreatedAt: now}, audit("binding")))(t)
	storage := archiveControllerMust(f.pg.CreateHostingStorage(ctx, "usr_bootstrap", resource.ID, domain.HostingStorageInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, MountPath: "/uploads"}, audit("storage")))(t)
	for i, container := range []string{strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64)} {
		f.seedImage(resource, storage, fmt.Sprintf("source_%d", i), container)
	}
	f.resource = resource
}

func (f *archiveControllerFixture) seedImage(resource domain.Resource, storage domain.HostingStorage, id, container string) {
	t, ctx := f.t, f.ctx
	audit := domain.AuditEvent{ID: "aud_archive_fixture_" + id, ActorID: "usr_bootstrap", CreatedAt: time.Now().UTC(), Metadata: map[string]any{}}
	op := archiveControllerMust(f.pg.RequestHostingDeployment(ctx, "hop_archive_fixture_"+id, resource.ID, "usr_bootstrap", domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "archive_fixture_" + id, ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, audit))(t)
	if err := f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim := archiveControllerMust(f.pg.ClaimRun(ctx, "runner_archive_protocol", time.Now(), time.Minute))(t)
	a := hostingAuthority(claim.Run.ID, op.ID, claim.Lease)
	_ = archiveControllerMust(f.pg.HostingRunnerPlan(ctx, "runner_archive_protocol", a))(t)
	release := archiveRuntimePlan(t).SourceHealthy.Release
	release.DaemonID = f.helper.DaemonID
	release.Hash = ""
	release.Hash = domain.HostingDocumentHash(release)
	op = archiveControllerMust(f.pg.HostingPrepared(ctx, "runner_archive_protocol", domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: id + "_prepared", Release: release}))(t)
	ack := archiveControllerMust(f.pg.HostingStorageCreateBegin(ctx, "runner_archive_protocol", domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: storage.Reference(), CreationNonce: op.Plan.Storage.CreationNonce}))(t)
	m := ack.Materialization
	_ = archiveControllerMust(f.pg.HostingStorageMaterialized(ctx, "runner_archive_protocol", domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: id + "_materialized", PlanHash: op.Plan.Hash, StorageRef: storage.Reference(), CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}))(t)
	_ = archiveControllerMust(f.pg.HostingApplyBegin(ctx, "runner_archive_protocol", a))(t)
	_ = archiveControllerMust(f.pg.HostingComplete(ctx, "runner_archive_protocol", domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: id + "_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: container, ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}}))(t)
}

func (f *archiveControllerFixture) backup(request string) domain.HostingStorageBackup {
	f.t.Helper()
	var backup domain.HostingStorageBackup
	f.json("POST", "/api/v1/resources/"+f.resource.ID+"/storage-backups", f.owner, domain.HostingStorageBackupInput{RequestID: request, ExpectedResourceGeneration: f.resource.Generation}, 202, &backup)
	return backup
}

func (f *archiveControllerFixture) coordinator(operation string, j *runner.AttemptJournal) (*archiveCoordinator, domain.HostingOperation, *archiveRuntimeEngine) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		op := archiveControllerMust(f.pg.GetHostingOperation(f.ctx, operation))(f.t)
		if op.RunID != "" {
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatal("real River archive dispatch timed out")
		}
		select {
		case <-f.ctx.Done():
			f.t.Fatal("fixture deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}
	ctx := runnerclient.WithStorageArchiveTransport(f.ctx, runnerclient.StorageArchiveTransport{})
	claim := archiveControllerMust(f.client.Claim(ctx))(f.t)
	if claim.Run.RunSpec.HostingOperationID != operation || domain.ValidateStorageArchiveTask(claim.Run) != nil {
		f.t.Fatal("actual typed claim mismatch")
	}
	a := hostingAuthority(claim.Run.ID, operation, claim.Lease)
	op := archiveControllerMust(f.client.HostingStorageArchivePlan(ctx, a))(f.t)
	if j == nil {
		j = archiveControllerMust(runner.OpenAttemptJournal(filepath.Join(f.t.TempDir(), "journal")))(f.t)
		f.t.Cleanup(func() {
			if err := j.Close(); err != nil {
				f.t.Error(err)
			}
		})
	}
	s := newAttemptSupervisorFrom(ctx, claim.Lease)
	f.t.Cleanup(s.Close)
	engine := &archiveRuntimeEngine{t: f.t, j: j}
	for _, h := range op.StorageArchive.Plan.Writers {
		engine.writers = append(engine.writers, domain.HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, ReleaseHash: h.Release.Hash, PlanHash: h.PlanHash, Running: true})
	}
	if op.StorageArchive.Plan.Target != nil {
		p := op.StorageArchive.Plan
		target := p.Target
		engine.restoreMaterialization = &domain.HostingStorageMaterialization{StorageRef: target.Storage.Reference(), State: "materialized", CreationOperationID: target.Plan.CreationOperationID, CreationPlanHash: p.Hash, CreationNonce: target.Plan.CreationNonce, DaemonID: p.Helper.DaemonID}
		engine.restoreMaterialization.Labels = domain.ExpectedHostingStorageLabels(target.Storage, *engine.restoreMaterialization)
	}
	x := &archiveCoordinator{s: s, j: j, c: f.client, e: engine, a: a, lease: claim.Lease, p: op.StorageArchive.Plan, drain: func(context.Context) error { return nil }}
	return x, op, engine
}

func TestStorageArchiveActualControllerBackupAndRestore(t *testing.T) {
	f := newArchiveControllerFixture(t)
	backup := f.backup(strings.Repeat("1", 32))
	x, op, e := f.coordinator(backup.OperationID, nil)
	if len(e.writers) != 3 {
		t.Fatalf("accepted writer inventory=%d", len(e.writers))
	}
	e.writers[2].Running = false
	if err := x.execute(op); err != nil {
		t.Fatalf("backup actual-controller boundary: %v", err)
	}
	settled := archiveControllerMust(f.pg.GetHostingOperation(f.ctx, op.ID))(t)
	if settled.State != "succeeded" || settled.MutationHold || settled.StorageArchive.Result == nil || settled.StorageArchive.Result.Blob == nil || len(x.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("backup did not settle")
	}
	if !slices.Equal(e.effects, []string{"stop:" + e.writers[0].ContainerID, "stop:" + e.writers[1].ContainerID, "create", "backup", "cleanup", "resume:" + e.writers[0].ContainerID, "resume:" + e.writers[1].ContainerID}) {
		t.Fatalf("backup effects=%v", e.effects)
	}
	var restored domain.HostingStorageRestore
	f.json("POST", "/api/v1/storage-backups/"+backup.ID+"/restores", f.owner, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("2", 32), Name: "restored protocol"}, 202, &restored)
	y, restoreOp, restoreEngine := f.coordinator(restored.Operation.ID, nil)
	if err := y.execute(restoreOp); err != nil {
		t.Fatalf("restore actual-controller boundary: %v", err)
	}
	settled = archiveControllerMust(f.pg.GetHostingOperation(f.ctx, restoreOp.ID))(t)
	if settled.State != "succeeded" || settled.MutationHold || settled.RestoreOwnershipHeld || settled.StorageArchive.Result == nil || !settled.StorageArchive.Result.RestoreVerified || domain.HostingDocumentHash(settled.StorageArchive.Result.Blob) != domain.HostingDocumentHash(y.p.Blob) || len(y.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("restore did not settle exact blob")
	}
	if !slices.Equal(restoreEngine.effects, []string{"volume", "create", "restore", "cleanup"}) {
		t.Fatalf("restore effects=%v", restoreEngine.effects)
	}
}

func TestStorageArchiveActualControllerFailureRecoveryAndACKReplay(t *testing.T) {
	for _, failure := range []string{"inspect", "second stop", "backup", "second resume", "all stopped", "lost resumed", "lost stop intent", "lost stopped", "lost published"} {
		t.Run(failure, func(t *testing.T) {
			f := newArchiveControllerFixture(t)
			backup := f.backup(strings.Repeat("3", 32))
			x, op, e := f.coordinator(backup.OperationID, nil)
			e.writers[2].Running = false
			switch failure {
			case "inspect":
				e.failAt = "inspect"
			case "second stop":
				e.failAt = "stop:" + e.writers[1].ContainerID
			case "backup":
				e.failAt = "backup"
			case "second resume":
				e.failAt = "resume:" + e.writers[1].ContainerID
			case "all stopped":
				for i := range e.writers {
					e.writers[i].Running = false
				}
			case "lost resumed":
				f.losePhase = "resumed"
			case "lost stop intent":
				f.losePhase = "stop_intent"
			case "lost stopped":
				f.losePhase = "stopped"
			case "lost published":
				f.losePhase = "archive_published"
			}
			err := x.execute(op)
			settled := archiveControllerMust(f.pg.GetHostingOperation(f.ctx, op.ID))(t)
			if settled.StorageArchive.Result == nil || settled.CompleteReceipt == nil {
				t.Fatalf("actual controller refused failure settlement: state=%s err=%v", settled.State, err)
			}
			if failure == "all stopped" {
				if err != nil || settled.State != "succeeded" || settled.StorageArchive.Result.ResumeOutcome != "not_needed" || !slices.Equal(e.effects, []string{"create", "backup", "cleanup"}) {
					t.Fatalf("all-stopped backup: effects=%v state=%s err=%v", e.effects, settled.State, err)
				}
				return
			}
			if failure != "second resume" && failure != "lost resumed" {
				if err != nil || settled.State != "failed" || settled.MutationHold || settled.StorageArchive.Result.ArchiveOutcome != "failed" || len(x.j.Snapshot().StorageArchive) != 0 {
					t.Fatalf("ordinary failure did not settle: state=%s err=%v", settled.State, err)
				}
				if failure == "second stop" && (!slices.Contains(e.effects, "resume:"+e.writers[0].ContainerID) || !slices.Contains(e.effects, "resume:"+e.writers[1].ContainerID) || slices.Contains(e.effects, "create")) {
					t.Fatalf("partial stop not compensated: %v", e.effects)
				}
				return
			}
			if !errors.Is(err, errStorageArchiveHeld) || settled.State != "reconciliation_required" || !settled.MutationHold || !settled.StorageArchive.Result.Uncertain || settled.StorageArchive.Result.Blob != nil {
				t.Fatal("partial/lost resume did not retain conservative failure")
			}
			var recovery domain.HostingOperation
			f.json("POST", "/api/v1/hosting-operations/"+op.ID+"/storage-archive-recovery", f.owner, domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("4", 32), ExpectedPlanHash: x.p.Hash}, 202, &recovery)
			y, recoveryOp, recoveryEngine := f.coordinator(recovery.ID, x.j)
			required := y.p.RecoveryEvidence.ResumeRequiredContainerIDs
			if failure == "second resume" && len(required) != 2 || failure == "lost resumed" && len(required) != 0 {
				t.Fatalf("controller remaining resume set=%v", required)
			}
			if err := y.execute(recoveryOp); err != nil {
				t.Fatalf("actual recovery boundary: %v", err)
			}
			if len(x.j.Snapshot().StorageArchive) != 0 {
				t.Fatal("settled recovery did not retire matching chain")
			}
			want := []string{"cleanup"}
			for _, id := range required {
				want = append(want, "resume:"+id)
			}
			if !slices.Equal(recoveryEngine.effects, want) {
				t.Fatalf("recovery effects=%v want=%v", recoveryEngine.effects, want)
			}
			settled = archiveControllerMust(f.pg.GetHostingOperation(f.ctx, recovery.ID))(t)
			if settled.State != "succeeded" || settled.MutationHold || settled.StorageArchive.Result.ArchiveOutcome != "not_attempted" {
				t.Fatal("recovery invented archive effects or retained hold")
			}
		})
	}
}

func (f *archiveControllerFixture) replay(x *archiveCoordinator, engine *archiveRuntimeEngine) {
	f.t.Helper()
	before := slices.Clone(engine.effects)
	// The startup entry point constructs its own client. This explicitly trusted
	// literal-loopback listener uses the same authenticated actual API handler;
	// live execution above still uses verified TLS and its private test root.
	listener := httptest.NewServer(f.server.Config.Handler)
	defer listener.Close()
	config := Config{Server: listener.URL, Token: f.client.Token, RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: f.helper, TrustedLoopbackHTTP: true}}
	if err := reconcileStorageArchiveJournal(f.ctx, config, x.j); err != nil {
		f.t.Fatalf("actual controller startup replay: %v", err)
	}
	if !slices.Equal(before, engine.effects) || len(x.j.Snapshot().StorageArchive) != 0 {
		f.t.Fatal("completion ACK replay repeated effects or retained settled evidence")
	}
}

func TestStorageArchiveActualControllerLostCompletionDoesNotRepeatBackupOrRestore(t *testing.T) {
	f := newArchiveControllerFixture(t)
	backup := f.backup(strings.Repeat("5", 32))
	x, op, e := f.coordinator(backup.OperationID, nil)
	f.loseComplete = true
	if err := x.execute(op); !errors.Is(err, errStorageArchiveHeld) {
		t.Fatalf("lost backup completion err=%v", err)
	}
	f.replay(x, e)
	var restored domain.HostingStorageRestore
	f.json("POST", "/api/v1/storage-backups/"+backup.ID+"/restores", f.owner, domain.HostingStorageRestoreInput{RequestID: strings.Repeat("6", 32), Name: "restore ACK replay"}, 202, &restored)
	y, restoreOp, restoreEngine := f.coordinator(restored.Operation.ID, nil)
	f.loseComplete = true
	if err := y.execute(restoreOp); !errors.Is(err, errStorageArchiveHeld) {
		t.Fatalf("lost restore completion err=%v", err)
	}
	f.replay(y, restoreEngine)
}

func TestStorageArchiveActualControllerRejectsPerWriterAndMixedPhasePayloads(t *testing.T) {
	f := newArchiveControllerFixture(t)
	backup := f.backup(strings.Repeat("7", 32))
	x, op, e := f.coordinator(backup.OperationID, nil)
	checked := false
	e.beforeEffect = func(name string) {
		if checked || !strings.HasPrefix(name, "stop:") {
			return
		}
		checked = true
		requests := []domain.HostingStorageArchiveProgress{
			{HostingAuthority: x.a, ReceiptID: "bad_per_writer", PlanHash: x.p.Hash, Phase: "stop_intent", Writers: e.writers[:1]},
			{HostingAuthority: x.a, ReceiptID: "bad_running_stopped", PlanHash: x.p.Hash, Phase: "stopped", Writers: e.writers},
			{HostingAuthority: x.a, ReceiptID: "bad_helper_phase", PlanHash: x.p.Hash, Phase: "archive_published", HelperContainerID: strings.Repeat("f", 64)},
		}
		for _, request := range requests {
			if _, err := x.c.HostingStorageArchiveProgress(x.s.Context(), request); !runnerclient.HTTPStatus(err, http.StatusConflict) {
				t.Fatalf("strict actual phase accepted %s: %v", request.ReceiptID, err)
			}
		}
		bad := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: x.a, ReceiptID: "bad_early_complete", PlanHash: x.p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed"}}
		if _, err := x.c.HostingStorageArchiveComplete(x.s.Context(), bad); !runnerclient.HTTPStatus(err, http.StatusConflict) {
			t.Fatalf("premature successful result accepted: %v", err)
		}
	}
	if err := x.execute(op); err != nil || !checked {
		t.Fatalf("strict denials changed valid batch execution: %v", err)
	}
}
