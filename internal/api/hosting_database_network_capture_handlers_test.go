package api

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
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

func TestDatabaseNetworkCaptureHTTPClosedRoutes(t *testing.T) {
	s, mem := newTestServer()
	user := createTestSession(t, s)
	token := "capture_runner_token_canary"
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_capture", Name: "capture", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now(), Capabilities: []string{domain.HostingDatabaseNetworkCaptureCapability}}); err != nil {
		t.Fatal(err)
	}
	id := "hnc_" + strings.Repeat("a", 32)
	for _, phase := range []string{"claim", "plan", "origin"} {
		path := "/api/v1/runners/database-network-captures/" + phase
		call := func(credential, body, suffix string, want int) {
			t.Helper()
			r := httptest.NewRequest("POST", path+suffix, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if credential != "" {
				r.Header.Set("Authorization", "Bearer "+credential)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s status=%d want=%d cache=%s", phase, w.Code, want, w.Header().Get("Cache-Control"))
			}
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("bearer reflected")
			}
		}
		valid := `{"capture_id":"` + id + `"}`
		if phase != "claim" {
			valid = `{"operation_id":"` + id + `","run_id":"capture_` + id + `","lease_id":"lease","attempt":1,"fence":"fence"}`
		}
		if phase == "origin" {
			valid = strings.TrimSuffix(valid, "}") + `,"receipt_id":"origin","plan_hash":"sha256:` + strings.Repeat("b", 64) + `","observed_at":"2026-10-24T00:00:00Z","origin":{}}`
		}
		call("", valid, "", 401)
		call(user, valid, "", 401)
		call(token, valid, "", 503)
		for _, bad := range []string{`null`, `[]`, `{}`, `{"capture_id":null}`, `{"capture_id":"a","capture_id":"b"}`, `{"Capture_id":"a"}`, `{"unknown":1}`, valid + `{}`} {
			call(token, bad, "", 400)
		}
		call(token, valid, "?capture_id=foreign", 400)
		call(token, strings.Repeat(" ", domain.HostingDatabaseBindingJSONLimit+1)+valid, "", 413)
	}
}

func TestDatabaseNetworkCaptureClaimNextHTTPClosedBody(t *testing.T) {
	s, mem := newTestServer()
	user := createTestSession(t, s)
	token := "capture_next_runner_canary"
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_capture_next", Name: "capture", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))), Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now(), Capabilities: []string{domain.HostingDatabaseNetworkCaptureCapability}}); err != nil {
		t.Fatal(err)
	}
	call := func(credential, body, suffix string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runners/database-network-captures/claim-next"+suffix, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), token) {
			t.Fatalf("closed claim-next status=%d want=%d", w.Code, want)
		}
	}
	call("", "{}", "", 401)
	call(user, "{}", "", 401)
	call(token, "{}", "", 503)
	for _, body := range []string{"", `null`, `[]`, `{"capture_id":"foreign"}`, `{"runner_id":"foreign"}`, `{"unknown":1}`, `{"unknown":1,"unknown":2}`, `{"Unknown":1}`, `{} {}`, `{"field":null}`} {
		call(token, body, "", 400)
	}
	call(token, "{}", "?capture_id=foreign", 400)
	call(token, strings.Repeat(" ", domain.HostingDatabaseBindingJSONLimit+1)+"{}", "", 413)
}

