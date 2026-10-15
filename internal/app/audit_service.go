package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// ListAuditEvents lists authorized audit events.
func (s *Service) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, store.Page{})
	return result.Items, err
}

// ListAuditEventsPage lists authorized audit events page.
func (s *Service) ListAuditEventsPage(ctx context.Context, page store.Page) (store.PageResult[domain.AuditEvent], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	if isSystemAdmin(principal) {
		return s.audit.ListAuditEventsPage(ctx, page)
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	projectIDs := make([]string, 0, len(allowed))
	for projectID := range allowed {
		projectIDs = append(projectIDs, projectID)
	}
	return s.audit.ListAuditEventsPageForScope(ctx, store.AuditAuthorizationScope{ActorID: principal.ID, ProjectIDs: projectIDs}, page)
}
