package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageConnectionRunner(ctx context.Context, token string) (store.HostingApplicationDatabaseStorageRunnerRepository, string, string, error) {
	base, runner, credential, err := s.connectionRunner(ctx, token)
	if err != nil {
		return nil, "", "", err
	}
	repo, ok := base.(store.HostingApplicationDatabaseStorageRunnerRepository)
	if !ok {
		return nil, "", "", store.ErrHostingDatabaseConnectionUnavailable
	}
	return repo, runner, credential, nil
}

// RunnerHostingApplicationDatabaseStorageWriter records a storage writer receipt for the
// authenticated runner.
func (s *Service) RunnerHostingApplicationDatabaseStorageWriter(ctx context.Context, token string, in domain.HostingApplicationDatabaseStorageWriterRequest) (domain.HostingOperation, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, runner, credential, err := s.storageConnectionRunner(ctx, token)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.HostingApplicationDatabaseStorageWriter(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseStorageAuthorize authorizes a storage effect for the
// authenticated runner.
func (s *Service) RunnerHostingApplicationDatabaseStorageAuthorize(ctx context.Context, token string, in domain.HostingApplicationDatabaseStorageEffectRequest) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingApplicationDatabaseStorageEffectAuthorization{}, InvalidInput(err)
	}
	repo, runner, credential, err := s.storageConnectionRunner(ctx, token)
	if err != nil {
		return domain.HostingApplicationDatabaseStorageEffectAuthorization{}, err
	}
	return repo.AuthorizeHostingApplicationDatabaseStorageEffect(ctx, runner, credential, in)
}

// RunnerHostingApplicationDatabaseStorageProxyPlan returns the storage proxy plan for the
// authenticated runner.
func (s *Service) RunnerHostingApplicationDatabaseStorageProxyPlan(ctx context.Context, token string, in domain.HostingAuthority) (domain.HostingApplicationDatabaseStorageProxyPlan, error) {
	repo, runner, credential, err := s.storageConnectionRunner(ctx, token)
	if err != nil {
		return domain.HostingApplicationDatabaseStorageProxyPlan{}, err
	}
	return repo.HostingApplicationDatabaseStorageProxyPlan(ctx, runner, credential, in)
}
