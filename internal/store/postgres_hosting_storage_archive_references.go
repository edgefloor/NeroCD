package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// Callers already hold destination/resource/binding locks and need exactly one
// image identity. This shared admission lock also serializes different bindings
// on the same daemon without granting authority over those bindings.
func archiveImageLock(ctx context.Context, tx pgx.Tx, release domain.HostingRelease) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-archive-image:'||$1::text||':'||$2::text,0))`, release.DaemonID, release.ImageID)
	return err
}

func archiveImageReferenced(ctx context.Context, tx pgx.Tx, h domain.HostingHealthy) error {
	present, err := sqlcgen.New(tx).StorageArchiveSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	if err = archiveImageLock(ctx, tx, h.Release); err != nil {
		return err
	}
	var referenced bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id WHERE p.body->'source_healthy'->'release'->>'daemon_id'=$1 AND p.body->'source_healthy'->'release'->>'image_id'=$2 AND (EXISTS(SELECT 1 FROM hosting_storage_backups b WHERE b.operation_id=p.operation_id) OR (p.target_resource_id IS NOT NULL AND o.state NOT IN ('succeeded','failed','canceled'))))`, h.Release.DaemonID, h.Release.ImageID).Scan(&referenced)
	if err != nil {
		return err
	}
	if referenced {
		return ErrConflict
	}
	return nil
}

func archiveImageCleanupPending(ctx context.Context, tx pgx.Tx, h domain.HostingHealthy) error {
	if err := archiveImageLock(ctx, tx, h.Release); err != nil {
		return err
	}
	var pending bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_retained_releases WHERE state='cleanup_pending' AND body->'release'->>'daemon_id'=$1 AND body->'release'->>'image_id'=$2)`, h.Release.DaemonID, h.Release.ImageID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
}
