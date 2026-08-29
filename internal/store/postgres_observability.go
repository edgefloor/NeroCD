package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/observability"
	"nerocd/internal/store/sqlcgen"
)

// OperationalSnapshot reads authoritative aggregate state in one database
// statement. DB clock supplies all ages so an application host clock cannot
// make stale queue/lease signals go backwards.
func (s *PostgresStore) OperationalSnapshot(ctx context.Context) (observability.Snapshot, error) {
	base, err := s.queries.OperationalSnapshotBase(ctx)
	if err != nil {
		return observability.Snapshot{}, fmt.Errorf("operational snapshot query: %w", err)
	}
	snapshot := observability.Snapshot{CollectedAt: base.CollectedAt, QueueDepth: base.Depth, QueueOldestAgeSeconds: maxSnapshotAge(base.QueueOldestAge), ActiveLeases: base.ActiveLeases, ExpiredLeases: base.ExpiredLeases, OldestRunnerHeartbeatSecond: maxSnapshotAge(base.RunnerOldestAge), RunnerJournalDepth: base.JournalDepth, RunnerRetryCount: base.RetryCount, RunnerRenewFailures: base.RenewFailures, BackupScheduleStatus: base.BackupScheduleStatus, BackupScheduleNextSeconds: maxSnapshotAge(base.BackupScheduleNextSeconds), BackupScheduleFailures: int64(base.BackupScheduleFailures)}
	snapshot.TerminalRuns = map[string]observability.DurationAggregate{
		"succeeded": {Count: base.SucceededCount, SumSeconds: maxSnapshotAge(base.SucceededDuration)},
		"failed":    {Count: base.FailedCount, SumSeconds: maxSnapshotAge(base.FailedDuration)},
		"canceled":  {Count: base.CanceledCount, SumSeconds: maxSnapshotAge(base.CanceledDuration)},
	}
	snapshot.Deployments = map[string]int64{}
	counts, err := s.queries.OperationalDeploymentCounts(ctx)
	if err != nil {
		return observability.Snapshot{}, fmt.Errorf("operational deployment counts query: %w", err)
	}
	for _, row := range counts {
		snapshot.Deployments[row.Status] = row.Count
	}
	health, err := s.queries.OperationalDeploymentHealth(ctx)
	if err != nil {
		return observability.Snapshot{}, fmt.Errorf("operational deployment health query: %w", err)
	}
	snapshot.DeploymentHealthPassed, snapshot.DeploymentHealthFailed, snapshot.RollbackSucceeded, snapshot.RollbackFailed = health.Count, health.Count_2, health.Count_3, health.Count_4
	snapshot.BackupOutcome, snapshot.BackupReason = observability.BackupNone, "none"
	if base.Outcome != nil {
		snapshot.BackupOutcome = *base.Outcome
	}
	if base.Reason != nil {
		snapshot.BackupReason = *base.Reason
	}
	if base.BackupAge != nil {
		snapshot.BackupAgeSeconds = maxSnapshotAge(*base.BackupAge)
	}
	stat := s.pool.Stat()
	snapshot.Pool = observability.PoolState{Total: int64(stat.TotalConns()), Idle: int64(stat.IdleConns()), Acquired: int64(stat.AcquiredConns())}
	if err := snapshot.Validate(); err != nil {
		return observability.Snapshot{}, fmt.Errorf("validate snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *PostgresStore) RecordRunnerOperationalObservation(ctx context.Context, runnerID string, journalDepth, retryCount, renewFailures int) error {
	if journalDepth < 0 || journalDepth > 8192 || retryCount < 0 || retryCount > 100000 || renewFailures < 0 || renewFailures > 100000 {
		return ErrConflict
	}
	return s.queries.UpsertRunnerOperationalObservation(ctx, sqlcgen.UpsertRunnerOperationalObservationParams{RunnerID: runnerID, JournalDepth: int32(journalDepth), RetryCount: int32(retryCount), RenewFailures: int32(renewFailures)})
}

func (s *PostgresStore) RunnerOperationalObservation(ctx context.Context, runnerID string) (RunnerOperationalObservation, error) {
	value, err := s.queries.GetRunnerOperationalObservation(ctx, runnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunnerOperationalObservation{}, ErrNotFound
	}
	if err != nil {
		return RunnerOperationalObservation{}, fmt.Errorf("get runner operational observation query: %w", err)
	}
	return RunnerOperationalObservation{ObservedAt: value.ObservedAt, JournalDepth: int(value.JournalDepth), RetryCount: int(value.RetryCount), RenewFailures: int(value.RenewFailures)}, nil
}
