package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListApprovals implements the corresponding repository operation.
func (s *PostgresStore) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	rows, err := s.queries.ListApprovals(ctx, status)
	if err != nil {
		return nil, fmt.Errorf("list approvals query: %w", err)
	}
	approvals := make([]domain.Approval, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, approvalFromSQLC(row))
	}
	return approvals, nil
}

// CreateApproval implements the corresponding repository operation.
func (s *PostgresStore) CreateApproval(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, approval.RunID); err != nil {
		return domain.Approval{}, fmt.Errorf("reject generic deployment run: %w", err)
	}
	inserted, err := s.queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt})
	if err != nil {
		return domain.Approval{}, fmt.Errorf("create approval query: %w", err)
	}
	return approvalFromSQLC(inserted), nil
}

// ApproveRun implements the corresponding repository operation.
func (s *PostgresStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time, opts ...MutationOption) (domain.Approval, error) {
	audit := resolveMutationOptions(opts)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, fmt.Errorf("reject generic deployment run: %w", err)
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalApproved, ApprovedBy: &actorID, ApprovedAt: &approvedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, fmt.Errorf("resolve approval query: %w", err)
	}
	if _, err := queries.QueueApprovedRun(ctx, runID); err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	} else if err != nil {
		return domain.Approval{}, fmt.Errorf("queue approved run query: %w", err)
	}
	if audit != nil && audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, *audit); err != nil {
			return domain.Approval{}, fmt.Errorf("create audit event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, fmt.Errorf("commit transaction: %w", err)
	}
	return approvalFromSQLC(updated), nil
}

// RejectRun implements the corresponding repository operation.
func (s *PostgresStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time, opts ...MutationOption) (domain.Approval, error) {
	audit := resolveMutationOptions(opts)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, fmt.Errorf("reject generic deployment run: %w", err)
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalRejected, ApprovedBy: &actorID, ApprovedAt: &rejectedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, fmt.Errorf("resolve approval query: %w", err)
	}
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: runID, Status: domain.RunCanceled, FinishedAt: &rejectedAt}); err != nil {
		return domain.Approval{}, fmt.Errorf("update run status query: %w", err)
	}
	if audit != nil && audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, *audit); err != nil {
			return domain.Approval{}, fmt.Errorf("create audit event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, fmt.Errorf("commit transaction: %w", err)
	}
	return approvalFromSQLC(updated), nil
}
