package app

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// GetHostingDatabaseBinding returns desired references; connectivity is pending.
func (s *Service) GetHostingDatabaseBinding(ctx context.Context, id string) (domain.HostingDatabaseBinding, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, id, domain.RoleViewer)
	if err != nil {
		return domain.HostingDatabaseBinding{}, err
	}
	repository, ok := s.hosting.(store.HostingDatabaseBindingRepository)
	if !ok {
		return domain.HostingDatabaseBinding{}, store.ErrHostingDatabaseBindingUnavailable
	}
	return repository.GetHostingDatabaseBinding(ctx, actor.ID, resource.ID)
}

// PutHostingDatabaseBinding saves an internal pending binding under current owner
// authority. It neither schedules deployment nor connects running containers.
func (s *Service) PutHostingDatabaseBinding(ctx context.Context, id string, in domain.HostingDatabaseBindingInput) (domain.HostingDatabaseBinding, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, id, domain.RoleOwner)
	if err != nil {
		return domain.HostingDatabaseBinding{}, err
	}
	if resource.Kind != "application" || resource.Source.Kind != "image" {
		return domain.HostingDatabaseBinding{}, InvalidInput(errors.New("database binding requires an image application"))
	}
	if err = in.Validate(); err != nil {
		return domain.HostingDatabaseBinding{}, InvalidInput(err)
	}
	repository, ok := s.hosting.(store.HostingDatabaseBindingRepository)
	if !ok {
		return domain.HostingDatabaseBinding{}, store.ErrHostingDatabaseBindingUnavailable
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.runtime_configuration.updated", resource.ID, nil)
	if err != nil {
		return domain.HostingDatabaseBinding{}, err
	}
	return repository.PutHostingDatabaseBinding(ctx, actor.ID, resource.ID, in, event)
}
