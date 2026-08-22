package store

import (
	"context"

	"nerocd/internal/domain"
)

func (s *MemoryStore) ListTemplates(_ context.Context, projectID string) ([]domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskTemplate, 0, len(s.templates))
	for _, template := range s.templates {
		if projectID == "" || template.ProjectID == projectID {
			out = append(out, template)
		}
	}
	return out, nil
}

func (s *MemoryStore) GetTemplate(_ context.Context, id string) (domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, template := range s.templates {
		if template.ID == id {
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}

func (s *MemoryStore) CreateTemplate(_ context.Context, template domain.TaskTemplate, opts ...MutationOption) (domain.TaskTemplate, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	if audit != nil {
		s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
	}
	return template, nil
}

func (s *MemoryStore) UpdateTemplate(_ context.Context, template domain.TaskTemplate, opts ...MutationOption) (domain.TaskTemplate, error) {
	audit := resolveMutationOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.templates {
		if old.ID == template.ID {
			s.templates[i] = template
			if audit != nil {
				s.auditEvents = append([]domain.AuditEvent{*audit}, s.auditEvents...)
			}
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}
