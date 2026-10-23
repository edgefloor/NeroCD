package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func serverFromDestinationSQL(v sqlcgen.Server) domain.Server {
	return domain.Server{ID: v.ID, Name: v.Name, RunnerID: v.RunnerID, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt}
}
func destinationFromSQL(v sqlcgen.Destination) domain.Destination {
	return domain.Destination{ID: v.ID, ServerID: v.ServerID, Name: v.Name, RuntimeKind: v.RuntimeKind, CreatedAt: v.CreatedAt}
}
func grantFromDestinationSQL(v sqlcgen.DestinationProjectGrant) domain.DestinationProjectGrant {
	return domain.DestinationProjectGrant{DestinationID: v.DestinationID, ProjectID: v.ProjectID, GrantedBy: v.GrantedBy, CreatedAt: v.CreatedAt, RevokedAt: v.RevokedAt}
}
func bindingFromDestinationSQL(v sqlcgen.ResourceBinding) domain.ResourceBinding {
	return domain.ResourceBinding{ID: v.ID, ResourceID: v.ResourceID, DestinationID: v.DestinationID, RuntimeNamespace: v.RuntimeNamespace, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt}
}
func auditDestinationMutation(ctx context.Context, q *sqlcgen.Queries, event domain.AuditEvent, action, target string, metadata map[string]any) error {
	exists, err := q.DestinationPrincipalExists(ctx, event.ActorID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrConflict
	}
	event, err = destinationAudit(event, action, target, metadata)
	if err != nil {
		return err
	}
	return createAuditWithQueries(ctx, q, event)
}

type serverDestinationPair struct {
	server      domain.Server
	destination domain.Destination
}

// CreateServerWithDestination commits the server, its only destination, and audit together.
// Database admission checks current enrolled runner evidence; it does not alter legacy scheduling.
func (s *PostgresStore) CreateServerWithDestination(ctx context.Context, server domain.Server, destination domain.Destination, event domain.AuditEvent) (domain.Server, domain.Destination, error) {
	if !validServerDestination(server, destination, event) {
		return domain.Server{}, domain.Destination{}, ErrConflict
	}
	result, err := hostingMutation(ctx, s, nil, func(q *sqlcgen.Queries) (serverDestinationPair, error) {
		return createServerDestinationTx(ctx, q, server, destination, event)
	})
	if err != nil {
		return domain.Server{}, domain.Destination{}, err
	}
	return result.server, result.destination, nil
}

func createServerDestinationTx(ctx context.Context, q *sqlcgen.Queries, server domain.Server, destination domain.Destination, event domain.AuditEvent) (serverDestinationPair, error) {
	row, err := q.CreateHostingServer(ctx, sqlcgen.CreateHostingServerParams{ID: server.ID, Name: server.Name, RunnerID: server.RunnerID, CreatedBy: server.CreatedBy, CreatedAt: server.CreatedAt})
	if err != nil {
		return serverDestinationPair{}, err
	}
	dest, err := q.CreateHostingDestination(ctx, sqlcgen.CreateHostingDestinationParams{ID: destination.ID, ServerID: server.ID, Name: destination.Name, RuntimeKind: destination.RuntimeKind, CreatedAt: destination.CreatedAt})
	if err != nil {
		return serverDestinationPair{}, err
	}
	if err = auditDestinationMutation(ctx, q, event, "server.created", server.ID, map[string]any{"runner_id": server.RunnerID, "destination_id": destination.ID}); err != nil {
		return serverDestinationPair{}, err
	}
	return serverDestinationPair{serverFromDestinationSQL(row), destinationFromSQL(dest)}, nil
}

// GetServer retrieves stable server metadata.
func (s *PostgresStore) GetServer(ctx context.Context, id string) (domain.Server, error) {
	row, err := s.queries.GetHostingServer(ctx, id)
	if err != nil {
		return domain.Server{}, hostingError(err)
	}
	return serverFromDestinationSQL(row), nil
}

