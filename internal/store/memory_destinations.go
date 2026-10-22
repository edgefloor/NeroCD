package store

import (
	"context"
	"nerocd/internal/domain"
	"sort"
)

func (s *MemoryStore) destinationIndexLocked(id string) int {
	for i, v := range s.destinations {
		if v.ID == id {
			return i
		}
	}
	return -1
}
func (s *MemoryStore) destinationGrantIndexLocked(destination, project string) int {
	for i, v := range s.destinationGrants {
		if v.DestinationID == destination && v.ProjectID == project {
			return i
		}
	}
	return -1
}
func (s *MemoryStore) destinationAuditAvailableLocked(event domain.AuditEvent) bool {
	return event.ID != "" && s.auditIDAvailableLocked(event.ID) && s.hostingPrincipalExistsLocked(event.ActorID)
}
func (s *MemoryStore) eligibleDestinationRunnerLocked(id string) bool {
	eligible := false
	for _, v := range s.runners {
		if v.ID == id {
			eligible = v.Status == domain.RunnerActive && v.TokenHash != "" && contains(v.Capabilities, "compose_deploy") && !contains(v.Capabilities, "shell")
			break
		}
	}
	if !eligible {
		return false
	}
	for _, v := range s.runnerEnrollments {
		if v.RunnerID == id && v.UsedAt != nil && v.RevokedAt == nil {
			return true
		}
	}
	return false
}

// CreateServerWithDestination commits stable association and audit after enrolled Compose-runner admission.
// The unique association does not change legacy tag scheduling or prove physical runtime continuity.
func (s *MemoryStore) CreateServerWithDestination(_ context.Context, server domain.Server, destination domain.Destination, event domain.AuditEvent) (domain.Server, domain.Destination, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validServerDestination(server, destination, event) || !s.eligibleDestinationRunnerLocked(server.RunnerID) || !s.destinationAuditAvailableLocked(event) || !s.hostingPrincipalExistsLocked(server.CreatedBy) {
		return domain.Server{}, domain.Destination{}, ErrConflict
	}
	for _, old := range s.servers {
		if old.ID == server.ID || old.RunnerID == server.RunnerID {
			return domain.Server{}, domain.Destination{}, ErrConflict
		}
	}
	if s.destinationIndexLocked(destination.ID) >= 0 {
		return domain.Server{}, domain.Destination{}, ErrConflict
	}
	event, err := destinationAudit(event, "server.created", server.ID, map[string]any{"runner_id": server.RunnerID, "destination_id": destination.ID})
	if err != nil {
		return domain.Server{}, domain.Destination{}, err
	}
	s.servers = append(s.servers, server)
	s.destinations = append(s.destinations, destination)
	s.appendHostingAuditLocked(&event)
	return server, destination, nil
}

// GetServer returns one stable server identity.
func (s *MemoryStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.servers {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.Server{}, ErrNotFound
}

// ListServers returns a bounded stable page.
func (s *MemoryStore) ListServers(_ context.Context, page Page) (PageResult[domain.Server], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := append([]domain.Server{}, s.servers...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return paginateSlice(items, page), nil
}

// GetDestination returns one immutable runtime target.
func (s *MemoryStore) GetDestination(_ context.Context, id string) (domain.Destination, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.destinationIndexLocked(id)
	if i < 0 {
		return domain.Destination{}, ErrNotFound
	}
	return s.destinations[i], nil
}
func sortDestinations(items []domain.Destination) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
}

// ListDestinations optionally restricts the bounded page to one server.
func (s *MemoryStore) ListDestinations(_ context.Context, server string, page Page) (PageResult[domain.Destination], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Destination, 0)
	for _, v := range s.destinations {
		if server == "" || v.ServerID == server {
			items = append(items, v)
		}
	}
	sortDestinations(items)
	return paginateSlice(items, page), nil
}

