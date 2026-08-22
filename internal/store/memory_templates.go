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

func (s *MemoryStore) CreateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	return template, nil
}
func (s *MemoryStore) CreateTemplateWithAudit(_ context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return template, nil
}

func (s *MemoryStore) UpdateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.templates {
		if s.templates[i].ID == template.ID {
			s.templates[i] = template
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}
func (s *MemoryStore) UpdateTemplateWithAudit(_ context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.templates {
		if old.ID == template.ID {
			s.templates[i] = template
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}
