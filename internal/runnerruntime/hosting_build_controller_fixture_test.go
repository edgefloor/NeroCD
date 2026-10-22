//go:build linux

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/source"
	"nerocd/internal/store"
	"nerocd/web"
)

// controllerFixtureHosting supplies operator-owned fixture routing through the
// existing trusted repository option. Public source requests contain no policy.
// All authorization, transaction, audit, source generations and receipts remain
// the real service/PostgreSQL implementation.
type controllerFixtureHosting struct {
	store.HostingRepository
	policy source.RepositoryPolicy
}

func (h controllerFixtureHosting) CreateResource(ctx context.Context, r domain.Resource, opts ...store.MutationOption) (domain.Resource, error) {
	if r.Source.Kind == "git" && r.Source.Git != nil {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: h.policy}))
	}
	return h.HostingRepository.CreateResource(ctx, r, opts...)
}

type controllerBuildFixture struct {
	t                                                            *testing.T
	pg                                                           *store.PostgresStore
	db                                                           *pgxpool.Pool
	server                                                       *httptest.Server
	admin, credential, work, configFile, credentialFile, journal string
	config                                                       BuildConfig
	environment                                                  domain.ProjectEnvironment
	target                                                       app.ServerDetail
	generic, renewals, heartbeats                                atomic.Int32
	loseCompletion                                               atomic.Bool
	mu                                                           sync.Mutex
	initialExpiries                                              map[string]time.Time
	containers                                                   []string
}

