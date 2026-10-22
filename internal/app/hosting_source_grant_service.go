package app

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) sourceGrants() (store.HostingSourceGrantRepository, error) {
	r, ok := s.runs.(store.HostingSourceGrantRepository)
	if !ok {
		return nil, store.ErrHostingBuildUnavailable
	}
	return r, nil
}

// RequestHostingSourceGrant submits immutable selection under current owner authority.
func (s *Service) RequestHostingSourceGrant(ctx context.Context, resource string, in domain.HostingSourceGrantRequest) (domain.PublicHostingSourceGrant, error) {
	var zero domain.PublicHostingSourceGrant
	r, p, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if !domain.ValidHostingSourceDeliveryID(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.ExpectedSourceGeneration < 1 || in.SecretID == "" || len(in.SecretID) > 128 || in.SecretVersion < 1 {
		return zero, InvalidInput(errors.New("invalid source grant request"))
	}
	repo, err := s.sourceGrants()
	if err != nil {
		return zero, err
	}
	audit, err := s.auditEvent(ctx, p.ID, "hosting.source_grant.created", domain.HostingSourceGrantID(r.ID, in.RequestID), nil)
	if err != nil {
		return zero, err
	}
	return repo.RequestHostingSourceGrant(ctx, p.ID, r.ID, in, audit)
}

// ListHostingSourceGrants returns bounded sanitized selection metadata to maintainers.
func (s *Service) ListHostingSourceGrants(ctx context.Context, resource string, page store.Page) (store.PageResult[domain.PublicHostingSourceGrant], error) {
	var zero store.PageResult[domain.PublicHostingSourceGrant]
	r, p, err := s.resourceForDestinationRole(ctx, resource, domain.RoleMaintainer)
	if err != nil {
		return zero, err
	}
	repo, err := s.sourceGrants()
	if err != nil {
		return zero, err
	}
	return repo.ListHostingSourceGrants(ctx, p.ID, r.ID, page)
}

// RevokeHostingSourceGrant denies future grant use under current owner authority.
func (s *Service) RevokeHostingSourceGrant(ctx context.Context, resource, id string) error {
	r, p, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return err
	}
	if err = validateDestinationID(id, "grant_id"); err != nil {
		return err
	}
	repo, err := s.sourceGrants()
	if err != nil {
		return err
	}
	audit, err := s.auditEvent(ctx, p.ID, "hosting.source_grant.revoked", id, nil)
	if err != nil {
		return err
	}
	return repo.RevokeResourceHostingSourceGrant(ctx, p.ID, r.ID, id, audit)
}
