package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type hostingContractStore interface {
	store.HostingRepository
	store.AuditRepository
}

func TestHostingRepositoryContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		hostingRepositoryContract(t, store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash"))
	})
	t.Run("postgres", func(t *testing.T) {
		pg, _ := openPostgresIntegrationStore(t, t.Context(), "hosting")
		defer closePostgresStore(t, pg)
		hostingRepositoryContract(t, pg)
	})
}
func hostingRepositoryContract(t *testing.T, s hostingContractStore) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := domain.AuditEvent{ID: "aud_hosting_environment", ActorID: "usr_bootstrap", Action: "project_environment.created", TargetID: "env_hosting", Metadata: map[string]any{"project_id": "proj_platform"}, CreatedAt: now}
	environment, err := s.CreateProjectEnvironment(ctx, domain.ProjectEnvironment{ID: "env_hosting", ProjectID: "proj_platform", Name: "production", CreatedAt: now}, store.WithAudit(audit))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := environment
	duplicate.ID = "env_duplicate"
	if _, err = s.CreateProjectEnvironment(ctx, duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate env: %v", err)
	}
	other := environment
	other.ID = "env_other"
	other.ProjectID = "proj_security"
	if _, err = s.CreateProjectEnvironment(ctx, other); err != nil {
		t.Fatal(err)
	}
	failedEnv := environment
	failedEnv.ID = "env_failed_audit"
	failedEnv.Name = "failed audit"
	if _, err = s.CreateProjectEnvironment(ctx, failedEnv, store.WithAudit(audit)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate audit: %v", err)
	}
	if _, err = s.GetProjectEnvironment(ctx, failedEnv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed audit leaked environment: %v", err)
	}
	item := domain.Resource{ID: "res_hosting", ProjectID: environment.ProjectID, ProjectEnvironmentID: environment.ID, Name: "web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "localhost:5000/team/web:v1"}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}
	createAudit := audit
	createAudit.ID = "aud_hosting_resource"
	createAudit.Action = "resource.created"
	createAudit.TargetID = item.ID
	created, err := s.CreateResource(ctx, item, store.WithAudit(createAudit))
	if err != nil || created.Generation != 1 {
		t.Fatalf("create: %+v %v", created, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Resource)
	}{
		{"duplicate name", func(r *domain.Resource) { r.ID = "res_duplicate" }},
		{"cross project", func(r *domain.Resource) { r.ID = "res_cross"; r.ProjectID = "proj_security" }},
		{"missing env", func(r *domain.Resource) { r.ID = "res_missing"; r.ProjectEnvironmentID = "missing" }},
		{"missing creator", func(r *domain.Resource) { r.ID = "res_creator"; r.CreatedBy = "missing" }},
		{"invalid source", func(r *domain.Resource) { r.ID = "res_invalid"; r.Source.Image.Reference = "nginx" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := item
			tc.mutate(&candidate)
			if _, err := s.CreateResource(ctx, candidate); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("error: %v", err)
			}
		})
	}
	failed := item
	failed.ID = "res_failed_audit"
	failed.Name = "failed audit"
	if _, err = s.CreateResource(ctx, failed, store.WithAudit(createAudit)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("create audit conflict: %v", err)
	}
	if _, err = s.GetResource(ctx, failed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed create leaked: %v", err)
	}
	changed := created
	changed.Name = "renamed"
	changed.UpdatedAt = now.Add(time.Second)
	if _, err = s.UpdateResource(ctx, changed, 1, store.WithAudit(createAudit)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update audit conflict: %v", err)
	}
	unchanged, err := s.GetResource(ctx, item.ID)
	if err != nil || unchanged.Generation != 1 || unchanged.Name != "web" {
		t.Fatalf("audit rollback: %+v %v", unchanged, err)
	}
	immutable := created
	immutable.ProjectEnvironmentID = other.ID
	if _, err = s.UpdateResource(ctx, immutable, 1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("immutable scope: %v", err)
	}
	// All contenders start with the same generation. Exactly one may commit and audit.
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := changed
			candidate.Name = fmt.Sprintf("web-%d", i)
			event := createAudit
			event.ID = fmt.Sprintf("aud_hosting_cas_%d", i)
			event.Action = "resource.updated"
			_, err := s.UpdateResource(ctx, candidate, 1, store.WithAudit(event))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes = %d", success)
	}
	updated, err := s.GetResource(ctx, item.ID)
	if err != nil || updated.Generation != 2 || updated.Source != created.Source {
		t.Fatalf("CAS result: %+v %v", updated, err)
	}
	if _, err = s.UpdateResource(ctx, changed, 1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	events, err := s.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Action == "resource.updated" && e.TargetID == item.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("CAS audits = %d", count)
	}
	page, err := s.ListResources(ctx, environment.ProjectID, environment.ID, store.Page{Enabled: true, Limit: 1, Offset: 50, LimitSpecified: true})
	if err != nil || page.Total != 1 || len(page.Items) != 0 || page.Offset != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}
	envPage, err := s.ListProjectEnvironments(ctx, environment.ProjectID, store.Page{})
	if err != nil || envPage.Total != 1 || len(envPage.Items) != 1 {
		t.Fatalf("env page: %+v %v", envPage, err)
	}
}
