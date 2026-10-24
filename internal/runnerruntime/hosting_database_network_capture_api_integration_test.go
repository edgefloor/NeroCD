//go:build unix

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
	"reflect"
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
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

// This fixture exercises the real authenticated API/app/store boundary only.
// Source settlement and origin are typed test attestations, never Docker evidence.
type captureRecoveryAPIFixture struct {
	pg                                      *store.PostgresStore
	pool                                    *pgxpool.Pool
	service                                 *app.Service
	admin                                   context.Context
	runner, token, destination, environment string
}

func newCaptureRecoveryAPIFixture(t *testing.T) *captureRecoveryAPIFixture {
	t.Helper()
	raw := os.Getenv("NEROCD_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires explicitly leased disposable PostgreSQL fixture")
	}
	work, stopWork := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(stopWork)
	ctx, cancel := context.WithTimeout(work, time.Minute)
	defer cancel()
	// No implicit libpq environment or default endpoint is accepted. The Leader
	// separately leases this loopback endpoint, database and user before execution.
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil || parsed.User.Username() == "" || parsed.Port() == "" || strings.Trim(parsed.Path, "/") == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		t.Fatal("explicit leased loopback PostgreSQL URI required")
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid fixture database configuration")
	}
	if config.ConnConfig.Host != parsed.Hostname() || fmt.Sprint(config.ConnConfig.Port) != parsed.Port() || config.ConnConfig.User != parsed.User.Username() || config.ConnConfig.Database != strings.TrimPrefix(parsed.Path, "/") {
		t.Fatal("fixture endpoint configuration differs")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("fixture endpoint fallback differs")
		}
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_capture_recovery_inventory"
	// pgxpool retains its config. Later schema configuration must not mutate
	// the live inventory pool's connection parameters.
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal("fixture inventory pool failed")
	}
	t.Cleanup(admin.Close)
	var database, user string
	var clients int
	if err = admin.QueryRow(ctx, `SELECT current_database(),current_user,(SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend' AND pid<>pg_backend_pid())`).Scan(&database, &user, &clients); err != nil || database != config.ConnConfig.Database || user != config.ConnConfig.User || clients != 0 {
		t.Fatal("fixture identity/exclusive-client check failed")
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal("fixture schema identity failed")
	}
	schema := "nerocd_capture_recovery_" + hex.EncodeToString(nonce[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	created := false
	// Register exact fresh-context cleanup before the first possible mutation.
	t.Cleanup(func() {
		if !created {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(cleanup, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); e != nil {
			t.Error("owned capture schema cleanup failed")
			return
		}
		var n int
		if e := admin.QueryRow(cleanup, `SELECT count(*) FROM pg_namespace WHERE nspname=$1`, schema).Scan(&n); e != nil || n != 0 {
			t.Error("owned capture schema remains")
		}
		t.Logf("capture owned schema removed %s", schema)
	})
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("owned schema creation failed")
	}
	delete(config.ConnConfig.RuntimeParams, "options")
	created = true
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_capture_recovery"
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("owned schema pool failed")
	}
	t.Cleanup(pool.Close)
	var currentSchema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatal("owned search path differs")
	}
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, e := db.Files.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal("capture recovery fixture setup failed")
		}
		if _, e = pool.Exec(ctx, string(raw)); e != nil {
			t.Fatalf("owned migration %s failed", entry.Name())
		}
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	if _, err = pool.Exec(ctx, string(seed)); err != nil {
		t.Fatal("owned seed failed")
	}
	query := parsed.Query()
	query.Del("options")
	query.Set("search_path", schema)
	query.Set("application_name", "nerocd_capture_recovery")
	parsed.RawQuery = query.Encode()
	pg, err := store.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal("owned store failed")
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error("owned store close failed")
		}
	})
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	ring, err := secretcrypto.New("capture", map[string][]byte{"capture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	f := &captureRecoveryAPIFixture{pg: pg, pool: pool, service: service, admin: auth.WithPrincipal(work, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}}), runner: "runner_capture", token: "ncr_capture_recovery_bearer_canary"}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability}
	enrollment, err := service.CreateRunnerEnrollment(f.admin, app.RunnerEnrollmentInput{RunnerID: f.runner, RunnerName: "capture", Capabilities: caps})
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	if _, err = service.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: fmt.Sprintf("%x", sha256.Sum256([]byte(f.token)))}); err != nil {
		t.Fatal("capture runner enrollment failed")
	}
	target, err := service.CreateServer(f.admin, app.ServerInput{Name: "capture", RunnerID: f.runner})
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	f.destination = target.Destination.ID
	if _, err = service.GrantDestinationProject(f.admin, f.destination, app.DestinationProjectInput{ProjectID: "proj_platform"}); err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	env, err := service.CreateProjectEnvironment(f.admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "capture"})
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	f.environment = env.ID
	for {
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal("capture recovery fixture setup failed")
		}
		changed, e := hostingjobs.MigrateNextTx(ctx, tx)
		if e != nil {
			if rollback := tx.Rollback(ctx); rollback != nil {
				t.Error("queue migration rollback failed")
			}
			t.Fatal("queue migration failed")
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal("queue migration commit failed")
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(work)
	if err != nil {
		t.Fatal("capture recovery fixture setup failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(cleanup); e != nil {
			t.Error("capture River did not join")
		}
	})
	t.Logf("capture owned schema ready %s", schema)
	return f
}

