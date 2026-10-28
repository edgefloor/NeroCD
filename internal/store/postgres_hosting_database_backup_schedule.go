package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func databaseBackupScheduleView(r sqlcgen.HostingDatabaseBackupSchedule, version string) domain.HostingDatabaseBackupSchedule {
	return domain.HostingDatabaseBackupSchedule{Component: r.Component, ID: r.ID, Version: version, ProjectID: r.ProjectID, ResourceID: r.ResourceID, Revision: r.Revision, Frequency: r.Frequency, Timezone: r.Timezone, Enabled: r.Enabled, LastScheduledAt: r.LastScheduledAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type databaseBackupScheduleSource struct {
	Resource   domain.Resource
	Binding    domain.ResourceBinding
	Version    string
	Capability string
}

// Placement admission precedes the schedule lock in both writers and controller
// passes. Current actor checks do not inherit a former owner's privileges.
func databaseBackupScheduleAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (databaseBackupScheduleSource, error) {
	var zero databaseBackupScheduleSource
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return zero, err
	}
	q := sqlcgen.New(tx)
	target, err := q.LockHostingResourceTarget(ctx, resource)
	if err != nil {
		return zero, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, target.ProjectID, role, false); err != nil {
		return zero, err
	}
	var source domain.ResourceSource
	if json.Unmarshal(target.Source, &source) != nil {
		return zero, ErrConflict
	}
	if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
		if errors.Is(err, ErrConflict) && (source.Kind == "compose" || source.Kind == "git") {
			return zero, auth.ErrForbidden
		}
		return zero, err
	}
	if source.Kind == "git" {
		if _, err = hostingHistoricalStackSnapshot(ctx, tx, resource, target.ProjectID, target.BindingID); err != nil {
			return zero, err
		}
	}
	if source.Kind == "compose" || source.Kind == "git" {
		return databaseBackupScheduleSource{Resource: domain.Resource{ID: resource, ProjectID: target.ProjectID, Generation: target.Generation, Source: source}, Binding: domain.ResourceBinding{ID: target.BindingID, DestinationID: target.DestinationID}, Version: domain.HostingDatabaseBackupComponentScheduleVersion, Capability: domain.HostingDatabaseBackupCapability}, proxyCheckLegacyToken(ctx, tx, actor)
	}
	value, err := hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return zero, err
	}
	version := domain.HostingDatabaseBackupScheduleVersionFor(value.Engine, value.RecipeVersion)
	if version == "" {
		return zero, ErrConflict
	}
	return databaseBackupScheduleSource{Resource: value.Resource, Binding: value.Binding, Version: version, Capability: domain.HostingDatabaseBackupCapabilityFor(domain.HostingDatabaseBackupVersionFor(value.Engine, value.RecipeVersion))}, proxyCheckLegacyToken(ctx, tx, actor)
}

