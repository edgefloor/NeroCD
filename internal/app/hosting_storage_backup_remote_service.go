package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageBackupRemoteRepository() (store.HostingStorageBackupRemoteRepository, error) {
	r, ok := s.hosting.(store.HostingStorageBackupRemoteRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// ValidateStorageBackupTarget queues remote target validation for the selected destination.
func (s *Service) ValidateStorageBackupTarget(ctx context.Context, project, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingStorageBackupTransfer, error) {
	var zero domain.HostingStorageBackupTransfer
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidStorageBackupRemote)
	}
	actor, err := s.authorizeHostingProject(ctx, project, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("stx")
	if err != nil {
		return zero, err
	}
	return repo.ValidateStorageBackupTarget(ctx, id, project, actor.ID, target, in)
}

// ListStorageBackupTransfers lists authorized storage backup transfers or target validations.
func (s *Service) ListStorageBackupTransfers(ctx context.Context, id string, validation bool) ([]domain.HostingStorageBackupTransfer, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListStorageBackupTransfers(ctx, actor.ID, id, validation)
}

// GetStorageBackupTransfer returns an authorized storage backup transfer view.
func (s *Service) GetStorageBackupTransfer(ctx context.Context, id string) (domain.HostingStorageBackupTransfer, error) {
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupTransfer{}, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return domain.HostingStorageBackupTransfer{}, err
	}
	return repo.GetStorageBackupTransfer(ctx, actor.ID, id)
}

// RetryStorageBackupTransfer admits a retry of the selected failed storage backup upload.
func (s *Service) RetryStorageBackupTransfer(ctx context.Context, backup string, in domain.HostingStorageBackupTransferRetryInput) (domain.HostingStorageBackupTransfer, error) {
	var zero domain.HostingStorageBackupTransfer
	if in.Validate() != nil {
		return zero, InvalidInput(domain.ErrInvalidStorageBackupRemote)
	}
	actor, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return zero, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("stx")
	if err != nil {
		return zero, err
	}
	return repo.RetryStorageBackupTransfer(ctx, id, actor.ID, backup, in)
}

// RunnerStorageBackupTransferPlan returns a storage transfer plan for the authenticated runner.
func (s *Service) RunnerStorageBackupTransferPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingStorageBackupTransferPlan, error) {
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupTransferPlan{}, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return domain.HostingStorageBackupTransferPlan{}, err
	}
	return repo.StorageBackupTransferPlan(ctx, runner.ID, in)
}

// RunnerStorageBackupTransferMutation records a storage transfer mutation from the authenticated
// runner.
func (s *Service) RunnerStorageBackupTransferMutation(ctx context.Context, in domain.HostingStorageBackupTransferMutation) (domain.HostingStorageBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingStorageBackupTransfer{}, InvalidInput(domain.ErrInvalidStorageBackupRemote)
	}
	runner, err := s.requireRunnerPrincipal(ctx)
	if err != nil {
		return domain.HostingStorageBackupTransfer{}, err
	}
	repo, err := s.storageBackupRemoteRepository()
	if err != nil {
		return domain.HostingStorageBackupTransfer{}, err
	}
	return repo.StorageBackupTransferMutation(ctx, runner.ID, in)
}