func TestDatabaseNetworkCaptureClaimNextPostgresAuthenticatedClient(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newCaptureHTTPFixture(t)
			source := f.source(t, engine)
			capture, err := f.service.RequestHostingDatabaseNetworkCapture(f.admin, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			handler := NewServer(f.service, slog.New(slog.NewTextHandler(&f.logs, nil)), web.Static())
			var lose atomic.Bool
			lose.Store(true)
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/runners/database-network-captures/claim-next" && lose.CompareAndSwap(true, false) {
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					if rec.Code != 200 {
						t.Error("claim loss fault did not follow commit")
						w.WriteHeader(rec.Code)
						return
					}
					w.WriteHeader(503)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer tls.Close()
			client := runnerclient.New(tls.URL, f.token)
			client.HTTP = tls.Client()
			if _, err = client.ClaimNextHostingDatabaseNetworkCapture(f.admin, f.runner); !runnerclient.HTTPStatus(err, 503) {
				t.Fatal("lost claim response was not injected", err)
			}
			claim, err := client.ClaimNextHostingDatabaseNetworkCapture(f.admin, f.runner)
			if err != nil || claim.Capture.ID != capture.ID || claim.Lease.Attempt != 1 {
				t.Fatal("automatic selected claim replay failed", err)
			}
			again, err := client.ClaimNextHostingDatabaseNetworkCapture(f.admin, f.runner)
			if err != nil || again.Lease.ID != claim.Lease.ID || again.Lease.Fence != claim.Lease.Fence || again.Lease.Attempt != claim.Lease.Attempt {
				t.Fatal("poll replay changed authority", err)
			}
			p := claim.Capture.Plan
			m := p.Materialization
			in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: domain.HostingAuthority{OperationID: capture.ID, RunID: claim.Lease.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}, ReceiptID: "automatic_origin", PlanHash: p.Hash, ObservedAt: time.Now().UTC(), Origin: domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: m.DaemonID, Configuration: p.SourcePlan.Snapshot.Database.ConfigurationHash, OriginOperation: m.CreationOperationID, OriginPlan: m.CreationPlanHash, Nonce: strings.Repeat("b", 32), Storage: m.StorageRef, VolumeNonce: m.CreationNonce, VolumeCreatedAt: "2026-10-23T00:00:00Z", NetworkID: strings.Repeat("c", 64), InitReady: true}}
			if _, err = client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p); err != nil {
				t.Fatal(err)
			}
			_, err = client.ClaimNextHostingDatabaseNetworkCapture(f.admin, f.runner)
			var status *runnerclient.APIError
			if !errors.As(err, &status) || !runnerclient.HTTPStatus(err, 404) || status.Detail != "" {
				t.Fatal("committed capture did not yield typed empty queue", err)
			}
		})
	}
}

// This fixture exercises the real authenticated API/app/store boundary only.
// Source settlement and origin are typed test attestations, never Docker evidence.
type captureHTTPFixture struct {
	pg                                      *store.PostgresStore
	pool                                    *pgxpool.Pool
	service                                 *app.Service
	admin                                   context.Context
	runner, token, destination, environment string
	logs                                    bytes.Buffer
}