// GrantDestinationProject creates or reactivates an explicit grant with a new audit.
func (s *MemoryStore) GrantDestinationProject(_ context.Context, grant domain.DestinationProjectGrant, event domain.AuditEvent) (domain.DestinationProjectGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destinationIndexLocked(grant.DestinationID) < 0 {
		return domain.DestinationProjectGrant{}, ErrNotFound
	}
	if !s.hostingProjectExistsLocked(grant.ProjectID) || grant.GrantedBy != event.ActorID || !s.destinationAuditAvailableLocked(event) {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	i := s.destinationGrantIndexLocked(grant.DestinationID, grant.ProjectID)
	if i >= 0 && s.destinationGrants[i].RevokedAt == nil {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	event, err := destinationAudit(event, "destination.project_granted", grant.DestinationID, map[string]any{"project_id": grant.ProjectID})
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	grant.RevokedAt = nil
	if i < 0 {
		s.destinationGrants = append(s.destinationGrants, grant)
	} else {
		s.destinationGrants[i] = grant
	}
	s.appendHostingAuditLocked(&event)
	return grant, nil
}

// RevokeDestinationProject revokes permission without moving or removing bindings or workloads.
func (s *MemoryStore) RevokeDestinationProject(_ context.Context, destination, project string, event domain.AuditEvent) (domain.DestinationProjectGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.destinationGrantIndexLocked(destination, project)
	if i < 0 {
		return domain.DestinationProjectGrant{}, ErrNotFound
	}
	grant := s.destinationGrants[i]
	if grant.RevokedAt != nil || !s.destinationAuditAvailableLocked(event) {
		return domain.DestinationProjectGrant{}, ErrConflict
	}
	event, err := destinationAudit(event, "destination.project_revoked", destination, map[string]any{"project_id": project})
	if err != nil {
		return domain.DestinationProjectGrant{}, err
	}
	at := event.CreatedAt
	grant.RevokedAt = &at
	s.destinationGrants[i] = grant
	s.appendHostingAuditLocked(&event)
	return copyDestinationGrant(grant), nil
}

// GetDestinationProjectGrant includes retained revocation state.
func (s *MemoryStore) GetDestinationProjectGrant(_ context.Context, destination, project string) (domain.DestinationProjectGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.destinationGrantIndexLocked(destination, project)
	if i < 0 {
		return domain.DestinationProjectGrant{}, ErrNotFound
	}
	return copyDestinationGrant(s.destinationGrants[i]), nil
}

// ListDestinationProjectGrants includes active and revoked grants.
func (s *MemoryStore) ListDestinationProjectGrants(_ context.Context, destination string, page Page) (PageResult[domain.DestinationProjectGrant], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.DestinationProjectGrant, 0)
	for _, v := range s.destinationGrants {
		if v.DestinationID == destination {
			items = append(items, copyDestinationGrant(v))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ProjectID < items[j].ProjectID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return paginateSlice(items, page), nil
}

// ListProjectDestinations returns only destinations with currently active explicit grants.
func (s *MemoryStore) ListProjectDestinations(_ context.Context, project string, page Page) (PageResult[domain.Destination], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Destination, 0)
	for _, v := range s.destinations {
		i := s.destinationGrantIndexLocked(v.ID, project)
		if i >= 0 && s.destinationGrants[i].RevokedAt == nil {
			items = append(items, v)
		}
	}
	sortDestinations(items)
	return paginateSlice(items, page), nil
}

// BindResourceDestination derives project scope from the resource and creates a namespace under the grant/revoke lock.
func (s *MemoryStore) BindResourceDestination(_ context.Context, in ResourceBindingInput, event domain.AuditEvent) (domain.ResourceBinding, error) {
	namespace, err := newLeaseToken("ncd")
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.ID == "" || in.CreatedBy != event.ActorID || !s.destinationAuditAvailableLocked(event) {
		return domain.ResourceBinding{}, ErrConflict
	}
	if s.destinationIndexLocked(in.DestinationID) < 0 {
		return domain.ResourceBinding{}, ErrNotFound
	}
	project := ""
	for _, v := range s.resources {
		if v.ID == in.ResourceID {
			project = v.ProjectID
			break
		}
	}
	if project == "" {
		return domain.ResourceBinding{}, ErrNotFound
	}
	i := s.destinationGrantIndexLocked(in.DestinationID, project)
	if i < 0 || s.destinationGrants[i].RevokedAt != nil {
		return domain.ResourceBinding{}, ErrConflict
	}
	for _, v := range s.resourceBindings {
		if v.ID == in.ID || v.ResourceID == in.ResourceID || v.DestinationID == in.DestinationID && v.RuntimeNamespace == namespace {
			return domain.ResourceBinding{}, ErrConflict
		}
	}
	event, err = destinationAudit(event, "resource.destination_bound", in.ResourceID, map[string]any{"project_id": project, "destination_id": in.DestinationID, "binding_id": in.ID})
	if err != nil {
		return domain.ResourceBinding{}, err
	}
	binding := domain.ResourceBinding{ID: in.ID, ResourceID: in.ResourceID, DestinationID: in.DestinationID, RuntimeNamespace: namespace, CreatedBy: in.CreatedBy, CreatedAt: in.CreatedAt}
	s.resourceBindings = append(s.resourceBindings, binding)
	s.appendHostingAuditLocked(&event)
	return binding, nil
}

// GetResourceBinding returns the resource's immutable placement.
func (s *MemoryStore) GetResourceBinding(_ context.Context, resource string) (domain.ResourceBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.resourceBindings {
		if v.ResourceID == resource {
			return v, nil
		}
	}
	return domain.ResourceBinding{}, ErrNotFound
}

// ListResourceBindings returns a bounded destination page.
func (s *MemoryStore) ListResourceBindings(_ context.Context, destination string, page Page) (PageResult[domain.ResourceBinding], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ResourceBinding, 0)
	for _, v := range s.resourceBindings {
		if v.DestinationID == destination {
			items = append(items, v)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return paginateSlice(items, page), nil
}

// ResolveResourceDestination follows only exact persisted IDs and returns current grant and runner state, even when revoked/offline.
func (s *MemoryStore) ResolveResourceDestination(_ context.Context, resource string) (domain.ResourceDestination, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result domain.ResourceDestination
	found := false
	for _, v := range s.resourceBindings {
		if v.ResourceID == resource {
			result.Binding = v
			found = true
			break
		}
	}
	if !found {
		return result, ErrNotFound
	}
	i := s.destinationIndexLocked(result.Binding.DestinationID)
	if i < 0 {
		return domain.ResourceDestination{}, ErrNotFound
	}
	result.Destination = s.destinations[i]
	for _, v := range s.servers {
		if v.ID == result.Destination.ServerID {
			result.Server = v
			break
		}
	}
	if result.Server.ID == "" {
		return domain.ResourceDestination{}, ErrNotFound
	}
	for _, v := range s.runners {
		if v.ID == result.Server.RunnerID {
			result.Runner = copyDestinationRunner(v)
			break
		}
	}
	if result.Runner.ID == "" {
		return domain.ResourceDestination{}, ErrNotFound
	}
	project := ""
	for _, v := range s.resources {
		if v.ID == resource {
			project = v.ProjectID
			break
		}
	}
	i = s.destinationGrantIndexLocked(result.Destination.ID, project)
	if i < 0 {
		return domain.ResourceDestination{}, ErrNotFound
	}
	result.Grant = copyDestinationGrant(s.destinationGrants[i])
	return result, nil
}
