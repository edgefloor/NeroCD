package store

import (
	"context"
	"sort"
	"time"

	"nerocd/internal/domain"
)

func (s *MemoryStore) ListRunners(context.Context) ([]domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Runner, len(s.runners))
	copy(out, s.runners)
	return out, nil
}

func (s *MemoryStore) GetRunnerByID(_ context.Context, id string) (domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, runner := range s.runners {
		if runner.ID == id {
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) RegisterRunner(_ context.Context, runner domain.Runner) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runners {
		if existing.ID == runner.ID {
			return domain.Runner{}, ErrConflict
		}
	}
	s.runners = append(s.runners, runner)
	return runner, nil
}
func (s *MemoryStore) RegisterRunnerWithAudit(_ context.Context, runner domain.Runner, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runners {
		if r.ID == runner.ID {
			return domain.Runner{}, ErrConflict
		}
	}
	s.runners = append(s.runners, runner)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return runner, nil
}

func (s *MemoryStore) CreateRunnerEnrollment(_ context.Context, enrollment domain.RunnerEnrollment, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runnerEnrollments {
		if existing.ID == enrollment.ID || existing.TokenHash == enrollment.TokenHash || existing.RunnerID == enrollment.RunnerID {
			return domain.RunnerEnrollment{}, ErrConflict
		}
	}
	s.runnerEnrollments = append(s.runnerEnrollments, enrollment)
	s.auditEvents = append(s.auditEvents, audit)
	return enrollment, nil
}

func (s *MemoryStore) RevokeRunnerEnrollment(_ context.Context, enrollmentID string, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, enrollment := range s.runnerEnrollments {
		if enrollment.ID != enrollmentID || enrollment.RevokedAt != nil || enrollment.UsedAt != nil {
			continue
		}
		enrollment.RevokedAt = &now
		s.runnerEnrollments[i] = enrollment
		s.auditEvents = append(s.auditEvents, audit)
		return enrollment, nil
	}
	return domain.RunnerEnrollment{}, ErrNotFound
}

func (s *MemoryStore) ConsumeRunnerEnrollment(_ context.Context, consume domain.RunnerEnrollmentConsume, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, enrollment := range s.runnerEnrollments {
		if enrollment.TokenHash != consume.TokenHash {
			continue
		}
		if enrollment.UsedAt != nil {
			if enrollment.ConsumeRequestID == nil || enrollment.CredentialHash == nil || *enrollment.ConsumeRequestID != consume.RequestID || *enrollment.CredentialHash != consume.CredentialHash {
				return domain.Runner{}, ErrConflict
			}
			for _, registered := range s.runners {
				if registered.ID == enrollment.RunnerID && registered.TokenHash == consume.CredentialHash {
					return registered, nil
				}
			}
			return domain.Runner{}, ErrConflict
		}
		if enrollment.RevokedAt != nil || !enrollment.ExpiresAt.After(now) {
			return domain.Runner{}, ErrNotFound
		}
		for _, registered := range s.runners {
			if registered.ID == enrollment.RunnerID {
				return domain.Runner{}, ErrConflict
			}
		}
		runner := domain.Runner{ID: enrollment.RunnerID, Name: enrollment.RunnerName, Tags: append([]string(nil), enrollment.Tags...), Capabilities: append([]string(nil), enrollment.Capabilities...), TokenHash: consume.CredentialHash, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}
		s.runners = append(s.runners, runner)
		enrollment.UsedAt = &now
		enrollment.ConsumeRequestID = stringPointer(consume.RequestID)
		enrollment.CredentialHash = stringPointer(consume.CredentialHash)
		s.runnerEnrollments[i] = enrollment
		audit.ActorID = enrollment.RunnerID
		audit.TargetID = enrollment.RunnerID
		audit.Metadata = map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}
		s.auditEvents = append(s.auditEvents, audit)
		return runner, nil
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) UpdateRunnerToken(_ context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID != runnerID {
			continue
		}
		runner.TokenHash = tokenHash
		runner.Status = status
		runner.LastHeartbeatAt = updatedAt
		s.runners[i] = runner
		return runner, nil
	}
	return domain.Runner{}, ErrNotFound
}
func (s *MemoryStore) UpdateRunnerTokenWithAudit(_ context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.runners {
		if r.ID == runnerID {
			r.TokenHash, r.Status, r.LastHeartbeatAt = tokenHash, status, updatedAt
			s.runners[i] = r
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return r, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) GetRunnerByTokenHash(_ context.Context, tokenHash string) (domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, runner := range s.runners {
		if runner.TokenHash == tokenHash && runner.Status == domain.RunnerActive {
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) HeartbeatRunner(_ context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID == id {
			runner.Status = domain.RunnerActive
			runner.LastHeartbeatAt = heartbeatAt
			s.runners[i] = runner
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	return s.ClaimRunWithAudit(ctx, runnerID, now, ttl, domain.AuditEvent{})
}

func (s *MemoryStore) ClaimRunWithAudit(_ context.Context, runnerID string, now time.Time, ttl time.Duration, audit domain.AuditEvent) (domain.ClaimedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.ClaimedRun{}, ErrConflict
	}
	s.expireLeasesLocked(now)
	staleBefore := now.Add(-2 * ttl)
	var runner domain.Runner
	foundRunner := false
	for i, candidate := range s.runners {
		if candidate.ID == runnerID {
			if candidate.Status == domain.RunnerActive && candidate.LastHeartbeatAt.Before(staleBefore) {
				candidate.Status = domain.RunnerStale
				s.runners[i] = candidate
			}
			runner = candidate
			foundRunner = true
			break
		}
	}
	if !foundRunner || runner.Status != domain.RunnerActive || runner.LastHeartbeatAt.Before(now.Add(-2*ttl)) {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if s.claimCursors == nil {
		s.claimCursors = make(map[string]memoryClaimCursor)
	}
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	storedCursor, hasCursor := s.claimCursors[runner.ID]
	candidateIndexes := make([]int, 0)
	for i, run := range s.runs {
		if run.Status != domain.RunQueued {
			continue
		}
		claimOrderAt, ok := s.claimOrderByRun[run.ID]
		if !ok {
			claimOrderAt = run.StartedAt
			s.claimOrderByRun[run.ID] = claimOrderAt
		}
		if hasCursor && (claimOrderAt.Before(storedCursor.claimOrderAt) || (claimOrderAt.Equal(storedCursor.claimOrderAt) && run.ID <= storedCursor.runID)) {
			continue
		}
		candidateIndexes = append(candidateIndexes, i)
	}
	sort.Slice(candidateIndexes, func(i, j int) bool {
		left, right := s.runs[candidateIndexes[i]], s.runs[candidateIndexes[j]]
		leftOrder, rightOrder := s.claimOrderByRun[left.ID], s.claimOrderByRun[right.ID]
		if leftOrder.Equal(rightOrder) {
			return left.ID < right.ID
		}
		return leftOrder.Before(rightOrder)
	})
	examineCount := len(candidateIndexes)
	if examineCount > claimCandidateLimit {
		examineCount = claimCandidateLimit
	}
	for _, runIndex := range candidateIndexes[:examineCount] {
		run := s.runs[runIndex]
		cursor := memoryClaimCursor{claimOrderAt: s.claimOrderByRun[run.ID], runID: run.ID}
		if !covers(runner.Tags, run.RunnerTags) || !contains(runner.Capabilities, claimRunType(run)) {
			storedCursor = cursor
			continue
		}
		run.Status = domain.RunRunning
		run.RunnerID = &runner.ID
		if s.nextAttemptByRun == nil {
			s.nextAttemptByRun = make(map[string]int)
		}
		attempt := s.nextAttemptByRun[run.ID]
		if attempt < 1 {
			attempt = 1
		}
		leaseID, err := newLeaseToken("lease")
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		fence, err := newLeaseToken("fence")
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		s.nextAttemptByRun[run.ID] = attempt + 1
		s.claimCursors[runner.ID] = cursor
		s.runs[runIndex] = run
		lease := domain.RunLease{ID: leaseID, RunID: run.ID, RunnerID: runner.ID, Status: domain.LeaseActive, ExpiresAt: now.Add(ttl), CreatedAt: now, Attempt: attempt, Fence: fence}
		s.leases = append(s.leases, lease)
		for i := range s.deployments {
			if s.deployments[i].TaskRunID == nil || *s.deployments[i].TaskRunID != run.ID {
				continue
			}
			s.deploymentAttempts = append(s.deploymentAttempts, domain.DeploymentAttempt{DeploymentID: s.deployments[i].ID, RunID: run.ID, LeaseID: lease.ID, RunnerID: runner.ID, Attempt: attempt, Fence: fence, Status: "active", CreatedAt: now})
			if s.deployments[i].Status == domain.DeploymentQueued {
				s.deploymentAttempts[len(s.deploymentAttempts)-1].CreatedAt = now
				s.deployments[i].Status = domain.DeploymentAssigned
				s.deployments[i].UpdatedAt = now
			}
		}
		if audit.ID != "" {
			audit.TargetID = run.ID
			audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"runner_id": runner.ID, "lease_id": lease.ID, "attempt": lease.Attempt, "fence": lease.Fence})
			s.auditEvents = append(s.auditEvents, audit)
		}
		return domain.ClaimedRun{Lease: lease, Run: run, PrimitivePlan: primitivePlanForRun(run)}, nil
	}
	// As in PostgreSQL, a full-size page cannot prove it reached the queue tail;
	// retain its last key so the next bounded call can confirm/reset the wrap.
	if examineCount == claimCandidateLimit {
		s.claimCursors[runner.ID] = storedCursor
	} else {
		delete(s.claimCursors, runner.ID)
	}
	return domain.ClaimedRun{}, ErrNotFound
}

func (s *MemoryStore) RenewLease(_ context.Context, runnerID, leaseID, fence string, attempt int, now time.Time, ttl time.Duration) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Fence != fence || lease.Attempt != attempt || lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(now) {
			continue
		}
		lease.ExpiresAt = now.Add(ttl)
		s.leases[i] = lease
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) expireLeasesLocked(now time.Time) {
	for i, lease := range s.leases {
		if lease.Status != domain.LeaseActive || lease.ExpiresAt.After(now) {
			continue
		}
		lease.Status = domain.LeaseExpired
		lease.CompletedAt = &now
		s.leases[i] = lease
		// A reclaimed run receives a fresh lease and deployment attempt.  The
		// attempt bound to the expired fence must therefore be terminal as well;
		// otherwise it remains falsely active alongside the replacement attempt.
		for j := range s.deploymentAttempts {
			attempt := &s.deploymentAttempts[j]
			if attempt.LeaseID == lease.ID && attempt.Status == "active" {
				attempt.Status = "failed"
				attempt.FinishedAt = &now
			}
		}
		for j, run := range s.runs {
			if run.ID == lease.RunID && run.Status == domain.RunRunning {
				run.Status = domain.RunQueued
				run.RunnerID = nil
				run.FinishedAt = nil
				s.runs[j] = run
				if s.claimOrderByRun == nil {
					s.claimOrderByRun = make(map[string]time.Time)
				}
				s.claimOrderByRun[run.ID] = now
				break
			}
		}
	}
}

func (s *MemoryStore) ExpireLeases(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLeasesLocked(now)
	return nil
}

func (s *MemoryStore) CompleteLeaseRequest(_ context.Context, leaseID string, runnerID string, status string, attempt int, fence string, completionKey string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Attempt != attempt || lease.Fence != fence {
			continue
		}
		for _, deployment := range s.deployments {
			if deployment.TaskRunID != nil && *deployment.TaskRunID == lease.RunID {
				return domain.RunLease{}, ErrConflict
			}
		}
		if lease.CompletionKey != "" {
			if lease.CompletionKey == completionKey && lease.Status == status {
				return lease, nil
			}
			return domain.RunLease{}, ErrConflict
		}
		if lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(completedAt) {
			return domain.RunLease{}, ErrNotFound
		}
		lease.Status = status
		lease.CompletedAt = &completedAt
		lease.CompletionKey = completionKey
		s.leases[i] = lease
		for j, run := range s.runs {
			if run.ID != lease.RunID {
				continue
			}
			run.Status = runStatus
			run.FinishedAt = finishedAt
			if workflowState != nil {
				run.WorkflowState = *workflowState
			}
			s.runs[j] = run
			break
		}
		if runStatus == domain.RunQueued {
			if s.claimOrderByRun == nil {
				s.claimOrderByRun = make(map[string]time.Time)
			}
			s.claimOrderByRun[lease.RunID] = completedAt
		}
		for _, log := range logs {
			s.createRunLogLocked(log)
		}
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) CancelRunRequest(_ context.Context, runID string, canceledAt time.Time, log domain.RunLog, audit domain.AuditEvent) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(runID) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID != runID || domain.IsTerminalRunStatus(run.Status) {
			continue
		}
		for j, lease := range s.leases {
			if lease.RunID == runID && lease.Status == domain.LeaseActive {
				lease.Status = domain.RunCanceled
				lease.CompletedAt = &canceledAt
				s.leases[j] = lease
			}
		}
		run.Status = domain.RunCanceled
		run.RunnerID = nil
		run.FinishedAt = &canceledAt
		s.runs[i] = run
		s.createRunLogLocked(log)
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return run, nil
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) AuthorizeSecretAccess(_ context.Context, request domain.SecretAccessRequest) (domain.SecretAccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID != request.LeaseID || lease.RunID != request.RunID || lease.RunnerID != request.RunnerID || lease.Attempt != request.Attempt || lease.Fence != request.Fence || lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(request.RequestedAt) {
			continue
		}
		expected := secretAccessAudit(request, request.RequestedAt)
		var replay *domain.AuditEvent
		for _, existing := range s.auditEvents {
			if existing.ID != request.AccessID {
				continue
			}
			if !secretAccessAuditsEqual(existing, expected) {
				return domain.SecretAccessGrant{}, ErrConflict
			}
			copy := existing
			replay = &copy
			break
		}
		var targetRun domain.TaskRun
		for _, run := range s.runs {
			if run.ID == request.RunID {
				targetRun = run
				break
			}
		}
		if !runAuthorizesSecretAccess(targetRun, request) {
			return domain.SecretAccessGrant{}, ErrNotFound
		}
		if replay != nil {
			return secretAccessGrant(request, replay.CreatedAt), nil
		}
		s.auditEvents = append(s.auditEvents, expected)
		return secretAccessGrant(request, expected.CreatedAt), nil
	}
	return domain.SecretAccessGrant{}, ErrNotFound
}
