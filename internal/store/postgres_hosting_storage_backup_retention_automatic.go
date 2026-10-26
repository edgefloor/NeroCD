package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageRetentionPublication(ctx context.Context, tx pgx.Tx, resource, publication string) error {
	e := sqlcgen.New(tx).StorageRetentionPublish(ctx, sqlcgen.StorageRetentionPublishParams{ID: publication, ResourceID: resource})
	return e
}

// ReconcileStorageBackupRetention gives each publication one stable request identity. Discovery failure never
// rewrites the successful dump/upload outcome, and a replay cannot pick a batch.
func (s *PostgresStore) ReconcileStorageBackupRetention(ctx context.Context) error {
	if !s.hostingJobs.StorageBackupRetentionEnabled() {
		return nil
	}
	rows, e := sqlcgen.New(s.pool).StorageRetentionPendingPublications(ctx)
	if e != nil {
		return e
	}
	type event struct {
		id, resource, actor string
		revision            int64
	}
	events := make([]event, 0, len(rows))
	for _, v := range rows {
		events = append(events, event{id: v.ID, resource: v.ResourceID, actor: v.ActorID, revision: v.PolicyRevision})
	}
	for _, v := range events {
		key := domain.HostingDocumentHash(v.id)[7:39]
		preview, e := s.PreviewStorageBackupRetention(ctx, "sbrp_auto_"+key, v.actor, v.resource, domain.HostingStorageBackupRetentionPreviewInput{RequestID: "auto_preview_" + key, ExpectedPolicyRevision: v.revision})
		state, reason := "admitted", ""
		if e == nil {
			selected := false
			for _, i := range preview.Items {
				selected = selected || i.Selected
			}
			if !selected {
				state, reason = "skipped", "no_eligible_copy"
			} else {
				_, e = s.ExecuteStorageBackupRetention(ctx, "sbre_auto_"+key, v.actor, v.resource, domain.HostingStorageBackupRetentionExecutionInput{RequestID: "auto_execute_" + key, PreviewID: preview.ID, ExpectedPolicyRevision: v.revision, ExpectedPlanHash: preview.PlanHash})
			}
		}
		if errors.Is(e, ErrBackupRetentionBusy) {
			continue
		}
		if e != nil {
			switch {
			case errors.Is(e, auth.ErrForbidden), errors.Is(e, auth.ErrUnauthenticated):
				state, reason = "denied", "authority_denied"
			case errors.Is(e, ErrConflict), errors.Is(e, ErrNotFound):
				state, reason = "denied", "scope_changed"
			default:
				return e
			}
		}
		if e = sqlcgen.New(s.pool).StorageRetentionSetPublication(ctx, sqlcgen.StorageRetentionSetPublicationParams{ID: v.id, State: state, Reason: reason}); e != nil {
			return e
		}
	}
	return nil
}
