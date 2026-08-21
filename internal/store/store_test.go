package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"nerocd/internal/observability"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestMemoryOperationalSnapshotAggregatesWithoutIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	passed := true
	mem := NewMemoryStore()
	mem.runs = []domain.TaskRun{{ID: "private-run", Status: domain.RunQueued, StartedAt: now.Add(-2 * time.Minute)}, {ID: "done", Status: domain.RunSucceeded, StartedAt: now.Add(-3 * time.Minute), FinishedAt: &finished}}
	mem.runners = []domain.Runner{{ID: "private-runner", LastHeartbeatAt: now.Add(-time.Minute)}}
	mem.leases = []domain.RunLease{{ID: "lease", Status: domain.LeaseActive}, {ID: "expired", Status: domain.LeaseExpired}}
	mem.deployments = []domain.Deployment{{ID: "deployment", Status: domain.DeploymentSucceeded, HealthPassed: &passed}}
	if err := mem.RecordRunnerOperationalObservation(t.Context(), "private-runner", 3, 2, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err := mem.OperationalSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.QueueDepth != 1 || snapshot.ActiveLeases != 1 || snapshot.ExpiredLeases != 1 || snapshot.RunnerJournalDepth != 3 || snapshot.TerminalRuns[domain.RunSucceeded].Count != 1 || snapshot.DeploymentHealthPassed != 1 || snapshot.BackupOutcome != observability.BackupNone {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestMemoryRunLogRetentionIsBoundedReplaySafeAndAtomic(t *testing.T) {
	now := time.Now().UTC()
	finished := now.Add(-48 * time.Hour)
	mem := NewMemoryStore()
	mem.runs = []domain.TaskRun{{ID: "retention_done", Status: domain.RunSucceeded, FinishedAt: &finished}, {ID: "retention_active", Status: domain.RunSucceeded, FinishedAt: &finished}}
	mem.logs = []domain.RunLog{
		{ID: "retention_old_1", RunID: "retention_done", Message: "abc", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "retention_old_2", RunID: "retention_done", Message: "wxyz", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "retention_leased", RunID: "retention_active", Message: "must remain", CreatedAt: now.Add(-48 * time.Hour)},
	}
	mem.leases = []domain.RunLease{{ID: "retention_lease", RunID: "retention_active", Status: domain.LeaseActive}}
	policy, err := mem.UpdateRunLogRetentionPolicy(t.Context(), domain.RunLogRetentionPolicy{Enabled: true, KeepDays: 1, BatchSize: 1, UpdatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	body := RunLogRetentionBodyHash(policy)
	first, err := mem.ExecuteRunLogRetention(t.Context(), "retention_request", body, domain.AuditEvent{ID: "retention_audit", Action: "run_log_retention.execute"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deleted != 1 || first.DeletedBytes != 3 || first.Preview.EligibleLogs != 2 || first.Preview.EligibleBytes != 7 || len(mem.logs) != 2 {
		t.Fatalf("first retention=%#v logs=%#v", first, mem.logs)
	}
	replayed, err := mem.ExecuteRunLogRetention(t.Context(), "retention_request", body, domain.AuditEvent{ID: "other_audit"})
	if err != nil || replayed != first || len(mem.logs) != 2 || len(mem.auditEvents) != 1 {
		t.Fatalf("replay=%#v err=%v logs=%d audits=%d", replayed, err, len(mem.logs), len(mem.auditEvents))
	}
	if _, err := mem.ExecuteRunLogRetention(t.Context(), "retention_request", RunLogRetentionBodyHash(domain.RunLogRetentionPolicy{Version: policy.Version + 1}), domain.AuditEvent{ID: "conflict_audit"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	before := append([]domain.RunLog(nil), mem.logs...)
	if _, err := mem.ExecuteRunLogRetention(t.Context(), "retention_audit_conflict", body, domain.AuditEvent{ID: "retention_audit"}); err == nil || len(mem.logs) != len(before) {
		t.Fatalf("audit conflict err=%v logs=%#v", err, mem.logs)
	}
	if mem.logs[1].ID != "retention_leased" && mem.logs[0].ID != "retention_leased" {
		t.Fatalf("active-lease log was removed: %#v", mem.logs)
	}
}

func TestMemoryStoreBoundedClaimCursorProgressesAndWraps(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{
		ID:              "runner_cursor",
		Name:            "Cursor Runner",
		Tags:            []string{"local"},
		Capabilities:    []string{"shell"},
		Status:          domain.RunnerActive,
		RegisteredAt:    now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < claimCandidateLimit; i++ {
		run := domain.TaskRun{
			ID:          fmt.Sprintf("run_memory_incompatible_%03d", i),
			ProjectID:   "proj_platform",
			RunSpec:     domain.RunSpec{Type: domain.RunTypeShell},
			RunnerTags:  []string{"other"},
			Status:      domain.RunQueued,
			RequestedBy: "usr_bootstrap",
			StartedAt:   now.Add(time.Duration(i+1) * time.Microsecond),
		}
		if _, err := mem.CreateRun(t.Context(), run); err != nil {
			t.Fatalf("create incompatible run %d: %v", i, err)
		}
	}
	if _, err := mem.CreateRun(t.Context(), domain.TaskRun{
		ID:          "run_memory_after_bound",
		ProjectID:   "proj_platform",
		RunSpec:     domain.RunSpec{Type: domain.RunTypeShell},
		RunnerTags:  []string{"local"},
		Status:      domain.RunQueued,
		RequestedBy: "usr_bootstrap",
		StartedAt:   now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := mem.ClaimRun(t.Context(), "runner_cursor", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first bounded claim error = %v, want ErrNotFound", err)
	}
	claim, err := mem.ClaimRun(t.Context(), "runner_cursor", now, time.Minute)
	if err != nil {
		t.Fatalf("second bounded claim: %v", err)
	}
	if claim.Run.ID != "run_memory_after_bound" || claim.Lease.Attempt != 1 || claim.Lease.Fence == "" {
		t.Fatalf("progressed claim = %#v", claim)
	}
	finishedAt := now.Add(time.Second)
	if _, err := mem.CompleteLeaseRequest(t.Context(), claim.Lease.ID, "runner_cursor", domain.RunSucceeded, claim.Lease.Attempt, claim.Lease.Fence, "completion_cursor", finishedAt, domain.RunSucceeded, &finishedAt, nil, nil, domain.AuditEvent{}); err != nil {
		t.Fatalf("complete progressed claim: %v", err)
	}
	// Reaching the tail resets the cursor. A subsequently inserted earlier key
	// must then become visible instead of being permanently stranded.
	if _, err := mem.ClaimRun(t.Context(), "runner_cursor", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("queue-tail reset claim error = %v, want ErrNotFound", err)
	}
	if _, err := mem.CreateRun(t.Context(), domain.TaskRun{
		ID:          "run_memory_before_cursor",
		ProjectID:   "proj_platform",
		RunSpec:     domain.RunSpec{Type: domain.RunTypeShell},
		RunnerTags:  []string{"local"},
		Status:      domain.RunQueued,
		RequestedBy: "usr_bootstrap",
		StartedAt:   now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	wrapped, err := mem.ClaimRun(t.Context(), "runner_cursor", now, time.Minute)
	if err != nil || wrapped.Run.ID != "run_memory_before_cursor" {
		t.Fatalf("wrapped claim run = %q, err = %v", wrapped.Run.ID, err)
	}
	runs, err := mem.ListRuns(t.Context(), "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if len(run.ID) >= len("run_memory_incompatible_") && run.ID[:len("run_memory_incompatible_")] == "run_memory_incompatible_" && run.Status != domain.RunQueued {
			t.Fatalf("incompatible run %q was mutated to %q", run.ID, run.Status)
		}
	}
}

func TestMemorySessionLastSeenIsIntervalGated(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	session := domain.Session{ID: "ses_seen", UserID: "user_seen", CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: &now}
	mem.users = append(mem.users, domain.User{ID: "user_seen", Email: "seen@example.invalid", Status: domain.UserActive})
	if err := mem.CreateSession(t.Context(), session, "session-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.GetPrincipalBySessionTokenHash(t.Context(), "session-token", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := *mem.sessions[0].LastSeenAt; !got.Equal(now) {
		t.Fatalf("last_seen updated before interval: %s", got)
	}
	after := now.Add(SessionLastSeenUpdateInterval)
	if _, err := mem.GetPrincipalBySessionTokenHash(t.Context(), "session-token", after); err != nil {
		t.Fatal(err)
	}
	if got := *mem.sessions[0].LastSeenAt; !got.Equal(after) {
		t.Fatalf("last_seen=%s want=%s", got, after)
	}
}

func TestMemoryClaimAndApprovalAuditAreAtomic(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	mem := NewMemoryStore()
	if _, err := mem.RegisterRunner(ctx, domain.Runner{ID: "runner_audit", Name: "audit", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateRun(ctx, domain.TaskRun{ID: "run_claim_audit", ProjectID: "project", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "user", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimRun(ctx, "runner_audit", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if events, err := mem.ListAuditEvents(ctx); err != nil || len(events) != 0 {
		t.Fatalf("claim without audit events=%#v err=%v", events, err)
	}
	if _, err := mem.CreateRun(ctx, domain.TaskRun{ID: "run_claim_duplicate", ProjectID: "project", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "user", StartedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	duplicate := domain.AuditEvent{ID: "aud_claim_duplicate", ActorID: "runner_audit", Action: "runner.claim", CreatedAt: now}
	if err := mem.CreateAuditEvent(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimRunWithAudit(ctx, "runner_audit", now.Add(time.Second), time.Minute, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim with duplicate audit error=%v, want conflict", err)
	}
	runs, err := mem.ListRuns(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == "run_claim_duplicate" && run.Status != domain.RunQueued {
			t.Fatalf("audit failure partially claimed run: %#v", run)
		}
	}

	fixture := NewFixtureMemoryStore("admin@example.invalid", "viewer@example.invalid", "hash-a", "hash-b")
	approveAudit := domain.AuditEvent{ID: "aud_approve", ActorID: "usr_bootstrap", Action: "run.approve", CreatedAt: now}
	approval, err := fixture.ApproveRunWithAudit(ctx, "run_002", "usr_bootstrap", now, approveAudit)
	if err != nil {
		t.Fatal(err)
	}
	events, err := fixture.ListAuditEvents(ctx)
	if err != nil || len(events) != 1 || events[0].TargetID != "run_002" || events[0].Metadata["approval_id"] != approval.ID {
		t.Fatalf("approve audit=%#v err=%v", events, err)
	}
	if _, err := fixture.ApproveRunWithAudit(ctx, "run_002", "usr_bootstrap", now, approveAudit); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat approval error=%v, want conflict", err)
	}
}

func TestMemoryStoreEventReplayRequiresExactAttemptIdentity(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	mem := NewMemoryStore()
	runnerID := "runner_memory_replay"
	if _, err := mem.RegisterRunner(ctx, domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run_memory_replay", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	if _, err := mem.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claim, err := mem.ClaimRun(ctx, runnerID, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []domain.RunLog{{ID: "log_memory_replay", RunID: run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, EventKey: "event_memory_replay", Sequence: 1, RequestedSequence: 1, Stream: domain.LogStdout, Message: "one", CreatedAt: now}}
	first, err := mem.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := mem.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, claim.Lease.Fence, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(replayed) != 1 || first[0].ID != replayed[0].ID {
		t.Fatalf("exact replay changed event: first=%#v replayed=%#v", first, replayed)
	}
	if _, err := mem.CreateRunLogsForLease(ctx, events, run.ID, runnerID, claim.Lease.ID, claim.Lease.Attempt, "wrong-fence", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-fence exact replay error=%v, want ErrNotFound", err)
	}
}

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
	_, err = mem.RegisterRunner(t.Context(), domain.Runner{
		ID:              "runner_unrelated_stale",
		Name:            "Unrelated Stale Runner",
		Tags:            []string{"local"},
		Capabilities:    []string{"shell"},
		TokenHash:       "unrelated_hash",
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
	statuses := map[string]string{}
	for _, runner := range runners {
		statuses[runner.ID] = runner.Status
	}
	if statuses["runner_stale"] != domain.RunnerStale {
		t.Fatalf("requested stale runner status = %q, want stale", statuses["runner_stale"])
	}
	if statuses["runner_unrelated_stale"] != domain.RunnerActive {
		t.Fatalf("unrelated stale-heartbeat runner status = %q, want active", statuses["runner_unrelated_stale"])
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
	if first.Lease.Attempt != 1 || second.Lease.Attempt != 2 {
		t.Fatalf("lease attempts first=%d second=%d", first.Lease.Attempt, second.Lease.Attempt)
	}
	if second.Run.ID != run.ID || second.Run.RunnerID == nil || *second.Run.RunnerID != "runner_second" {
		t.Fatalf("unexpected reclaimed claim: %#v", second.Run)
	}
	if _, err := mem.CompleteLeaseRequest(t.Context(), first.Lease.ID, "runner_first", "succeeded", first.Lease.Attempt, first.Lease.Fence, "completion_stale", now.Add(3*time.Minute), domain.RunSucceeded, nil, nil, nil, domain.AuditEvent{}); err != ErrNotFound {
		t.Fatalf("expired first lease completion error = %v, want ErrNotFound", err)
	}
}

func TestMemoryStoreRequeuedRunAdvancesPastCursor(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	for _, runnerID := range []string{"runner_memory_owner", "runner_memory_seeker"} {
		if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: runnerID, Name: runnerID, Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	target := domain.TaskRun{ID: "run_memory_requeue_target", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-2 * time.Minute)}
	if _, err := mem.CreateRun(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	firstTarget, err := mem.ClaimRun(t.Context(), "runner_memory_owner", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	anchor := domain.TaskRun{ID: "run_memory_requeue_anchor", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-time.Minute)}
	if _, err := mem.CreateRun(t.Context(), anchor); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimRun(t.Context(), "runner_memory_seeker", now, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		run := domain.TaskRun{ID: fmt.Sprintf("run_memory_requeue_ahead_%d", i), ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-30*time.Second + time.Duration(i)*time.Second)}
		if _, err := mem.CreateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	requeuedAt := now.Add(2 * time.Minute)
	if err := mem.ExpireLeases(t.Context(), requeuedAt); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 6; i++ {
		newer := domain.TaskRun{ID: fmt.Sprintf("run_memory_requeue_newer_%d", i), ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: requeuedAt.Add(time.Duration(i+1) * time.Minute)}
		if _, err := mem.CreateRun(t.Context(), newer); err != nil {
			t.Fatal(err)
		}
		claim, err := mem.ClaimRun(t.Context(), "runner_memory_seeker", requeuedAt, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claim.Run.ID == target.ID {
			if claim.Lease.Attempt != firstTarget.Lease.Attempt+1 {
				t.Fatalf("requeued attempt=%d, want %d", claim.Lease.Attempt, firstTarget.Lease.Attempt+1)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("memory requeued run starved behind durable cursor")
	}
	runs, err := mem.ListRuns(t.Context(), "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == target.ID && !run.StartedAt.Equal(target.StartedAt) {
			t.Fatalf("memory started_at changed: before=%s after=%s", target.StartedAt, run.StartedAt)
		}
	}
}

func TestMemoryStoreApprovedRunAdvancesPastCursor(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 8, 15, 16, 0, 0, 0, time.UTC)
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_memory_approval", Name: "approval", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	blocked := domain.TaskRun{ID: "run_memory_approval_target", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunWaitingApproval, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-2 * time.Minute)}
	if _, err := mem.CreateRun(t.Context(), blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateApproval(t.Context(), domain.Approval{ID: "approval_memory_target", RunID: blocked.ID, Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: blocked.StartedAt}); err != nil {
		t.Fatal(err)
	}
	anchor := domain.TaskRun{ID: "run_memory_approval_anchor", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-time.Minute)}
	if _, err := mem.CreateRun(t.Context(), anchor); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimRun(t.Context(), "runner_memory_approval", now, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		run := domain.TaskRun{ID: fmt.Sprintf("run_memory_approval_ahead_%d", i), ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-30*time.Second + time.Duration(i)*time.Second)}
		if _, err := mem.CreateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	approvedAt := now.Add(2 * time.Minute)
	if _, err := mem.ApproveRun(t.Context(), blocked.ID, "usr_bootstrap", approvedAt); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 6; i++ {
		newer := domain.TaskRun{ID: fmt.Sprintf("run_memory_approval_newer_%d", i), ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, RunnerTags: []string{"local"}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: approvedAt.Add(time.Duration(i+1) * time.Minute)}
		if _, err := mem.CreateRun(t.Context(), newer); err != nil {
			t.Fatal(err)
		}
		claim, err := mem.ClaimRun(t.Context(), "runner_memory_approval", approvedAt, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claim.Run.ID == blocked.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("approved memory run starved behind durable cursor")
	}
	runs, err := mem.ListRuns(t.Context(), blocked.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == blocked.ID && !run.StartedAt.Equal(blocked.StartedAt) {
			t.Fatalf("approval changed started_at: before=%s after=%s", blocked.StartedAt, run.StartedAt)
		}
	}
}

func TestMemoryStoreSecretAccessIsFencedIdempotentAndSafe(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	runnerID := "runner_secret_memory"
	runID := "run_secret_memory"
	leaseID := "lease_secret_memory"
	mem.runners = append(mem.runners, domain.Runner{ID: runnerID, Status: domain.RunnerActive})
	secretBinding := domain.SecretBinding{Name: "database-password", Provider: domain.ProviderRunnerFile, Reference: "database-password", Target: "env:DATABASE_PASSWORD", Required: true, Version: "v1", Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	mem.runs = append(mem.runs, domain.TaskRun{ID: runID, ProjectID: "proj_platform", Status: domain.RunRunning, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{secretBinding}}})
	mem.leases = append(mem.leases, domain.RunLease{ID: leaseID, RunID: runID, RunnerID: runnerID, Attempt: 1, Fence: "opaque-fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	request := domain.SecretAccessRequest{
		AccessID: "secret_access_0123456789abcdef0123456789abcdef", RunnerID: runnerID, RunID: runID, LeaseID: leaseID,
		Attempt: 1, Fence: "opaque-fence", Binding: "database-password", Provider: domain.ProviderRunnerFile,
		Version: "v1", RequestedAt: now,
	}
	first, err := mem.AuthorizeSecretAccess(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	grantJSON, _ := json.Marshal(first)
	if strings.Contains(string(grantJSON), "fingerprint") || strings.Contains(string(grantJSON), strings.Repeat("a", 64)) {
		t.Fatalf("grant leaked configured fingerprint: %s", grantJSON)
	}
	replayed, err := mem.AuthorizeSecretAccess(t.Context(), request)
	if err != nil || replayed != first {
		t.Fatalf("replayed=%#v first=%#v err=%v", replayed, first, err)
	}
	conflict := request
	conflict.Version = "v2"
	if _, err := mem.AuthorizeSecretAccess(t.Context(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	unknown := request
	unknown.AccessID = "secret_access_1123456789abcdef0123456789abcdef"
	unknown.Binding = "not-in-run-spec"
	if _, err := mem.AuthorizeSecretAccess(t.Context(), unknown); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown binding error=%v", err)
	}
	expired := request
	expired.RequestedAt = now.Add(2 * time.Minute)
	if _, err := mem.AuthorizeSecretAccess(t.Context(), expired); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired replay error=%v", err)
	}
	events, _ := mem.ListAuditEvents(t.Context())
	count := 0
	for _, event := range events {
		if event.ID != request.AccessID {
			continue
		}
		count++
		serialized, _ := json.Marshal(event)
		for _, forbidden := range []string{request.Fence, "fingerprint", strings.Repeat("a", 64), "logical-file-reference", "env:DATABASE_PASSWORD", "secret-value"} {
			if strings.Contains(string(serialized), forbidden) {
				t.Fatalf("audit leaked %q: %s", forbidden, serialized)
			}
		}
	}
	if count != 1 {
		t.Fatalf("secret access audit count=%d", count)
	}
}

func TestMemoryStoreSecretAccessUsesOnlyCurrentWorkflowStep(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	makeBinding := func(name, version, fingerprint string) domain.SecretBinding {
		return domain.SecretBinding{Name: name, Provider: domain.ProviderRunnerFile, Reference: name, Target: "env:" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")), Required: true, Version: version, Fingerprint: "sha256:" + strings.Repeat(fingerprint, 64)}
	}
	top := makeBinding("top-secret", "top-v1", "a")
	first := makeBinding("first-secret", "first-v1", "b")
	second := makeBinding("second-secret", "second-v1", "c")
	run := domain.TaskRun{
		ID: "run_secret_workflow_memory", ProjectID: "proj_platform", Status: domain.RunRunning,
		RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{top}},
		Workflow: domain.Workflow{Steps: []domain.WorkflowStep{
			{ID: "first", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{first}}},
			{ID: "second", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Secrets: []domain.SecretBinding{second}}},
		}},
		WorkflowState: domain.WorkflowState{CurrentStepID: "first"},
	}
	mem.runs = append(mem.runs, run)
	mem.leases = append(mem.leases, domain.RunLease{ID: "lease_secret_workflow_memory", RunID: run.ID, RunnerID: "runner_secret_workflow_memory", Attempt: 1, Fence: "workflow-fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	request := func(accessID string, binding domain.SecretBinding) domain.SecretAccessRequest {
		return domain.SecretAccessRequest{AccessID: accessID, RunnerID: "runner_secret_workflow_memory", RunID: run.ID, LeaseID: "lease_secret_workflow_memory", Attempt: 1, Fence: "workflow-fence", Binding: binding.Name, Provider: binding.Provider, Version: binding.Version, RequestedAt: now}
	}
	firstAccessID := "secret_access_10000000000000000000000000000000"
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request(firstAccessID, first)); err != nil {
		t.Fatalf("current first step: %v", err)
	}
	for _, candidate := range []struct {
		name     string
		accessID string
		binding  domain.SecretBinding
	}{
		{name: "top", accessID: "secret_access_20000000000000000000000000000000", binding: top},
		{name: "other_step", accessID: "secret_access_21000000000000000000000000000000", binding: second},
	} {
		if _, err := mem.AuthorizeSecretAccess(t.Context(), request(candidate.accessID, candidate.binding)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s binding error=%v", candidate.name, err)
		}
	}
	run.WorkflowState.CurrentStepID = ""
	if _, err := mem.UpdateRunWorkflowState(t.Context(), run.ID, run.WorkflowState); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request("secret_access_29000000000000000000000000000000", top)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workflow without current step must fail closed, error=%v", err)
	}
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request(firstAccessID, first)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exact prior audit replay without current step error=%v", err)
	}
	run.WorkflowState.CurrentStepID = "second"
	if _, err := mem.UpdateRunWorkflowState(t.Context(), run.ID, run.WorkflowState); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request("secret_access_30000000000000000000000000000000", first)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("prior step after transition error=%v", err)
	}
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request(firstAccessID, first)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exact prior audit replay after transition error=%v", err)
	}
	if _, err := mem.AuthorizeSecretAccess(t.Context(), request("secret_access_40000000000000000000000000000000", second)); err != nil {
		t.Fatalf("current second step: %v", err)
	}
	events, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstAuditCount := 0
	for _, event := range events {
		if event.ID == firstAccessID {
			firstAuditCount++
		}
	}
	if firstAuditCount != 1 {
		t.Fatalf("first access audit count=%d, want 1", firstAuditCount)
	}
}

func TestMemoryStoreRunnerEnrollmentIsOneTimeIdempotentAndSafe(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Now().UTC()
	enrollment := domain.RunnerEnrollment{ID: "enroll_memory_one", TokenHash: strings.Repeat("a", 64), RunnerID: "runner_memory_enrolled", RunnerName: "Memory Enrolled", Tags: []string{"linux"}, Capabilities: []string{"shell"}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	createAudit := domain.AuditEvent{ID: "aud_enroll_memory_create", ActorID: "usr_bootstrap", Action: "runner.enrollment.create", TargetID: enrollment.ID, Metadata: map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}, CreatedAt: now}
	if _, err := mem.CreateRunnerEnrollment(t.Context(), enrollment, createAudit); err != nil {
		t.Fatal(err)
	}
	consume := domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "enroll_consume_0123456789abcdef0123456789abcdef", CredentialHash: strings.Repeat("b", 64)}
	consumeAudit := domain.AuditEvent{ID: "aud_enroll_memory_consume", Action: "runner.enrollment.consume", CreatedAt: now}
	first, err := mem.ConsumeRunnerEnrollment(t.Context(), consume, consumeAudit)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := mem.ConsumeRunnerEnrollment(t.Context(), consume, domain.AuditEvent{ID: "aud_must_not_persist"})
	if err != nil || replayed.ID != first.ID || !replayed.RegisteredAt.Equal(first.RegisteredAt) {
		t.Fatalf("exact replay=(%+v,%v), first=%+v", replayed, err, first)
	}
	conflict := consume
	conflict.RequestID = "enroll_consume_1123456789abcdef0123456789abcdef"
	if _, err := mem.ConsumeRunnerEnrollment(t.Context(), conflict, consumeAudit); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: first.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate runner registration error=%v", err)
	}
	events, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	consumeCount := 0
	for _, event := range events {
		serialized, _ := json.Marshal(event)
		if strings.Contains(string(serialized), enrollment.TokenHash) || strings.Contains(string(serialized), consume.CredentialHash) || strings.Contains(string(serialized), consume.RequestID) {
			t.Fatalf("enrollment audit leaked hash or request identity: %s", serialized)
		}
		if event.Action == "runner.enrollment.consume" {
			consumeCount++
		}
	}
	if consumeCount != 1 {
		t.Fatalf("consume audit count=%d", consumeCount)
	}

	revoked := domain.RunnerEnrollment{ID: "enroll_memory_revoked", TokenHash: strings.Repeat("c", 64), RunnerID: "runner_memory_revoked", RunnerName: "Revoked", Capabilities: []string{"shell"}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := mem.CreateRunnerEnrollment(t.Context(), revoked, domain.AuditEvent{ID: "aud_revoked_create"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.RevokeRunnerEnrollment(t.Context(), revoked.ID, domain.AuditEvent{ID: "aud_revoked", Action: "runner.enrollment.revoke"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ConsumeRunnerEnrollment(t.Context(), domain.RunnerEnrollmentConsume{TokenHash: revoked.TokenHash, RequestID: consume.RequestID, CredentialHash: strings.Repeat("d", 64)}, consumeAudit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked enrollment consume error=%v", err)
	}

	racing := domain.RunnerEnrollment{ID: "enroll_memory_race", TokenHash: strings.Repeat("e", 64), RunnerID: "runner_memory_race", RunnerName: "Race", Capabilities: []string{"shell"}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := mem.CreateRunnerEnrollment(t.Context(), racing, domain.AuditEvent{ID: "aud_memory_race_create"}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			_, raceErr := mem.ConsumeRunnerEnrollment(t.Context(), domain.RunnerEnrollmentConsume{TokenHash: racing.TokenHash, RequestID: fmt.Sprintf("enroll_consume_%032x", index+20), CredentialHash: strings.Repeat(strconv.Itoa(index+1), 64)}, domain.AuditEvent{ID: fmt.Sprintf("aud_memory_race_%d", index), Action: "runner.enrollment.consume"})
			results <- raceErr
		}()
	}
	winners, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		switch err := <-results; {
		case err == nil:
			winners++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("memory race consume error=%v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("memory race winners=%d conflicts=%d", winners, conflicts)
	}
}
