package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

// HostingStackLifecycle returns authorized settled evidence without runtime demand.
func (s *Service) HostingStackLifecycle(ctx context.Context, resource string) (domain.HostingStackLifecycle, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return domain.HostingStackLifecycle{}, err
	}
	repo, ok := s.runs.(store.HostingStackLifecycleRepository)
	if !ok {
		return domain.HostingStackLifecycle{}, hostingjobs.ErrUnavailable
	}
	return repo.HostingStackLifecycle(ctx, actor.ID, r.ID)
}

// RequestHostingStackStop admits an exact whole-stack removal for a maintainer.
func (s *Service) RequestHostingStackStop(ctx context.Context, resource string, in domain.HostingStackStopInput) (domain.HostingOperation, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if err = in.Validate(); err != nil {
		return domain.HostingOperation{}, InvalidInput(err)
	}
	repo, ok := s.runs.(store.HostingStackLifecycleRepository)
	if !ok {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.stack_stop_requested", id, map[string]any{"resource_id": r.ID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingStackStop(ctx, id, r.ID, actor.ID, in, audit)
}
