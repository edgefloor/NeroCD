package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) GetRunLogRetentionPolicy(ctx context.Context) (domain.RunLogRetentionPolicy, error) {
	r, err := s.queries.GetRunLogRetentionPolicy(ctx)
	if err != nil {
		return domain.RunLogRetentionPolicy{}, err
	}
	return domain.RunLogRetentionPolicy{Enabled: r.Enabled, KeepDays: int(r.KeepDays), BatchSize: int(r.BatchSize), Version: int(r.Version), UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}, nil
}
func (s *PostgresStore) UpdateRunLogRetentionPolicy(ctx context.Context, p domain.RunLogRetentionPolicy, opts ...MutationOption) (domain.RunLogRetentionPolicy, error) {
	audit := resolveMutationOptions(opts)
	if !validRunLogRetentionPolicy(p) {
		return domain.RunLogRetentionPolicy{}, ErrConflict
	}
	if audit == nil {
		r, err := s.queries.UpdateRunLogRetentionPolicy(ctx, sqlcgen.UpdateRunLogRetentionPolicyParams{Enabled: p.Enabled, KeepDays: int32(p.KeepDays), BatchSize: int32(p.BatchSize), UpdatedBy: p.UpdatedBy})
		if err != nil {
			return domain.RunLogRetentionPolicy{}, err
		}
		return domain.RunLogRetentionPolicy{Enabled: r.Enabled, KeepDays: int(r.KeepDays), BatchSize: int(r.BatchSize), Version: int(r.Version), UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLogRetentionPolicy{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	r, err := q.UpdateRunLogRetentionPolicy(ctx, sqlcgen.UpdateRunLogRetentionPolicyParams{Enabled: p.Enabled, KeepDays: int32(p.KeepDays), BatchSize: int32(p.BatchSize), UpdatedBy: p.UpdatedBy})
	if err != nil {
		return domain.RunLogRetentionPolicy{}, err
	}
	audit.Metadata = map[string]any{"enabled": r.Enabled, "keep_days": r.KeepDays, "batch_size": r.BatchSize, "policy_version": r.Version}
	audit.CreatedAt = r.UpdatedAt
	if err := createAuditWithQueries(ctx, q, *audit); err != nil {
		return domain.RunLogRetentionPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLogRetentionPolicy{}, err
	}
	return domain.RunLogRetentionPolicy{Enabled: r.Enabled, KeepDays: int(r.KeepDays), BatchSize: int(r.BatchSize), Version: int(r.Version), UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}, nil
}
func (s *PostgresStore) PreviewRunLogRetention(ctx context.Context) (domain.RunLogRetentionPreview, error) {
	r, err := s.queries.PreviewRunLogRetention(ctx)
	if err != nil {
		return domain.RunLogRetentionPreview{}, err
	}
	return domain.RunLogRetentionPreview{Cutoff: r.Cutoff, EligibleLogs: r.EligibleLogs, EligibleBytes: r.EligibleBytes}, nil
}

func retentionExecutionFromReceipt(receipt sqlcgen.RunLogRetentionReceipt) domain.RunLogRetentionExecution {
	policy := domain.RunLogRetentionPolicy{Enabled: true, KeepDays: int(receipt.KeepDays), BatchSize: int(receipt.BatchSize), Version: int(receipt.PolicyVersion), UpdatedAt: receipt.CreatedAt}
	preview := domain.RunLogRetentionPreview{Cutoff: receipt.Cutoff, EligibleLogs: receipt.EligibleLogs, EligibleBytes: receipt.EligibleBytes}
	return domain.RunLogRetentionExecution{RequestID: receipt.RequestID, Policy: policy, Preview: preview, Deleted: receipt.DeletedCount, DeletedBytes: receipt.DeletedBytes}
}

// ExecuteRunLogRetention is a bounded all-or-nothing maintenance transaction.
// Its lock order is policy/receipt -> task run -> per-run log advisory lock ->
// active leases, which stays compatible with fenced log append and terminal
// transition paths. It selects and deletes run_logs only.
func (s *PostgresStore) ExecuteRunLogRetention(ctx context.Context, requestID, bodyHash string, audit domain.AuditEvent) (domain.RunLogRetentionExecution, error) {
	requestID, bodyHash = strings.TrimSpace(requestID), strings.TrimSpace(bodyHash)
	if requestID == "" || bodyHash == "" {
		return domain.RunLogRetentionExecution{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	locked, err := q.LockRunLogRetentionPolicy(ctx)
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	now := locked.DatabaseTime
	policy := domain.RunLogRetentionPolicy{Enabled: locked.Enabled, KeepDays: int(locked.KeepDays), BatchSize: int(locked.BatchSize), Version: int(locked.Version), UpdatedBy: locked.UpdatedBy, UpdatedAt: locked.UpdatedAt}
	receipt, receiptErr := q.GetRunLogRetentionReceiptForUpdate(ctx, requestID)
	if receiptErr == nil {
		if receipt.BodySha256 != bodyHash {
			return domain.RunLogRetentionExecution{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		return retentionExecutionFromReceipt(receipt), nil
	}
	if receiptErr != nil && !errors.Is(receiptErr, pgx.ErrNoRows) {
		return domain.RunLogRetentionExecution{}, receiptErr
	}
	if !policy.Enabled || bodyHash != RunLogRetentionBodyHash(policy) {
		return domain.RunLogRetentionExecution{}, ErrConflict
	}
	cutoff := now.UTC().Add(-time.Duration(policy.KeepDays) * 24 * time.Hour)
	previewRow, err := q.CountRunLogRetentionCandidates(ctx, cutoff)
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	preview := domain.RunLogRetentionPreview{Cutoff: cutoff, EligibleLogs: previewRow.EligibleLogs, EligibleBytes: previewRow.EligibleBytes}
	runIDs, err := q.ListRunLogRetentionCandidateRuns(ctx, sqlcgen.ListRunLogRetentionCandidateRunsParams{Cutoff: cutoff, CandidateLimit: int32(policy.BatchSize)})
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	logIDs := make([]string, 0, policy.BatchSize)
	for _, runID := range runIDs {
		if len(logIDs) == policy.BatchSize {
			break
		}
		if _, err := q.LockTerminalRunForRetention(ctx, runID); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		if err := q.AcquireRunLogLock(ctx, runID); err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		active, err := q.LockActiveLeasesForRetention(ctx, runID)
		if err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		if len(active) != 0 {
			continue
		}
		rows, err := q.ListRunLogRetentionCandidatesForRun(ctx, sqlcgen.ListRunLogRetentionCandidatesForRunParams{RunID: runID, Cutoff: cutoff, CandidateLimit: int32(policy.BatchSize - len(logIDs))})
		if err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		for _, row := range rows {
			logIDs = append(logIDs, row.ID)
		}
	}
	deleted, deletedBytes := int64(0), int64(0)
	if len(logIDs) != 0 {
		rows, err := q.DeleteRunLogRetentionCandidates(ctx, logIDs)
		if err != nil {
			return domain.RunLogRetentionExecution{}, err
		}
		if len(rows) != len(logIDs) {
			return domain.RunLogRetentionExecution{}, ErrConflict
		}
		for _, row := range rows {
			deleted++
			deletedBytes += row.MessageBytes
		}
	}
	audit.Metadata = map[string]any{"policy_version": policy.Version, "cutoff": cutoff.UTC().Format(time.RFC3339), "deleted": deleted, "deleted_bytes": deletedBytes}
	audit.CreatedAt = now.UTC()
	if err := createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	receipt, err = q.CreateRunLogRetentionReceipt(ctx, sqlcgen.CreateRunLogRetentionReceiptParams{RequestID: requestID, BodySha256: bodyHash, KeepDays: int32(policy.KeepDays), BatchSize: int32(policy.BatchSize), PolicyVersion: int32(policy.Version), Cutoff: cutoff, EligibleLogs: preview.EligibleLogs, EligibleBytes: preview.EligibleBytes, DeletedCount: deleted, DeletedBytes: deletedBytes, AuditID: audit.ID})
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	return retentionExecutionFromReceipt(receipt), nil
}