// GetDestination retrieves an immutable target.
func (s *PostgresStore) GetDestination(ctx context.Context, id string) (domain.Destination, error) {
	row, err := s.queries.GetHostingDestination(ctx, id)
	if err != nil {
		return domain.Destination{}, hostingError(err)
	}
	return destinationFromSQL(row), nil
}

// GetDestinationProjectGrant includes retained revocation state.
func (s *PostgresStore) GetDestinationProjectGrant(ctx context.Context, destination, project string) (domain.DestinationProjectGrant, error) {
	row, err := s.queries.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: destination, ProjectID: project})
	if err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	return grantFromDestinationSQL(row), nil
}

// GetResourceBinding retrieves the resource's immutable placement.
func (s *PostgresStore) GetResourceBinding(ctx context.Context, resource string) (domain.ResourceBinding, error) {
	row, err := s.queries.GetResourceBinding(ctx, resource)
	if err != nil {
		return domain.ResourceBinding{}, hostingError(err)
	}
	return bindingFromDestinationSQL(row), nil
}

// GrantDestinationProject serializes grant creation/reactivation with binding and revocation.
func (s *PostgresStore) GrantDestinationProject(ctx context.Context, grant domain.DestinationProjectGrant, event domain.AuditEvent) (domain.DestinationProjectGrant, error) {
	if grant.ProjectID == "" || grant.GrantedBy == "" || grant.GrantedBy != event.ActorID {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	return hostingMutation(ctx, s, nil, func(q *sqlcgen.Queries) (domain.DestinationProjectGrant, error) {
		return grantDestinationProjectTx(ctx, q, grant, event)
	})
}

func grantDestinationProjectTx(ctx context.Context, q *sqlcgen.Queries, grant domain.DestinationProjectGrant, event domain.AuditEvent) (domain.DestinationProjectGrant, error) {
	if _, err := q.LockHostingDestination(ctx, grant.DestinationID); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if _, err := q.GetActiveDestinationProject(ctx, grant.ProjectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DestinationProjectGrant{}, ErrConflict
		}
		return domain.DestinationProjectGrant{}, err
	}

	row, err := q.GrantDestinationProject(ctx, sqlcgen.GrantDestinationProjectParams{DestinationID: grant.DestinationID, ProjectID: grant.ProjectID, GrantedBy: grant.GrantedBy, CreatedAt: grant.CreatedAt})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if err = auditDestinationMutation(ctx, q, event, "destination.project_granted", grant.DestinationID, map[string]any{"project_id": grant.ProjectID}); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	return grantFromDestinationSQL(row), nil
}

// RevokeDestinationProject keeps all bindings and serializes with new bind admission.
func (s *PostgresStore) RevokeDestinationProject(ctx context.Context, destination, project string, event domain.AuditEvent) (domain.DestinationProjectGrant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if err = q.LockHostingExecutionDestination(ctx, destination); err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	placement, err := q.LockHostingDestination(ctx, destination)
	if err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	old, err := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: destination, ProjectID: project})
	if err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	if old.RevokedAt != nil {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	// Proxy ownership and all route resources precede either grant or desired-route mutation.
	var proxy *domain.HostingProxy
	var held *domain.HostingProxyOperation
	proxyRow, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if err == nil {
		p, e := proxyLock(ctx, tx, proxyRow.ID)
		if e != nil {
			return domain.DestinationProjectGrant{}, hostingError(e)
		}
		rows, e := q.ProxyListRoutes(ctx, p.ID)
		if e != nil {
			return domain.DestinationProjectGrant{}, hostingError(e)
		}
		if e = proxyLockRouteResources(ctx, tx, rows); e != nil {
			return domain.DestinationProjectGrant{}, hostingError(e)
		}
		p, held, e = proxyPrepareRevocation(ctx, tx, p)
		if e != nil {
			return domain.DestinationProjectGrant{}, hostingError(e)
		}
		proxy = &p
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	row, err := q.RevokeDestinationProject(ctx, sqlcgen.RevokeDestinationProjectParams{DestinationID: destination, ProjectID: project, RevokedAt: &event.CreatedAt})
	if err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	if proxy != nil {
		if err = s.proxyWithdrawDestinationProject(ctx, tx, *proxy, held, project, event); err != nil {
			return domain.DestinationProjectGrant{}, hostingError(err)
		}
	}
	if err = auditDestinationMutation(ctx, q, event, "destination.project_revoked", destination, map[string]any{"project_id": project}); err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.DestinationProjectGrant{}, hostingError(err)
	}
	return grantFromDestinationSQL(row), nil
}

