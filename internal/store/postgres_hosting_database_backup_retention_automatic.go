package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func backupRetentionPublication(ctx context.Context, tx pgx.Tx, resource, publication string) error {
	_, e := tx.Exec(ctx, `INSERT INTO hosting_database_backup_retention_publications(id,resource_id,actor_id,policy_revision) SELECT $1,resource_id,actor_id,revision FROM hosting_database_backup_retention_policies WHERE resource_id=$2 AND body->>'enabled'='true' AND body->>'automatic'='true' ON CONFLICT DO NOTHING`, publication, resource)
	return e
}

// Each publication has one stable request identity. Discovery failure never
// rewrites the successful dump/upload outcome, and a replay cannot pick a batch.
func (s *PostgresStore) ReconcileDatabaseBackupRetention(ctx context.Context) error {
	if !s.hostingJobs.DatabaseBackupRetentionEnabled() {
		return nil
	}
	rows, e := s.pool.Query(ctx, `SELECT id,resource_id,actor_id,policy_revision FROM hosting_database_backup_retention_publications WHERE state='pending' ORDER BY created_at,id LIMIT 50`)
	if e != nil {
		return e
	}
	type event struct {
		id, resource, actor string
		revision            int64
	}
	var events []event
	for rows.Next() {
		var v event
		if e = rows.Scan(&v.id, &v.resource, &v.actor, &v.revision); e != nil {
			rows.Close()
			return e
		}
		events = append(events, v)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, v := range events {
		key := domain.HostingDocumentHash(v.id)[7:39]
		preview, e := s.PreviewDatabaseBackupRetention(ctx, "dbrp_auto_"+key, v.actor, v.resource, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "auto_preview_" + key, ExpectedPolicyRevision: v.revision})
		state, reason := "admitted", ""
		if e == nil {
			selected := false
			for _, i := range preview.Items {
				selected = selected || i.Selected
			}
			if !selected {
				state, reason = "skipped", "no_eligible_copy"
			} else {
				_, e = s.ExecuteDatabaseBackupRetention(ctx, "dbre_auto_"+key, v.actor, v.resource, domain.HostingDatabaseBackupRetentionExecutionInput{RequestID: "auto_execute_" + key, PreviewID: preview.ID, ExpectedPolicyRevision: v.revision, ExpectedPlanHash: preview.PlanHash})
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
		if _, e = s.pool.Exec(ctx, `UPDATE hosting_database_backup_retention_publications SET state=$2,reason=$3 WHERE id=$1 AND state='pending'`, v.id, state, reason); e != nil {
			return e
		}
	}
	return nil
}
