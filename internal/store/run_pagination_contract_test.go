package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type runPaginationContractStore interface {
	store.HostingRepository
	store.DestinationRepository
	store.ProjectRepository
	store.RunRepository
}

func TestRunPaginationContractAppliesAuthorizationBeforePaging(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		memory := store.NewMemoryStore()
		runPaginationContract(t, memory, func(ctx context.Context, artifact domain.ArtifactRecord) error {
			return memory.CreateArtifact(ctx, artifact)
		})
	})
	t.Run("postgres", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		pg, database := openPostgresIntegrationStore(t, ctx, "run_pagination_contract")
		defer closePostgresStore(t, pg)
		runPaginationContract(t, pg, postgresArtifactSeeder(database))

		service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
		if err != nil {
			t.Fatal(err)
		}
		adminCtx := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})
		canceled, err := service.CancelRun(adminCtx, "run_page_allowed_000")
		if err != nil || canceled.Status != domain.RunCanceled {
			t.Fatalf("cancel exact run beyond first page = %#v, %v", canceled, err)
		}
	})
}

func runPaginationContract(t *testing.T, repository runPaginationContractStore, seedArtifact func(context.Context, domain.ArtifactRecord) error) {
	t.Helper()
	ctx := t.Context()
	allowedProject := domain.Project{ID: "proj_page_allowed", Name: "Allowed pagination", CreatedAt: time.Now().UTC()}
	deniedProject := domain.Project{ID: "proj_page_denied", Name: "Denied pagination", CreatedAt: time.Now().UTC()}
	for _, project := range []domain.Project{allowedProject, deniedProject} {
		if _, err := repository.CreateProject(ctx, project); err != nil {
			t.Fatalf("create project %s: %v", project.ID, err)
		}
	}

	started := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 150; i++ {
		projectID := allowedProject.ID
		prefix := "allowed"
		if i >= 130 {
			projectID = deniedProject.ID
			prefix = "denied"
		}
		runID := fmt.Sprintf("run_page_%s_%03d", prefix, i)
		run := domain.TaskRun{
			ID: runID, ProjectID: projectID, RunSpec: domain.RunSpec{Type: domain.RunTypeShell},
			Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{},
			Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: started.Add(time.Duration(i) * time.Second),
		}
		if _, err := repository.CreateRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		if err := repository.CreateRunLog(ctx, domain.RunLog{ID: "log_" + runID, RunID: runID, Sequence: 1, Stream: domain.LogStdout, Message: runID, CreatedAt: run.StartedAt}); err != nil {
			t.Fatalf("create log for %s: %v", runID, err)
		}
		if err := seedArtifact(ctx, domain.ArtifactRecord{ID: "art_" + runID, RunID: runID, LeaseID: "lease_" + runID, Name: runID, Path: runID, Found: true, Kind: domain.ArtifactFile, CreatedAt: run.StartedAt}); err != nil {
			t.Fatalf("create artifact for %s: %v", runID, err)
		}
	}

	scope := store.RunAuthorizationScope{ProjectIDs: []string{allowedProject.ID}}
	explicitZero, err := repository.ListRunsPageForScope(ctx, scope, store.Page{Enabled: true, LimitSpecified: true})
	if err != nil {
		t.Fatalf("list scoped run page with explicit zero limit: %v", err)
	}
	if explicitZero.Total != 130 || explicitZero.Offset != 0 || explicitZero.Limit != 0 || len(explicitZero.Items) != 0 {
		t.Fatalf("explicit-zero scoped run page = %#v, want total 130 and no items", explicitZero)
	}

	offsetOnly, err := repository.ListRunsPageForScope(ctx, scope, store.Page{Enabled: true, Offset: 100})
	if err != nil {
		t.Fatalf("list scoped run page with offset only: %v", err)
	}
	if offsetOnly.Total != 130 || offsetOnly.Offset != 100 || offsetOnly.Limit != store.MaxPageLimit || len(offsetOnly.Items) != 30 {
		t.Fatalf("offset-only scoped run page = %#v, want bounded default and 30 later-page items", offsetOnly)
	}

	positive, err := repository.ListRunsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: 1})
	if err != nil {
		t.Fatalf("list scoped run page with positive limit: %v", err)
	}
	if positive.Total != 130 || positive.Offset != 0 || positive.Limit != 1 || len(positive.Items) != 1 {
		t.Fatalf("positive-limit scoped run page = %#v, want one of 130 items", positive)
	}

	maxBounded, err := repository.ListRunsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: store.MaxPageLimit + 1})
	if err != nil {
		t.Fatalf("list scoped run page above maximum limit: %v", err)
	}
	if maxBounded.Total != 130 || maxBounded.Offset != 0 || maxBounded.Limit != store.MaxPageLimit || len(maxBounded.Items) != store.MaxPageLimit {
		t.Fatalf("maximum-bounded scoped run page = %#v, want %d of 130 items", maxBounded, store.MaxPageLimit)
	}

	page := store.Page{Enabled: true, Limit: 100, Offset: 100}
	runs, err := repository.ListRunsPageForScope(ctx, scope, page)
	if err != nil {
		t.Fatalf("list scoped run page: %v", err)
	}
	if runs.Total != 130 || runs.Offset != 100 || runs.Limit != 100 || len(runs.Items) != 30 {
		t.Fatalf("scoped run page = %#v, want total 130 and 30 later-page items", runs)
	}
	for _, run := range runs.Items {
		if run.ProjectID != allowedProject.ID {
			t.Fatalf("scoped run page leaked project %s", run.ProjectID)
		}
	}

	logs, err := repository.ListRunLogsPageForScope(ctx, scope, page)
	if err != nil {
		t.Fatalf("list scoped log page: %v", err)
	}
	if logs.Total != 130 || logs.Offset != 100 || logs.Limit != 100 || len(logs.Items) != 30 {
		t.Fatalf("scoped log page = %#v, want total 130 and 30 later-page items", logs)
	}
	for _, log := range logs.Items {
		if !strings.Contains(log.RunID, "_allowed_") {
			t.Fatalf("scoped log page leaked run %s", log.RunID)
		}
	}
	artifacts, err := repository.ListArtifactsPageForScope(ctx, scope, page)
	if err != nil {
		t.Fatalf("list scoped artifact page: %v", err)
	}
	if artifacts.Total != 130 || artifacts.Offset != 100 || artifacts.Limit != 100 || len(artifacts.Items) != 30 {
		t.Fatalf("scoped artifact page = %#v, want total 130 and 30 later-page items", artifacts)
	}
	for _, artifact := range artifacts.Items {
		if !strings.Contains(artifact.RunID, "_allowed_") {
			t.Fatalf("scoped artifact page leaked run %s", artifact.RunID)
		}
	}

	oldRun, err := repository.GetRun(ctx, "run_page_allowed_000")
	if err != nil || oldRun.ID != "run_page_allowed_000" {
		t.Fatalf("exact lookup beyond first page = %#v, %v", oldRun, err)
	}
	empty, err := repository.ListRunsPageForScope(ctx, store.RunAuthorizationScope{}, store.Page{Enabled: true, Limit: 100})
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("empty authorization scope = %#v, %v", empty, err)
	}
	emptyLogs, err := repository.ListRunLogsPageForScope(ctx, store.RunAuthorizationScope{}, store.Page{Enabled: true, Limit: 100})
	if err != nil || emptyLogs.Total != 0 || len(emptyLogs.Items) != 0 {
		t.Fatalf("empty log authorization scope = %#v, %v", emptyLogs, err)
	}
	emptyArtifacts, err := repository.ListArtifactsPageForScope(ctx, store.RunAuthorizationScope{}, store.Page{Enabled: true, Limit: 100})
	if err != nil || emptyArtifacts.Total != 0 || len(emptyArtifacts.Items) != 0 {
		t.Fatalf("empty artifact authorization scope = %#v, %v", emptyArtifacts, err)
	}
	all, err := repository.ListRunsPageForScope(ctx, store.RunAuthorizationScope{AllProjects: true}, store.Page{Enabled: true, Limit: 100})
	if err != nil || all.Total < 150 || len(all.Items) != 100 {
		t.Fatalf("all-project run scope = %#v, %v", all, err)
	}
	allLogs, err := repository.ListRunLogsPageForScope(ctx, store.RunAuthorizationScope{AllProjects: true}, store.Page{Enabled: true, Limit: 100})
	if err != nil || allLogs.Total < 150 || len(allLogs.Items) != 100 {
		t.Fatalf("all-project log scope = %#v, %v", allLogs, err)
	}
	allArtifacts, err := repository.ListArtifactsPageForScope(ctx, store.RunAuthorizationScope{AllProjects: true}, store.Page{Enabled: true, Limit: 100})
	if err != nil || allArtifacts.Total < 150 || len(allArtifacts.Items) != 100 {
		t.Fatalf("all-project artifact scope = %#v, %v", allArtifacts, err)
	}
}

func postgresArtifactSeeder(database *pgxpool.Pool) func(context.Context, domain.ArtifactRecord) error {
	return func(ctx context.Context, artifact domain.ArtifactRecord) error {
		if _, err := database.Exec(ctx, `INSERT INTO runners (id,name,status) VALUES ('runner_page_contract','pagination contract','active') ON CONFLICT (id) DO NOTHING`); err != nil {
			return err
		}
		if _, err := database.Exec(ctx, `INSERT INTO run_leases (id,run_id,runner_id,status,expires_at,completed_at,attempt,fence) VALUES ($1,$2,'runner_page_contract','succeeded',$3,$3,1,$4)`, artifact.LeaseID, artifact.RunID, artifact.CreatedAt, "fence_"+artifact.RunID); err != nil {
			return err
		}
		return database.QueryRow(ctx, `INSERT INTO run_artifacts (id,run_id,lease_id,name,path,found,required,size,kind,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, artifact.ID, artifact.RunID, artifact.LeaseID, artifact.Name, artifact.Path, artifact.Found, artifact.Required, artifact.Size, artifact.Kind, artifact.CreatedAt).Scan(&artifact.ID)
	}
}
