package store

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
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

func TestMemoryMalformedBuildCannotApproveClaimOrRequeue(t *testing.T) {
	s := NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash")
	now := time.Now().UTC()
	run := domain.TaskRun{ID: "build_run_malformed", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.HostingBuildTaskType, HostingBuildID: "malformed"}, Status: domain.RunRunning, RequestedBy: "usr_bootstrap"}
	s.runs = append(s.runs, run)
	s.leases = append(s.leases, domain.RunLease{ID: "lease_malformed", RunID: run.ID, RunnerID: "absent", Status: domain.LeaseActive, ExpiresAt: now.Add(-time.Second)})
	if _, err := s.CreateApproval(t.Context(), domain.Approval{ID: "bad", RunID: run.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval bypass %v", err)
	}
	if err := s.ExpireLeases(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.runs {
		if r.ID == run.ID && r.Status != domain.RunFailed {
			t.Fatalf("internal build requeued: %s", r.Status)
		}
	}
}

func TestDisabledBuildControllerPreservesOtherWorkers(t *testing.T) {
	handler := deploymentOnlyHandler{}
	if _, ok := any(handler).(hostingjobs.BuildHandler); ok {
		t.Fatal("disabled build handler registered")
	}
	if _, ok := any(handler).(hostingjobs.ProxyHandler); !ok {
		t.Fatal("disabled builds removed proxy handler")
	}
}
