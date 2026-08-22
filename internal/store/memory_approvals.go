package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

func (s *MemoryStore) ListApprovals(_ context.Context, status string) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Approval, 0, len(s.approvals))
	for _, approval := range s.approvals {
		if status == "" || approval.Status == status {
			out = append(out, approval)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateApproval(_ context.Context, approval domain.Approval) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(approval.RunID) {
		return domain.Approval{}, ErrConflict
	}
	s.approvals = append(s.approvals, approval)
	return approval, nil
}

func (s *MemoryStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	return s.ApproveRunWithAudit(ctx, runID, actorID, approvedAt, domain.AuditEvent{})
}

func (s *MemoryStore) ApproveRunWithAudit(_ context.Context, runID string, actorID string, approvedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.Approval{}, ErrConflict
	}
	if s.deploymentBackedRunLocked(runID) {
		return domain.Approval{}, ErrConflict
	}
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			runIndex := -1
			for j, run := range s.runs {
				if run.ID == runID && run.Status == domain.RunWaitingApproval {
					runIndex = j
					break
				}
			}
			if runIndex < 0 {
				return domain.Approval{}, ErrNotFound
			}
			approval.Status = domain.ApprovalApproved
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &approvedAt
			s.approvals[i] = approval
			run := s.runs[runIndex]
			run.Status = domain.RunQueued
			s.runs[runIndex] = run
			if s.claimOrderByRun == nil {
				s.claimOrderByRun = make(map[string]time.Time)
			}
			s.claimOrderByRun[runID] = approvedAt
			if audit.ID != "" {
				audit.TargetID = runID
				audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": approval.ID})
				s.auditEvents = append(s.auditEvents, audit)
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}

func (s *MemoryStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	return s.RejectRunWithAudit(ctx, runID, actorID, rejectedAt, domain.AuditEvent{})
}

func (s *MemoryStore) RejectRunWithAudit(_ context.Context, runID string, actorID string, rejectedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.Approval{}, ErrConflict
	}
	if s.deploymentBackedRunLocked(runID) {
		return domain.Approval{}, ErrConflict
	}
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			approval.Status = domain.ApprovalRejected
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &rejectedAt
			s.approvals[i] = approval
			for j, run := range s.runs {
				if run.ID == runID {
					run.Status = domain.RunCanceled
					run.FinishedAt = &rejectedAt
					s.runs[j] = run
					break
				}
			}
			if audit.ID != "" {
				audit.TargetID = runID
				audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": approval.ID})
				s.auditEvents = append(s.auditEvents, audit)
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}
