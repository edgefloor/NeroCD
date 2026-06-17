package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresIntegrationControlPlanePrimitives(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	schema := "nerocd_test_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_"))
	adminDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = adminDB.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	migrationDB, err := sql.Open("pgx", schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedSQL(ctx, migrationDB); err != nil {
		_ = migrationDB.Close()
		t.Fatal(err)
	}
	if err := migrationDB.Close(); err != nil {
		t.Fatal(err)
	}

	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	service := app.NewService(auth.ContextProvider{}, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg, pg)
	session, err := service.CreateSession(ctx, "admin@example.local", "admin")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	adminPrincipal, err := service.AuthenticateSessionToken(ctx, session.Token)
	if err != nil {
		t.Fatalf("authenticate admin session: %v", err)
	}
	if !hasRole(adminPrincipal, "system_admin") {
		t.Fatalf("admin principal roles = %#v, want system_admin", adminPrincipal.Roles)
	}

	adminCtx := auth.WithPrincipal(ctx, adminPrincipal)
	createdToken, err := service.CreateAPIToken(adminCtx, app.APITokenInput{Name: "Integration Bootstrap", Roles: []string{"system_admin"}})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	apiPrincipal, err := service.AuthenticateSessionToken(ctx, createdToken.Token)
	if err != nil {
		t.Fatalf("authenticate api token: %v", err)
	}
	if apiPrincipal.Provider != "api_token" || !hasRole(apiPrincipal, "system_admin") {
		t.Fatalf("api token principal = %#v", apiPrincipal)
	}

	apiCtx := auth.WithPrincipal(ctx, apiPrincipal)
	registered, err := service.RegisterRunner(apiCtx, app.RunnerInput{ID: "runner_pg", Name: "Postgres Runner", Tags: []string{"local"}, Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatalf("register runner with api token: %v", err)
	}
	runnerPrincipal, err := service.AuthenticateRunnerToken(ctx, registered.Token)
	if err != nil {
		t.Fatalf("authenticate runner token: %v", err)
	}

	run, err := pg.CreateRun(ctx, domain.TaskRun{
		ID:            "run_pg_json",
		ProjectID:     "proj_platform",
		RunSpec:       domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo pg"}, Process: &domain.ProcessSpec{Command: []string{"echo", "pg"}, TimeoutSeconds: 30}, Artifacts: []domain.ArtifactSpec{{Name: "stdout", Path: "stdout.txt", Required: true}}},
		Workflow:      domain.Workflow{Steps: []domain.WorkflowStep{{ID: "execute", Name: "Execute", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo pg"}}}}},
		WorkflowState: domain.WorkflowState{Steps: []domain.WorkflowStepState{{ID: "execute", Name: "Execute", Status: "pending"}}},
		RunnerTags:    []string{"local"},
		Status:        "queued",
		RequestedBy:   adminPrincipal.ID,
		StartedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create json run: %v", err)
	}
	if run.RunSpec.Process == nil || run.Workflow.Steps[0].ID != "execute" || run.WorkflowState.Steps[0].Status != "pending" {
		t.Fatalf("created run did not preserve JSON fields: %#v", run)
	}

	claimed, err := service.ClaimRun(auth.WithPrincipal(ctx, runnerPrincipal))
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if claimed.Run.ID != run.ID || claimed.Run.Status != "running" || claimed.PrimitivePlan.Process == nil {
		t.Fatalf("unexpected claimed run: %#v", claimed)
	}
	if _, err := service.AppendRunLog(auth.WithPrincipal(ctx, runnerPrincipal), app.RunLogInput{RunID: run.ID, LeaseID: claimed.Lease.ID, Sequence: 1, Stream: "stdout", Message: "pg ok"}); err != nil {
		t.Fatalf("append run log: %v", err)
	}
	if _, err := service.CreateArtifact(auth.WithPrincipal(ctx, runnerPrincipal), app.ArtifactInput{RunID: run.ID, LeaseID: claimed.Lease.ID, Name: "stdout", Path: "stdout.txt", Found: true, Required: false, Size: 12, Kind: "file"}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, err := service.CompleteLease(auth.WithPrincipal(ctx, runnerPrincipal), claimed.Lease.ID, "succeeded"); err != nil {
		t.Fatalf("complete lease: %v", err)
	}

	logs, err := pg.ListRunLogs(ctx, run.ID)
	if err != nil {
		t.Fatalf("list run logs: %v", err)
	}
	if !hasRunLogMessage(logs, "pg ok") {
		t.Fatalf("unexpected run logs: %#v", logs)
	}
	artifacts, err := pg.ListArtifacts(ctx, run.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != "stdout.txt" {
		t.Fatalf("unexpected artifacts: %#v", artifacts)
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	for _, action := range []string{"api_token.create", "runner.register", "runner.claim", "runner.complete"} {
		if !hasAuditAction(events, action) {
			t.Fatalf("missing audit action %s in %#v", action, events)
		}
	}

	revoked, err := service.RevokeAPIToken(adminCtx, app.RevokeAPITokenInput{TokenID: createdToken.APIToken.ID})
	if err != nil {
		t.Fatalf("revoke api token: %v", err)
	}
	if revoked.Status != "revoked" {
		t.Fatalf("revoked api token status = %q, want revoked", revoked.Status)
	}
	if _, err := service.AuthenticateSessionToken(ctx, createdToken.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked api token auth error = %v, want unauthenticated", err)
	}

	revokedRunner, err := service.RevokeRunnerToken(adminCtx, app.RunnerTokenInput{RunnerID: registered.Runner.ID})
	if err != nil {
		t.Fatalf("revoke runner token: %v", err)
	}
	if revokedRunner.Status != "revoked" {
		t.Fatalf("revoked runner status = %q, want revoked", revokedRunner.Status)
	}
	if _, err := service.AuthenticateRunnerToken(ctx, registered.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked runner token auth error = %v, want unauthenticated", err)
	}
}

func databaseURLWithSearchPath(t *testing.T, databaseURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func applyEmbeddedSQL(ctx context.Context, database *sql.DB) error {
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, "migrations/"+entry.Name())
	}
	sort.Strings(files)
	for _, file := range files {
		content, err := db.Files.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := database.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("apply %s: %w", file, err)
		}
	}
	seed, err := db.Files.ReadFile("seeds/dev.sql")
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, string(seed)); err != nil {
		return fmt.Errorf("apply seeds/dev.sql: %w", err)
	}
	return nil
}

func hasRole(principal auth.Principal, role string) bool {
	for _, candidate := range principal.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func hasAuditAction(events []domain.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func hasRunLogMessage(logs []domain.RunLog, message string) bool {
	for _, log := range logs {
		if log.Message == message {
			return true
		}
	}
	return false
}
