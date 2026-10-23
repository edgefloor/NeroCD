package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageDeploymentReader(ctx context.Context, resource string) (domain.Resource, string, store.HostingStorageDeploymentReadRepository, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return r, "", nil, err
	}
	repo, ok := s.hosting.(store.HostingStorageDeploymentReadRepository)
	if !ok {
		return r, "", nil, store.ErrHostingStorageUnavailable
	}
	return r, actor.ID, repo, nil
}

// HostingStorageDeploymentOptions reads choices without authorizing a deployment.
func (s *Service) HostingStorageDeploymentOptions(ctx context.Context, resource string) (domain.HostingStorageDeploymentOptions, error) {
	r, actor, repo, err := s.storageDeploymentReader(ctx, resource)
	if err != nil {
		return domain.HostingStorageDeploymentOptions{}, err
	}
	view, err := repo.HostingStorageDeploymentOptions(ctx, r.ID, actor)
	if err == nil && view.ResourceID != r.ID {
		return domain.HostingStorageDeploymentOptions{}, store.ErrConflict
	}
	return view, err
}

// HostingStorageRouteContinuityStatus returns only an exact resource operation.
func (s *Service) HostingStorageRouteContinuityStatus(ctx context.Context, resource, operation string) (domain.HostingStorageRouteContinuityStatus, error) {
	r, actor, repo, err := s.storageDeploymentReader(ctx, resource)
	if err != nil {
		return domain.HostingStorageRouteContinuityStatus{}, err
	}
	if err = validateDestinationID(operation, "operation_id"); err != nil {
		return domain.HostingStorageRouteContinuityStatus{}, err
	}
	view, err := repo.HostingStorageRouteContinuityStatus(ctx, r.ID, actor, operation)
	if err == nil && (view.ResourceID != r.ID || view.OperationID != operation) {
		return domain.HostingStorageRouteContinuityStatus{}, store.ErrConflict
	}
	return view, err
}
