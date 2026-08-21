package store

import (
	"context"
	"nerocd/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestMemoryDeploymentControlPlaneInvariant(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	svc, err := s.CreateService(ctx, domain.Service{ID: "svc", ProjectID: "proj_platform", Name: "web", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", OwnerID: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, domain.Environment{ID: "env", ServiceID: svc.ID, Name: "prod", ComposeProject: "web-prod", TimeoutSeconds: 60, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.CreateRevision(ctx, domain.Revision{ID: "rev", ServiceID: svc.ID, RequestedRef: "main", GitCommit: "abc", ComposeHash: "hash", ContentIdentity: "abc:hash", CreatedBy: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateRevision(ctx, rev)
	if err != nil || replay.ID != rev.ID {
		t.Fatalf("revision replay: %#v %v", replay, err)
	}
	d, err := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: "dep", EnvironmentID: env.ID, DesiredRevisionID: rev.ID, IdempotencyKey: "key", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_dep", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "a", Action: "deployment.create"})
	if err != nil {
		t.Fatal(err)
	}
	if d.TaskRunID == nil || *d.TaskRunID != "run_dep" {
		t.Fatalf("deployment did not receive server-owned run: %#v", d.TaskRunID)
	}
	var wg sync.WaitGroup
	wins := 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "parallel" + string(rune('a'+i))
			_, e := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: env.ID, DesiredRevisionID: rev.ID, IdempotencyKey: id, Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_" + id, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{})
			if e == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 0 {
		t.Fatalf("active environment lock admitted %d additional deployments", wins)
	}
	if _, err := s.CreateRevision(ctx, domain.Revision{ID: "other", ServiceID: "missing", RequestedRef: "main", GitCommit: "def", ComposeHash: "other", ContentIdentity: "other", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: "cross", EnvironmentID: env.ID, DesiredRevisionID: "other", IdempotencyKey: "cross", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_cross", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{}); err != ErrNotFound {
		t.Fatalf("cross-service revision = %v", err)
	}
}

func TestMemoryPostApplyFailureAtomicallyQueuesRollback(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().UTC()
	s.services = []domain.Service{{ID: "svc", ProjectID: "proj"}}
	s.environments = []domain.Environment{{ID: "env", ServiceID: "svc", RollbackSafe: true, CurrentHealthyRevisionID: stringPointer("rev_a")}}
	s.revisions = []domain.Revision{{ID: "rev_a", ServiceID: "svc"}, {ID: "rev_b", ServiceID: "svc", ProvenanceResolved: true}}
	runnerID := "runner"
	s.runs = []domain.TaskRun{{ID: "run_b", ProjectID: "proj", Status: domain.RunRunning, RunnerID: &runnerID}}
	s.leases = []domain.RunLease{{ID: "lease", RunID: "run_b", RunnerID: runnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "fence", ExpiresAt: now.Add(time.Minute)}}
	s.deployments = []domain.Deployment{{ID: "dep_b", EnvironmentID: "env", DesiredRevisionID: "rev_b", PreviousHealthyRevisionID: stringPointer("rev_a"), TaskRunID: stringPointer("run_b"), Status: domain.DeploymentApplying}}
	s.deploymentAttempts = []domain.DeploymentAttempt{{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", Status: "active"}}
	beforeRuns, beforeDeployments, beforeAudits := len(s.runs), len(s.deployments), len(s.auditEvents)
	if _, err := s.CreateDeploymentRequest(context.Background(), domain.Deployment{ID: "generic_child", EnvironmentID: "env", DesiredRevisionID: "rev_a", IdempotencyKey: "generic-child", RollbackOfID: stringPointer("dep_b")}, domain.TaskRun{ID: "generic_child_run"}, domain.AuditEvent{ID: "generic_child_audit"}); err != ErrConflict || len(s.runs) != beforeRuns || len(s.deployments) != beforeDeployments || len(s.auditEvents) != beforeAudits {
		t.Fatalf("generic rollback child bypass err=%v runs=%d/%d deployments=%d/%d audits=%d/%d", err, beforeRuns, len(s.runs), beforeDeployments, len(s.deployments), beforeAudits, len(s.auditEvents))
	}
	if _, err := s.TransitionDeploymentAttempt(context.Background(), domain.DeploymentTransitionRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", TransitionKey: "root-bypass", ExpectedStatus: domain.DeploymentApplying, TargetStatus: domain.DeploymentRolledBack}, domain.AuditEvent{}); err != ErrConflict || s.deployments[0].Status != domain.DeploymentApplying {
		t.Fatalf("root direct rollback bypass err=%v status=%q", err, s.deployments[0].Status)
	}
	result, err := s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", RequestID: "health-c", ExpectedStatus: domain.DeploymentApplying, FailureCode: "health_failed"}, domain.AuditEvent{ID: "audit_failed"}, domain.AuditEvent{ID: "audit_rollback"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed.Status != domain.DeploymentRollingBack || result.Rollback.Status != domain.DeploymentQueued || result.Rollback.RollbackOfID == nil || *result.Rollback.RollbackOfID != "dep_b" || result.Rollback.DesiredRevisionID != "rev_a" {
		t.Fatalf("unexpected atomic result: %#v", result)
	}
	if s.runs[0].Status != domain.RunQueued || s.leases[0].Status != domain.RunFailed {
		t.Fatalf("source settlement/rollback queue: runs=%#v leases=%#v", s.runs, s.leases)
	}
	rollbackID, rollbackRunID := domain.RollbackObjectIDs("dep_b", "health-c")
	if got := s.runs[0].RunSpec.Inputs; got["deployment_id"] != rollbackID || got["rollback_of_id"] != "dep_b" || got["desired_revision_id"] != "rev_a" {
		t.Fatalf("rollback run lacks server-owned linkage: %#v", got)
	}
	// Receipt replay is accepted without requiring that the old lease remain valid.
	s.leases[0].ExpiresAt = now.Add(-time.Minute)
	replay, err := s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", RequestID: "health-c", ExpectedStatus: domain.DeploymentApplying, FailureCode: "health_failed"}, domain.AuditEvent{}, domain.AuditEvent{})
	if err != nil || replay.Rollback.ID != rollbackID {
		t.Fatalf("response-loss replay: %#v %v", replay, err)
	}
	if _, err = s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", RequestID: "health-c", ExpectedStatus: domain.DeploymentApplying, FailureCode: "changed"}, domain.AuditEvent{}, domain.AuditEvent{}); err != ErrConflict {
		t.Fatalf("changed replay body = %v", err)
	}
	if _, err = s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "stale", RequestID: "health-c", ExpectedStatus: domain.DeploymentApplying, FailureCode: "health_failed"}, domain.AuditEvent{}, domain.AuditEvent{}); err != ErrNotFound {
		t.Fatalf("stale replay fence = %v", err)
	}
	// The child owns a fresh attempt; its healthy terminal transition settles
	// both records and releases the root environment lock.
	s.runs[0].Status, s.runs[0].RunnerID = domain.RunRunning, &runnerID
	s.leases = append(s.leases, domain.RunLease{ID: "rollback-lease", RunID: rollbackRunID, RunnerID: runnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "rollback-fence", ExpiresAt: now.Add(time.Minute)})
	s.deploymentAttempts = append(s.deploymentAttempts, domain.DeploymentAttempt{DeploymentID: rollbackID, RunID: rollbackRunID, LeaseID: "rollback-lease", RunnerID: runnerID, Attempt: 1, Fence: "rollback-fence", Status: "active"})
	s.deployments[1].Status = domain.DeploymentAssigned
	s.revisions[0].ProvenanceResolved = true
	if _, err = s.TransitionDeploymentAttempt(context.Background(), domain.DeploymentTransitionRequest{DeploymentID: rollbackID, RunID: rollbackRunID, LeaseID: "rollback-lease", RunnerID: runnerID, Attempt: 1, Fence: "rollback-fence", TransitionKey: "child-bypass", ExpectedStatus: domain.DeploymentAssigned, TargetStatus: domain.DeploymentFailed}, domain.AuditEvent{}); err != ErrConflict || s.deployments[1].Status != domain.DeploymentAssigned {
		t.Fatalf("child ordinary terminal bypass err=%v status=%q", err, s.deployments[1].Status)
	}
	transition := func(expected, target domain.DeploymentStatus, health *bool) error {
		_, err := s.TransitionDeploymentAttempt(context.Background(), domain.DeploymentTransitionRequest{DeploymentID: rollbackID, RunID: rollbackRunID, LeaseID: "rollback-lease", RunnerID: runnerID, Attempt: 1, Fence: "rollback-fence", TransitionKey: string(expected) + ":" + string(target), ExpectedStatus: expected, TargetStatus: target, HealthPassed: health}, domain.AuditEvent{})
		return err
	}
	if err := transition(domain.DeploymentAssigned, domain.DeploymentPreparing, nil); err != nil {
		t.Fatal(err)
	}
	if err := transition(domain.DeploymentPreparing, domain.DeploymentApplying, nil); err != nil {
		t.Fatal(err)
	}
	if err := transition(domain.DeploymentApplying, domain.DeploymentVerifying, nil); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := transition(domain.DeploymentVerifying, domain.DeploymentRolledBack, &yes); err != nil {
		t.Fatal(err)
	}
	if s.deployments[0].Status != domain.DeploymentRolledBack || s.deployments[1].Status != domain.DeploymentRolledBack {
		t.Fatalf("linked terminal states: %#v", s.deployments)
	}
}

func TestMemoryCancellationReceiptQueuesRollback(t *testing.T) {
	s := NewMemoryStore()
	now := time.Now().UTC()
	runnerID := "runner"
	s.services = []domain.Service{{ID: "svc", ProjectID: "proj"}}
	s.environments = []domain.Environment{{ID: "env", ServiceID: "svc", RollbackSafe: true, CurrentHealthyRevisionID: stringPointer("rev_a")}}
	s.revisions = []domain.Revision{{ID: "rev_a", ServiceID: "svc"}, {ID: "rev_b", ServiceID: "svc"}}
	s.runs = []domain.TaskRun{{ID: "run_b", ProjectID: "proj", Status: domain.RunRunning, RunnerID: &runnerID, RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}}}
	s.leases = []domain.RunLease{{ID: "lease", RunID: "run_b", RunnerID: runnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "fence", ExpiresAt: now.Add(time.Minute)}}
	s.deployments = []domain.Deployment{{ID: "dep_b", EnvironmentID: "env", DesiredRevisionID: "rev_b", PreviousHealthyRevisionID: stringPointer("rev_a"), TaskRunID: stringPointer("run_b"), Status: domain.DeploymentApplying}}
	s.deploymentAttempts = []domain.DeploymentAttempt{{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", Status: "active"}}
	if _, err := s.CancelDeploymentRequest(context.Background(), domain.DeploymentCancelRequest{DeploymentID: "dep_b", RequestID: "cancel-receipt", ActorID: "maintainer"}, domain.AuditEvent{ID: "cancel-audit"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", RequestID: "cancel-receipt", CancellationRequestID: "cancel-receipt", ExpectedStatus: domain.DeploymentCancelRequested, FailureCode: "cancellation_requested"}, domain.AuditEvent{ID: "handoff"}, domain.AuditEvent{ID: "queued"})
	if err != nil || result.Failed.Status != domain.DeploymentRollingBack || result.Rollback.RollbackOfID == nil || *result.Rollback.RollbackOfID != "dep_b" {
		t.Fatalf("cancellation rollback=%#v err=%v", result, err)
	}
	if _, err := s.FailDeploymentAndCreateRollback(context.Background(), domain.DeploymentFailureRollbackRequest{DeploymentID: "dep_b", RunID: "run_b", LeaseID: "lease", RunnerID: runnerID, Attempt: 1, Fence: "fence", RequestID: "cancel-receipt", CancellationRequestID: "changed", ExpectedStatus: domain.DeploymentCancelRequested, FailureCode: "cancellation_requested"}, domain.AuditEvent{}, domain.AuditEvent{}); err != ErrConflict {
		t.Fatalf("changed cancellation receipt replay=%v", err)
	}
}

func TestMemoryFencedDeploymentAttemptTransitions(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	svc, err := s.CreateService(ctx, domain.Service{ID: "svc_fenced", ProjectID: "proj_platform", Name: "fenced", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", OwnerID: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, domain.Environment{ID: "env_fenced", ServiceID: svc.ID, Name: "prod", ComposeProject: "fenced-prod", TimeoutSeconds: 60, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	revA, err := s.CreateRevision(ctx, domain.Revision{ID: "rev_fenced_a", ServiceID: svc.ID, RequestedRef: "a", GitCommit: "aaa", ComposeHash: "a", ContentIdentity: "a", CreatedBy: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	revB, err := s.CreateRevision(ctx, domain.Revision{ID: "rev_fenced_b", ServiceID: svc.ID, RequestedRef: "b", GitCommit: "bbb", ComposeHash: "b", ContentIdentity: "b", CreatedBy: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterRunner(ctx, domain.Runner{ID: "runner_deploy", Name: "deploy", Capabilities: []string{domain.RunTypeComposeDeploy}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}

	create := func(id, runID, revision, key string) domain.Deployment {
		d, createErr := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: env.ID, DesiredRevisionID: revision, IdempotencyKey: key, Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now, FenceRequired: true}, domain.TaskRun{ID: runID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_" + id, ActorID: "usr_bootstrap", Action: "deployment.create", TargetID: id, CreatedAt: now})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return d
	}
	transition := func(d domain.Deployment, claim domain.ClaimedRun, expected, target, key string, healthy *bool) (domain.Deployment, error) {
		return s.TransitionDeploymentAttempt(ctx, domain.DeploymentTransitionRequest{DeploymentID: d.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, RunnerID: claim.Lease.RunnerID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence, TransitionKey: key, ExpectedStatus: expected, TargetStatus: target, HealthPassed: healthy, Metadata: map[string]any{"stage": target}}, domain.AuditEvent{ID: "audit_" + key, ActorID: claim.Lease.RunnerID, Action: "runner.deployment.transition", TargetID: d.ID, CreatedAt: now})
	}

	dA := create("dep_fenced_a", "run_fenced_a", revA.ID, "a")
	claimA, err := s.ClaimRun(ctx, "runner_deploy", now, time.Minute)
	if err != nil || claimA.Run.ID != "run_fenced_a" {
		t.Fatalf("claim deployment: %#v %v", claimA, err)
	}
	auditCount := len(s.auditEvents)
	if _, err = s.CancelRunRequest(ctx, claimA.Run.ID, now, domain.RunLog{ID: "log_generic_cancel", RunID: claimA.Run.ID, Stream: domain.LogSystem}, domain.AuditEvent{ID: "audit_generic_cancel"}); err != ErrConflict {
		t.Fatalf("generic deployment cancel = %v", err)
	}
	if len(s.auditEvents) != auditCount {
		t.Fatal("generic deployment cancel wrote an audit event")
	}
	if active, activeErr := s.ActiveLeaseForRun(ctx, claimA.Run.ID); activeErr != nil || active.ID != claimA.Lease.ID || active.Status != domain.LeaseActive {
		t.Fatalf("generic deployment cancel changed lease %#v, %v", active, activeErr)
	}
	if _, err = s.UpdateRunStatus(ctx, claimA.Run.ID, domain.RunCanceled, &now); err != ErrConflict {
		t.Fatalf("generic deployment status update = %v", err)
	}
	if _, err = s.UpdateRunWorkflowState(ctx, claimA.Run.ID, domain.WorkflowState{}); err != ErrConflict {
		t.Fatalf("generic deployment workflow update = %v", err)
	}
	logCount, artifactCount := len(s.logs), len(s.artifacts)
	if err = s.CreateRunLog(ctx, domain.RunLog{ID: "log_generic_deployment", RunID: claimA.Run.ID, Sequence: 1, Stream: domain.LogSystem, Message: "generic"}); err != ErrConflict {
		t.Fatalf("generic deployment log = %v", err)
	}
	if err = s.CreateArtifact(ctx, domain.ArtifactRecord{ID: "art_generic_deployment", RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, Name: "generic", Path: "generic", Kind: domain.ArtifactFile}); err != ErrConflict {
		t.Fatalf("generic deployment artifact = %v", err)
	}
	if len(s.logs) != logCount || len(s.artifacts) != artifactCount {
		t.Fatalf("generic deployment append mutated logs=%d artifacts=%d", len(s.logs), len(s.artifacts))
	}
	if _, err = s.CreateRunLogForLease(ctx, domain.RunLog{ID: "log_fenced_deployment", RunID: claimA.Run.ID, Sequence: 1, Stream: domain.LogStdout, Message: "fenced"}, claimA.Lease.RunnerID, claimA.Lease.ID, claimA.Lease.Attempt, claimA.Lease.Fence, now); err != nil {
		t.Fatalf("fenced deployment log: %v", err)
	}
	if err = s.CreateArtifactForLease(ctx, domain.ArtifactRecord{ID: "art_fenced_deployment", RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, Name: "fenced", Path: "fenced", Kind: domain.ArtifactFile}, claimA.Lease.RunnerID, claimA.Lease.Attempt, claimA.Lease.Fence, now); err != nil {
		t.Fatalf("fenced deployment artifact: %v", err)
	}
	if _, err = s.CreateRunLogForLease(ctx, domain.RunLog{ID: "log_stale_fenced_deployment", RunID: claimA.Run.ID, Sequence: 1, Stream: domain.LogStdout, Message: "stale"}, claimA.Lease.RunnerID, claimA.Lease.ID, claimA.Lease.Attempt, "stale", now); err != ErrNotFound {
		t.Fatalf("stale fenced deployment log = %v", err)
	}
	if err = s.CreateArtifactForLease(ctx, domain.ArtifactRecord{ID: "art_stale_fenced_deployment", RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, Name: "stale", Path: "stale", Kind: domain.ArtifactFile}, claimA.Lease.RunnerID, claimA.Lease.Attempt, "stale", now); err != ErrNotFound {
		t.Fatalf("stale fenced deployment artifact = %v", err)
	}
	if _, err = transition(dA, claimA, domain.DeploymentAssigned, domain.DeploymentPreparing, "prepare", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dA, claimA, domain.DeploymentPreparing, domain.DeploymentApplying, "apply", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dA, claimA, domain.DeploymentApplying, domain.DeploymentVerifying, "verify", nil); err != nil {
		t.Fatal(err)
	}
	yes := true
	succeeded, err := transition(dA, claimA, domain.DeploymentVerifying, domain.DeploymentSucceeded, "success", &yes)
	if err != nil || succeeded.Status != domain.DeploymentSucceeded {
		t.Fatalf("success: %#v %v", succeeded, err)
	}
	if replay, replayErr := transition(dA, claimA, domain.DeploymentVerifying, domain.DeploymentSucceeded, "success", &yes); replayErr != nil || replay.ID != dA.ID {
		t.Fatalf("exact replay: %#v %v", replay, replayErr)
	}
	if _, err = transition(dA, claimA, domain.DeploymentVerifying, domain.DeploymentFailed, "success", nil); err != ErrConflict {
		t.Fatalf("conflicting replay = %v", err)
	}
	if _, err = s.ActiveLeaseForRun(ctx, claimA.Run.ID); err != ErrNotFound {
		t.Fatalf("terminal deployment retained active lease: %v", err)
	}
	if err = s.CreateRunLog(ctx, domain.RunLog{ID: "log_terminal_generic_deployment", RunID: claimA.Run.ID, Sequence: 1, Stream: domain.LogSystem}); err != ErrConflict {
		t.Fatalf("terminal generic deployment log = %v", err)
	}
	if err = s.CreateArtifact(ctx, domain.ArtifactRecord{ID: "art_terminal_generic_deployment", RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, Name: "terminal", Path: "terminal", Kind: domain.ArtifactFile}); err != ErrConflict {
		t.Fatalf("terminal generic deployment artifact = %v", err)
	}
	if _, err = s.CreateRunLogForLease(ctx, domain.RunLog{ID: "log_terminal_fenced_deployment", RunID: claimA.Run.ID, Sequence: 1, Stream: domain.LogSystem}, claimA.Lease.RunnerID, claimA.Lease.ID, claimA.Lease.Attempt, claimA.Lease.Fence, now); err != ErrNotFound {
		t.Fatalf("terminal fenced deployment log = %v", err)
	}
	if err = s.CreateArtifactForLease(ctx, domain.ArtifactRecord{ID: "art_terminal_fenced_deployment", RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, Name: "terminal-fenced", Path: "terminal-fenced", Kind: domain.ArtifactFile}, claimA.Lease.RunnerID, claimA.Lease.Attempt, claimA.Lease.Fence, now); err != ErrNotFound {
		t.Fatalf("terminal fenced deployment artifact = %v", err)
	}
	runs, err := s.ListRuns(ctx, "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == claimA.Run.ID && (run.Status != domain.RunSucceeded || run.RunnerID != nil || run.FinishedAt == nil) {
			t.Fatalf("terminal run lifecycle %#v", run)
		}
	}
	if _, err = s.CompleteLeaseRequest(ctx, claimA.Lease.ID, claimA.Lease.RunnerID, domain.RunSucceeded, claimA.Lease.Attempt, claimA.Lease.Fence, "generic-deployment", time.Now().UTC(), domain.RunSucceeded, nil, nil, nil, domain.AuditEvent{}); err != ErrConflict {
		t.Fatalf("generic deployment completion = %v", err)
	}
	if _, err = s.TransitionDeploymentAttempt(ctx, domain.DeploymentTransitionRequest{DeploymentID: dA.ID, RunID: claimA.Run.ID, LeaseID: claimA.Lease.ID, RunnerID: claimA.Lease.RunnerID, Attempt: claimA.Lease.Attempt, Fence: "stale", TransitionKey: "bad", ExpectedStatus: domain.DeploymentSucceeded, TargetStatus: domain.DeploymentFailed}, domain.AuditEvent{}); err != ErrNotFound {
		t.Fatalf("stale fence = %v", err)
	}
	es, _ := s.ListEnvironments(ctx, svc.ID)
	if es[0].CurrentHealthyRevisionID == nil || *es[0].CurrentHealthyRevisionID != revA.ID {
		t.Fatalf("healthy pointer = %#v", es[0].CurrentHealthyRevisionID)
	}

	dB := create("dep_fenced_b", "run_fenced_b", revB.ID, "b")
	claimB, err := s.ClaimRun(ctx, "runner_deploy", now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dB, claimB, domain.DeploymentAssigned, domain.DeploymentPreparing, "bprepare", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dB, claimB, domain.DeploymentPreparing, domain.DeploymentApplying, "bapply", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dB, claimB, domain.DeploymentApplying, domain.DeploymentCancelRequested, "bcancel-requested", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = transition(dB, claimB, domain.DeploymentCancelRequested, domain.DeploymentCanceled, "bcancelled", nil); err != ErrConflict {
		t.Fatalf("post-apply direct cancellation = %v", err)
	}
	if _, err = transition(dB, claimB, domain.DeploymentCancelRequested, domain.DeploymentManualIntervention, "bmanual", nil); err != nil {
		t.Fatal(err)
	}
	es, _ = s.ListEnvironments(ctx, svc.ID)
	if es[0].CurrentHealthyRevisionID == nil || *es[0].CurrentHealthyRevisionID != revA.ID {
		t.Fatalf("rollback changed healthy pointer: %#v", es[0].CurrentHealthyRevisionID)
	}

	dCanceled := create("dep_fenced_cancel", "run_fenced_cancel", revB.ID, "cancel")
	claimCanceled, err := s.ClaimRun(ctx, "runner_deploy", now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CancelRunRequest(ctx, claimCanceled.Run.ID, now, domain.RunLog{ID: "log_generic_cancel_again", RunID: claimCanceled.Run.ID, Stream: domain.LogSystem}, domain.AuditEvent{ID: "audit_generic_cancel_again"}); err != ErrConflict {
		t.Fatalf("assigned deployment generic cancel = %v", err)
	}
	if canceled, cancelErr := transition(dCanceled, claimCanceled, domain.DeploymentAssigned, domain.DeploymentCanceled, "canceled", nil); cancelErr != nil || canceled.Status != domain.DeploymentCanceled {
		t.Fatalf("fenced cancellation: %#v %v", canceled, cancelErr)
	}
	if _, err = s.ActiveLeaseForRun(ctx, claimCanceled.Run.ID); err != ErrNotFound {
		t.Fatalf("fenced cancellation retained lease: %v", err)
	}
	if _, err = s.CancelRunRequest(ctx, claimCanceled.Run.ID, now, domain.RunLog{ID: "log_stale_generic_cancel", RunID: claimCanceled.Run.ID, Stream: domain.LogSystem}, domain.AuditEvent{ID: "audit_stale_generic_cancel"}); err != ErrConflict {
		t.Fatalf("stale generic deployment cancel = %v", err)
	}

	confirmed, err := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: "dep_confirm", EnvironmentID: env.ID, DesiredRevisionID: revA.ID, IdempotencyKey: "confirm", Status: domain.DeploymentWaitingConfirmation, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now, FenceRequired: true}, domain.TaskRun{ID: "run_confirm", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunWaitingApproval, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_confirm", ActorID: "usr_bootstrap", Action: "deployment.create", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimRun(ctx, "runner_deploy", now.Add(2*time.Second), time.Minute); err != ErrNotFound {
		t.Fatalf("unconfirmed deployment claimed: %v", err)
	}
	if _, err = s.ConfirmDeployment(ctx, confirmed.ID, "usr_bootstrap", domain.AuditEvent{ID: "audit_confirmed", ActorID: "usr_bootstrap", Action: "deployment.confirm", TargetID: confirmed.ID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if claim, claimErr := s.ClaimRun(ctx, "runner_deploy", now.Add(3*time.Second), time.Minute); claimErr != nil || claim.Run.ID != "run_confirm" {
		t.Fatalf("confirmed deployment claim: %#v %v", claim, claimErr)
	}
}

func TestMemoryDeploymentTransitionTable(t *testing.T) {
	statuses := []domain.DeploymentStatus{
		domain.DeploymentQueued, domain.DeploymentWaitingConfirmation, domain.DeploymentAssigned,
		domain.DeploymentPreparing, domain.DeploymentApplying, domain.DeploymentVerifying,
		domain.DeploymentCancelRequested, domain.DeploymentRollingBack, domain.DeploymentSucceeded,
		domain.DeploymentFailed, domain.DeploymentCanceled, domain.DeploymentRolledBack,
		domain.DeploymentRollbackFailed, domain.DeploymentManualIntervention,
	}
	allowed := map[[2]domain.DeploymentStatus]bool{
		{domain.DeploymentQueued, domain.DeploymentWaitingConfirmation}:         true,
		{domain.DeploymentQueued, domain.DeploymentAssigned}:                    true,
		{domain.DeploymentQueued, domain.DeploymentFailed}:                      true,
		{domain.DeploymentQueued, domain.DeploymentCanceled}:                    true,
		{domain.DeploymentWaitingConfirmation, domain.DeploymentFailed}:         true,
		{domain.DeploymentWaitingConfirmation, domain.DeploymentCanceled}:       true,
		{domain.DeploymentAssigned, domain.DeploymentPreparing}:                 true,
		{domain.DeploymentAssigned, domain.DeploymentFailed}:                    true,
		{domain.DeploymentAssigned, domain.DeploymentCanceled}:                  true,
		{domain.DeploymentPreparing, domain.DeploymentApplying}:                 true,
		{domain.DeploymentPreparing, domain.DeploymentFailed}:                   true,
		{domain.DeploymentPreparing, domain.DeploymentCanceled}:                 true,
		{domain.DeploymentApplying, domain.DeploymentVerifying}:                 true,
		{domain.DeploymentApplying, domain.DeploymentCancelRequested}:           true,
		{domain.DeploymentVerifying, domain.DeploymentSucceeded}:                true,
		{domain.DeploymentVerifying, domain.DeploymentCancelRequested}:          true,
		{domain.DeploymentCancelRequested, domain.DeploymentManualIntervention}: true,
	}
	if !domain.DeploymentRoleTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentRolledBack) || !domain.DeploymentRoleTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentRollbackFailed) || domain.DeploymentRoleTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentSucceeded) || domain.DeploymentRoleTransitionAllowed(false, domain.DeploymentVerifying, domain.DeploymentRolledBack) {
		t.Fatal("role-specific deployment transition table is bypassable")
	}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]domain.DeploymentStatus{from, to}]
			if got := deploymentTransitionAllowed(from, to); got != want {
				t.Errorf("transition %s -> %s allowed=%t, want %t", from, to, got, want)
			}
		}
	}
}

func TestMemoryRollbackProvenanceAndPreAssignmentFailure(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	svc, err := s.CreateService(ctx, domain.Service{ID: "svc_rollback", ProjectID: "proj_platform", Name: "rollback", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", OwnerID: "usr_bootstrap", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.CreateEnvironment(ctx, domain.Environment{ID: "env_rollback", ServiceID: svc.ID, Name: "prod", ComposeProject: "rollback-prod", TimeoutSeconds: 60, CurrentHealthyRevisionID: stringPointer("rev_rollback_a"), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []domain.Revision{{ID: "rev_rollback_a", ServiceID: svc.ID, RequestedRef: "a", GitCommit: "aaa", ComposeHash: "a", ContentIdentity: "a", CreatedBy: "usr_bootstrap", CreatedAt: now}, {ID: "rev_rollback_b", ServiceID: svc.ID, RequestedRef: "b", GitCommit: "bbb", ComposeHash: "b", ContentIdentity: "b", CreatedBy: "usr_bootstrap", CreatedAt: now}} {
		if _, err = s.CreateRevision(ctx, revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.RegisterRunner(ctx, domain.Runner{ID: "runner_rollback", Name: "rollback", Capabilities: []string{domain.RunTypeComposeDeploy}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	create := func(id, runID, revision string, rollbackOf *string, status, runStatus string) domain.Deployment {
		d, createErr := s.CreateDeploymentRequest(ctx, domain.Deployment{ID: id, EnvironmentID: env.ID, DesiredRevisionID: revision, IdempotencyKey: id, Status: status, RequestedBy: "usr_bootstrap", RollbackOfID: rollbackOf, FenceRequired: true, CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: runID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: runStatus, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_" + id, Action: "deployment.create", TargetID: id})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return d
	}
	transition := func(d domain.Deployment, claim domain.ClaimedRun, from, to, key string, health *bool) error {
		_, transitionErr := s.TransitionDeploymentAttempt(ctx, domain.DeploymentTransitionRequest{DeploymentID: d.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, RunnerID: claim.Lease.RunnerID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence, TransitionKey: key, ExpectedStatus: from, TargetStatus: to, HealthPassed: health}, domain.AuditEvent{ID: "audit_" + key, Action: "runner.deployment.transition", TargetID: d.ID})
		return transitionErr
	}

	succeeded := create("dep_rollback_succeeded", "run_rollback_succeeded", "rev_rollback_b", nil, domain.DeploymentQueued, domain.RunQueued)
	claimSucceeded, err := s.ClaimRun(ctx, "runner_rollback", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]string{{domain.DeploymentAssigned, domain.DeploymentPreparing}, {domain.DeploymentPreparing, domain.DeploymentApplying}, {domain.DeploymentApplying, domain.DeploymentVerifying}, {domain.DeploymentVerifying, domain.DeploymentSucceeded}} {
		health := (*bool)(nil)
		if step[1] == domain.DeploymentSucceeded {
			yes := true
			health = &yes
		}
		if err = transition(succeeded, claimSucceeded, step[0], step[1], "succeeded-"+step[1], health); err != nil {
			t.Fatal(err)
		}
	}
	runsBefore, auditsBefore := len(s.runs), len(s.auditEvents)
	succeededID := succeeded.ID
	if _, err = s.CreateDeploymentRequest(ctx, domain.Deployment{ID: "dep_invalid_rollback", EnvironmentID: env.ID, DesiredRevisionID: "rev_rollback_a", IdempotencyKey: "invalid-rollback", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", RollbackOfID: &succeededID, FenceRequired: true, CreatedAt: now, UpdatedAt: now}, domain.TaskRun{ID: "run_invalid_rollback", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}, domain.AuditEvent{ID: "audit_invalid_rollback"}); err != ErrConflict {
		t.Fatalf("rollback against succeeded source = %v", err)
	}
	if len(s.runs) != runsBefore || len(s.auditEvents) != auditsBefore {
		t.Fatal("invalid rollback wrote run or audit")
	}

	waiting := create("dep_waiting_pre_fail", "run_waiting_pre_fail", "rev_rollback_a", nil, domain.DeploymentWaitingConfirmation, domain.RunWaitingApproval)
	if _, err = s.FailPreAssignmentDeployment(ctx, waiting.ID, "validation_failed", domain.AuditEvent{ID: "audit_waiting_pre_fail"}); err != nil {
		t.Fatalf("waiting-confirmation pre-assignment failure: %v", err)
	}
}
