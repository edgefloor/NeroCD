package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageBackupRetentionRepository() (store.HostingStorageBackupRetentionRepository, error) {
	r, ok := s.hosting.(store.HostingStorageBackupRetentionRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return r, nil
}

// GetStorageBackupRetention returns the authorized storage backup retention policy.
func (s *Service) GetStorageBackupRetention(ctx context.Context, resource string) (*domain.HostingStorageBackupRetentionPolicy, error) {
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return nil, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return nil, e
	}
	return r.GetStorageBackupRetention(ctx, a.ID, resource)
}

// SaveStorageBackupRetention updates the authorized storage backup retention policy.
func (s *Service) SaveStorageBackupRetention(ctx context.Context, resource string, in domain.HostingStorageBackupRetentionInput) (domain.HostingStorageBackupRetentionPolicy, error) {
	var z domain.HostingStorageBackupRetentionPolicy
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidStorageBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.SaveStorageBackupRetention(ctx, a.ID, resource, in)
}

// PreviewStorageBackupRetention records a retention preview for storage backup copies.
func (s *Service) PreviewStorageBackupRetention(ctx context.Context, resource string, in domain.HostingStorageBackupRetentionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	var z domain.HostingStorageBackupRetentionPreview
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidStorageBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("sbrp")
	if e != nil {
		return z, e
	}
	return r.PreviewStorageBackupRetention(ctx, id, a.ID, resource, in)
}

// ExecuteStorageBackupRetention admits deletion of the storage copies selected by a saved
// preview.
func (s *Service) ExecuteStorageBackupRetention(ctx context.Context, resource string, in domain.HostingStorageBackupRetentionExecutionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	var z domain.HostingStorageBackupRetentionExecution
	if in.Validate() != nil {
		return z, InvalidInput(domain.ErrInvalidStorageBackupRetention)
	}
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	id, e := prefixedID("sbre")
	if e != nil {
		return z, e
	}
	return r.ExecuteStorageBackupRetention(ctx, id, a.ID, resource, in)
}

// ListStorageBackupRetentionExecutions lists authorized storage backup retention executions.
func (s *Service) ListStorageBackupRetentionExecutions(ctx context.Context, resource string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return nil, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return nil, e
	}
	return r.ListStorageBackupRetentionExecutions(ctx, a.ID, resource)
}

// GetStorageBackupRetentionExecution returns an authorized storage backup retention execution.
func (s *Service) GetStorageBackupRetentionExecution(ctx context.Context, resource, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	var z domain.HostingStorageBackupRetentionExecution
	_, a, e := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.GetStorageBackupRetentionExecution(ctx, a.ID, resource, id)
}

// RunnerStorageBackupRetentionPlan returns a storage retention plan for the authenticated runner.
func (s *Service) RunnerStorageBackupRetentionPlan(ctx context.Context, a domain.HostingAuthority) (domain.HostingStorageBackupRetentionPlan, error) {
	var z domain.HostingStorageBackupRetentionPlan
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.StorageBackupRetentionPlan(ctx, p.ID, a)
}

// RunnerStorageBackupRetentionMutation records a storage retention mutation from the
// authenticated runner.
func (s *Service) RunnerStorageBackupRetentionMutation(ctx context.Context, in domain.HostingStorageBackupRetentionMutation) (domain.HostingStorageBackupRetentionExecution, error) {
	var z domain.HostingStorageBackupRetentionExecution
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.StorageBackupRetentionMutation(ctx, p.ID, in)
}

// RunnerStorageBackupRetentionRetirement checks retirement of a storage retention lease for the
// authenticated runner.
func (s *Service) RunnerStorageBackupRetentionRetirement(ctx context.Context, in domain.HostingStorageBackupRetentionRetirementInput) (domain.HostingStorageBackupRetentionRetirement, error) {
	var z domain.HostingStorageBackupRetentionRetirement
	p, e := s.requireRunnerPrincipal(ctx)
	if e != nil {
		return z, e
	}
	r, e := s.storageBackupRetentionRepository()
	if e != nil {
		return z, e
	}
	return r.StorageBackupRetentionRetirement(ctx, p.ID, in)
}
