package store

import (
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryStoreClaimMarksStaleRunner(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	_, err := mem.RegisterRunner(t.Context(), domain.Runner{
		ID:              "runner_stale",
		Name:            "Stale Runner",
		Tags:            []string{"local"},
		Capabilities:    []string{"shell"},
		TokenHash:       "hash",
		Status:          "active",
		RegisteredAt:    now.Add(-10 * time.Minute),
		LastHeartbeatAt: now.Add(-5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mem.ClaimRun(t.Context(), "runner_stale", now, 2*time.Minute); err != ErrNotFound {
		t.Fatalf("ClaimRun with stale runner error = %v, want ErrNotFound", err)
	}

	runners, err := mem.ListRunners(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, runner := range runners {
		if runner.ID == "runner_stale" && runner.Status != "stale" {
			t.Fatalf("stale runner status = %q, want stale", runner.Status)
		}
	}
}

func TestMemoryStoreClaimRequeuesExpiredLease(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	run := domain.TaskRun{
		ID:          "run_expiring",
		ProjectID:   "proj_platform",
		RunSpec:     domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo ok"}, Process: &domain.ProcessSpec{Command: []string{"echo", "ok"}}},
		Workflow:    domain.Workflow{},
		RunnerTags:  []string{"local"},
		Status:      "queued",
		RequestedBy: "usr_bootstrap",
		StartedAt:   now.Add(-10 * time.Minute),
	}
	if _, err := mem.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"runner_first", "runner_second"} {
		if _, err := mem.RegisterRunner(t.Context(), domain.Runner{
			ID:              id,
			Name:            id,
			Tags:            []string{"local"},
			Capabilities:    []string{"shell"},
			TokenHash:       id + "_hash",
			Status:          "active",
			RegisteredAt:    now,
			LastHeartbeatAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := mem.ClaimRun(t.Context(), "runner_first", now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mem.ClaimRun(t.Context(), "runner_second", now.Add(3*time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Lease.ID == second.Lease.ID {
		t.Fatalf("reclaimed lease reused id %q", first.Lease.ID)
	}
	if second.Run.ID != run.ID || second.Run.RunnerID == nil || *second.Run.RunnerID != "runner_second" {
		t.Fatalf("unexpected reclaimed claim: %#v", second.Run)
	}
	if _, err := mem.CompleteLease(t.Context(), first.Lease.ID, "runner_first", "succeeded", now.Add(3*time.Minute)); err != ErrNotFound {
		t.Fatalf("expired first lease completion error = %v, want ErrNotFound", err)
	}
}
