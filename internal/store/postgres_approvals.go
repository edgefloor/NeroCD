package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func (s *PostgresStore) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	rows, err := s.queries.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	approvals := make([]domain.Approval, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, approvalFromSQLC(row))
	}
	return approvals, nil
}

func (s *PostgresStore) CreateApproval(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, approval.RunID); err != nil {
		return domain.Approval{}, err
	}
	inserted, err := s.queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt})
	if err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(inserted), nil
}

func (s *PostgresStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	return s.ApproveRunWithAudit(ctx, runID, actorID, approvedAt, domain.AuditEvent{})
}

func (s *PostgresStore) ApproveRunWithAudit(ctx context.Context, runID string, actorID string, approvedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, err
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalApproved, ApprovedBy: &actorID, ApprovedAt: &approvedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.QueueApprovedRun(ctx, runID); err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	} else if err != nil {
		return domain.Approval{}, err
	}
	if audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, audit); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}

func (s *PostgresStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	return s.RejectRunWithAudit(ctx, runID, actorID, rejectedAt, domain.AuditEvent{})
}

func (s *PostgresStore) RejectRunWithAudit(ctx context.Context, runID string, actorID string, rejectedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, err
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalRejected, ApprovedBy: &actorID, ApprovedAt: &rejectedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: runID, Status: domain.RunCanceled, FinishedAt: &rejectedAt}); err != nil {
		return domain.Approval{}, err
	}
	if audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, audit); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}
