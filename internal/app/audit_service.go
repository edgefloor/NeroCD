package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, store.Page{})
	return result.Items, err
}

func (s *Service) ListAuditEventsPage(ctx context.Context, page store.Page) (store.PageResult[domain.AuditEvent], error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	if isSystemAdmin(principal) {
		return s.audit.ListAuditEventsPage(ctx, page)
	}
	events, err := s.audit.ListAuditEvents(ctx)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	allowed, err := s.allowedProjects(ctx, principal.ID)
	if err != nil {
		return store.PageResult[domain.AuditEvent]{}, err
	}
	out := make([]domain.AuditEvent, 0, len(events))
	for _, event := range events {
		if event.ActorID == principal.ID {
			out = append(out, event)
			continue
		}
		projectID, ok := s.auditProjectID(ctx, event)
		if !ok {
			continue
		}
		if _, allowedProject := allowed[projectID]; allowedProject {
			out = append(out, event)
		}
	}
	return paginateForService(out, page), nil
}
