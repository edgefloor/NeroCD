package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func databaseBackupScheduleView(r sqlcgen.HostingDatabaseBackupSchedule) domain.HostingDatabaseBackupSchedule {
	return domain.HostingDatabaseBackupSchedule{ID: r.ID, Version: domain.HostingDatabaseBackupScheduleVersion, ProjectID: r.ProjectID, ResourceID: r.ResourceID, Revision: r.Revision, Frequency: r.Frequency, Timezone: r.Timezone, Enabled: r.Enabled, LastScheduledAt: r.LastScheduledAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// Placement admission precedes the schedule lock in both writers and controller
// passes. Current actor checks do not inherit a former owner's privileges.
func databaseBackupScheduleAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (domain.HostingDatabase, error) {
	var zero domain.HostingDatabase
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
	if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
		return zero, err
	}
	value, err := hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return zero, err
	}
	if value.Engine != "postgresql" || value.RecipeVersion != domain.HostingDatabasePostgresRecipe {
		return zero, ErrConflict
	}
	return value, proxyCheckLegacyToken(ctx, tx, actor)
}

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
		if existingID == "" {
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM hosting_database_backup_schedules WHERE resource_id=$1`, resource).Scan(&count); err != nil {
				return zero, err
			}
			if count >= 100 {
				return zero, ErrConflict
			}
			_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled) VALUES($1,$2,$3,$4,$5,$6,1,$7,$8,$9)`, id, value.Resource.ProjectID, resource, value.Binding.ID, value.Binding.DestinationID, actor, in.Frequency, in.Timezone, in.Enabled)
		} else {
			prior, e := q.LockDatabaseBackupSchedule(ctx, existingID)
			if e != nil {
				return zero, hostingError(e)
			}
			if prior.ResourceID != resource || prior.ProjectID != value.Resource.ProjectID || prior.Revision != in.ExpectedVersion || prior.BindingID != value.Binding.ID || prior.DestinationID != value.Binding.DestinationID {
				return zero, ErrConflict
			}
			id = existingID
			_, err = tx.Exec(ctx, `UPDATE hosting_database_backup_schedules SET actor_id=$2,revision=revision+1,frequency=$3,timezone=$4,enabled=$5,updated_at=clock_timestamp() WHERE id=$1`, id, actor, in.Frequency, in.Timezone, in.Enabled)
		}
		if err != nil {
			return zero, hostingError(err)
		}
		saved, err := q.ReadDatabaseBackupSchedule(ctx, id)
		if err != nil {
			return zero, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, saved.Revision, actor, in.ExpectedResourceGeneration, in.Frequency, in.Timezone, in.Enabled)
		if err != nil {
			return zero, err
		}
		out := databaseBackupScheduleView(saved)
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_requests(resource_id,request_id,actor_id,request_hash,schedule_id,revision,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, resource, in.RequestID, actor, hash, id, saved.Revision, buildJSON(out))
		if err != nil {
			return zero, err
		}
		if err = databaseBackupAudit(ctx, tx, actor, id, "schedule_"+strconv.FormatInt(saved.Revision, 10), saved.ProjectID); err != nil {
			return zero, err
		}
		return out, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListDatabaseBackupSchedules(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupSchedule, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupSchedule, error) {
		if _, err := databaseBackupScheduleAuthority(ctx, tx, actor, resource, domain.RoleViewer); err != nil {
			return nil, err
		}
		rows, err := sqlcgen.New(tx).ListDatabaseBackupSchedules(ctx, resource)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupSchedule, 0, len(rows))
		for _, r := range rows {
			out = append(out, databaseBackupScheduleView(r))
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
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
			out = append(out, domain.HostingDatabaseBackupOccurrence{ID: r.ID, ScheduleID: r.ScheduleID, ProjectID: r.ProjectID, ResourceID: r.ResourceID, ScheduleRevision: r.ScheduleRevision, ScheduledAt: r.ScheduledAt, Frequency: r.Frequency, Timezone: r.Timezone, OperationID: proxyString(r.OperationID), State: r.State, Reason: r.Reason, OwnershipHeld: r.OwnershipHeld, CreatedAt: r.CreatedAt})
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

var _ HostingDatabaseBackupScheduleRepository = (*PostgresStore)(nil)
