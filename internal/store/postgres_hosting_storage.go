package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func hostingStorageCompatible(ctx context.Context, q *sqlcgen.Queries) error {
	compatible, err := q.HostingStorageSchemaCompatible(ctx)
	if err != nil || !compatible {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrHostingStorageUnavailable
	}
	return nil
}

func hostingStorageFromSQL(row sqlcgen.HostingStorage) (domain.HostingStorage, error) {
	item := domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	if item.Validate() != nil {
		return domain.HostingStorage{}, ErrConflict
	}
	return item, nil
}

// Storage uses the existing resource -> actor/project -> destination grant lock
// order. Membership and grants stay locked through receipt/audit commit or replay.
func hostingStoragePlacement(ctx context.Context, tx pgx.Tx, actor, resource, role string) (domain.Resource, domain.ResourceBinding, error) {
	var r domain.Resource
	q := sqlcgen.New(tx)
	if err := hostingStorageCompatible(ctx, q); err != nil {
		return r, domain.ResourceBinding{}, err
	}
	if role == domain.RoleOwner {
		row, err := q.LockHostingRuntimeResource(ctx, resource)
		if err != nil {
			return r, domain.ResourceBinding{}, err
		}
		r.ID, r.ProjectID, r.Kind, r.Generation = row.ID, row.ProjectID, row.Kind, row.Generation
		if json.Unmarshal(row.Source, &r.Source) != nil {
			return r, domain.ResourceBinding{}, ErrConflict
		}
	} else {
		row, err := q.ReadHostingRuntimeResource(ctx, resource)
		if err != nil {
			return r, domain.ResourceBinding{}, err
		}
		r.ID, r.ProjectID, r.Kind, r.Generation = row.ID, row.ProjectID, row.Kind, row.Generation
		if json.Unmarshal(row.Source, &r.Source) != nil {
			return r, domain.ResourceBinding{}, ErrConflict
		}
	}
	if err := proxyAuthorize(ctx, tx, actor, r.ProjectID, role, false); err != nil {
		return r, domain.ResourceBinding{}, err
	}
	if r.Kind != "application" || r.Source.Kind != "image" {
		return r, domain.ResourceBinding{}, ErrConflict
	}
	row, err := q.GetResourceBinding(ctx, resource)
	if err != nil {
		return r, domain.ResourceBinding{}, err
	}
	if _, err = q.LockHostingStorageGrant(ctx, sqlcgen.LockHostingStorageGrantParams{DestinationID: row.DestinationID, ProjectID: r.ProjectID}); errors.Is(err, pgx.ErrNoRows) {
		return r, domain.ResourceBinding{}, auth.ErrForbidden
	} else if err != nil {
		return r, domain.ResourceBinding{}, err
	}
	return r, bindingFromDestinationSQL(row), nil
}

// GetHostingStorage requires current persisted viewer and destination authority.
func (s *PostgresStore) GetHostingStorage(ctx context.Context, actor, resource string) (domain.HostingStorage, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorage, error) {
		var zero domain.HostingStorage
		r, binding, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleViewer)
		if err != nil {
			return zero, err
		}
		row, err := sqlcgen.New(tx).ReadHostingStorage(ctx, resource)
		if err != nil {
			return zero, err
		}
		item, err := hostingStorageFromSQL(row)
		if err != nil {
			return zero, err
		}
		if item.ProjectID != r.ProjectID || item.BindingID != binding.ID || item.DestinationID != binding.DestinationID {
			return zero, ErrConflict
		}
		return item, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// CreateHostingStorage commits immutable metadata, request receipt and sanitized
// audit atomically. Exact retries retain identity across resource renames, but
// must still pass current owner, image-source and destination-grant checks.
func (s *PostgresStore) CreateHostingStorage(ctx context.Context, actor, resource string, in domain.HostingStorageInput, event domain.AuditEvent) (domain.HostingStorage, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingStorage{}, err
	}
	requestHash := domain.HostingDocumentHash(struct {
		Actor, Resource string
		Input           domain.HostingStorageInput
	}{actor, resource, in})
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorage, error) {
		var zero domain.HostingStorage
		r, binding, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		receipt, err := q.ReadHostingStorageReceipt(ctx, sqlcgen.ReadHostingStorageReceiptParams{ResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if receipt.ActorID != actor || receipt.RequestHash != requestHash {
				return zero, ErrConflict
			}
			row, readErr := q.ReadHostingStorage(ctx, resource)
			if readErr != nil {
				return zero, readErr
			}
			item, readErr := hostingStorageFromSQL(row)
			if readErr != nil {
				return zero, readErr
			}
			if item.ID != receipt.StorageID || item.ProjectID != r.ProjectID || item.BindingID != binding.ID || item.DestinationID != binding.DestinationID || item.MountPath != in.MountPath {
				return zero, ErrConflict
			}
			return item, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if r.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		id, volume := domain.HostingStorageIdentity(binding.ID)
		item := domain.HostingStorage{ID: id, ProjectID: r.ProjectID, ResourceID: resource, BindingID: binding.ID, DestinationID: binding.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: in.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: actor, CreatedAt: time.Now().UTC()}
		item.Hash = item.DocumentHash()
		if item.Validate() != nil {
			return zero, ErrConflict
		}
		row, err := q.InsertHostingStorage(ctx, sqlcgen.InsertHostingStorageParams{ID: item.ID, ProjectID: item.ProjectID, ResourceID: item.ResourceID, BindingID: item.BindingID, DestinationID: item.DestinationID, Kind: item.Kind, Driver: item.Driver, VolumeName: item.VolumeName, MountPath: item.MountPath, AccessMode: item.AccessMode, Retention: item.Retention, BodyHash: item.Hash, CreatedBy: actor})
		if err != nil {
			return zero, err
		}
		event.ID = domain.HostingStorageAuditID(resource, in.RequestID)
		event.ActorID = actor
		event.Action = "hosting.storage.created"
		event.TargetID = item.ID
		event.Metadata = map[string]any{"project_id": item.ProjectID, "resource_id": resource, "binding_id": item.BindingID, "destination_id": item.DestinationID, "storage_generation": item.Generation, "storage_hash": item.Hash}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertHostingStorageReceipt(ctx, sqlcgen.InsertHostingStorageReceiptParams{ResourceID: resource, RequestID: in.RequestID, StorageID: item.ID, ActorID: actor, RequestHash: requestHash, ExpectedResourceGeneration: in.ExpectedResourceGeneration, AuditID: event.ID}); err != nil {
			return zero, err
		}
		item, err = hostingStorageFromSQL(row)
		if err != nil {
			return zero, err
		}
		return item, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
