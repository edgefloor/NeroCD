package app

import (
	"context"

	"nerocd/internal/store"
)

// HostingDatabaseConnectionRunnerContext binds the original authenticated bearer
// fingerprint for new-family generic claim and common hosting phase requests.
// It carries no bearer value and changes no ordinary-family authorization policy.
func (s *Service) HostingDatabaseConnectionRunnerContext(ctx context.Context, token string) (context.Context, error) {
	runner, fingerprint, err := s.observationRunner(ctx, token)
	if err != nil {
		return nil, err
	}
	return store.BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
}