func (s *PostgresStore) proxyWithdrawDestinationProject(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, held *domain.HostingProxyOperation, project string, event domain.AuditEvent) error {
	q := sqlcgen.New(tx)
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return err
	}
	changed := false
	for _, r := range rows {
		if r.ProjectID != project || !r.Enabled {
			continue
		}
		count, err := q.ProxyDeleteRoute(ctx, sqlcgen.ProxyDeleteRouteParams{ID: r.ID, Version: r.Version})
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		changed = true
	}
	if !changed {
		return nil
	}
	digest := strings.TrimPrefix(domain.HostingDocumentHash(event.ID), "sha256:")
	withdrawalAudit := event
	withdrawalAudit.ID = "aud_destination_withdraw_" + digest[:24]
	_, err = s.proxyQueueRevocation(ctx, tx, p, held, "pop_destination_withdraw_"+digest[:24], event.ActorID, "destination-withdraw:"+digest, domain.HostingDocumentHash(struct{ Destination, Project, Event string }{p.DestinationID, project, event.ID}), withdrawalAudit)
	return err
}

// BindResourceDestination derives project from persisted resource scope and generates its namespace internally.
func (s *PostgresStore) BindResourceDestination(ctx context.Context, in ResourceBindingInput, event domain.AuditEvent) (domain.ResourceBinding, error) {
	if in.ID == "" || in.CreatedBy == "" || in.CreatedBy != event.ActorID {
		return domain.ResourceBinding{}, ErrConflict
	}
	namespace, err := newLeaseToken("ncd")
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	return hostingMutation(ctx, s, nil, func(q *sqlcgen.Queries) (domain.ResourceBinding, error) {
		if _, err := q.LockHostingDestination(ctx, in.DestinationID); err != nil {
			return domain.ResourceBinding{}, err
		}
		project, err := q.GetBindingResourceProject(ctx, in.ResourceID)
		if err != nil {
			return domain.ResourceBinding{}, err
		}
		row, err := q.CreateResourceBinding(ctx, sqlcgen.CreateResourceBindingParams{ID: in.ID, ResourceID: in.ResourceID, DestinationID: in.DestinationID, RuntimeNamespace: namespace, CreatedBy: in.CreatedBy, CreatedAt: in.CreatedAt})
		if err != nil {
			return domain.ResourceBinding{}, err
		}
		if err = auditDestinationMutation(ctx, q, event, "resource.destination_bound", in.ResourceID, map[string]any{"project_id": project, "destination_id": in.DestinationID, "binding_id": in.ID}); err != nil {
			return domain.ResourceBinding{}, err
		}
		return bindingFromDestinationSQL(row), nil
	})
}

// ResolveResourceDestination returns one consistent snapshot of exact IDs and current evidence.
// A revoked grant or inactive runner is returned explicitly, never replaced by a matching tag.
func (s *PostgresStore) ResolveResourceDestination(ctx context.Context, resource string) (domain.ResourceDestination, error) {
	var result domain.ResourceDestination
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, hostingError(err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	binding, err := q.GetResourceBinding(ctx, resource)
	if err != nil {
		return result, hostingError(err)
	}
	result.Binding = bindingFromDestinationSQL(binding)
	destination, err := q.GetHostingDestination(ctx, binding.DestinationID)
	if err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	result.Destination = destinationFromSQL(destination)
	server, err := q.GetHostingServer(ctx, destination.ServerID)
	if err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	result.Server = serverFromDestinationSQL(server)
	runner, err := q.GetRunnerByID(ctx, server.RunnerID)
	if err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	result.Runner = runnerFromSQLC(runner)
	project, err := q.GetBindingResourceProject(ctx, resource)
	if err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	grant, err := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: destination.ID, ProjectID: project})
	if err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	result.Grant = grantFromDestinationSQL(grant)
	if err = tx.Commit(ctx); err != nil {
		return domain.ResourceDestination{}, hostingError(err)
	}
	return result, nil
}

