package store

import (
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryExpiryMatchesSupportedPostgresLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, kind, status, want string
		expires                  time.Time
		expired                  bool
	}{
		{"generic", domain.RunTypeShell, domain.RunRunning, domain.RunQueued, now, true},
		{"deployment", domain.RunTypeComposeDeploy, domain.RunRunning, domain.RunQueued, now.Add(-time.Second), true},
		{"malformed_build", domain.HostingBuildTaskType, domain.RunRunning, domain.RunFailed, now, true},
		{"future", domain.RunTypeShell, domain.RunRunning, domain.RunRunning, now.Add(time.Second), false},
		{"terminal", domain.RunTypeShell, domain.RunSucceeded, domain.RunSucceeded, now, false},
		{"queued", domain.RunTypeShell, domain.RunQueued, domain.RunQueued, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryStore()
			runner := "runner"
			s.runs = []domain.TaskRun{{ID: "run", RunSpec: domain.RunSpec{Type: tc.kind}, Status: tc.status, RunnerID: &runner}}
			s.leases = []domain.RunLease{{ID: "lease", RunID: "run", RunnerID: runner, Attempt: 2, Fence: "fence", Status: domain.LeaseActive, ExpiresAt: tc.expires}}
			s.deploymentAttempts = []domain.DeploymentAttempt{{LeaseID: "lease", Status: "active"}, {LeaseID: "another", Status: "active"}}
			if err := s.ExpireLeases(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			run, lease, attempt := s.runs[0], s.leases[0], s.deploymentAttempts[0]
			if run.Status != tc.want {
				t.Fatalf("run=%s want=%s", run.Status, tc.want)
			}
			if tc.expired {
				if lease.Status != domain.LeaseExpired || lease.CompletedAt == nil || !lease.CompletedAt.Equal(now) || lease.Attempt != 2 || lease.Fence != "fence" || run.RunnerID != nil || attempt.Status != "failed" || attempt.FinishedAt == nil {
					t.Fatal("incomplete settlement", run, lease, attempt)
				}
				if (run.FinishedAt != nil) != (tc.want == domain.RunFailed) {
					t.Fatal("incorrect finish time", run)
				}
				if !s.claimOrderByRun[run.ID].Equal(now) {
					t.Fatal("claim order not advanced")
				}
			} else if lease.Status != domain.LeaseActive || attempt.Status != "active" {
				t.Fatal("unselected authority settled", lease, attempt)
			}
			if s.deploymentAttempts[1].Status != "active" {
				t.Fatal("unrelated attempt changed")
			}
			if err := s.ExpireLeases(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			if s.runs[0].Status != tc.want || s.leases[0].Status != lease.Status {
				t.Fatal("repeated expiry changed settled state")
			}
		})
	}
}
