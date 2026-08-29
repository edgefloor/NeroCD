package app

import (
	"context"
	"errors"
	"strings"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// RunLogRetentionPolicyInput supplies a run-log retention policy update.
type RunLogRetentionPolicyInput struct {
	Enabled   bool `json:"enabled"`
	KeepDays  int  `json:"keep_days"`
	BatchSize int  `json:"batch_size"`
}

// RunLogRetentionExecuteInput identifies an idempotent retention execution.
type RunLogRetentionExecuteInput struct {
	PolicyVersion int `json:"policy_version"`
}

// RunLogRetentionStatus combines the retention policy with its current preview.
type RunLogRetentionStatus struct {
	Policy  domain.RunLogRetentionPolicy  `json:"policy"`
	Preview domain.RunLogRetentionPreview `json:"preview"`
}

func (s *Service) requireSystemAdmin(ctx context.Context) (auth.Principal, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	if !isSystemAdmin(principal) {
		return auth.Principal{}, auth.ErrForbidden
	}
	if s.retention == nil {
		return auth.Principal{}, errors.New("run-log retention is unavailable")
	}
	return principal, nil
}

// RunLogRetentionStatus returns the current run-log retention policy and preview.
func (s *Service) RunLogRetentionStatus(ctx context.Context) (RunLogRetentionStatus, error) {
	if _, err := s.requireSystemAdmin(ctx); err != nil {
		return RunLogRetentionStatus{}, err
	}
	policy, err := s.retention.GetRunLogRetentionPolicy(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	preview, err := s.retention.PreviewRunLogRetention(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	return RunLogRetentionStatus{Policy: policy, Preview: preview}, nil
}

// UpdateRunLogRetentionPolicy updates the authorized run log retention policy.
func (s *Service) UpdateRunLogRetentionPolicy(ctx context.Context, input RunLogRetentionPolicyInput) (RunLogRetentionStatus, error) {
	principal, err := s.requireSystemAdmin(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run_log_retention.policy_updated", "run-log-retention", nil)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	policy, err := s.retention.UpdateRunLogRetentionPolicy(ctx, domain.RunLogRetentionPolicy{Enabled: input.Enabled, KeepDays: input.KeepDays, BatchSize: input.BatchSize, UpdatedBy: principal.ID}, store.WithAudit(audit))
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	preview, err := s.retention.PreviewRunLogRetention(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	return RunLogRetentionStatus{Policy: policy, Preview: preview}, nil
}

// ExecuteRunLogRetention performs an authorized run-log retention operation.
func (s *Service) ExecuteRunLogRetention(ctx context.Context, input RunLogRetentionExecuteInput) (domain.RunLogRetentionExecution, error) {
	principal, err := s.requireSystemAdmin(ctx)
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return domain.RunLogRetentionExecution{}, errors.New("request ID is required")
	}
	if input.PolicyVersion <= 0 {
		return domain.RunLogRetentionExecution{}, store.ErrConflict
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run_log_retention.execute", "run-log-retention", nil)
	if err != nil {
		return domain.RunLogRetentionExecution{}, err
	}
	return s.retention.ExecuteRunLogRetention(ctx, requestID, store.RunLogRetentionBodyHash(domain.RunLogRetentionPolicy{Version: input.PolicyVersion}), audit)
}
