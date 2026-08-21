package app

import (
	"context"

	"nerocd/internal/domain"
)

func (s *Service) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := s.approvals.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	if isSystemAdmin(principal) {
		return approvals, nil
	}
	runs, err := s.runs.ListRuns(ctx, "")
	if err != nil {
		return nil, err
	}
	visibleRuns, err := s.filterRunsForPrincipal(ctx, principal, runs)
	if err != nil {
		return nil, err
	}
	allowedRuns := map[string]struct{}{}
	for _, run := range visibleRuns {
		allowedRuns[run.ID] = struct{}{}
	}
	out := make([]domain.Approval, 0, len(approvals))
	for _, approval := range approvals {
		if _, ok := allowedRuns[approval.RunID]; ok {
			out = append(out, approval)
		}
	}
	return out, nil
}
