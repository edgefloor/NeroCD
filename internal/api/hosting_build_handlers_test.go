package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
	"nerocd/web"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func newHostingBuildAPIFixture(t *testing.T) (*Server, *store.PostgresStore, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL identity contract tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminDB.Close)
	schema := fmt.Sprintf("nerocd_build_api_contract_%d", time.Now().UTC().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := adminDB.Exec(cleanup, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	query.Set("application_name", "nerocd_build_api_contract")
	parsed.RawQuery = query.Encode()
	database, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		migration, err := db.Files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(service, slog.Default(), web.Static()), pg, database
}

func TestHostingBuildAPIPublicGitRiverAndRunnerPhases(t *testing.T) {
	for _, compose := range []bool{false, true} {
		t.Run(fmt.Sprintf("compose=%t", compose), func(t *testing.T) {
			server, pg, database := newHostingBuildAPIFixture(t)
			ctx := t.Context()
			admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
			env, err := server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "build-api"})
			if err != nil {
				t.Fatal(err)
			}
			settings := domain.HostingBuildSettings{Strategy: "dockerfile"}
			if compose {
				settings = domain.HostingBuildSettings{Strategy: "compose", ComposeFile: "compose.yaml", PublicComponent: "web", ContainerPort: 8080}
			}
			resource, err := server.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "Git web", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/example/web", RequestedRef: "main", CredentialKind: "public", Settings: settings}}}})
			if err != nil {
				t.Fatal(err)
			}
			capabilities := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType}
			if compose {
				capabilities = append(capabilities, domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability)
			}
			enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_build_api", RunnerName: "build", Capabilities: capabilities})
			if err != nil {
				t.Fatal(err)
			}
			runnerToken := "ncr_build_api_fixture"
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(runnerToken)))
			enrolled, err := server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			detail, err := server.app.CreateServer(admin, app.ServerInput{Name: "build", RunnerID: enrolled.Runner.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = server.app.GrantDestinationProject(admin, detail.Destination.ID, app.DestinationProjectInput{ProjectID: resource.ProjectID}); err != nil {
				t.Fatal(err)
			}
			if _, err = server.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: detail.Destination.ID}); err != nil {
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
			d := "sha256:" + strings.Repeat("a", 64)
			policy := domain.HostingBuildPolicy{Identity: d, ToolchainIdentity: d, Protocol: domain.HostingBuildProtocol, Platform: "linux/amd64", SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 10 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 10 << 20, UnpackedBytes: 10 << 20, UsageBytes: 20 << 20, ReserveBytes: 10 << 20, PollInterval: time.Second, DiskMode: "monitored"}}
			policy.GitCompose = compose
			session := createTestSession(t, server)
			request := func(method, path, token, body string, want int) []byte {
				t.Helper()
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s %s got%d want%d: %s", method, path, w.Code, want, w.Body.String())
				}
				return w.Body.Bytes()
			}
			if compose {
				body := fmt.Sprintf(`{"project_id":%q,"project_environment_id":%q,"name":"Argument decoder","kind":"application","source":{"kind":"git","git":{"repository_url":"https://github.com/example/web","requested_ref":"main","credential_kind":"public","settings":{"strategy":"compose","compose_file":"compose.yaml","public_component":"web","container_port":8080,"build_variables":[{"name":"ARG","value":"public"}]}}}}`, env.ProjectID, env.ID)
				var decoded domain.Resource
				if err := json.Unmarshal(request("POST", "/api/v1/resources", session, body, 201), &decoded); err != nil || decoded.Source.Git == nil || len(decoded.Source.Git.Settings.BuildVariables) != 1 || decoded.Source.Git.Settings.BuildVariables[0].Value != "public" {
					t.Fatal("real resource decoder lost public build variable", err)
				}
				for _, invalid := range []string{`{"name":"ARG"}`, `{"name":"ARG","value":null}`, `{"name":"ARG","value":1}`, `{"name":"ARG","value":"public","secret_id":"forbidden"}`, `{"name":"BUILDKIT_SYNTAX","value":"public"}`} {
					request("POST", "/api/v1/resources", session, strings.Replace(body, `{"name":"ARG","value":"public"}`, invalid, 1), 400)
				}
			}
			if _, err = pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "member_build_api", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
				t.Fatal(err)
			}
			viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
			account, err := server.app.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_security", Name: "other project build bot", Role: domain.RoleMaintainer})
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/resources/" + resource.ID + "/builds"
			input := `{"expected_resource_generation":1,"expected_source_generation":1,"idempotency_key":"api-build"}`
			request("POST", path, session, input, 503)
			request("POST", path, viewer, input, 403)
			request("GET", path, account.Token, "", 403)
			request("POST", path, account.Token, input, 403)

			request("POST", path, session, `{"expected_resource_generation":1,"expected_source_generation":1,"idempotency_key":"api-build","grant_id":"forbidden"}`, 400)
			if err = pg.ConfigureHostingBuild(&policy); err != nil {
				t.Fatal(err)
			}
			stop, err := pg.StartHostingExecution(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if e := stop(c); e != nil {
					t.Error(e)
				}
			})
			raw := request("POST", path, session, input, 202)
			var response struct {
				Build domain.PublicHostingBuild `json:"build"`
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			id := response.Build.ID
			replay := request("POST", path, session, input, 202)
			var repeated struct {
				Build domain.PublicHostingBuild `json:"build"`
			}
			_ = json.Unmarshal(replay, &repeated)
			if repeated.Build.ID != id {
				t.Fatal("request replay changed build")
			}
			for _, forbidden := range []string{"operator_policy", "policy_hash", "input_id", "allowed_hosts", "grant", "toolchain_identity"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("public leak %s", forbidden)
				}
			}
			request("GET", path+"?limit=1&offset=0", session, "", 200)
			request("GET", path, viewer, "", 200)
			request("POST", "/api/v1/hosting-builds/"+id+"/cancel", viewer, `{"expected_revision":1}`, 403)

			request("GET", "/api/v1/hosting-builds/"+id, session, "", 200)
			deadline := time.Now().Add(10 * time.Second)
			for {
				b, e := pg.GetHostingBuild(ctx, "usr_bootstrap", id)
				if e != nil {
					t.Fatal(e)
				}
				if b.RunID != "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no real River dispatch")
				}
				time.Sleep(20 * time.Millisecond)
			}
			claim, err := server.app.ClaimRun(auth.WithPrincipal(ctx, auth.Principal{ID: enrolled.Runner.ID, Provider: domain.PrincipalRunner}))
			if err != nil {
				t.Fatal(err)
			}
			authority := domain.HostingBuildAuthority{BuildID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			statusBody, _ := json.Marshal(hostingbuildprotocol.StatusRequest{Authority: authority})
			request("POST", "/api/v1/runners/builds/status", session, string(statusBody), 401)
			statusRaw := request("POST", "/api/v1/runners/builds/status", runnerToken, string(statusBody), 200)
			var status hostingbuildprotocol.StatusResponse
			if json.Unmarshal(statusRaw, &status) != nil || !status.MayExecute {
				t.Fatal("live status denied")
			}
			begin, _ := json.Marshal(hostingbuildprotocol.SourceBeginRequest{Authority: authority, ExpectedRevision: status.Build.Revision})
			phaseRaw := request("POST", "/api/v1/runners/builds/source-begin", runnerToken, string(begin), 200)
			var phase hostingbuildprotocol.PhaseResponse
			_ = json.Unmarshal(phaseRaw, &phase)
			source := domain.HostingBuildSourceReceipt{BuildID: id, SourceID: phase.Build.Snapshot.Source.ID, InputID: phase.Build.Snapshot.InputID, Commit: strings.Repeat("b", 40), PolicyHash: domain.HostingDocumentHash(phase.Build.Snapshot.Source.Policy), TreeDigest: "sha256:" + strings.Repeat("b", 64)}
			commit := hostingbuildprotocol.SourceCommitRequest{Authority: authority, ReceiptID: "api-source", Receipt: source}
			body, _ := json.Marshal(commit)
			phaseRaw = request("POST", "/api/v1/runners/builds/source-commit", runnerToken, string(body), 200)
			_ = json.Unmarshal(phaseRaw, &phase)
			if phase.Acknowledgment == nil || hostingbuildprotocol.ValidateAcknowledgment(*phase.Acknowledgment, authority, "source", commit.ReceiptID, hostingbuildprotocol.SourceReceiptHash(source)) != nil {
				t.Fatal("incorrect source acknowledgment")
			}
			body, _ = json.Marshal(hostingbuildprotocol.PrepareRequest{Authority: authority, ExpectedRevision: phase.Build.Revision, DaemonID: "daemon-api"})
			phaseRaw = request("POST", "/api/v1/runners/builds/prepare", runnerToken, string(body), 200)
			_ = json.Unmarshal(phaseRaw, &phase)
			completion := hostingbuildprotocol.CompleteRequest{Authority: authority, ReceiptID: "api-failure", ExpectedRevision: phase.Build.Revision, Completion: hostingbuildprotocol.Completion{State: "failed", FailureCode: "build_failed"}}
			if compose {
				source := domain.HostingStackSource{Original: "services:\n  web:\n    build: .\n  db:\n    image: postgres:17\n", PublicComponent: "web", ContainerPort: 8080}
				definition, e := domain.ParseHostingGitCompose(source, settings)
				if e != nil {
					t.Fatal(e)
				}
				digest := "sha256:" + strings.Repeat("c", 64)
				artifact := domain.HostingBuildArtifact{Origin: "local_build", BuildID: domain.HostingComposeBuildID(id, "web"), SourceID: phase.Build.Snapshot.Source.ID, InputID: phase.Build.Snapshot.InputID, SourceReceiptHash: domain.HostingDocumentHash(phase.Build.SourceReceipt), ContextDigest: digest, RequestDigest: digest, DaemonID: "daemon-api", ImageID: digest, IndexDigest: digest, ManifestDigest: digest, ConfigDigest: digest, Platform: "linux/amd64"}
				result := domain.HostingBuildComposeResult{SourceReceiptHash: domain.HostingDocumentHash(phase.Build.SourceReceipt), Source: source, Definition: definition, Services: []domain.HostingBuildComposeService{{Component: "web", Artifact: artifact}}}
				result.Hash = domain.HostingDocumentHash(result)
				completion.Completion = hostingbuildprotocol.Completion{State: "succeeded", ComposeResult: &result}
			}
			body, _ = json.Marshal(completion)
			phaseRaw = request("POST", "/api/v1/runners/builds/complete", runnerToken, string(body), 200)
			_ = json.Unmarshal(phaseRaw, &phase)
			expectedState := "failed"
			if compose {
				expectedState = "succeeded"
			}
			if phase.Build.State != expectedState || phase.Acknowledgment == nil {
				t.Fatal("terminal phase not committed")
			}
			request("POST", "/api/v1/runners/builds/complete", runnerToken, string(body), 200)
			if compose {
				public := request("GET", "/api/v1/hosting-builds/"+id, session, "", 200)
				var prepared struct {
					Build domain.PublicHostingBuild `json:"build"`
				}
				if json.Unmarshal(public, &prepared) != nil || prepared.Build.ComposeResult == nil || prepared.Build.Artifact != nil || prepared.Build.ComposeResult.Hash != completion.Completion.ComposeResult.Hash {
					t.Fatal("public Compose result projection lost typed completion")
				}
				if strings.Contains(string(public), "source_receipt_hash") || strings.Contains(string(public), "request_digest") {
					t.Fatal("public Compose result leaked internal artifact fingerprints")
				}
			}

			if _, err = database.Exec(ctx, `UPDATE runners SET status='revoked' WHERE id=$1`, enrolled.Runner.ID); err != nil {
				t.Fatal(err)
			}
			request("POST", "/api/v1/runners/builds/status", runnerToken, string(statusBody), 401)
		})
	}
}

