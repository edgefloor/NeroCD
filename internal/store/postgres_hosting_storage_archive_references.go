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
	return sqlcgen.New(tx).LockStorageArchiveImage(ctx, sqlcgen.LockStorageArchiveImageParams{DaemonID: release.DaemonID, ImageID: release.ImageID})
}

func archiveImageReferenced(ctx context.Context, tx pgx.Tx, h domain.HostingHealthy) error {
	present, err := sqlcgen.New(tx).StorageArchiveSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	if err = archiveImageLock(ctx, tx, h.Release); err != nil {
		return err
	}
	referenced, err := sqlcgen.New(tx).StorageArchiveImageReferenced(ctx, sqlcgen.StorageArchiveImageReferencedParams{DaemonID: h.Release.DaemonID, ImageID: h.Release.ImageID})
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
	pending, err := sqlcgen.New(tx).StorageArchiveImageCleanupPending(ctx, sqlcgen.StorageArchiveImageCleanupPendingParams{DaemonID: h.Release.DaemonID, ImageID: h.Release.ImageID})
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
}
