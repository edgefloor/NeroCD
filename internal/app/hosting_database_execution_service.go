package app

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

// RequestHostingDatabaseDeployment queues an immutable private database target.
func (s *Service) RequestHostingDatabaseDeployment(ctx context.Context, resourceID string, in domain.HostingDatabaseDeploymentInput) (domain.HostingOperation, error) {
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if resource.Kind != "database" {
		return domain.HostingOperation{}, InvalidInput(errors.New("database deployment requires a database resource"))
	}
	if err = in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, ok := s.runs.(store.HostingDatabaseExecutionRepository)
	if !ok {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.database_deployment_requested", id, nil)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingDatabaseDeployment(ctx, id, resource.ID, principal.ID, in, audit)
}
