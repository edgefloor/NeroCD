package app

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func (s *Service) proxyRepository() (store.HostingProxyRepository, error) {
	repository, ok := s.runs.(store.HostingProxyRepository)
	if !ok {
		return nil, hostingjobs.ErrUnavailable
	}
	return repository, nil
}
func validateProxyKey(key string) error {
	if key == "" || len(key) > 128 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return InvalidInput(errors.New("a bounded idempotency_key is required"))
	}
	return nil
}
func (s *Service) proxyAdmin(ctx context.Context, server string, mutation bool) (store.HostingProxyRepository, domain.HostingProxy, auth.Principal, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return nil, domain.HostingProxy{}, principal, err
	}
	if err = validateDestinationID(server, "server_id"); err != nil {
		return nil, domain.HostingProxy{}, principal, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, domain.HostingProxy{}, principal, err
	}
	p, err := repository.GetHostingProxyByServer(ctx, server)
	if err != nil {
		return nil, p, principal, err
	}
	if mutation {
		if _, err = s.authorizeHostingProject(ctx, p.ControlProjectID, domain.RoleOwner); err != nil {
			return nil, p, principal, err
		}
	}
	return repository, p, principal, nil
}

// ConfigureHostingProxy requires server administration and explicit ownership
// of the existing immutable control project. It never accepts local host paths.
func (s *Service) ConfigureHostingProxy(ctx context.Context, server string, in domain.HostingProxyInput) (domain.HostingProxy, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return domain.HostingProxy{}, err
	}
	if err = validateDestinationID(server, "server_id"); err != nil {
		return domain.HostingProxy{}, err
	}
	if _, err = s.authorizeHostingProject(ctx, in.ControlProjectID, domain.RoleOwner); err != nil {
		return domain.HostingProxy{}, err
	}
	if err = validateProxyKey(in.IdempotencyKey); err != nil {
		return domain.HostingProxy{}, err
	}
	if in.Configuration.GeneratedDomain != "" {
		in.Configuration.GeneratedDomain, err = domain.NormalizeHostingHostname(in.Configuration.GeneratedDomain)
		if err != nil {
			return domain.HostingProxy{}, InvalidInput(err)
		}
	}
	if err = domain.ValidateHostingProxyConfiguration(in.Configuration); err != nil {
		return domain.HostingProxy{}, InvalidInput(err)
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingProxy{}, err
	}
	id, err := prefixedID("proxy")
	if err != nil {
		return domain.HostingProxy{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.proxy_configured", id, nil)
	if err != nil {
		return domain.HostingProxy{}, err
	}
	return repository.ConfigureHostingProxy(ctx, id, server, principal.ID, in, audit)
}

// HostingProxy returns server-administrator detail without operator filesystem settings.
func (s *Service) HostingProxy(ctx context.Context, server string) (domain.HostingProxy, error) {
	_, p, _, err := s.proxyAdmin(ctx, server, false)
	return p, err
}

// DisableHostingProxy queues an empty generation; owned proxy state and certificates persist.
func (s *Service) DisableHostingProxy(ctx context.Context, server string, in domain.HostingProxyMutationInput) (domain.HostingProxy, error) {
	repository, p, principal, err := s.proxyAdmin(ctx, server, true)
	if err != nil {
		return p, err
	}
	if err = validateProxyKey(in.IdempotencyKey); err != nil {
		return p, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.proxy_disabled", p.ID, nil)
	if err != nil {
		return p, err
	}
	return repository.SetHostingProxyEnabled(ctx, p.ID, principal.ID, in.IdempotencyKey, false, audit)
}

// HostingHostnameGrants lists explicit administrator delegations for one proxy.
func (s *Service) HostingHostnameGrants(ctx context.Context, server string) ([]domain.HostingHostnameGrant, error) {
	repository, p, _, err := s.proxyAdmin(ctx, server, false)
	if err != nil {
		return nil, err
	}
	return repository.ListHostingHostnameGrants(ctx, p.ID)
}

// GrantHostingHostname delegates an exact hostname or an explicit descendant namespace.
func (s *Service) GrantHostingHostname(ctx context.Context, server string, in domain.HostingHostnameGrantInput) (domain.HostingHostnameGrant, error) {
	repository, p, principal, err := s.proxyAdmin(ctx, server, true)
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	if err = s.destinationProjectExists(ctx, in.ProjectID); err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	in.Hostname, err = domain.NormalizeHostingHostname(in.Hostname)
	if err != nil {
		return domain.HostingHostnameGrant{}, InvalidInput(err)
	}
	id, err := prefixedID("hostgrant")
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.proxy_hostname_granted", id, nil)
	if err != nil {
		return domain.HostingHostnameGrant{}, err
	}
	return repository.GrantHostingHostname(ctx, p.ID, id, principal.ID, in, audit)
}

// RevokeHostingHostname queues withdrawal while retaining admitted uncertainty.
func (s *Service) RevokeHostingHostname(ctx context.Context, server, grant string, in domain.HostingProxyMutationInput) (domain.HostingProxy, error) {
	repository, p, principal, err := s.proxyAdmin(ctx, server, true)
	if err != nil {
		return p, err
	}
	if err = validateDestinationID(grant, "grant_id"); err != nil {
		return p, err
	}
	if err = validateProxyKey(in.IdempotencyKey); err != nil {
		return p, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.proxy_hostname_revoked", grant, nil)
	if err != nil {
		return p, err
	}
	return repository.RevokeHostingHostname(ctx, p.ID, grant, principal.ID, in.IdempotencyKey, audit)
}

func normalizeProxyRoute(in domain.HostingRouteInput) (domain.HostingRouteInput, error) {
	if err := validateProxyKey(in.IdempotencyKey); err != nil {
		return in, err
	}
	if in.ExpectedRouteVersion < 0 {
		return in, InvalidInput(errors.New("expected_route_version must not be negative"))
	}
	if err := validateDestinationID(in.ExpectedHealthyOperationID, "expected_healthy_operation_id"); err != nil {
		return in, err
	}
	if err := domain.ValidateHostingRoutePath(in.PathPrefix); err != nil {
		return in, InvalidInput(err)
	}
	if in.GenerateHostname {
		if in.Hostname != "" {
			return in, InvalidInput(errors.New("generated hostname requests must omit hostname"))
		}
	} else {
		host, err := domain.NormalizeHostingHostname(in.Hostname)
		if err != nil {
			return in, InvalidInput(err)
		}
		in.Hostname = host
	}
	return in, nil
}

// HostingRoutes returns only routes for a viewer-authorized persisted resource.
func (s *Service) HostingRoutes(ctx context.Context, resource string) ([]domain.HostingRoute, error) {
	item, _, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListHostingRoutes(ctx, item.ID)
}

// RequestHostingRoute selects the caller's current settled healthy resource release.
func (s *Service) RequestHostingRoute(ctx context.Context, resource string, in domain.HostingRouteInput) (domain.HostingRoute, error) {
	item, principal, err := s.resourceForDestinationRole(ctx, resource, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if in.ExpectedRouteVersion != 0 {
		return domain.HostingRoute{}, InvalidInput(errors.New("new routes require expected_route_version zero"))
	}
	in, err = normalizeProxyRoute(in)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingRoute{}, err
	}
	id, err := prefixedID("pop")
	if err != nil {
		return domain.HostingRoute{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.route_requested", id, nil)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	return repository.RequestHostingRoute(ctx, item.ID, "", id, principal.ID, in, audit)
}

func (s *Service) proxyRouteForRole(ctx context.Context, id, role string) (store.HostingProxyRepository, store.HostingProxyRouteRecord, auth.Principal, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, store.HostingProxyRouteRecord{}, principal, err
	}
	if err = validateDestinationID(id, "route_id"); err != nil {
		return nil, store.HostingProxyRouteRecord{}, principal, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, store.HostingProxyRouteRecord{}, principal, err
	}
	row, err := repository.GetHostingRoute(ctx, id)
	if err != nil {
		return nil, row, principal, err
	}
	if _, err = s.authorizeHostingProject(ctx, row.ProjectID, role); err != nil {
		return nil, store.HostingProxyRouteRecord{}, principal, err
	}
	return repository, row, principal, nil
}

// HostingRoute returns a route after checking its persisted project scope.
func (s *Service) HostingRoute(ctx context.Context, id string) (domain.HostingRoute, error) {
	_, row, _, err := s.proxyRouteForRole(ctx, id, domain.RoleViewer)
	return row.Route, err
}

// UpdateHostingRoute advances one expected route version and selects a healthy release explicitly.
func (s *Service) UpdateHostingRoute(ctx context.Context, id string, in domain.HostingRouteInput) (domain.HostingRoute, error) {
	repository, row, principal, err := s.proxyRouteForRole(ctx, id, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if in.ExpectedRouteVersion < 1 {
		return domain.HostingRoute{}, InvalidInput(errors.New("expected_route_version is required"))
	}
	in, err = normalizeProxyRoute(in)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	operation, err := prefixedID("pop")
	if err != nil {
		return domain.HostingRoute{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.route_changed", id, nil)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	return repository.RequestHostingRoute(ctx, row.Route.ResourceID, id, operation, principal.ID, in, audit)
}

// DeleteHostingRoute queues withdrawal without deleting immutable release references.
func (s *Service) DeleteHostingRoute(ctx context.Context, id string, in domain.HostingRouteDeletionInput) (domain.HostingRoute, error) {
	repository, _, principal, err := s.proxyRouteForRole(ctx, id, domain.RoleMaintainer)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	if in.ExpectedRouteVersion < 1 {
		return domain.HostingRoute{}, InvalidInput(errors.New("expected_route_version is required"))
	}
	if err = validateProxyKey(in.IdempotencyKey); err != nil {
		return domain.HostingRoute{}, err
	}
	operation, err := prefixedID("pop")
	if err != nil {
		return domain.HostingRoute{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.route_withdrawn", id, nil)
	if err != nil {
		return domain.HostingRoute{}, err
	}
	return repository.DeleteHostingRoute(ctx, id, operation, principal.ID, in, audit)
}

// ProxyOperation returns administrator detail or a project-sanitized route-only view.
func (s *Service) ProxyOperation(ctx context.Context, id string) (any, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateDestinationID(id, "operation_id"); err != nil {
		return nil, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, err
	}
	op, err := repository.GetHostingProxyOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	if isRunnerAdmin(principal) {
		return op, nil
	}
	return s.proxyProjectOperationView(ctx, op)
}

func (s *Service) proxyProjectOperationView(ctx context.Context, op domain.HostingProxyOperation) (domain.HostingProxyOperationView, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingProxyOperationView{}, err
	}
	view := domain.HostingProxyOperationView{ID: op.ID, State: op.State, MutationHold: op.MutationHold, RestoreOwnershipHeld: op.RestoreOwnershipHeld, MetadataConflict: op.MetadataConflict, Routes: make([]domain.HostingRoute, 0)}
	allowed := map[string]bool{}
	checked := map[string]bool{}
	check := func(project string) (bool, error) {
		if checked[project] {
			return allowed[project], nil
		}
		checked[project] = true
		_, err := s.authorizeHostingProject(ctx, project, domain.RoleViewer)
		if errors.Is(err, auth.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		allowed[project] = true
		return true, nil
	}
	for _, target := range op.Snapshot.Routes {
		ok, err := check(target.ProjectID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		if !ok {
			continue
		}
		record, err := repository.GetHostingRoute(ctx, target.RouteID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		if record.ProjectID != target.ProjectID {
			return domain.HostingProxyOperationView{}, store.ErrConflict
		}
		view.Routes = append(view.Routes, record.Route)
	}
	if op.RouteID != "" {
		record, err := repository.GetHostingRoute(ctx, op.RouteID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		ok, err := check(record.ProjectID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		if ok {
			found := false
			for _, route := range view.Routes {
				found = found || route.ID == record.Route.ID
			}
			if !found {
				view.Routes = append(view.Routes, record.Route)
			}
		}
	}
	if len(view.Routes) == 0 {
		ok, err := check(op.ProjectID)
		if err != nil {
			return domain.HostingProxyOperationView{}, err
		}
		if !ok {
			return domain.HostingProxyOperationView{}, auth.ErrForbidden
		}
	}
	return view, nil
}

// CancelProxyOperation requires server administration and control-project ownership.
func (s *Service) CancelProxyOperation(ctx context.Context, id string) (domain.HostingProxyOperation, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	op, err := repository.GetHostingProxyOperation(ctx, id)
	if err != nil {
		return op, err
	}
	p, err := repository.GetHostingProxy(ctx, op.ProxyID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if _, err = s.authorizeHostingProject(ctx, p.ControlProjectID, domain.RoleOwner); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.proxy_cancel_requested", id, nil)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return repository.CancelHostingProxyOperation(ctx, id, principal.ID, audit)
}

func (s *Service) withProxyRunner(ctx context.Context, call func(store.HostingProxyRepository, string) (domain.HostingProxyOperation, error)) (domain.HostingProxyOperation, error) {
	identity, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	repository, err := s.proxyRepository()
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return call(repository, identity.RunnerID)
}

// RunnerProxyPlan returns typed input for one exact live runner attempt.
func (s *Service) RunnerProxyPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.withProxyRunner(ctx, func(repo store.HostingProxyRepository, runner string) (domain.HostingProxyOperation, error) {
		return repo.RunnerProxyPlan(ctx, runner, in)
	})
}

// RunnerProxyPrepared commits or replays exact local preparation evidence.
func (s *Service) RunnerProxyPrepared(ctx context.Context, in domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error) {
	return s.withProxyRunner(ctx, func(repo store.HostingProxyRepository, runner string) (domain.HostingProxyOperation, error) {
		return repo.RunnerProxyPrepared(ctx, runner, in)
	})
}

// RunnerProxyApplyBegin obtains durable proxy ownership before external effects.
func (s *Service) RunnerProxyApplyBegin(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.withProxyRunner(ctx, func(repo store.HostingProxyRepository, runner string) (domain.HostingProxyOperation, error) {
		return repo.RunnerProxyApplyBegin(ctx, runner, in)
	})
}

// RunnerProxyComplete settles only live first reports or exact committed replay.
func (s *Service) RunnerProxyComplete(ctx context.Context, in domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error) {
	return s.withProxyRunner(ctx, func(repo store.HostingProxyRepository, runner string) (domain.HostingProxyOperation, error) {
		return repo.RunnerProxyComplete(ctx, runner, in)
	})
}

// RunnerProxyStatus reads exact attempt identity without restoring mutation authority.
func (s *Service) RunnerProxyStatus(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.withProxyRunner(ctx, func(repo store.HostingProxyRepository, runner string) (domain.HostingProxyOperation, error) {
		return repo.RunnerProxyStatus(ctx, runner, in)
	})
}

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
