package store

import (
	"context"
	"strings"

	"nerocd/internal/domain"
)

// ListAuditEvents implements the corresponding repository operation.
func (s *MemoryStore) ListAuditEvents(context.Context) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AuditEvent, len(s.auditEvents))
	copy(out, s.auditEvents)
	return out, nil
}

// ListAuditEventsPage implements the corresponding repository operation.
func (s *MemoryStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	events, err := s.ListAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	return paginateSlice(events, page), nil
}

// ListAuditEventsPageForScope filters authorization before counting and paging.
func (s *MemoryStore) ListAuditEventsPageForScope(_ context.Context, scope AuditAuthorizationScope, page Page) (PageResult[domain.AuditEvent], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allowedProjects := make(map[string]struct{}, len(scope.ProjectIDs))
	for _, projectID := range scope.ProjectIDs {
		allowedProjects[projectID] = struct{}{}
	}
	events := make([]domain.AuditEvent, 0, len(s.auditEvents))
	for _, event := range s.auditEvents {
		if scope.ActorID != "" && event.ActorID == scope.ActorID {
			events = append(events, event)
			continue
		}
		projectID, ok := s.auditProjectIDLocked(event)
		if !ok {
			continue
		}
		if _, ok := allowedProjects[projectID]; ok {
			events = append(events, event)
		}
	}
	return paginateSlice(events, page), nil
}

func (s *MemoryStore) auditProjectIDLocked(event domain.AuditEvent) (string, bool) {
	if projectID, ok := event.Metadata["project_id"].(string); ok && strings.TrimSpace(projectID) != "" {
		return strings.TrimSpace(projectID), true
	}
	switch {
	case strings.HasPrefix(event.TargetID, "proj_"):
		return event.TargetID, true
	case strings.HasPrefix(event.TargetID, "run_"):
		targetID := strings.TrimSpace(event.TargetID)
		for _, run := range s.runs {
			if run.ID == targetID {
				return run.ProjectID, true
			}
		}
	case strings.HasPrefix(event.TargetID, "tpl_"):
		targetID := strings.TrimSpace(event.TargetID)
		for _, template := range s.templates {
			if template.ID == targetID {
				return template.ProjectID, true
			}
		}
	case strings.HasPrefix(event.TargetID, "repo_"):
		targetID := strings.TrimSpace(event.TargetID)
		for _, repository := range s.repositories {
			if repository.ID == targetID {
				return repository.ProjectID, true
			}
		}
	}
	return "", false
}

// CreateAuditEvent implements the corresponding repository operation.
func (s *MemoryStore) CreateAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = append([]domain.AuditEvent{event}, s.auditEvents...)
	return nil
}
