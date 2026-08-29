package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/observability"
)

// OperationalSnapshot mirrors the PostgreSQL aggregate contract for explicit
// development/test memory stores. It deliberately reads under one lock so a
// scrape cannot combine queue and lease state from different mutations.
// OperationalSnapshot implements the corresponding repository operation.
func (s *MemoryStore) OperationalSnapshot(_ context.Context) (observability.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	result := observability.Snapshot{
		CollectedAt:          now,
		TerminalRuns:         map[string]observability.DurationAggregate{},
		Deployments:          map[string]int64{},
		BackupOutcome:        observability.BackupNone,
		BackupReason:         "none",
		BackupScheduleStatus: "disabled",
	}
	var oldestQueue *time.Time
	for _, run := range s.runs {
		switch run.Status {
		case domain.RunQueued, domain.RunWaitingApproval:
			result.QueueDepth++
			if oldestQueue == nil || run.StartedAt.Before(*oldestQueue) {
				v := run.StartedAt
				oldestQueue = &v
			}
		case domain.RunSucceeded, domain.RunFailed, domain.RunCanceled:
			v := result.TerminalRuns[run.Status]
			v.Count++
			if run.FinishedAt != nil && run.FinishedAt.After(run.StartedAt) {
				v.SumSeconds += run.FinishedAt.Sub(run.StartedAt).Seconds()
			}
			result.TerminalRuns[run.Status] = v
		}
	}
	if oldestQueue != nil {
		result.QueueOldestAgeSeconds = nonNegativeAge(now, *oldestQueue)
	}
	var oldestHeartbeat *time.Time
	for _, runner := range s.runners {
		if oldestHeartbeat == nil || runner.LastHeartbeatAt.Before(*oldestHeartbeat) {
			v := runner.LastHeartbeatAt
			oldestHeartbeat = &v
		}
	}
	if oldestHeartbeat != nil {
		result.OldestRunnerHeartbeatSecond = nonNegativeAge(now, *oldestHeartbeat)
	}
	for _, lease := range s.leases {
		if lease.Status == domain.LeaseActive {
			result.ActiveLeases++
		}
		if lease.Status == domain.LeaseExpired {
			result.ExpiredLeases++
		}
	}
	for _, deployment := range s.deployments {
		result.Deployments[string(deployment.Status)]++
		if deployment.HealthPassed != nil {
			if *deployment.HealthPassed {
				result.DeploymentHealthPassed++
			} else {
				result.DeploymentHealthFailed++
			}
		}
		if deployment.RollbackOfID != nil {
			if deployment.Status == "rolled_back" {
				result.RollbackSucceeded++
			}
			if deployment.Status == "rollback_failed" {
				result.RollbackFailed++
			}
		}
	}
	for _, observation := range s.runnerObservations {
		result.RunnerJournalDepth += int64(observation.journalDepth)
		result.RunnerRetryCount += int64(observation.retryCount)
		result.RunnerRenewFailures += int64(observation.renewFailures)
	}
	return result, nil
}

// RecordRunnerOperationalObservation implements the corresponding repository operation.
func (s *MemoryStore) RecordRunnerOperationalObservation(_ context.Context, runnerID string, journalDepth, retryCount, renewFailures int) error {
	if runnerID == "" || journalDepth < 0 || journalDepth > 8192 || retryCount < 0 || retryCount > 100000 || renewFailures < 0 || renewFailures > 100000 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, r := range s.runners {
		if r.ID == runnerID {
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	s.runnerObservations[runnerID] = memoryRunnerObservation{observedAt: time.Now().UTC(), journalDepth: journalDepth, retryCount: retryCount, renewFailures: renewFailures}
	return nil
}

// RunnerOperationalObservation implements the corresponding repository operation.
func (s *MemoryStore) RunnerOperationalObservation(_ context.Context, runnerID string) (RunnerOperationalObservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	observation, ok := s.runnerObservations[runnerID]
	if !ok {
		return RunnerOperationalObservation{}, ErrNotFound
	}
	return RunnerOperationalObservation{ObservedAt: observation.observedAt, JournalDepth: observation.journalDepth, RetryCount: observation.retryCount, RenewFailures: observation.renewFailures}, nil
}

func nonNegativeAge(now, then time.Time) float64 {
	if then.IsZero() || now.Before(then) {
		return 0
	}
	return now.Sub(then).Seconds()
}
