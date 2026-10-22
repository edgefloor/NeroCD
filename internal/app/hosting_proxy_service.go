package app

import (
	"context"

	"nerocd/internal/domain"
)

// ListHostingRetainedReleases returns only releases owned by the authorized resource.
func (s *Service) ListHostingRetainedReleases(ctx context.Context, resourceID string) ([]domain.HostingRetainedRelease, error) {
	resource, _, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repository, err := s.hostingExecutor()
	if err != nil {
		return nil, err
	}
	return repository.ListHostingRetainedReleases(ctx, resource.ID)
}

// RequestHostingReleaseCleanup authorizes cleanup by resource and release identity.
// The repository selects the container and refuses every protected route reference.
func (s *Service) RequestHostingReleaseCleanup(ctx context.Context, resourceID, releaseID string, in domain.HostingCleanupInput) (domain.HostingOperation, error) {
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if err = validateDestinationID(releaseID, "release_operation_id"); err != nil {
		return domain.HostingOperation{}, err
	}
	repository, err := s.hostingExecutor()
	if err != nil {
		return domain.HostingOperation{}, err
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.release_cleanup_requested", id, map[string]any{"resource_id": resource.ID, "release_operation_id": releaseID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repository.RequestHostingReleaseCleanup(ctx, id, resource.ID, releaseID, principal.ID, in, audit)
}
