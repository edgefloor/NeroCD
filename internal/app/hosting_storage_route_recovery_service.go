package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// RecoverHostingStorageRouteContinuity queues the missing proxy successor after
// current maintainer authorization. The repository rechecks all durable evidence.
func (s *Service) RecoverHostingStorageRouteContinuity(ctx context.Context, resource, operation string, in domain.HostingStorageRouteRecoveryInput) (domain.HostingStorageRouteContinuityStatus, error) {
	var view domain.HostingStorageRouteContinuityStatus
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleMaintainer)
	if err != nil {
		return view, err
	}
	if err = validateDestinationID(operation, "operation_id"); err != nil {
		return view, err
	}
	if err = in.Validate(); err != nil {
		return view, InvalidInput(err)
	}
	repo, ok := s.hosting.(store.HostingStorageRouteRecoveryRepository)
	if !ok {
		return view, store.ErrHostingStorageUnavailable
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.storage_route_continuity_recovered", operation, nil)
	if err != nil {
		return view, err
	}
	view, err = repo.RecoverHostingStorageRouteContinuity(ctx, r.ID, operation, actor.ID, in, audit)
	if err == nil && (view.ResourceID != r.ID || view.OperationID != operation) {
		return domain.HostingStorageRouteContinuityStatus{}, store.ErrConflict
	}
	return view, err
}
