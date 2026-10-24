package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/internal/secretcrypto"
	"nerocd/web"
)

func TestHostingDatabasePublicDispatchAndTypedClaim(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to an exclusive disposable PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("fixture database configuration invalid")
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = "nerocd_database_dispatch_inventory"
	inventory, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("fixture inventory connection unavailable")
	}
	t.Cleanup(inventory.Close)
	schemas := func(ctx context.Context) []string {
		rows, err := inventory.Query(ctx, "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY nspname")
		if err != nil {
			t.Fatal("fixture schema inventory unavailable")
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal("fixture schema inventory invalid")
			}
			names = append(names, name)
		}
		if rows.Err() != nil {
			t.Fatal("fixture schema inventory incomplete")
		}
		return names
	}
	clients := func(ctx context.Context) int {
		var count int
		if inventory.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND pid<>pg_backend_pid()").Scan(&count) != nil {
			t.Fatal("fixture client inventory unavailable")
		}
		return count
	}
	before := schemas(ctx)
	if clients(ctx) != 0 {
		t.Fatal("disposable database is not exclusively leased")
	}
	t.Logf("fixture inventory before schemas=%v other_clients=0", before)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		after, others := schemas(cleanup), clients(cleanup)
		if !slices.Equal(before, after) || others != 0 {
			t.Errorf("fixture cleanup inventory mismatch schemas_unchanged=%t other_clients=%d", slices.Equal(before, after), others)
		}
		t.Logf("fixture inventory after schemas=%v other_clients=%d", after, others)
	})

	for i, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			_, pg, database := newHostingBuildAPIFixture(t)
			var key [32]byte
			if _, err := rand.Read(key[:]); err != nil {
				t.Fatal("fixture key generation failed")
			}
			ring, err := secretcrypto.New("database-dispatch", map[string][]byte{"database-dispatch": key[:]})
			clear(key[:])
			if err != nil {
				t.Fatal("fixture keyring unavailable")
			}
			service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
			if err != nil {
				t.Fatal("fixture service unavailable")
			}
			server := httptest.NewTLSServer(NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static()))
			t.Cleanup(server.Close)
			f := &databaseRunnerFixture{t: t, server: server, client: server.Client()}
			f.client.Timeout = 5 * time.Second
			f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			login := databaseRunnerJSON[struct {
				Token string `json:"token"`
			}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
			caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
			enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, login.Token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_dispatch_" + engine, RunnerName: "database dispatch fixture", Capabilities: caps}, 201)
			credential := "ncr_database_dispatch_fixture_only_" + engine
			enrolled := databaseRunnerJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", databaseRunnerEnrollmentConsumeInput(credential), 200)
			target := databaseRunnerJSON[app.ServerDetail](f, login.Token, "POST", "/api/v1/servers", app.ServerInput{Name: "database dispatch", RunnerID: enrolled.Runner.ID}, 201)
			databaseRunnerJSON[json.RawMessage](f, login.Token, "POST", "/api/v1/destinations/"+target.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
			environment := databaseRunnerJSON[domain.ProjectEnvironment](f, login.Token, "POST", "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "database dispatch"}, 201)
			t.Logf("phase=public_admission engine=%s runner=%s destination=%s", engine, enrolled.Runner.ID, target.Destination.ID)
			databaseRunnerJSON[json.RawMessage](f, login.Token, "POST", "/api/v1/runs", app.RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy, HostingOperationID: "hop_forbidden", HostingMode: "cleanup"}}, http.StatusBadRequest)

			// Direct transactions initialize queue infrastructure only. Hosting
			// resources, authority, deployment and claim all use public APIs.
			for {
				tx, err := database.Begin(ctx)
				if err != nil {
					t.Fatal("queue migration transaction unavailable")
				}
				changed, err := func() (changed bool, err error) {
					defer func() {
						cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
						defer stop()
						if rollbackErr := tx.Rollback(cleanup); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
							err = errors.Join(err, rollbackErr)
						}
					}()
					changed, err = hostingjobs.MigrateNextTx(ctx, tx)
					if err != nil {
						return false, err
					}
					return changed, tx.Commit(ctx)
				}()
				if err != nil {
					t.Fatal("queue migration failed")
				}
				if !changed {
					break
				}
			}
			stop, err := pg.StartHostingExecution(ctx)
			if err != nil {
				t.Fatal("real River consumer unavailable")
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if stop(cleanup) != nil {
					t.Error("River consumer did not join")
				}
			})
			version := domain.HostingDatabasePostgresRecipe
			if engine == "redis" {
				version = domain.HostingDatabaseRedisRecipe
			}
			view := databaseRunnerJSON[domain.HostingDatabase](f, login.Token, "POST", "/api/v1/databases", domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(string(rune('a'+i)), 32), ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "dispatch " + engine, DestinationID: target.Destination.ID, Engine: engine, RecipeVersion: version}, 201)
			op := databaseRunnerJSON[domain.HostingOperation](f, login.Token, "POST", "/api/v1/resources/"+view.Resource.ID+"/database/deployments", domain.HostingDatabaseDeploymentInput{ExpectedGeneration: view.Resource.Generation, IdempotencyKey: "database_dispatch"}, 202)
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			poll := time.NewTicker(20 * time.Millisecond)
			defer poll.Stop()
			for op.RunID == "" {
				select {
				case <-ctx.Done():
					t.Fatal("dispatch context expired")
				case <-deadline.C:
					t.Fatalf("phase=river_dispatch engine=%s operation=%s state=%s run_created=false", engine, op.ID, op.State)
				case <-poll.C:
					op = databaseRunnerJSON[domain.HostingOperation](f, login.Token, "GET", "/api/v1/hosting-operations/"+op.ID, nil, 200)
				}
			}
			run := databaseRunnerJSON[domain.TaskRun](f, login.Token, "GET", "/api/v1/runs/"+op.RunID, nil, 200)
			if domain.ValidateDatabaseDeploymentTask(run) != nil || run.RunSpec.HostingOperationID != op.ID || run.RunSpec.HostingMode != "deploy" || op.Snapshot.DestinationID != target.Destination.ID || op.Snapshot.RunnerID != enrolled.Runner.ID || op.Snapshot.Database.Engine != engine {
				t.Fatal("River dispatch changed typed database identity or destination")
			}
			t.Logf("phase=river_dispatch engine=%s operation=%s state=%s run=%s type=%s", engine, op.ID, op.State, run.ID, run.RunSpec.Type)
			client := runnerclient.New(server.URL, credential)
			client.HTTP = f.client
			if _, err := client.Heartbeat(ctx); err != nil {
				t.Fatal("public runner heartbeat failed")
			}
			claim, err := client.Claim(ctx)
			if err != nil {
				var apiErr *runnerclient.APIError
				status := 0
				if errors.As(err, &apiErr) {
					status = apiErr.StatusCode
				}
				run = databaseRunnerJSON[domain.TaskRun](f, login.Token, "GET", "/api/v1/runs/"+op.RunID, nil, 200)
				op = databaseRunnerJSON[domain.HostingOperation](f, login.Token, "GET", "/api/v1/hosting-operations/"+op.ID, nil, 200)
				t.Fatalf("phase=typed_claim engine=%s status=%d operation_state=%s run_state=%s run_type=%s runner_assigned=%t typed_task_valid=%t", engine, status, op.State, run.Status, run.RunSpec.Type, run.RunnerID != nil && *run.RunnerID == enrolled.Runner.ID, domain.ValidateDatabaseDeploymentTask(run) == nil)
			}
			if claim.Run.ID != op.RunID || claim.Lease.RunID != op.RunID || claim.Lease.RunnerID != enrolled.Runner.ID || claim.Lease.ID == "" || claim.Lease.Attempt <= 0 || claim.Lease.Fence == "" || domain.ValidateDatabaseDeploymentTask(claim.Run) != nil || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
				t.Fatal("claim changed lease authority or exposed generic execution")
			}
			authority := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			plan, err := client.HostingPlan(ctx, authority)
			if err != nil || plan.ID != op.ID || plan.RunID != claim.Run.ID || plan.TaskMode != "deploy" || plan.Snapshot != op.Snapshot {
				t.Fatal("typed runner preparation plan changed operation or mode")
			}
			t.Logf("phase=typed_claim engine=%s operation=%s run=%s mode=%s exact_lease=true generic_process=false preparation_not_executed=true", engine, op.ID, claim.Run.ID, plan.TaskMode)
		})
	}
}
