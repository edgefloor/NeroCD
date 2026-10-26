package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageBackupScheduleView(r sqlcgen.HostingStorageBackupSchedule) domain.HostingStorageBackupSchedule {
	return domain.HostingStorageBackupSchedule{ID: r.ID, Version: domain.HostingStorageBackupScheduleVersion, ProjectID: r.ProjectID, ResourceID: r.ResourceID, Revision: r.Revision, Frequency: r.Frequency, Timezone: r.Timezone, Enabled: r.Enabled, LastScheduledAt: r.LastScheduledAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// Placement admission precedes the schedule lock in both writers and controller
// passes. Current actor checks do not inherit a former owner's privileges.
func storageBackupScheduleAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (domain.Resource, domain.ResourceBinding, domain.HostingStorage, error) {
	var zero domain.HostingStorage
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return domain.Resource{}, domain.ResourceBinding{}, zero, err
	}
	r, b, err := hostingStoragePlacement(ctx, tx, actor, resource, role)
	if err != nil {
		return r, b, zero, err
	}
	st, err := currentHostingStorage(ctx, tx, r.ProjectID, resource)
	if err != nil {
		return r, b, zero, err
	}
	return r, b, st, proxyCheckLegacyToken(ctx, tx, actor)
}

func storageBackupScheduleSelection(ctx context.Context, tx pgx.Tx, id string, revision int64) (*domain.BackupTargetSelection, error) {
	raw, err := sqlcgen.New(tx).ReadStorageBackupScheduleSelection(ctx, sqlcgen.ReadStorageBackupScheduleSelectionParams{ScheduleID: id, Revision: revision})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var out domain.BackupTargetSelection
	if json.Unmarshal(raw, &out) != nil || out.Validate() != nil {
		return nil, ErrConflict
	}
	return &out, nil
}
func (s *PostgresStore) storageBackupScheduleTarget(ctx context.Context, tx pgx.Tx, actor string, st domain.HostingStorage, selection *domain.BackupTargetSelection) error {
	if selection == nil {
		return nil
	}
	if err := s.archiveConfigured(ctx, sqlcgen.New(tx)); err != nil {
		return err
	}
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, st.BindingID)
	if err != nil {
		return err
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(raw, &healthy) != nil {
		return ErrConflict
	}
	return s.storageRemoteSelect(ctx, tx, domain.HostingStorageArchivePlan{ProjectID: st.ProjectID, DestinationID: st.DestinationID, RunnerID: healthy.Snapshot.RunnerID, ServerID: healthy.Snapshot.ServerID, Helper: s.storageArchiveConfiguration.helper, Limits: s.storageArchiveConfiguration.limits}, actor, *selection)
}

func (s *PostgresStore) SaveStorageBackupSchedule(ctx context.Context, id, resource, actor, existingID string, in domain.HostingStorageBackupScheduleInput) (domain.HostingStorageBackupSchedule, error) {
	if in.Validate() != nil || existingID == "" && in.ExpectedVersion != 0 || existingID != "" && in.ExpectedVersion < 1 {
		return domain.HostingStorageBackupSchedule{}, domain.ErrInvalidStorageBackupSchedule
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupSchedule, error) {
		var zero domain.HostingStorageBackupSchedule
		resourceValue, binding, storageValue, err := storageBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Schedule string
			Input           domain.HostingStorageBackupScheduleInput
		}{actor, existingID, in})
		replay, err := q.ReadStorageBackupScheduleRequest(ctx, sqlcgen.ReadStorageBackupScheduleRequestParams{ResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if replay.ActorID != actor || replay.RequestHash != hash || json.Unmarshal(replay.Response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if resourceValue.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		if existingID == "" {
			count, countErr := q.CountStorageBackupSchedules(ctx, resource)
			if countErr != nil {
				return zero, countErr
			}
			if count >= 100 {
				return zero, ErrConflict
			}
			err = q.CreateStorageBackupSchedule(ctx, sqlcgen.CreateStorageBackupScheduleParams{ID: id, ProjectID: resourceValue.ProjectID, ResourceID: resource, BindingID: binding.ID, DestinationID: binding.DestinationID, ActorID: actor, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled, StorageID: storageValue.ID, SourceReferenceHash: domain.HostingDocumentHash(storageValue.Reference())})
		} else {
			prior, e := q.LockStorageBackupSchedule(ctx, existingID)
			if e != nil {
				return zero, hostingError(e)
			}
			if prior.ResourceID != resource || prior.ProjectID != resourceValue.ProjectID || prior.Revision != in.ExpectedVersion || prior.BindingID != binding.ID || prior.DestinationID != binding.DestinationID || prior.StorageID != storageValue.ID || prior.SourceReferenceHash != domain.HostingDocumentHash(storageValue.Reference()) {
				return zero, ErrConflict
			}
			id = existingID
			err = q.UpdateStorageBackupSchedule(ctx, sqlcgen.UpdateStorageBackupScheduleParams{ID: id, ActorID: actor, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled})
		}
		if err != nil {
			return zero, hostingError(err)
		}
		saved, err := q.ReadStorageBackupSchedule(ctx, id)
		if err != nil {
			return zero, err
		}
		selection := in.RemoteTarget
		if !in.Enabled && existingID != "" && selection == nil {
			selection, err = storageBackupScheduleSelection(ctx, tx, id, saved.Revision-1)
			if err != nil {
				return zero, err
			}
		}
		var remote []byte
		if selection != nil {
			remote = buildJSON(selection)
		}
		err = q.InsertStorageBackupScheduleVersion(ctx, sqlcgen.InsertStorageBackupScheduleVersionParams{ScheduleID: id, Revision: saved.Revision, ActorID: actor, ExpectedResourceGeneration: in.ExpectedResourceGeneration, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled, RemoteTarget: remote})
		if err != nil {
			return zero, fmt.Errorf("insert storage backup schedule version: %w", err)
		}

		// Remote validation forces deferred constraints. Persist the matching version
		// first, so the schedule/version invariant holds when they are checked.
		if in.Enabled {
			if err = s.archiveConfigured(ctx, q); err != nil {
				return zero, err
			}
			if err = s.storageBackupScheduleTarget(ctx, tx, actor, storageValue, selection); err != nil {
				return zero, err
			}
		}

		out := storageBackupScheduleView(saved)
		out.RemoteTarget = selection
		err = q.InsertStorageBackupScheduleRequest(ctx, sqlcgen.InsertStorageBackupScheduleRequestParams{ResourceID: resource, RequestID: in.RequestID, ActorID: actor, RequestHash: hash, ScheduleID: id, Revision: saved.Revision, Response: buildJSON(out)})
		if err != nil {
			return zero, err
		}
		if err = storageRemoteAudit(ctx, tx, actor, id, "schedule_"+strconv.FormatInt(saved.Revision, 10), saved.ProjectID); err != nil {
			return zero, err
		}
		return out, archiveFinalizeActor(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListStorageBackupSchedules(ctx context.Context, actor, resource string) ([]domain.HostingStorageBackupSchedule, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingStorageBackupSchedule, error) {
		if _, _, _, err := storageBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleViewer); err != nil {
			return nil, err
		}
		rows, err := sqlcgen.New(tx).ListStorageBackupSchedules(ctx, resource)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingStorageBackupSchedule, 0, len(rows))
		for _, r := range rows {
			item := storageBackupScheduleView(r)
			item.RemoteTarget, err = storageBackupScheduleSelection(ctx, tx, r.ID, r.Revision)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListStorageBackupOccurrences(ctx context.Context, actor, resource, id string) ([]domain.HostingStorageBackupOccurrence, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingStorageBackupOccurrence, error) {
		if _, _, _, err := storageBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleViewer); err != nil {
			return nil, err
		}
		q := sqlcgen.New(tx)
		schedule, err := q.ReadStorageBackupSchedule(ctx, id)
		if err != nil {
			return nil, hostingError(err)
		}
		if schedule.ResourceID != resource {
			return nil, ErrNotFound
		}
		rows, err := q.ListStorageBackupOccurrences(ctx, id)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingStorageBackupOccurrence, 0, len(rows))
		for _, r := range rows {
			out = append(out, domain.HostingStorageBackupOccurrence{ID: r.ID, ScheduleID: r.ScheduleID, ProjectID: r.ProjectID, ResourceID: r.ResourceID, ScheduleRevision: r.ScheduleRevision, ScheduledAt: r.ScheduledAt, Frequency: r.Frequency, Timezone: r.Timezone, BackupID: r.BackupID, OperationID: proxyString(r.OperationID), State: r.State, Reason: r.Reason, OwnershipHeld: r.OwnershipHeld, CreatedAt: r.CreatedAt})
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

var _ HostingStorageBackupScheduleRepository = (*PostgresStore)(nil)
