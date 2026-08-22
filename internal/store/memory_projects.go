package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

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

func (s *MemoryStore) CreateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = append(s.projects, project)
	return project, nil
}

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

func (s *MemoryStore) UpdateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID {
			project.CreatedAt = existing.CreatedAt
			project.ArchivedAt = existing.ArchivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) UpdateProjectWithAudit(_ context.Context, project domain.Project, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID && existing.ArchivedAt == nil {
			project.CreatedAt = existing.CreatedAt
			s.projects[i] = project
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ArchiveProject(_ context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ArchiveProjectWithAudit(_ context.Context, id string, archivedAt time.Time, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id && project.ArchivedAt == nil {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

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

func (s *MemoryStore) UpsertProjectMember(_ context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID = existing.ID
			member.CreatedAt = existing.CreatedAt
			s.projectMembers[i] = member
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	return member, nil
}

func (s *MemoryStore) UpsertProjectMemberWithAudit(_ context.Context, member domain.ProjectMember, audit domain.AuditEvent) (domain.ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID, member.CreatedAt = existing.ID, existing.CreatedAt
			s.projectMembers[i] = member
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return member, nil
}
