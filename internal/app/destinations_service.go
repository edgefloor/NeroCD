package app

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// ServerInput associates one eligible enrolled runner with a stable server.
type ServerInput struct {
	Name     string `json:"name"`
	RunnerID string `json:"runner_id"`
}

// ServerDetail pairs a server with its immutable default destination.
type ServerDetail struct {
	Server      domain.Server      `json:"server"`
	Destination domain.Destination `json:"destination"`
}

// DestinationProjectInput selects a project for an explicit grant or revocation.
type DestinationProjectInput struct {
	ProjectID string `json:"project_id"`
}

// ResourceBindingInput selects desired placement without accepting project scope or a namespace.
type ResourceBindingInput struct {
	DestinationID string `json:"destination_id"`
}

var destinationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateDestinationID(value, field string) error {
	if !destinationIDPattern.MatchString(value) {
		return InvalidInput(errors.New(field + " must contain 1 to 128 letters, digits, underscores, or hyphens"))
	}
	return nil
}
func (s *Service) requireDestinationAdmin(ctx context.Context) (auth.Principal, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	if !isRunnerAdmin(principal) {
		return auth.Principal{}, auth.ErrForbidden
	}
	return principal, nil
}
func (s *Service) destinationProjectExists(ctx context.Context, id string) error {
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project.ID == id && project.ArchivedAt == nil {
			return nil
		}
	}
	return store.ErrNotFound
}

