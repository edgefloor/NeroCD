package store

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryClaimRejectsPersistedNestedHostingSpec(t *testing.T) {
	s := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash")
	now := time.Now().UTC()
	if _, err := s.RegisterRunner(t.Context(), domain.Runner{ID: "runner_internal_guard", Name: "guard", Capabilities: []string{"ansible", domain.RunTypeImageDeploy}, Tags: []string{}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	bad := domain.TaskRun{ID: "run_nested_internal", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy}}}}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	if _, err := s.CreateRun(t.Context(), bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic create accepted nested image task: %v", err)
	}
	// Simulate an older persisted row; no process command is present or executed.
	s.runs = append(s.runs, bad)
	before := len(s.leases)
	if _, err := s.ClaimRun(t.Context(), "runner_internal_guard", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nested image task claimed: %v", err)
	}
	if len(s.leases) != before {
		t.Fatal("rejected workflow acquired lease authority")
	}
	good := bad
	good.ID = "run_generic_allowed"
	good.Workflow.Steps = []domain.WorkflowStep{{ID: "generic", RunSpec: domain.RunSpec{Type: "ansible"}}}
	if _, err := s.CreateRun(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimRun(t.Context(), "runner_internal_guard", now, time.Minute)
	if err != nil || claim.Run.ID != good.ID {
		t.Fatalf("legitimate workflow denied: %+v %v", claim, err)
	}
}