// SaveDatabaseBackupSchedule creates or revises an authorized database backup schedule.
func (s *PostgresStore) SaveDatabaseBackupSchedule(ctx context.Context, id, resource, actor, existingID string, in domain.HostingDatabaseBackupScheduleInput) (domain.HostingDatabaseBackupSchedule, error) {
	if in.Validate() != nil || existingID == "" && in.ExpectedVersion != 0 || existingID != "" && in.ExpectedVersion < 1 {
		return domain.HostingDatabaseBackupSchedule{}, domain.ErrInvalidDatabaseBackupSchedule
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupSchedule, error) {
		var zero domain.HostingDatabaseBackupSchedule
		value, err := databaseBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Schedule string
			Input           domain.HostingDatabaseBackupScheduleInput
		}{actor, existingID, in})
		replay, err := q.ReadDatabaseBackupScheduleRequest(ctx, sqlcgen.ReadDatabaseBackupScheduleRequestParams{ResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if replay.ActorID != actor || replay.RequestHash != hash || json.Unmarshal(replay.Response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if value.Resource.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		if (value.Resource.Source.Kind == "compose" || value.Resource.Source.Kind == "git") != (in.Component != "") {
			return zero, ErrConflict
		}
		if in.Component != "" && (in.Enabled || existingID == "") {
			h, op, _, e := databaseBackupComponentSource(ctx, tx, actor, resource, in.Component, domain.RoleOwner)
			if e != nil {
				return zero, e
			}
			if _, e = domain.HostingPostgresComponent(h, *op.Plan, in.Component); e != nil {
				return zero, ErrConflict
			}
			if h.OperationID != in.ExpectedDeployedOperationID {
				return zero, ErrConflict
			}
		}
		if existingID == "" {
			var count int64
			count, err = sqlcgen.New(tx).CountDatabaseBackupSchedules(ctx, resource)
			if err != nil {
				return zero, err
			}
			if count >= 100 {
				return zero, ErrConflict
			}
			err = sqlcgen.New(tx).CreateDatabaseBackupSchedule(ctx, sqlcgen.CreateDatabaseBackupScheduleParams{Component: in.Component, ID: id, ProjectID: value.Resource.ProjectID, ResourceID: resource, BindingID: value.Binding.ID, DestinationID: value.Binding.DestinationID, ActorID: actor, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled})
		} else {
			prior, e := q.LockDatabaseBackupSchedule(ctx, existingID)
			if e != nil {
				return zero, hostingError(e)
			}
			if prior.Component != in.Component || prior.ResourceID != resource || prior.ProjectID != value.Resource.ProjectID || prior.Revision != in.ExpectedVersion || prior.BindingID != value.Binding.ID || prior.DestinationID != value.Binding.DestinationID {
				return zero, ErrConflict
			}
			id = existingID
			err = sqlcgen.New(tx).UpdateDatabaseBackupSchedule(ctx, sqlcgen.UpdateDatabaseBackupScheduleParams{ID: id, ActorID: actor, Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled})
		}
		if err != nil {
			return zero, hostingError(err)
		}
		saved, err := q.ReadDatabaseBackupSchedule(ctx, id)
		if err != nil {
			return zero, err
		}
		err = sqlcgen.New(tx).CreateDatabaseBackupScheduleVersion(ctx, sqlcgen.CreateDatabaseBackupScheduleVersionParams{Component: in.Component, ScheduleID: id, Revision: int64(saved.Revision), ActorID: actor, ExpectedResourceGeneration: int64(in.ExpectedResourceGeneration), Frequency: in.Frequency, Timezone: in.Timezone, Enabled: in.Enabled})
		if err != nil {
			return zero, err
		}
		selection := in.RemoteTarget
		if !in.Enabled && existingID != "" && selection == nil {
			// Disabling is control over future discovery, not a request to
			// erase the historical destination selected by this schedule.
			selection, err = backupRemoteScheduleSelection(ctx, tx, id, saved.Revision-1)
			if err != nil {
				return zero, err
			}
		}
		if err = s.saveBackupRemoteScheduleSelection(ctx, tx, actor, resource, id, saved.Revision, in.Enabled, selection); err != nil {
			return zero, err
		}
		out := databaseBackupScheduleView(saved, value.Version)
		out.RemoteTarget = selection
		err = sqlcgen.New(tx).CreateDatabaseBackupScheduleRequest(ctx, sqlcgen.CreateDatabaseBackupScheduleRequestParams{ResourceID: resource, RequestID: in.RequestID, ActorID: actor, RequestHash: hash, ScheduleID: id, Revision: int64(saved.Revision), Response: buildJSON(out)})
		if err != nil {
			return zero, err
		}
		if err = databaseBackupAudit(ctx, tx, actor, id, "schedule_"+strconv.FormatInt(saved.Revision, 10), saved.ProjectID); err != nil {
			return zero, err
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

// ListDatabaseBackupSchedules lists authorized database backup schedules for a resource.
func (s *PostgresStore) ListDatabaseBackupSchedules(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupSchedule, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupSchedule, error) {
		value, err := databaseBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleViewer)
		if err != nil {
			return nil, err
		}
		rows, err := sqlcgen.New(tx).ListDatabaseBackupSchedules(ctx, resource)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupSchedule, 0, len(rows))
		for _, r := range rows {
			item := databaseBackupScheduleView(r, value.Version)
			item.RemoteTarget, err = backupRemoteScheduleSelection(ctx, tx, r.ID, r.Revision)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// ListDatabaseBackupOccurrences lists recorded occurrences of an authorized database backup
// schedule.
func (s *PostgresStore) ListDatabaseBackupOccurrences(ctx context.Context, actor, resource, id string) ([]domain.HostingDatabaseBackupOccurrence, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupOccurrence, error) {
		if _, err := databaseBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleViewer); err != nil {
			return nil, err
		}
		q := sqlcgen.New(tx)
		schedule, err := q.ReadDatabaseBackupSchedule(ctx, id)
		if err != nil {
			return nil, hostingError(err)
		}
		if schedule.ResourceID != resource {
			return nil, ErrNotFound
		}
		rows, err := q.ListDatabaseBackupOccurrences(ctx, id)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupOccurrence, 0, len(rows))
		for _, r := range rows {
			out = append(out, domain.HostingDatabaseBackupOccurrence{Component: r.Component, ID: r.ID, ScheduleID: r.ScheduleID, ProjectID: r.ProjectID, ResourceID: r.ResourceID, ScheduleRevision: r.ScheduleRevision, ScheduledAt: r.ScheduledAt, Frequency: r.Frequency, Timezone: r.Timezone, OperationID: proxyString(r.OperationID), State: r.State, Reason: r.Reason, OwnershipHeld: r.OwnershipHeld, CreatedAt: r.CreatedAt})
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

var _ HostingDatabaseBackupScheduleRepository = (*PostgresStore)(nil)
