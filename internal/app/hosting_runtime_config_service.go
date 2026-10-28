package app

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func supportedRuntimeConfiguration(resource domain.Resource) error {
	if resource.Kind != "application" || (resource.Source.Kind != "image" && resource.Source.Kind != "git" && resource.Source.Kind != "compose" && resource.Source.Kind != "dockerfile") {
		return InvalidInput(errors.New("runtime configuration supports application resources with image, Git, or Compose sources"))
	}
	return nil
}

// GetHostingRuntimeConfiguration returns literals and exact secret references only.
func (s *Service) GetHostingRuntimeConfiguration(ctx context.Context, id string) (domain.HostingRuntimeConfiguration, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, id, domain.RoleViewer)
	if err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	if err = supportedRuntimeConfiguration(resource); err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	repository, ok := s.hosting.(store.HostingRuntimeConfigurationRepository)
	if !ok {
		return domain.HostingRuntimeConfiguration{}, store.ErrHostingRuntimeConfigurationUnavailable
	}
	return repository.GetHostingRuntimeConfiguration(ctx, actor.ID, resource.ID)
}

// PutHostingRuntimeConfiguration saves desired input without starting a deployment.
func (s *Service) PutHostingRuntimeConfiguration(ctx context.Context, id string, in domain.HostingRuntimeConfigurationInput) (domain.HostingRuntimeConfiguration, error) {
	resource, actor, err := s.resourceForDestinationRole(ctx, id, domain.RoleOwner)
	if err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	if err = supportedRuntimeConfiguration(resource); err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	if _, _, err = in.Validate(); err != nil {
		return domain.HostingRuntimeConfiguration{}, InvalidInput(err)
	}
	repository, ok := s.hosting.(store.HostingRuntimeConfigurationRepository)
	if !ok {
		return domain.HostingRuntimeConfiguration{}, store.ErrHostingRuntimeConfigurationUnavailable
	}
	event, err := s.auditEvent(ctx, actor.ID, "hosting.runtime_configuration.updated", resource.ID, nil)
	if err != nil {
		return domain.HostingRuntimeConfiguration{}, err
	}
	return repository.PutHostingRuntimeConfiguration(ctx, actor.ID, resource.ID, in, event)
}
