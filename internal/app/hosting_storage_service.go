package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func supportedHostingStorage(resource domain.Resource) error {
	if resource.Kind != "application" || resource.Source.Kind != "image" {
		return InvalidInput(domain.ErrInvalidHostingStorage)
	}
	return nil
}

// GetHostingStorage returns immutable desired metadata under current viewer authority.
// The repository also rechecks membership and the current destination grant.
func (s *Service) GetHostingStorage(ctx context.Context, resource string) (domain.HostingStorage, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.HostingStorage{}, err
	}
	if err = supportedHostingStorage(r); err != nil {
		return domain.HostingStorage{}, err
	}
	repo, ok := s.hosting.(store.HostingStorageRepository)
	if !ok {
		return domain.HostingStorage{}, store.ErrHostingStorageUnavailable
	}
	return repo.GetHostingStorage(ctx, actor.ID, r.ID)
}

// CreateHostingStorage records one immutable local volume selection. It never
// creates a Docker volume, changes a deployment plan, or schedules execution.
func (s *Service) CreateHostingStorage(ctx context.Context, resource string, in domain.HostingStorageInput) (domain.HostingStorage, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return domain.HostingStorage{}, err
	}
	if err = supportedHostingStorage(r); err != nil {
		return domain.HostingStorage{}, err
	}
	if err = in.Validate(); err != nil {
		return domain.HostingStorage{}, InvalidInput(err)
	}
	repo, ok := s.hosting.(store.HostingStorageRepository)
	if !ok {
		return domain.HostingStorage{}, store.ErrHostingStorageUnavailable
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.storage.created", r.ID, nil)
	if err != nil {
		return domain.HostingStorage{}, err
	}
	return repo.CreateHostingStorage(ctx, actor.ID, r.ID, in, event)
}