func newCaptureHTTPFixture(t *testing.T) *captureHTTPFixture {
	t.Helper()
	raw := os.Getenv("NEROCD_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires explicitly leased disposable PostgreSQL fixture")
	}
	work, stopWork := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(stopWork)
	ctx, cancel := context.WithTimeout(work, time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid fixture database configuration")
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_capture_transport_inventory"
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
	schema := "nerocd_capture_transport_" + hex.EncodeToString(nonce[:])
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
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_capture_transport"
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
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, e := db.Files.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(raw)); e != nil {
			t.Fatalf("owned migration %s failed", entry.Name())
		}
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(seed)); err != nil {
		t.Fatal("owned seed failed")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal("fixture URI required")
	}
	query := parsed.Query()
	query.Del("options")
	query.Set("search_path", schema)
	query.Set("application_name", "nerocd_capture_transport")
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
		t.Fatal(err)
	}
	ring, err := secretcrypto.New("capture", map[string][]byte{"capture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	f := &captureHTTPFixture{pg: pg, pool: pool, service: service, admin: auth.WithPrincipal(work, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}}), runner: "runner_capture", token: "ncr_capture_transport_canary"}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseNetworkCaptureCapability}
	enrollment, err := service.CreateRunnerEnrollment(f.admin, app.RunnerEnrollmentInput{RunnerID: f.runner, RunnerName: "capture", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: fmt.Sprintf("%x", sha256.Sum256([]byte(f.token)))}); err != nil {
		t.Fatal("capture runner enrollment failed")
	}
	target, err := service.CreateServer(f.admin, app.ServerInput{Name: "capture", RunnerID: f.runner})
	if err != nil {
		t.Fatal(err)
	}
	f.destination = target.Destination.ID
	if _, err = service.GrantDestinationProject(f.admin, f.destination, app.DestinationProjectInput{ProjectID: "proj_platform"}); err != nil {
		t.Fatal(err)
	}
	env, err := service.CreateProjectEnvironment(f.admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	f.environment = env.ID
	for {
		tx, e := pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
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
		t.Fatal(err)
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

func (f *captureHTTPFixture) source(t *testing.T, engine string) domain.HostingOperation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	view, err := f.service.CreateHostingDatabase(f.admin, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(map[string]string{"postgresql": "a", "redis": "b"}[engine], 32), ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: engine, DestinationID: f.destination, Engine: engine, RecipeVersion: version})
	if err != nil {
		t.Fatal("source metadata failed", err)
	}
	op, err := f.service.RequestHostingDatabaseDeployment(f.admin, view.Resource.ID, domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "capture_source"})
	if err != nil {
		t.Fatal("source request failed", err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal("source dispatch failed", err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != op.ID {
		t.Fatal("source task claim failed")
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
		t.Fatal("source plan admission failed", err)
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal("source recipe failed", err)
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "capture_fixture_daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: release})
	if err != nil {
		t.Fatal("source prepared failed", err)
	}
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: op.Snapshot.Storage, CreationNonce: op.Plan.Storage.CreationNonce})
	if err != nil {
		t.Fatal("source create admission failed", err)
	}
	m := ack.Materialization
	_, err = f.pg.HostingStorageMaterialized(ctx, f.runner, domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "materialized_" + op.ID, PlanHash: op.Plan.Hash, StorageRef: m.StorageRef, CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: ack.Storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}})
	if err != nil {
		t.Fatal("source materialized failed", err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal("source apply admission failed", err)
	}
	op, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("a", 64), ImageID: release.ImageID, DaemonID: release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal("source complete failed", err)
	}
	return op
}

func TestDatabaseNetworkCapturePostgresAuthenticatedClient(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newCaptureHTTPFixture(t)
			source := f.source(t, engine)
			capture, err := f.service.RequestHostingDatabaseNetworkCapture(f.admin, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			handler := NewServer(f.service, slog.New(slog.NewTextHandler(&f.logs, nil)), web.Static())
			var lose atomic.Bool
			lose.Store(true)
			tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/runners/database-network-captures/origin" && lose.CompareAndSwap(true, false) {
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					if rec.Code != 200 {
						t.Error("origin ACK fault did not follow commit")

						w.WriteHeader(rec.Code)
						return
					}
					w.WriteHeader(503)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer tls.Close()
			client := runnerclient.New(tls.URL, f.token)
			client.HTTP = tls.Client()
			// Framing and authentication failures traverse the real TLS API,
			// before any exact-ID claim can create a lease.
			for _, input := range []struct {
				token, body, suffix string
				status              int
			}{
				{"", `{"capture_id":"` + capture.ID + `"}`, "", 401},
				{"invalid_bearer_canary", `{"capture_id":"` + capture.ID + `"}`, "", 401},
				{f.token, `{"capture_id":"` + capture.ID + `","capture_id":"foreign"}`, "", 400},
				{f.token, `{"capture_id":"` + capture.ID + `","socket":"foreign"}`, "", 400},
				{f.token, `{"capture_id":"` + capture.ID + `"}`, "?runner=foreign", 400},
			} {
				req, e := http.NewRequestWithContext(f.admin, http.MethodPost, tls.URL+"/api/v1/runners/database-network-captures/claim"+input.suffix, strings.NewReader(input.body))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Content-Type", "application/json")
				if input.token != "" {
					req.Header.Set("Authorization", "Bearer "+input.token)
				}
				response, e := tls.Client().Do(req)
				if e != nil {
					t.Fatal("capture boundary request failed")
				}
				raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || response.StatusCode != input.status || response.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(raw), "bearer_canary") {
					t.Fatal("capture boundary failed closed contract")
				}
			}
			foreignToken := "ncr_foreign_capture_canary"
			if _, err = f.pg.RegisterRunner(f.admin, domain.Runner{ID: "runner_foreign", Name: "foreign", Tags: []string{}, TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(foreignToken))), Status: domain.RunnerActive, Capabilities: []string{domain.HostingDatabaseNetworkCaptureCapability}, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			foreign := runnerclient.New(tls.URL, foreignToken)
			foreign.HTTP = tls.Client()
			if _, err = foreign.ClaimHostingDatabaseNetworkCapture(f.admin, "runner_foreign", capture.ID); !runnerclient.HTTPStatus(err, 409) {
				t.Fatal("wrong runner claim was not denied", err)
			}
			claim, err := client.ClaimHostingDatabaseNetworkCapture(f.admin, f.runner, capture.ID)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingAuthority{OperationID: capture.ID, RunID: claim.Lease.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			p, err := client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a)
			if err != nil || p.Plan.Hash != capture.Plan.Hash {
				t.Fatal("exact capture plan failed", err)
			}
			stale := a
			stale.Fence += "stale"
			if _, err = client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, stale); !runnerclient.HTTPStatus(err, 404) {
				t.Fatal("stale fence accepted", err)
			}
			replacement, err := f.service.RequestHostingDatabaseDeployment(f.admin, source.ResourceID, domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "replacement"})
			if err != nil {
				t.Fatal("replacement admission failed", err)
			}
			if _, err = client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a); !runnerclient.HTTPStatus(err, 409) {
				t.Fatal("changed source retained live capture authority", err)
			}
			if _, err = f.service.CancelHostingOperation(f.admin, replacement.ID); err != nil {
				t.Fatal("unused replacement cancel failed", err)
			}
			if _, err = f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
				t.Fatal(err)
			}
			if _, err = client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a); err == nil {
				t.Fatal("missing capability accepted")
			}
			if _, err = f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
				t.Fatal(err)
			}
			origin := domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.Plan.BindingID, Destination: p.Plan.DestinationID, Daemon: p.Plan.Materialization.DaemonID, Configuration: p.Plan.SourcePlan.Snapshot.Database.ConfigurationHash, OriginOperation: p.Plan.Materialization.CreationOperationID, OriginPlan: p.Plan.Materialization.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: p.Plan.Materialization.StorageRef, VolumeNonce: p.Plan.Materialization.CreationNonce, VolumeCreatedAt: "2026-10-24T00:00:00Z", NetworkID: strings.Repeat("d", 64), InitReady: true}
			in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: a, ReceiptID: "origin_" + capture.ID, PlanHash: p.Plan.Hash, ObservedAt: time.Now().UTC(), Origin: origin}
			if _, err = client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p.Plan); !runnerclient.HTTPStatus(err, 503) {
				t.Fatal("committed ACK loss not observed", err)
			}
			ack, err := client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p.Plan)
			if err != nil || ack.BodyHash != domain.HostingDocumentHash(in) {
				t.Fatal("exact historical ACK replay failed", err)
			}
			if _, err = client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a); err == nil {
				t.Fatal("historical completion renewed current authority")
			}
			if _, err = f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
				t.Fatal(err)
			}
			if _, err = client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p.Plan); !runnerclient.HTTPStatus(err, 409) {
				t.Fatal("historical ACK bypassed current capability", err)
			}
			if _, err = f.pool.Exec(f.admin, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseNetworkCaptureCapability); err != nil {
				t.Fatal(err)
			}
			changed := in
			changed.ObservedAt = changed.ObservedAt.Add(time.Second)
			if _, err = client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, changed, p.Plan); !runnerclient.HTTPStatus(err, 409) {
				t.Fatal("changed original request replay accepted", err)
			}
			var count int
			if err = f.pool.QueryRow(f.admin, `SELECT count(*) FROM hosting_database_network_origin_receipts WHERE capture_id=$1`, capture.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("origin replay duplicated receipt")
			}
			if _, err = f.pool.Exec(f.admin, `UPDATE runners SET token_hash=$2 WHERE id=$1`, f.runner, fmt.Sprintf("%x", sha256.Sum256([]byte("rotated_capture_token")))); err != nil {
				t.Fatal(err)
			}
			if _, err = client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p.Plan); err == nil {
				t.Fatal("revoked bearer replay accepted")
			}
			for _, secret := range []string{f.token, a.Fence, a.LeaseID} {
				if strings.Contains(f.logs.String(), secret) {
					t.Fatal("private authority leaked in API logs")
				}
			}
			encoded, err := json.Marshal(ack)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{f.token, a.Fence, a.LeaseID} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatal("origin receipt exposed bearer or private authority")
				}
			}
		})
	}
}

