package store

import (
	"context"
	"sort"

	"nerocd/internal/domain"
)

func (s *MemoryStore) hostingProjectExistsLocked(id string) bool {
	for _, p := range s.projects {
		if p.ID == id && p.ArchivedAt == nil {
			return true
		}
	}
	return false
}
func (s *MemoryStore) hostingPrincipalExistsLocked(id string) bool {
	for _, u := range s.users {
		if u.ID == id {
			return true
		}
	}
	for _, a := range s.serviceAccounts {
		if a.ID == id {
			return true
		}
	}
	for _, token := range s.apiTokens {
		if token.ID == id && token.ServiceAccountID == nil {
			return true
		}
	}
	return false
}
func (s *MemoryStore) hostingAuditAvailableLocked(audit *domain.AuditEvent) bool {
	return audit == nil || s.auditIDAvailableLocked(audit.ID)
}
func (s *MemoryStore) appendHostingAuditLocked(audit *domain.AuditEvent) {
	if audit != nil {
		s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
	}
}

// ListProjectEnvironments returns a bounded, stable project page.
func (s *MemoryStore) ListProjectEnvironments(_ context.Context, projectID string, page Page) (PageResult[domain.ProjectEnvironment], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ProjectEnvironment, 0)
	for _, e := range s.projectEnvironments {
		if e.ProjectID == projectID {
			items = append(items, e)
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

// GetProjectEnvironment returns one shared environment.
func (s *MemoryStore) GetProjectEnvironment(_ context.Context, id string) (domain.ProjectEnvironment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.projectEnvironments {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.ProjectEnvironment{}, ErrNotFound
}

// CreateProjectEnvironment inserts the environment and audit under one lock.
func (s *MemoryStore) CreateProjectEnvironment(_ context.Context, e domain.ProjectEnvironment, opts ...MutationOption) (domain.ProjectEnvironment, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" || domain.ValidateHostingName(e.Name) != nil || !s.hostingProjectExistsLocked(e.ProjectID) || !s.hostingAuditAvailableLocked(audit) {
		return domain.ProjectEnvironment{}, ErrConflict
	}
	for _, old := range s.projectEnvironments {
		if old.ID == e.ID || old.ProjectID == e.ProjectID && old.Name == e.Name {
			return domain.ProjectEnvironment{}, ErrConflict
		}
	}
	s.projectEnvironments = append(s.projectEnvironments, e)
	s.appendHostingAuditLocked(audit)
	return e, nil
}

// ListResources returns desired configuration, optionally restricted to an environment.
func (s *MemoryStore) ListResources(_ context.Context, projectID, environmentID string, page Page) (PageResult[domain.Resource], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Resource, 0)
	for _, item := range s.resources {
		if item.ProjectID == projectID && (environmentID == "" || item.ProjectEnvironmentID == environmentID) {
			items = append(items, item)
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

// GetResource returns one resource.
func (s *MemoryStore) GetResource(_ context.Context, id string) (domain.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.resources {
		if item.ID == id {
			return item, nil
		}
	}
	return domain.Resource{}, ErrNotFound
}

// CreateResource enforces scope, uniqueness, and creator existence atomically.
func (s *MemoryStore) CreateResource(_ context.Context, item domain.Resource, opts ...MutationOption) (domain.Resource, error) {
	if item.Source.Kind == "git" || resourceMutationOptions(opts).gitSource != nil {
		return domain.Resource{}, ErrGitResourceUnavailable
	}
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if validateResource(item) != nil || !s.hostingProjectExistsLocked(item.ProjectID) || !s.hostingPrincipalExistsLocked(item.CreatedBy) || !s.hostingAuditAvailableLocked(audit) {
		return domain.Resource{}, ErrConflict
	}
	found := false
	for _, e := range s.projectEnvironments {
		if e.ID == item.ProjectEnvironmentID && e.ProjectID == item.ProjectID {
			found = true
		}
	}
	if !found {
		return domain.Resource{}, ErrConflict
	}
	for _, old := range s.resources {
		if old.ID == item.ID || old.ProjectEnvironmentID == item.ProjectEnvironmentID && old.Name == item.Name {
			return domain.Resource{}, ErrConflict
		}
	}
	item.Generation = 1
	s.resources = append(s.resources, item)
	s.appendHostingAuditLocked(audit)
	return item, nil
}

// UpdateResource performs compare-and-swap and audit insertion under one lock.
func (s *MemoryStore) UpdateResource(_ context.Context, item domain.Resource, expectedGeneration int64, opts ...MutationOption) (domain.Resource, error) {
	if item.Source.Kind == "git" || resourceMutationOptions(opts).gitSource != nil {
		return domain.Resource{}, ErrGitResourceUnavailable
	}
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if validateResource(item) != nil || !s.hostingAuditAvailableLocked(audit) || expectedGeneration < 1 {
		return domain.Resource{}, ErrConflict
	}
	for i, old := range s.resources {
		if old.ID != item.ID {
			continue
		}
		if old.Generation != expectedGeneration || old.ProjectID != item.ProjectID || old.ProjectEnvironmentID != item.ProjectEnvironmentID || old.Kind != item.Kind || old.CreatedBy != item.CreatedBy || !old.CreatedAt.Equal(item.CreatedAt) || old.Generation == 9223372036854775807 {
			return domain.Resource{}, ErrConflict
		}
		for _, other := range s.resources {
			if other.ID != item.ID && other.ProjectEnvironmentID == item.ProjectEnvironmentID && other.Name == item.Name {
				return domain.Resource{}, ErrConflict
			}
		}
		item.Generation = old.Generation + 1
		s.resources[i] = item
		s.appendHostingAuditLocked(audit)
		return item, nil
	}
	return domain.Resource{}, ErrNotFound
}
