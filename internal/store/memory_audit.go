package store

import (
	"context"

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

// CreateAuditEvent implements the corresponding repository operation.
func (s *MemoryStore) CreateAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = append([]domain.AuditEvent{event}, s.auditEvents...)
	return nil
}