func TestPublicAPIsRejectBuildTaskAndAuthorityAtEveryWorkflowShape(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	internal := domain.RunSpec{Type: domain.HostingBuildTaskType, HostingBuildID: "forged"}
	generic := domain.RunSpec{Type: "ansible"}
	nested := &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "internal", RunSpec: internal}}}
	for _, shape := range []struct {
		spec     domain.RunSpec
		workflow domain.Workflow
	}{
		{internal, domain.Workflow{}}, {domain.RunSpec{Type: "ansible", HostingBuildID: "forged"}, domain.Workflow{}}, {generic, *nested}, {domain.RunSpec{Type: "ansible", Workflow: nested}, domain.Workflow{}}, {generic, domain.Workflow{Steps: []domain.WorkflowStep{{ID: "outer", RunSpec: domain.RunSpec{Type: "ansible", Workflow: nested}}}}},
	} {
		for _, endpoint := range []struct{ method, path string }{{"POST", "/api/v1/runs"}, {"POST", "/api/v1/templates"}, {"PATCH", "/api/v1/templates"}} {
			body, _ := json.Marshal(map[string]any{"project_id": "proj_platform", "id": "tpl_patch", "name": "forged", "kind": "ansible", "run_spec": shape.spec, "workflow": shape.workflow})
			req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)
			if w.Code != 400 {
				t.Fatalf("%s %s accepted internal task: %d %s", endpoint.method, endpoint.path, w.Code, w.Body.String())
			}
		}
	}
}
