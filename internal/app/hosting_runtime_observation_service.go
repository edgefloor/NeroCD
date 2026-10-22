package app

import (
	"context"
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) hostingObserver() (store.HostingRuntimeObservationRepository, error) {
	repo, ok := s.runs.(store.HostingRuntimeObservationRepository)
	if !ok {
		return nil, hostingjobs.ErrUnavailable
	}
	return repo, nil
}

// observationRunner binds the HTTP bearer to the current dedicated runner.
func (s *Service) observationRunner(ctx context.Context, token string) (string, string, error) {
	p, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return "", "", err
	}
	current, err := s.AuthenticateRunnerToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	if current.ID != p.ID {
		return "", "", auth.ErrForbidden
	}
	return p.ID, runnerTokenHash(token), nil
}

// RunnerHostingObservationTargets derives targets for the authenticated credential.
func (s *Service) RunnerHostingObservationTargets(ctx context.Context, token string, in domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
	var zero domain.HostingObservationTargets
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.hostingObserver()
	if err != nil {
		return zero, err
	}
	return repo.HostingObservationTargets(ctx, runner, hash, in)
}

// RunnerReportHostingObservation submits closed evidence under current authority.
func (s *Service) RunnerReportHostingObservation(ctx context.Context, token string, in domain.HostingRuntimeObservation) (domain.HostingObservationAck, error) {
	var zero domain.HostingObservationAck
	runner, hash, err := s.observationRunner(ctx, token)
	if err != nil {
		return zero, err
	}
	if err = in.Validate(); err != nil {
		return zero, InvalidInput(err)
	}
	repo, err := s.hostingObserver()
	if err != nil {
		return zero, err
	}
	return repo.ReportHostingObservation(ctx, runner, hash, in)
}

// HostingRuntimeStatus returns independent evidence under current viewer authority.
func (s *Service) HostingRuntimeStatus(ctx context.Context, id string) (domain.HostingRuntimeStatus, error) {
	var zero domain.HostingRuntimeStatus
	resource, actor, err := s.resourceForDestinationRole(ctx, id, domain.RoleViewer)
	if err != nil {
		return zero, err
	}
	if resource.ID == "" {
		return zero, InvalidInput(errors.New("resource is required"))
	}
	repo, err := s.hostingObserver()
	if err != nil {
		return zero, err
	}
	return repo.HostingRuntimeStatus(ctx, actor.ID, resource.ID)
}
