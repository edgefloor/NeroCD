package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) readinessRepository() (store.ServerReadinessRepository, error) {
	repo, ok := s.runs.(store.ServerReadinessRepository)
	if !ok {
		return nil, hostingjobs.ErrUnavailable
	}
	return repo, nil
}

// RunnerServerReadinessChallenge derives placement from the current bearer.
func (s *Service) RunnerServerReadinessChallenge(ctx context.Context, token string) (domain.ServerReadinessChallenge, error) {
	var zero domain.ServerReadinessChallenge
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	repo, err := s.readinessRepository()
	if err != nil {
		return zero, err
	}
	return repo.IssueServerReadiness(ctx, runner, hash)
}

// RunnerReportServerReadiness records evidence without asserting readiness.
func (s *Service) RunnerReportServerReadiness(ctx context.Context, token string, in domain.ServerReadinessReport) (domain.ServerReadinessAck, error) {
	var zero domain.ServerReadinessAck
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.readinessRepository()
	if err != nil {
		return zero, err
	}
	return repo.ReportServerReadiness(ctx, runner, hash, in)
}

// ServerReadiness requires the existing global destination administrator role.
func (s *Service) ServerReadiness(ctx context.Context, id string) (domain.ServerReadiness, error) {
	var zero domain.ServerReadiness
	if _, err := s.requireDestinationAdmin(ctx); err != nil {
		return zero, err
	}
	if err := validateDestinationID(id, "server_id"); err != nil {
		return zero, err
	}
	repo, err := s.readinessRepository()
	if err != nil {
		return zero, err
	}
	return repo.GetServerReadiness(ctx, id)
}
