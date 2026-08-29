package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

// ListProjects implements the corresponding repository operation.
func (s *MemoryStore) ListProjects(context.Context) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Project, 0, len(s.projects))
	for _, project := range s.projects {
		if project.ArchivedAt == nil {
			out = append(out, project)
		}
	}
	return out, nil
}

// CreateProject implements the corresponding repository operation.
func (s *MemoryStore) CreateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = append(s.projects, project)
	return project, nil
}

// CreateProjectWithOwner implements the corresponding repository operation.
func (s *MemoryStore) CreateProjectWithOwner(_ context.Context, project domain.Project, owner domain.ProjectMember, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.projects {
		if existing.ID == project.ID {
			return domain.Project{}, ErrConflict
		}
	}
	if owner.ProjectID != project.ID || owner.UserID == "" || owner.Role != domain.RoleOwner {
		return domain.Project{}, ErrConflict
	}
	s.projects = append(s.projects, project)
	s.projectMembers = append(s.projectMembers, owner)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return project, nil
}

// UpdateProject implements the corresponding repository operation.
func (s *MemoryStore) UpdateProject(_ context.Context, project domain.Project, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID && existing.ArchivedAt == nil {
			project.CreatedAt = existing.CreatedAt
			s.projects[i] = project
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

// ArchiveProject implements the corresponding repository operation.
func (s *MemoryStore) ArchiveProject(_ context.Context, id string, archivedAt time.Time, opts ...MutationOption) (domain.Project, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id && project.ArchivedAt == nil {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

// ListProjectMembers implements the corresponding repository operation.
func (s *MemoryStore) ListProjectMembers(_ context.Context, projectID string) ([]domain.ProjectMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ProjectMember, 0, len(s.projectMembers))
	for _, member := range s.projectMembers {
		if projectID == "" || member.ProjectID == projectID {
			out = append(out, member)
		}
	}
	return out, nil
}

// UpsertProjectMember implements the corresponding repository operation.
func (s *MemoryStore) UpsertProjectMember(_ context.Context, member domain.ProjectMember, opts ...MutationOption) (domain.ProjectMember, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID, member.CreatedAt = existing.ID, existing.CreatedAt
			s.projectMembers[i] = member
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	if audit != nil {
		s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
	}
	return member, nil
}
