package app

import (
	"context"
	"errors"
	"nerocd/internal/domain"
)

// RequestHostingBuildDeployment admits an exact current successful local build.
// Runtime execution requires a separately upgraded local deployment agent.
func (s *Service) RequestHostingBuildDeployment(ctx context.Context, resourceID string, in domain.HostingBuildDeploymentInput) (domain.HostingOperation, error) {
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if resource.Source.Kind != "git" {
		return domain.HostingOperation{}, InvalidInput(errors.New("build deployment requires a Git resource"))
	}
	if err = validateDestinationID(in.BuildID, "build_id"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = domain.ValidateHostingBuildDeployment(in); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	executor, err := s.hostingExecutor()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.build_deployment_requested", id, map[string]any{"resource_id": resource.ID, "build_id": in.BuildID, "expected_generation": in.ExpectedGeneration, "expected_source_generation": in.ExpectedSourceGeneration})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return executor.RequestHostingBuildDeployment(ctx, id, resource.ID, principal.ID, in, audit)
}
