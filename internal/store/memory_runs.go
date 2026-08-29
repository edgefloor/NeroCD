package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

// ListRuns implements the corresponding repository operation.
func (s *MemoryStore) ListRuns(_ context.Context, projectID string) ([]domain.TaskRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskRun, 0, len(s.runs))
	for _, run := range s.runs {
		if projectID == "" || run.ProjectID == projectID {
			out = append(out, run)
		}
	}
	return out, nil
}

// ListRunsPage implements the corresponding repository operation.
func (s *MemoryStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	runs, err := s.ListRuns(ctx, projectID)
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	return paginateSlice(runs, page), nil
}

// ListRunLogs implements the corresponding repository operation.
func (s *MemoryStore) ListRunLogs(_ context.Context, runID string) ([]domain.RunLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.RunLog, 0, len(s.logs))
	for _, log := range s.logs {
		if runID == "" || log.RunID == runID {
			out = append(out, log)
		}
	}
	return out, nil
}

// ListRunLogsPage implements the corresponding repository operation.
func (s *MemoryStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	logs, err := s.ListRunLogs(ctx, runID)
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	return paginateSlice(logs, page), nil
}

// ListArtifacts implements the corresponding repository operation.
func (s *MemoryStore) ListArtifacts(_ context.Context, runID string) ([]domain.ArtifactRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ArtifactRecord, 0, len(s.artifacts))
	for _, artifact := range s.artifacts {
		if runID == "" || artifact.RunID == runID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

// ListArtifactsPage implements the corresponding repository operation.
func (s *MemoryStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	artifacts, err := s.ListArtifacts(ctx, runID)
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	return paginateSlice(artifacts, page), nil
}

// CreateRun implements the corresponding repository operation.
func (s *MemoryStore) CreateRun(_ context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	return run, nil
}

// CreateRunRequest implements the corresponding repository operation.
func (s *MemoryStore) CreateRunRequest(_ context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	s.logs = append(s.logs, log)
	if approval != nil {
		s.approvals = append([]domain.Approval{*approval}, s.approvals...)
	}
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return run, nil
}

// UpdateRunStatus implements the corresponding repository operation.
func (s *MemoryStore) UpdateRunStatus(_ context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(id) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID == id {
			run.Status = status
			run.FinishedAt = finishedAt
			if status == domain.RunQueued {
				if s.claimOrderByRun == nil {
					s.claimOrderByRun = make(map[string]time.Time)
				}
				s.claimOrderByRun[run.ID] = time.Now().UTC()
			}
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

// UpdateRunWorkflowState implements the corresponding repository operation.
func (s *MemoryStore) UpdateRunWorkflowState(_ context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(id) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID == id {
			run.WorkflowState = workflowState
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

// ActiveLeaseForRun implements the corresponding repository operation.
func (s *MemoryStore) ActiveLeaseForRun(_ context.Context, runID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.RunID == runID && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(time.Now().UTC()) {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

// GetLeaseForRunner implements the corresponding repository operation.
func (s *MemoryStore) GetLeaseForRunner(_ context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunnerID == runnerID && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(time.Now().UTC()) {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

// GetLeaseForCompletion implements the corresponding repository operation.
func (s *MemoryStore) GetLeaseForCompletion(_ context.Context, leaseID, runnerID string, attempt int, fence string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

// CreateRunLog implements the corresponding repository operation.
func (s *MemoryStore) CreateRunLog(_ context.Context, log domain.RunLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(log.RunID) {
		return ErrConflict
	}
	s.createRunLogLocked(log)
	return nil
}

func (s *MemoryStore) createRunLogLocked(log domain.RunLog) {
	for {
		conflict := false
		for _, existing := range s.logs {
			if existing.RunID == log.RunID && existing.Sequence == log.Sequence {
				conflict = true
				log.Sequence++
				break
			}
		}
		if !conflict {
			break
		}
	}
	s.logs = append(s.logs, log)
}

// CreateArtifact implements the corresponding repository operation.
func (s *MemoryStore) CreateArtifact(_ context.Context, artifact domain.ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(artifact.RunID) {
		return ErrConflict
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

// CreateRunLogForLease implements the corresponding repository operation.
func (s *MemoryStore) CreateRunLogForLease(_ context.Context, log domain.RunLog, runnerID, leaseID string, attempt int, fence string, now time.Time) (domain.RunLog, error) {
	if log.EventKey != "" {
		logs, err := s.CreateRunLogsForLease(context.Background(), []domain.RunLog{log}, log.RunID, runnerID, leaseID, attempt, fence, now)
		if err != nil {
			return domain.RunLog{}, err
		}
		return logs[0], nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunID == log.RunID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
			s.createRunLogLocked(log)
			return s.logs[len(s.logs)-1], nil
		}
	}
	return domain.RunLog{}, ErrNotFound
}

// CreateRunLogsForLease implements the corresponding repository operation.
func (s *MemoryStore) CreateRunLogsForLease(_ context.Context, logs []domain.RunLog, runID, runnerID, leaseID string, attempt int, fence string, now time.Time) ([]domain.RunLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identityMatches := false
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunID == runID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence {
			identityMatches = true
			break
		}
	}
	if !identityMatches {
		return nil, ErrNotFound
	}
	results := make([]domain.RunLog, len(logs))
	newEvents := make([]bool, len(logs))
	hasNew := false
	for i, log := range logs {
		if log.RunID != runID || log.EventKey == "" || log.RequestedSequence <= 0 || log.LeaseID != leaseID || log.Attempt != attempt {
			return nil, ErrConflict
		}
		found := false
		for _, existing := range s.logs {
			if existing.RunID != runID || existing.EventKey != log.EventKey {
				continue
			}
			found = true
			if existing.LeaseID != leaseID || existing.Attempt != attempt || existing.RequestedSequence != log.RequestedSequence || existing.Stream != log.Stream || existing.Message != log.Message {
				return nil, ErrConflict
			}
			results[i] = existing
			break
		}
		if !found {
			newEvents[i] = true
			hasNew = true
		}
	}
	if hasNew {
		authorized := false
		for _, lease := range s.leases {
			if lease.ID == leaseID && lease.RunID == runID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
				authorized = true
				break
			}
		}
		if !authorized {
			return nil, ErrNotFound
		}
	}
	for i, log := range logs {
		if !newEvents[i] {
			continue
		}
		s.createRunLogLocked(log)
		results[i] = s.logs[len(s.logs)-1]
	}
	return results, nil
}

// CreateArtifactForLease implements the corresponding repository operation.
func (s *MemoryStore) CreateArtifactForLease(_ context.Context, artifact domain.ArtifactRecord, runnerID string, attempt int, fence string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID == artifact.LeaseID && lease.RunID == artifact.RunID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
			s.artifacts = append(s.artifacts, artifact)
			return nil
		}
	}
	return ErrNotFound
}