func newControllerBuildFixture(t *testing.T) *controllerBuildFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	dsn := os.Getenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL")
	database, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			raw, e := db.Files.ReadFile("migrations/" + entry.Name())
			if e != nil {
				t.Fatal(e)
			}
			if _, e = database.Exec(ctx, string(raw)); e != nil {
				t.Fatalf("migration %s: %v", entry.Name(), e)
			}
		}
	}
	seed, err := os.ReadFile("/fixture/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	policy := source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}, AllowedCIDRs: []string{"127.0.0.0/8"}}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: controllerFixtureHosting{pg, policy}, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ConfigureHostingGitHosts([]string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	f := &controllerBuildFixture{t: t, pg: pg, db: database, work: t.TempDir(), initialExpiries: map[string]time.Time{}}
	handler := api.NewServer(service, slog.Default(), web.Static())
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/complete", "/api/v1/runners/events", "/api/v1/runners/events/batch":
			f.generic.Add(1)
		case "/api/v1/runners/renew":
			f.renewals.Add(1)
		case "/api/v1/runners/heartbeat":
			f.heartbeats.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/builds/complete" || r.URL.Path == "/api/v1/runners/claim" {
			recorded := httptest.NewRecorder()
			handler.ServeHTTP(recorded, r)
			if r.URL.Path == "/api/v1/runners/claim" && recorded.Code == http.StatusOK {
				var claim domain.ClaimedRun
				if json.Unmarshal(recorded.Body.Bytes(), &claim) == nil {
					f.mu.Lock()
					f.initialExpiries[claim.Run.ID] = claim.Lease.ExpiresAt
					f.mu.Unlock()
				}
			}
			if r.URL.Path == "/api/v1/runners/builds/complete" && recorded.Code == http.StatusOK && f.loseCompletion.CompareAndSwap(true, false) {
				connection, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = connection.Close()
				return
			}
			for k, v := range recorded.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	var session struct {
		Token string `json:"token"`
	}
	f.request(t, "POST", "/api/v1/sessions", "", map[string]string{"email": "admin@example.local", "password": "admin"}, http.StatusCreated, &session)
	f.admin = session.Token
	f.request(t, "POST", "/api/v1/project-environments", f.admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "build runtime"}, http.StatusCreated, &f.environment)
	var enrollment app.CreatedRunnerEnrollment
	f.request(t, "POST", "/api/v1/runner-enrollments", f.admin, app.RunnerEnrollmentInput{RunnerID: "runner_build_runtime", RunnerName: "Build Runtime", Capabilities: []string{domain.HostingBuildTaskType, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy}}, http.StatusCreated, &enrollment)
	f.credential = "ncr_isolated_build_controller_credential"
	sum := sha256.Sum256([]byte(f.credential))
	var consumed app.ConsumedRunnerEnrollment
	f.request(t, "POST", "/api/v1/runner-enrollments/consume", enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: fmt.Sprintf("%x", sum)}, http.StatusOK, &consumed)
	f.request(t, "POST", "/api/v1/servers", f.admin, app.ServerInput{Name: "Build Runtime", RunnerID: consumed.Runner.ID}, http.StatusCreated, &f.target)
	f.request(t, "POST", "/api/v1/destinations/"+f.target.Destination.ID+"/project-grants", f.admin, app.DestinationProjectInput{ProjectID: f.environment.ProjectID}, http.StatusCreated, nil)
	sources := filepath.Join(f.work, "sources")
	if err = os.Mkdir(sources, 0700); err != nil {
		t.Fatal(err)
	}
	f.config = BuildConfig{SourceRoot: sources, Engine: hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE"), StateDir: filepath.Join(f.work, "state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}, Target: BuildTarget{RunnerID: consumed.Runner.ID, ServerID: f.target.Server.ID, DestinationID: f.target.Destination.ID}, StatusInterval: 200 * time.Millisecond, RequestTimeout: 5 * time.Second}
	d := "sha256:" + strings.Repeat("a", 64)
	f.config.Policy = domain.HostingBuildPolicy{Identity: d, Protocol: domain.HostingBuildProtocol, Platform: "linux/" + runtime.GOARCH, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 1 << 30, Deadline: 3 * time.Minute, ContextBytes: 8 << 20, ContextFiles: 1000, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 256 << 20, UnpackedBytes: 512 << 20, UsageBytes: 3 << 30, ReserveBytes: 128 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 8 << 20, MaxWorkspaceBytes: 32 << 20, MinFreeBytes: 128 << 20, MaxEntries: 1000, MaxDepth: 32, PollInterval: 100 * time.Millisecond}}
	f.config.Policy.ToolchainIdentity, err = ComputeBuildToolchainIdentity(t.Context(), f.config.Engine, f.config.Policy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.config.Validate(); err != nil {
		t.Fatal(err)
	}
	for {
		tx, e := database.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
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
	}
	if err = pg.ConfigureHostingBuild(&f.config.Policy); err != nil {
		t.Fatal(err)
	}
	stop, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stopCtx := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCtx()
		if err := stop(c); err != nil {
			t.Error(err)
		}
	})
	f.configFile = filepath.Join(f.work, "build.json")
	f.credentialFile = filepath.Join(f.work, "credential")
	f.journal = filepath.Join(f.work, "journal")
	f.writeConfig()
	if err = os.WriteFile(f.credentialFile, []byte(f.credential), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range f.containers {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			out, e := exec.CommandContext(ctx, "/usr/local/bin/docker", "inspect", "--format", `{{index .Config.Labels "io.nerocd.build-controller.fixture"}}`, id).Output()
			if e == nil && strings.TrimSpace(string(out)) == "owned" {
				if out, e = exec.CommandContext(ctx, "/usr/local/bin/docker", "rm", "--force", id).CombinedOutput(); e != nil {
					t.Errorf("owned cleanup: %v %.500s", e, out)
				}
			}
			cancel()
		}
	})
	return f
}
func (f *controllerBuildFixture) writeConfig() {
	c := f.config
	raw, err := json.Marshal(map[string]any{"version": 1, "source_root": c.SourceRoot, "engine": map[string]string{"docker_binary": c.Engine.DockerBinary, "socket": c.Engine.Socket, "buildx_binary": c.Engine.BuildxBinary, "buildx_sha256": c.Engine.BuildxSHA256, "planner_image": c.Engine.PlannerImage, "state_dir": c.Engine.StateDir, "cgroup_root": c.Engine.CgroupRoot}, "target": map[string]string{"runner_id": c.Target.RunnerID, "server_id": c.Target.ServerID, "destination_id": c.Target.DestinationID}, "policy": c.Policy, "status_interval_ns": c.StatusInterval, "request_timeout_ns": c.RequestTimeout})
	if err != nil {
		f.t.Fatal(err)
	}
	if err = os.WriteFile(f.configFile, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *controllerBuildFixture) request(t *testing.T, method, path, token string, body any, want int, result any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != want {
		t.Fatalf("%s %s: got%d want%d: %.1000s", method, path, response.StatusCode, want, b)
	}
	if result != nil && json.Unmarshal(b, result) != nil {
		t.Fatal("invalid fixture response")
	}
}
func (f *controllerBuildFixture) resource(t *testing.T, files map[string]string, settings domain.HostingBuildSettings) domain.Resource {
	t.Helper()
	url, _ := buildRuntimeGit(t, files)
	url = strings.Replace(url, "127.0.0.1", "example.com", 1)
	var r domain.Resource
	f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": fmt.Sprintf("Git %s %x", settings.Strategy, time.Now().UnixNano()), "kind": "application", "source": map[string]any{"kind": "git", "git": domain.GitResourceConfiguration{RepositoryURL: url, RequestedRef: "main", CredentialKind: "public", Settings: settings}}}, http.StatusCreated, &r)
	f.request(t, "POST", "/api/v1/resources/"+r.ID+"/binding", f.admin, app.ResourceBindingInput{DestinationID: f.target.Destination.ID}, http.StatusCreated, nil)
	return r
}
func (f *controllerBuildFixture) build(t *testing.T, r domain.Resource, key string) domain.PublicHostingBuild {
	var out struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	f.request(t, "POST", "/api/v1/resources/"+r.ID+"/builds", f.admin, domain.HostingBuildRequest{ExpectedResourceGeneration: r.Generation, ExpectedSourceGeneration: r.Source.Git.Generation, IdempotencyKey: key}, http.StatusAccepted, &out)
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", out.Build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.RunID != "" {
			out.Build.RunID = b.RunID
			return out.Build
		}
		if time.Now().After(deadline) {
			t.Fatal("real River did not dispatch")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func (f *controllerBuildFixture) agentCommand(ctx context.Context) *exec.Cmd {
	command := exec.CommandContext(ctx, "/fixture/nerocd", "runner", "--server", f.server.URL, "--credential-file", f.credentialFile, "--build-config-file", f.configFile, "--journal-dir", f.journal, "--work-dir", f.work, "--cancel-poll-interval", "200ms", "--once")
	command.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "NEROCD_RUNNER_CREDENTIAL_FILE=", "NEROCD_RUNNER_JOURNAL_DIR=")
	return command
}
func (f *controllerBuildFixture) agent(ctx context.Context) error {
	command := f.agentCommand(ctx)
	out, err := command.CombinedOutput()
	if err != nil {
		f.t.Logf("agent fixed diagnostic: %.1200s", out)
	}
	return err
}