// Initialize the real pgx time.Local=UTC scan representation in a separate
// process, without replacing time.Local while other tests may use it.
func TestDatabaseNetworkCapturePostgresUTCIdentity(t *testing.T) {
	if os.Getenv("NEROCD_TEST_DATABASE_URL") == "" {
		t.Skip("requires explicitly leased disposable PostgreSQL fixture")
	}
	if os.Getenv("NEROCD_CAPTURE_PG_UTC_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseNetworkCapturePostgresUTCIdentity$", "-test.timeout=90s")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TZ=") && !strings.HasPrefix(value, "NEROCD_CAPTURE_PG_UTC_CHILD=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TZ=UTC", "NEROCD_CAPTURE_PG_UTC_CHILD=1")
		// A failed driver/server diagnostic may contain connection details. The
		// parent exposes only the fixed outcome, never raw child output.
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		cmd.WaitDelay = 5 * time.Second
		if cmd.Run() != nil {
			t.Fatal("UTC PostgreSQL capture identity child failed")
		}
		t.Log("UTC PostgreSQL request, claim, plan, origin and historical replay passed for both engines")
		return
	}
	if time.Unix(0, 0).Location() != time.Local || time.Local == time.UTC {
		t.Fatal("UTC PostgreSQL process did not retain Local scan representation")
	}
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newCaptureHTTPFixture(t)
			source := f.source(t, engine)
			capture, err := f.service.RequestHostingDatabaseNetworkCapture(f.admin, source.ID)
			if err != nil {
				t.Fatal("UTC capture request failed")
			}
			var storageTime, receiptTime time.Time
			if f.pool.QueryRow(f.admin, `SELECT s.created_at,r.created_at FROM hosting_storage s JOIN hosting_storage_materialized_receipts r ON r.storage_id=s.id WHERE s.id=$1 AND r.receipt_id=$2`, capture.Plan.Storage.ID, capture.Plan.Creation.Receipt.ID).Scan(&storageTime, &receiptTime) != nil || storageTime.Location() != time.Local || receiptTime.Location() != time.Local || capture.Plan.Storage.CreatedAt.Location() != time.UTC || capture.Plan.Creation.Receipt.CreatedAt.Location() != time.UTC || !storageTime.Equal(capture.Plan.Storage.CreatedAt) || !receiptTime.Equal(capture.Plan.Creation.Receipt.CreatedAt) {
				t.Fatal("UTC fixture did not reproduce SQL Local versus JSON UTC identity")
			}
			duplicate, err := f.service.RequestHostingDatabaseNetworkCapture(f.admin, source.ID)
			if err != nil || duplicate.ID != capture.ID || duplicate.Plan.Hash != capture.Plan.Hash {
				t.Fatal("UTC pending capture request replay conflicted")
			}
			server := httptest.NewTLSServer(NewServer(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
			t.Cleanup(server.Close)
			client := runnerclient.New(server.URL, f.token)
			client.HTTP = server.Client()
			claim, err := client.ClaimHostingDatabaseNetworkCapture(f.admin, f.runner, capture.ID)
			if err != nil {
				t.Fatal("UTC capture claim conflicted")
			}
			a := domain.HostingAuthority{OperationID: capture.ID, RunID: claim.Lease.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			plan, err := client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a)
			if err != nil || plan.Plan.Hash != capture.Plan.Hash {
				t.Fatal("UTC current capture plan conflicted")
			}
			p := plan.Plan
			origin := domain.HostingDatabaseNetworkOrigin{Version: 1, Binding: p.BindingID, Destination: p.DestinationID, Daemon: p.Materialization.DaemonID, Configuration: p.SourcePlan.Snapshot.Database.ConfigurationHash, OriginOperation: p.Materialization.CreationOperationID, OriginPlan: p.Materialization.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: p.Materialization.StorageRef, VolumeNonce: p.Materialization.CreationNonce, VolumeCreatedAt: "2026-10-24T00:00:00Z", NetworkID: strings.Repeat("d", 64), InitReady: true}
			in := domain.HostingDatabaseNetworkOriginRequest{HostingAuthority: a, ReceiptID: "utc_origin_" + capture.ID, PlanHash: p.Hash, ObservedAt: time.Now().UTC(), Origin: origin}
			ack, err := client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p)
			if err != nil {
				t.Fatal("UTC origin record conflicted")
			}
			replay, err := client.RecordHostingDatabaseNetworkOrigin(f.admin, f.runner, in, p)
			if err != nil || domain.HostingDocumentHash(ack) != domain.HostingDocumentHash(replay) {
				t.Fatal("UTC historical origin replay changed full receipt")
			}
			final, err := f.service.RequestHostingDatabaseNetworkCapture(f.admin, source.ID)
			if err != nil || final.Receipt == nil || domain.HostingDocumentHash(*final.Receipt) != domain.HostingDocumentHash(ack) {
				t.Fatal("UTC historical request replay conflicted")
			}
			if _, err := client.HostingDatabaseNetworkCapturePlan(f.admin, f.runner, a); err == nil {
				t.Fatal("UTC historical receipt regained live plan authority")
			}
		})
	}
}
