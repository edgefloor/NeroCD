package store

import (
	"context"
	"encoding/json"
	"fmt"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// ListAuditEvents implements the corresponding repository operation.
func (s *PostgresStore) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, Page{})
	return result.Items, err
}

// ListAuditEventsPage implements the corresponding repository operation.
func (s *PostgresStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	total64, err := s.queries.CountAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, fmt.Errorf("count audit events query: %w", err)
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListAuditEventsPage(ctx, sqlcgen.ListAuditEventsPageParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return PageResult[domain.AuditEvent]{}, fmt.Errorf("list audit events query: %w", err)
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := auditEventFromSQLC(row)
		if err != nil {
			return PageResult[domain.AuditEvent]{}, fmt.Errorf("decode audit event row: %w", err)
		}
		events = append(events, event)
	}
	return PageResult[domain.AuditEvent]{Items: events, Limit: limit, Offset: offset, Total: total}, nil
}

// CreateAuditEvent implements the corresponding repository operation.
func (s *PostgresStore) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	return s.queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: event.ID, ActorID: event.ActorID, Action: event.Action, TargetID: event.TargetID, Metadata: metadata, CreatedAt: event.CreatedAt})
}
