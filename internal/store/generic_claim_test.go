package store_test

import (
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type genericClaimStore interface {
	store.RunRepository
	store.RunnerRepository
}

func testGenericClaimSkipsUnauthorizedDefinitions(t *testing.T, repo genericClaimStore) {
	t.Helper()
	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner_generic_claim", Name: "generic claim", Tags: []string{"claim-regression"}, Capabilities: []string{"ansible", domain.RunTypeComposeDeploy}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}
	if _, err := repo.RegisterRunner(t.Context(), runner); err != nil {
		t.Fatal(err)
	}
	unsafe := []domain.TaskRun{
		{ID: "unsafe_compose_process", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Process: &domain.ProcessSpec{Command: []string{"not-executed"}}}},
		{ID: "unsafe_compose_reference", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": "no-persisted-deployment"}}},
		{ID: "unsafe_nested_compose", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "compose", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}}}}},
		{ID: "unsafe_step_approval", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "approval", RequiresAck: true, RunSpec: domain.RunSpec{Type: "ansible"}}}}},
	}
	for i, run := range append(unsafe, domain.TaskRun{ID: "safe_generic", RunSpec: domain.RunSpec{Type: "ansible"}}) {
		run.ProjectID, run.Status, run.RequestedBy = "proj_platform", domain.RunQueued, "usr_bootstrap"
		run.StartedAt, run.RunnerTags = now.Add(time.Duration(i)*time.Millisecond), []string{"claim-regression"}
		// CreateRun represents a previously persisted/imported task. Admission via
		// the service is tested separately; claim must also reject historical rows.
		if _, err := repo.CreateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := repo.ClaimRun(t.Context(), runner.ID, now.Add(time.Second), time.Minute)
	if err != nil || claim.Run.ID != "safe_generic" {
		t.Fatalf("unsafe definition claimed or blocked valid work: id=%s err=%v", claim.Run.ID, err)
	}
	for _, run := range unsafe {
		stored, err := repo.GetRun(t.Context(), run.ID)
		if err != nil || stored.Status != domain.RunQueued || stored.RunnerID != nil {
			t.Fatalf("unsafe run acquired authority: %+v %v", stored, err)
		}
	}
}

func TestMemoryClaimSkipsUnauthorizedDefinitions(t *testing.T) {
	testGenericClaimSkipsUnauthorizedDefinitions(t, store.NewMemoryStore())
}

func TestPostgresClaimSkipsUnauthorizedDefinitions(t *testing.T) {
	pg, _ := openPostgresIntegrationStore(t, t.Context(), "generic_claim")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	testGenericClaimSkipsUnauthorizedDefinitions(t, pg)
}