func (f *captureRecoveryAPIFixture) source(t *testing.T, engine string) domain.HostingOperation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	view, err := f.service.CreateHostingDatabase(f.admin, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(map[string]string{"postgresql": "a", "redis": "b"}[engine], 32), ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: engine, DestinationID: f.destination, Engine: engine, RecipeVersion: version})
	if err != nil {
		t.Fatal("source metadata failed")
	}
	op, err := f.service.RequestHostingDatabaseDeployment(f.admin, view.Resource.ID, domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "capture_source"})
	if err != nil {
		t.Fatal("source request failed")
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal("source dispatch failed")
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("source task claim failed")
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
		t.Fatal("source plan admission failed")
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal("source recipe failed")
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "capture_fixture_daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: release})
	if err != nil {
		t.Fatal("source prepared failed")
	}
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: op.Snapshot.Storage, CreationNonce: op.Plan.Storage.CreationNonce})
	if err != nil {
		t.Fatal("source create admission failed")
	}
	m := ack.Materialization
	_, err = f.pg.HostingStorageMaterialized(ctx, f.runner, domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "materialized_" + op.ID, PlanHash: op.Plan.Hash, StorageRef: m.StorageRef, CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: ack.Storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}})
	if err != nil {
		t.Fatal("source materialized failed")
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal("source apply admission failed")
	}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("a", 64), ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal("source complete failed")
	}
	return op
}

// These observations are simulated engine attestations. This qualification proves
// controller transport and journal recovery; it does not qualify Docker identity.
type captureRecoveryObserver struct {
	calls, callbacks int
}

func (o *captureRecoveryObserver) ObserveDatabaseNetworkOrigin(ctx context.Context, p domain.HostingDatabaseNetworkCapturePlan, current func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
	o.calls++
	if current == nil {
		return domain.HostingDatabaseNetworkOrigin{}, errors.New("missing current authority callback")
	}
	o.callbacks++
	if err := current(ctx); err != nil {
		return domain.HostingDatabaseNetworkOrigin{}, err
	}
	return domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: p.Materialization.DaemonID, Configuration: p.SourcePlan.Snapshot.Database.ConfigurationHash, OriginOperation: p.Materialization.CreationOperationID, OriginPlan: p.Materialization.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: p.Materialization.StorageRef, VolumeNonce: p.Materialization.CreationNonce, VolumeCreatedAt: "2026-10-24T00:00:00Z", NetworkID: strings.Repeat("d", 64), InitReady: true}, nil
}

type captureRecoveryLogs struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *captureRecoveryLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}

func (l *captureRecoveryLogs) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.String()
}

// The mutex also joins each completed request with the test's wire assertions.
type captureRecoveryWire struct {
	mu          sync.Mutex
	claim, plan int
	bodies      [][]byte
	statuses    []int
	acks        [][]byte
	lost        bool
}

func (w *captureRecoveryWire) counts() (int, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.claim, w.plan, len(w.bodies)
}

type captureRecoveryStored struct {
	Request   domain.HostingDatabaseNetworkOriginRequest
	Authority domain.HostingAuthority
	Receipt   domain.HostingDatabaseNetworkOriginReceipt
	Hash      string
}

