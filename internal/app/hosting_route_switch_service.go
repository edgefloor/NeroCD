package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

// RequestHostingRouteSwitch authorizes a bounded selection and returns project-safe status.
func (s *Service) RequestHostingRouteSwitch(ctx context.Context, resource string, in domain.HostingRouteSwitchInput) (domain.HostingProxyOperationView, error) {
	item, principal, err := s.resourceForDestinationRole(ctx, resource, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	if err = validateProxyKey(in.IdempotencyKey); err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	in, err = domain.CanonicalHostingRouteSwitchInput(in)
	if err != nil {
		return domain.HostingProxyOperationView{}, InvalidInput(err)
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	switches, ok := repository.(store.HostingRouteSwitchRepository)
	if !ok {
		return domain.HostingProxyOperationView{}, hostingjobs.ErrUnavailable
	}
	id, err := prefixedID("pop")
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.route_switch_requested", id, nil)
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	op, err := switches.RequestHostingRouteSwitch(ctx, item.ID, id, principal.ID, in, audit)
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	return s.proxyProjectOperationView(ctx, op)
}
