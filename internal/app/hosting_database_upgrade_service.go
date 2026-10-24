package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

// RequestHostingDatabaseNetworkUpgrade requests a private, empty-network upgrade.
// The controller derives the complete proof from retained capture metadata.
func (s *Service) RequestHostingDatabaseNetworkUpgrade(ctx context.Context, databaseID string, in domain.HostingDatabaseUpgradeInput) (domain.HostingOperation, error) {
	r, actor, err := s.resourceForDestinationRole(ctx, databaseID, domain.RoleOwner)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if r.Kind != "database" || in.Validate() != nil {
		return domain.HostingOperation{}, InvalidInput(domain.ErrInvalidHostingDatabaseUpgrade)
	}
	repo, ok := s.runs.(store.HostingDatabaseUpgradeRepository)
	if !ok {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	id, err := prefixedID("hop")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	audit, err := s.auditEvent(ctx, actor.ID, "hosting.database_network_upgrade.requested", id, nil)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	return repo.RequestHostingDatabaseNetworkUpgrade(ctx, id, r.ID, actor.ID, in, audit)
}