func (f *captureRecoveryAPIFixture) stored(t *testing.T, capture string) captureRecoveryStored {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.admin, 5*time.Second)
	defer cancel()
	var body, authority, receipt []byte
	var stored captureRecoveryStored
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM hosting_database_network_origin_receipts WHERE capture_id=$1`, capture).Scan(&count); err != nil || count != 1 {
		t.Fatal("capture must have exactly one origin receipt")
	}
	if err := f.pool.QueryRow(ctx, `SELECT body,authority,receipt,body_hash FROM hosting_database_network_origin_receipts WHERE capture_id=$1`, capture).Scan(&body, &authority, &receipt, &stored.Hash); err != nil {
		t.Fatal("origin receipt read failed")
	}
	if json.Unmarshal(body, &stored.Request) != nil || json.Unmarshal(authority, &stored.Authority) != nil || json.Unmarshal(receipt, &stored.Receipt) != nil {
		t.Fatal("stored origin document invalid")
	}
	var taskStatus, leaseStatus, completionKey string
	var completed bool
	if err := f.pool.QueryRow(ctx, `SELECT t.status,l.status,l.completion_key,l.completed_at IS NOT NULL FROM task_runs t JOIN run_leases l ON l.run_id=t.id WHERE t.id=$1 AND l.id=$2`, stored.Authority.RunID, stored.Authority.LeaseID).Scan(&taskStatus, &leaseStatus, &completionKey, &completed); err != nil || taskStatus != "succeeded" || leaseStatus != "succeeded" || !completed || completionKey != stored.Request.ReceiptID {
		t.Fatal("capture task and original lease not completed atomically")
	}
	return stored
}

func TestDatabaseNetworkCaptureAPIJournalRecovery(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, revoked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/revoked_%t", engine, revoked), func(t *testing.T) {
				f := newCaptureRecoveryAPIFixture(t)
				source := f.source(t, engine)
				before, err := f.pg.GetHostingOperation(f.admin, source.ID)
				if err != nil {
					t.Fatal("source evidence read failed")
				}
				// The seeded bootstrap principal owns proj_platform. Drop its
				// system-admin role for the capture request to exercise owner admission.
				owner := auth.WithPrincipal(f.admin, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleUser}})
				capture, err := f.service.RequestHostingDatabaseNetworkCapture(owner, source.ID)
				if err != nil {
					t.Fatal("owner capture request failed")
				}
				path := filepath.Join(t.TempDir(), "journal")
				journal, err := runner.OpenAttemptJournal(path)
				if err != nil {
					t.Fatal("capture journal open failed")
				}
				t.Cleanup(func() {
					if journal != nil && journal.Close() != nil {
						t.Error("capture journal close failed")
					}
				})
				var logs captureRecoveryLogs
				handler := api.NewServer(f.service, slog.New(slog.NewTextHandler(&logs, nil)), nil)
				wire := &captureRecoveryWire{}
				initialJournal := journal
				tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					wire.mu.Lock()
					defer wire.mu.Unlock()
					switch r.URL.Path {
					case "/api/v1/runners/database-network-captures/claim":
						wire.claim++
					case "/api/v1/runners/database-network-captures/plan":
						wire.plan++
					case "/api/v1/runners/database-network-captures/origin":
						body, readErr := io.ReadAll(io.LimitReader(r.Body, domain.HostingDatabaseBindingJSONLimit+1))
						if readErr != nil || len(body) > domain.HostingDatabaseBindingJSONLimit {
							t.Error("origin wire body unavailable")
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						wire.bodies = append(wire.bodies, bytes.Clone(body))
						r.Body = io.NopCloser(bytes.NewReader(body))
						if !wire.lost {
							chain := initialJournal.Snapshot().DatabaseNetworkCapture
							var request domain.HostingDatabaseNetworkOriginRequest
							if len(chain) != 3 || chain[0].Phase != "claim" || chain[1].Phase != "plan" || chain[2].Phase != "origin" || json.Unmarshal(body, &request) != nil || !reflect.DeepEqual(chain[2].Origin, &request) {
								t.Error("origin sent before durable exact claim/plan/origin")
							}
						}
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, r)
						wire.statuses = append(wire.statuses, rec.Code)
						if rec.Code == http.StatusOK {
							wire.acks = append(wire.acks, bytes.Clone(rec.Body.Bytes()))
						}
						if !wire.lost {
							if rec.Code != http.StatusOK {
								t.Error("ACK fault did not follow committed HTTP 200")
							}
							wire.lost = true
							w.WriteHeader(http.StatusServiceUnavailable)
							// Both runner client and coordinator must hide server details.
							_, _ = io.WriteString(w, f.token+" private_capture_response_canary")
							return
						}
						for key, values := range rec.Header() {
							w.Header()[key] = values
						}
						w.WriteHeader(rec.Code)
						_, _ = w.Write(rec.Body.Bytes())
						return
					}
					handler.ServeHTTP(w, r)
				}))
				t.Cleanup(tls.Close)
				client := runnerclient.New(tls.URL, f.token)
				client.HTTP = tls.Client()
				observer := &captureRecoveryObserver{}
				err = executeDatabaseNetworkCapture(f.admin, journal, client, observer, f.runner, capture.ID)
				if !errors.Is(err, errDatabaseNetworkCaptureExecution) || err.Error() != errDatabaseNetworkCaptureExecution.Error() {
					t.Fatal("committed ACK loss did not produce redacted unresolved capture")
				}
				chain := journal.Snapshot().DatabaseNetworkCapture
				if len(chain) != 3 || chain[2].Origin == nil || observer.calls != 1 || observer.callbacks != 1 {
					t.Fatal("ACK loss did not retain exactly one observed origin")
				}
				original := *chain[2].Origin
				stored := f.stored(t, capture.ID)
				if !reflect.DeepEqual(stored.Request, original) || stored.Authority != original.HostingAuthority || stored.Hash != domain.HostingDocumentHash(original) || !captureExecutionAck(capture.Plan, original, stored.Receipt) {
					t.Fatal("committed receipt differs from durable original request")
				}
				if journal.Close() != nil {
					t.Fatal("journal close before restart failed")
				}
				journal = nil
				raw, err := os.ReadFile(filepath.Join(path, "journal.json"))
				if err != nil || !bytes.Contains(raw, []byte(`"version":13`)) {
					t.Fatal("restart did not retain v13 journal")
				}
				for _, secret := range []string{f.token, os.Getenv("NEROCD_TEST_DATABASE_URL"), "private_capture_response_canary", path} {
					if bytes.Contains(raw, []byte(secret)) {
						t.Fatal("journal retained secret or local path")
					}
				}
				journal, err = runner.OpenAttemptJournal(path)
				if err != nil || !reflect.DeepEqual(chain, journal.Snapshot().DatabaseNetworkCapture) {
					t.Fatal("restart changed retained capture chain")
				}
				claims, plans, requests := wire.counts()
				if claims != 1 || plans == 0 || requests != 1 {
					t.Fatal("unexpected initial authenticated capture requests")
				}
				if revoked {
					if _, err := f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
						t.Fatal("fixture-admin capability revocation failed")
					}
					if err := replayDatabaseNetworkCapture(f.admin, journal, client, nil, f.runner, capture.ID); !errors.Is(err, errDatabaseNetworkCaptureExecution) || err.Error() != errDatabaseNetworkCaptureExecution.Error() {
						t.Fatal("revoked replay did not fail closed with redacted error")
					}
					if !reflect.DeepEqual(chain, journal.Snapshot().DatabaseNetworkCapture) || !reflect.DeepEqual(stored, f.stored(t, capture.ID)) {
						t.Fatal("revoked replay changed durable chain or committed receipt")
					}
					// Explicit fixture-admin restoration is the only changed authority.
					if _, err := f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
						t.Fatal("fixture-admin capability restoration failed")
					}
				}
				if err := replayDatabaseNetworkCapture(f.admin, journal, client, nil, f.runner, capture.ID); err != nil {
					t.Fatal("historical replay did not retire capture")
				}
				gotClaims, gotPlans, gotRequests := wire.counts()
				wantRequests := 2
				if revoked {
					wantRequests++
				}
				if gotClaims != claims || gotPlans != plans || gotRequests != wantRequests || observer.calls != 1 || len(journal.Snapshot().DatabaseNetworkCapture) != 0 {
					t.Fatal("historical replay reobserved, reclaimed, fetched a plan or retained acknowledged chain")
				}
				wire.mu.Lock()
				for _, body := range wire.bodies {
					if !bytes.Equal(body, wire.bodies[0]) {
						t.Error("historical publication changed original wire body")
					}
				}
				if len(wire.acks) != 2 || !bytes.Equal(wire.acks[0], wire.acks[1]) || wire.statuses[0] != 200 || wire.statuses[len(wire.statuses)-1] != 200 || revoked && wire.statuses[1] != 409 {
					t.Error("historical ACK bytes or current-capability denial differ")
				}
				wire.mu.Unlock()
				if !reflect.DeepEqual(stored, f.stored(t, capture.ID)) {
					t.Fatal("historical replay replaced receipt, hash, authority or timestamps")
				}
				if _, err := client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, original.HostingAuthority); err == nil {
					t.Fatal("completed capture regained current plan authority")
				}
				after, err := f.pg.GetHostingOperation(f.admin, source.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("capture changed source operation or result")
				}
				for _, secret := range []string{f.token, original.Fence, original.LeaseID, os.Getenv("NEROCD_TEST_DATABASE_URL"), "private_capture_response_canary"} {
					if strings.Contains(logs.text(), secret) {
						t.Fatal("API logs leaked capture authority or credential")
					}
				}
				if journal.Close() != nil {
					t.Fatal("retired journal close failed")
				}
				journal, err = runner.OpenAttemptJournal(path)
				if err != nil || len(journal.Snapshot().DatabaseNetworkCapture) != 0 {
					t.Fatal("retired capture reappeared after second restart")
				}
			})
		}
	}
}
