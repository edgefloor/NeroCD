package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestAuthorizedRunResourcesRemainVisibleBeyondFirstHundred(t *testing.T) {
	mem := newSeededTestStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: mem, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 150; i++ {
		projectID := "proj_security"
		prefix := "security"
		if i >= 130 {
			projectID = "proj_platform"
			prefix = "platform"
		}
		runID := fmt.Sprintf("run_authorized_%s_%03d", prefix, i)
		run := domain.TaskRun{ID: runID, ProjectID: projectID, RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: started.Add(time.Duration(i) * time.Second)}
		if _, err := mem.CreateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		if err := mem.CreateRunLog(t.Context(), domain.RunLog{ID: "log_" + runID, RunID: runID, Sequence: 1, Stream: domain.LogStdout, Message: runID, CreatedAt: run.StartedAt}); err != nil {
			t.Fatal(err)
		}
		if err := mem.CreateArtifact(t.Context(), domain.ArtifactRecord{ID: "art_" + runID, RunID: runID, LeaseID: "lease_" + runID, Name: runID, Path: runID, Found: true, Kind: domain.ArtifactFile, CreatedAt: run.StartedAt}); err != nil {
			t.Fatal(err)
		}
	}

	viewer := auth.Principal{ID: "usr_viewer", Email: "viewer@example.local", Name: "Security Viewer", Provider: domain.PrincipalLocal}
	ctx := auth.WithPrincipal(t.Context(), viewer)
	page := store.Page{Enabled: true, Limit: 100, Offset: 100}
	runs, err := service.ListRunsPage(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	if runs.Total != 131 || len(runs.Items) != 31 {
		t.Fatalf("authorized later run page = %#v", runs)
	}
	for _, run := range runs.Items {
		if run.ProjectID != "proj_security" {
			t.Fatalf("run page leaked project %s", run.ProjectID)
		}
	}
	logs, err := service.ListRunLogsPage(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	if logs.Total != 131 || len(logs.Items) != 31 {
		t.Fatalf("authorized later log page = %#v", logs)
	}
	artifacts, err := service.ListArtifactsPage(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Total != 130 || len(artifacts.Items) != 30 {
		t.Fatalf("authorized later artifact page = %#v", artifacts)
	}

	run, err := service.RunByID(ctx, "run_authorized_security_000")
	if err != nil || run.ID != "run_authorized_security_000" {
		t.Fatalf("authorized exact run = %#v, %v", run, err)
	}
	if _, err := service.RunByID(ctx, "run_authorized_platform_130"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("inaccessible run error = %v, want not found", err)
	}
	if _, err := service.RunByID(ctx, "run_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run error = %v, want not found", err)
	}
}
