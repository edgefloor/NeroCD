package app

import (
	"context"
	"errors"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// RunnerIdentity is the persisted identity of a dedicated runner credential.
type RunnerIdentity struct {
	RunnerID string `json:"runner_id"`
}

// CurrentRunnerIdentity reads current authority without heartbeat or other writes.
func (s *Service) CurrentRunnerIdentity(ctx context.Context) (RunnerIdentity, error) {
	principal, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return RunnerIdentity{}, err
	}
	current, err := s.runners.GetRunnerByID(ctx, principal.ID)
	if errors.Is(err, store.ErrNotFound) {
		return RunnerIdentity{}, auth.ErrUnauthenticated
	}
	if err != nil {
		return RunnerIdentity{}, err
	}
	if current.ID != principal.ID || current.ID == "" || current.Status != domain.RunnerActive || current.TokenHash == "" {
		return RunnerIdentity{}, auth.ErrUnauthenticated
	}
	return RunnerIdentity{RunnerID: current.ID}, nil
}