// ListServers returns a bounded stable page.
func (s *PostgresStore) ListServers(ctx context.Context, page Page) (PageResult[domain.Server], error) {
	total, err := s.queries.CountHostingServers(ctx)
	if err != nil {
		return PageResult[domain.Server]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListHostingServers(ctx, sqlcgen.ListHostingServersParams{PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.Server]{}, hostingError(err)
	}
	items := make([]domain.Server, 0, len(rows))
	for _, row := range rows {
		items = append(items, serverFromDestinationSQL(row))
	}
	return PageResult[domain.Server]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// ListDestinations returns a bounded stable page.
func (s *PostgresStore) ListDestinations(ctx context.Context, server string, page Page) (PageResult[domain.Destination], error) {
	total, err := s.queries.CountHostingDestinations(ctx, server)
	if err != nil {
		return PageResult[domain.Destination]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListHostingDestinations(ctx, sqlcgen.ListHostingDestinationsParams{ServerID: server, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.Destination]{}, hostingError(err)
	}
	items := make([]domain.Destination, 0, len(rows))
	for _, row := range rows {
		items = append(items, destinationFromSQL(row))
	}
	return PageResult[domain.Destination]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// ListDestinationProjectGrants returns a bounded stable page.
func (s *PostgresStore) ListDestinationProjectGrants(ctx context.Context, destination string, page Page) (PageResult[domain.DestinationProjectGrant], error) {
	total, err := s.queries.CountDestinationProjectGrants(ctx, destination)
	if err != nil {
		return PageResult[domain.DestinationProjectGrant]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListDestinationProjectGrants(ctx, sqlcgen.ListDestinationProjectGrantsParams{DestinationID: destination, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.DestinationProjectGrant]{}, hostingError(err)
	}
	items := make([]domain.DestinationProjectGrant, 0, len(rows))
	for _, row := range rows {
		items = append(items, grantFromDestinationSQL(row))
	}
	return PageResult[domain.DestinationProjectGrant]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// ListResourceBindings returns a bounded stable page.
func (s *PostgresStore) ListResourceBindings(ctx context.Context, destination string, page Page) (PageResult[domain.ResourceBinding], error) {
	total, err := s.queries.CountResourceBindings(ctx, destination)
	if err != nil {
		return PageResult[domain.ResourceBinding]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListResourceBindings(ctx, sqlcgen.ListResourceBindingsParams{DestinationID: destination, PageLimit: int32(page.Limit), PageOffset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.ResourceBinding]{}, hostingError(err)
	}
	items := make([]domain.ResourceBinding, 0, len(rows))
	for _, row := range rows {
		items = append(items, bindingFromDestinationSQL(row))
	}
	return PageResult[domain.ResourceBinding]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}

// ListProjectDestinations returns a bounded stable page.
func (s *PostgresStore) ListProjectDestinations(ctx context.Context, project string, page Page) (PageResult[domain.Destination], error) {
	total, err := s.queries.CountProjectDestinations(ctx, project)
	if err != nil {
		return PageResult[domain.Destination]{}, hostingError(err)
	}
	page = hostingPage(page, int(total))
	rows, err := s.queries.ListProjectDestinations(ctx, sqlcgen.ListProjectDestinationsParams{ProjectID: project, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.Destination]{}, hostingError(err)
	}
	items := make([]domain.Destination, 0, len(rows))
	for _, row := range rows {
		items = append(items, destinationFromSQL(row))
	}
	return PageResult[domain.Destination]{Items: items, Total: int(total), Limit: page.Limit, Offset: page.Offset}, nil
}