// CreateServer atomically admits an enrolled Compose runner, default destination, and audit.
func (s *Service) CreateServer(ctx context.Context, in ServerInput) (ServerDetail, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return ServerDetail{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.RunnerID = strings.TrimSpace(in.RunnerID)
	if err = domain.ValidateHostingName(in.Name); err != nil {
		return ServerDetail{}, InvalidInput(err)
	}
	if err = validateDestinationID(in.RunnerID, "runner_id"); err != nil {
		return ServerDetail{}, err
	}
	// Missing identity is a lookup error. Eligibility remains atomic in the repository.
	if _, err = s.runners.GetRunnerByID(ctx, in.RunnerID); err != nil {
		return ServerDetail{}, err
	}
	serverID, err := prefixedID("server")
	if err != nil {
		return ServerDetail{}, err
	}
	destinationID, err := prefixedID("dest")
	if err != nil {
		return ServerDetail{}, err
	}
	now := s.clock().UTC()
	server := domain.Server{ID: serverID, Name: in.Name, RunnerID: in.RunnerID, CreatedBy: principal.ID, CreatedAt: now}
	destination := domain.Destination{ID: destinationID, ServerID: serverID, Name: "default", RuntimeKind: "docker_compose", CreatedAt: now}
	event, err := s.auditEvent(ctx, principal.ID, "server.created", serverID, map[string]any{"runner_id": in.RunnerID, "destination_id": destinationID})
	if err != nil {
		return ServerDetail{}, err
	}
	created, target, err := s.destinations.CreateServerWithDestination(ctx, server, destination, event)
	if err != nil {
		return ServerDetail{}, err
	}
	return ServerDetail{Server: created, Destination: target}, nil
}

// ListServers returns global server metadata only to runner administrators.
func (s *Service) ListServers(ctx context.Context, page store.Page) (store.PageResult[domain.Server], error) {
	if _, err := s.requireDestinationAdmin(ctx); err != nil {
		return store.PageResult[domain.Server]{}, err
	}
	return s.destinations.ListServers(ctx, page)
}

// GetServer returns the stable server and default destination without runtime credentials.
func (s *Service) GetServer(ctx context.Context, id string) (ServerDetail, error) {
	if _, err := s.requireDestinationAdmin(ctx); err != nil {
		return ServerDetail{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateDestinationID(id, "server_id"); err != nil {
		return ServerDetail{}, err
	}
	server, err := s.destinations.GetServer(ctx, id)
	if err != nil {
		return ServerDetail{}, err
	}
	destinations, err := s.destinations.ListDestinations(ctx, id, store.Page{Enabled: true, LimitSpecified: true, Limit: 1})
	if err != nil {
		return ServerDetail{}, err
	}
	if len(destinations.Items) != 1 {
		return ServerDetail{}, store.ErrNotFound
	}
	return ServerDetail{Server: server, Destination: destinations.Items[0]}, nil
}

// ListDestinations requires exactly one project scope or administrative server scope.
func (s *Service) ListDestinations(ctx context.Context, projectID, serverID string, page store.Page) (store.PageResult[domain.Destination], error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return store.PageResult[domain.Destination]{}, err
	}
	projectID = strings.TrimSpace(projectID)
	serverID = strings.TrimSpace(serverID)
	if (projectID == "") == (serverID == "") {
		return store.PageResult[domain.Destination]{}, InvalidInput(errors.New("exactly one of project_id or server_id is required"))
	}
	if projectID != "" {
		if err := validateDestinationID(projectID, "project_id"); err != nil {
			return store.PageResult[domain.Destination]{}, err
		}
		if _, err := s.authorizeHostingProject(ctx, projectID, domain.RoleViewer); err != nil {
			return store.PageResult[domain.Destination]{}, err
		}
		return s.destinations.ListProjectDestinations(ctx, projectID, page)
	}
	if _, err := s.requireDestinationAdmin(ctx); err != nil {
		return store.PageResult[domain.Destination]{}, err
	}
	if err := validateDestinationID(serverID, "server_id"); err != nil {
		return store.PageResult[domain.Destination]{}, err
	}
	if _, err := s.destinations.GetServer(ctx, serverID); err != nil {
		return store.PageResult[domain.Destination]{}, err
	}
	return s.destinations.ListDestinations(ctx, serverID, page)
}

// ListDestinationProjectGrants includes retained revocations for global administrators.
func (s *Service) ListDestinationProjectGrants(ctx context.Context, id string, page store.Page) (store.PageResult[domain.DestinationProjectGrant], error) {
	if _, err := s.requireDestinationAdmin(ctx); err != nil {
		return store.PageResult[domain.DestinationProjectGrant]{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateDestinationID(id, "destination_id"); err != nil {
		return store.PageResult[domain.DestinationProjectGrant]{}, err
	}
	if _, err := s.destinations.GetDestination(ctx, id); err != nil {
		return store.PageResult[domain.DestinationProjectGrant]{}, err
	}
	return s.destinations.ListDestinationProjectGrants(ctx, id, page)
}

// GrantDestinationProject explicitly permits new bindings for an active project.
func (s *Service) GrantDestinationProject(ctx context.Context, id string, in DestinationProjectInput) (domain.DestinationProjectGrant, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	id = strings.TrimSpace(id)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if err = validateDestinationID(id, "destination_id"); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if err = validateDestinationID(in.ProjectID, "project_id"); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if _, err = s.destinations.GetDestination(ctx, id); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if err = s.destinationProjectExists(ctx, in.ProjectID); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	event, err := s.auditEvent(ctx, principal.ID, "destination.project_granted", id, map[string]any{"project_id": in.ProjectID})
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	grant := domain.DestinationProjectGrant{DestinationID: id, ProjectID: in.ProjectID, GrantedBy: principal.ID, CreatedAt: s.clock().UTC()}
	return s.destinations.GrantDestinationProject(ctx, grant, event)
}

// RevokeDestinationProject retains bindings and does not stop or remove workloads.
func (s *Service) RevokeDestinationProject(ctx context.Context, id string, in DestinationProjectInput) (domain.DestinationProjectGrant, error) {
	principal, err := s.requireDestinationAdmin(ctx)
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	id = strings.TrimSpace(id)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if err = validateDestinationID(id, "destination_id"); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if err = validateDestinationID(in.ProjectID, "project_id"); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	if _, err = s.destinations.GetDestination(ctx, id); err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	event, err := s.auditEvent(ctx, principal.ID, "destination.project_revoked", id, map[string]any{"project_id": in.ProjectID})
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	return s.destinations.RevokeDestinationProject(ctx, id, in.ProjectID, event)
}
func (s *Service) resourceForDestinationRole(ctx context.Context, id, role string) (domain.Resource, auth.Principal, error) {
	if _, err := s.CurrentPrincipal(ctx); err != nil {
		return domain.Resource{}, auth.Principal{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateDestinationID(id, "resource_id"); err != nil {
		return domain.Resource{}, auth.Principal{}, err
	}
	item, err := s.hosting.GetResource(ctx, id)
	if err != nil {
		return domain.Resource{}, auth.Principal{}, err
	}
	principal, err := s.authorizeHostingProject(ctx, item.ProjectID, role)
	return item, principal, err
}

// GetResourceBinding authorizes the resource's persisted project, including retained revoked placements.
func (s *Service) GetResourceBinding(ctx context.Context, id string) (domain.ResourceBinding, error) {
	item, _, err := s.resourceForDestinationRole(ctx, id, domain.RoleViewer)
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	return s.destinations.GetResourceBinding(ctx, item.ID)
}

// BindResourceDestination creates one immutable desired placement under the current project grant.
func (s *Service) BindResourceDestination(ctx context.Context, id string, in ResourceBindingInput) (domain.ResourceBinding, error) {
	item, principal, err := s.resourceForDestinationRole(ctx, id, domain.RoleMaintainer)
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	in.DestinationID = strings.TrimSpace(in.DestinationID)
	if err = validateDestinationID(in.DestinationID, "destination_id"); err != nil {
		return domain.ResourceBinding{}, err
	}
	if _, err = s.destinations.GetDestination(ctx, in.DestinationID); err != nil {
		return domain.ResourceBinding{}, err
	}
	grant, err := s.destinations.GetDestinationProjectGrant(ctx, in.DestinationID, item.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.ResourceBinding{}, auth.ErrForbidden
	}
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	if grant.RevokedAt != nil {
		return domain.ResourceBinding{}, auth.ErrForbidden
	}
	bindingID, err := prefixedID("binding")
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	event, err := s.auditEvent(ctx, principal.ID, "resource.destination_bound", item.ID, map[string]any{"project_id": item.ProjectID, "destination_id": in.DestinationID, "binding_id": bindingID})
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	return s.destinations.BindResourceDestination(ctx, store.ResourceBindingInput{ID: bindingID, ResourceID: item.ID, DestinationID: in.DestinationID, CreatedBy: principal.ID, CreatedAt: s.clock().UTC()}, event)
}
