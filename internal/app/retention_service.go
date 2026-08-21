package app

import (
	"context"
	"errors"
	"strings"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type RunLogRetentionPolicyInput struct {
	Enabled   bool `json:"enabled"`
	KeepDays  int  `json:"keep_days"`
	BatchSize int  `json:"batch_size"`
}

type RunLogRetentionExecuteInput struct {
	PolicyVersion int `json:"policy_version"`
}

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

func (s *Service) UpdateRunLogRetentionPolicy(ctx context.Context, input RunLogRetentionPolicyInput) (RunLogRetentionStatus, error) {
	principal, err := s.requireSystemAdmin(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "run_log_retention.policy_updated", "run-log-retention", nil)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	policy, err := s.retention.UpdateRunLogRetentionPolicyWithAudit(ctx, domain.RunLogRetentionPolicy{Enabled: input.Enabled, KeepDays: input.KeepDays, BatchSize: input.BatchSize, UpdatedBy: principal.ID}, audit)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	preview, err := s.retention.PreviewRunLogRetention(ctx)
	if err != nil {
		return RunLogRetentionStatus{}, err
	}
	return RunLogRetentionStatus{Policy: policy, Preview: preview}, nil
}

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
