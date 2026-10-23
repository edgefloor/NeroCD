package app

import (
	"context"
	"errors"

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
	view, ok := domain.HostingRouteSwitchOperationView(op, item.ID, item.ProjectID, nil)
	if !ok {
		return domain.HostingProxyOperationView{}, store.ErrConflict
	}
	for _, target := range op.Snapshot.Routes {
		if target.ResourceID != item.ID || target.ProjectID != item.ProjectID {
			continue
		}
		record, err := repository.GetHostingRoute(ctx, target.RouteID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		if record.ProjectID != item.ProjectID || record.Route.ResourceID != item.ID {
			return domain.HostingProxyOperationView{}, store.ErrConflict
		}
		view.Routes = append(view.Routes, record.Route)
	}
	return view, nil
}

func (s *Service) routeSwitchReader(ctx context.Context, resource string) (domain.Resource, string, store.HostingRouteSwitchReadRepository, error) {
	item, principal, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return item, "", nil, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return item, "", nil, err
	}
	reader, ok := repository.(store.HostingRouteSwitchReadRepository)
	if !ok {
		return item, "", nil, hostingjobs.ErrUnavailable
	}
	return item, principal.ID, reader, nil
}

// HostingRouteSwitchOptions explains current choices; POST rechecks all authority.
func (s *Service) HostingRouteSwitchOptions(ctx context.Context, resource, after string) (domain.HostingRouteSwitchOptions, error) {
	item, actor, reader, err := s.routeSwitchReader(ctx, resource)
	if err != nil {
		return domain.HostingRouteSwitchOptions{}, err
	}
	if after != "" {
		if err = validateDestinationID(after, "after_operation_id"); err != nil {
			return domain.HostingRouteSwitchOptions{}, err
		}
	}
	return reader.HostingRouteSwitchOptions(ctx, item.ID, actor, after)
}

// HostingRouteSwitchOperation always returns the resource-safe recovery view.
func (s *Service) HostingRouteSwitchOperation(ctx context.Context, resource, operation string) (domain.HostingProxyOperationView, error) {
	item, actor, reader, err := s.routeSwitchReader(ctx, resource)
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	if operation == "" {
		return domain.HostingProxyOperationView{}, InvalidInput(errors.New("operation_id is required"))
	}
	if err = validateDestinationID(operation, "operation_id"); err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	return reader.HostingRouteSwitchOperation(ctx, item.ID, actor, operation)
}
