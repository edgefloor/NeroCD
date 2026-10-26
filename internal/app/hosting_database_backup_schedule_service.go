package app

import (
	"context"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) databaseBackupScheduleRepository() (store.HostingDatabaseBackupScheduleRepository, error) {
	repo, ok := s.hosting.(store.HostingDatabaseBackupScheduleRepository)
	if !ok {
		return nil, store.ErrHostingDatabaseUnavailable
	}
	return repo, nil
}

// SaveDatabaseBackupSchedule creates or revises an authorized database backup schedule.
func (s *Service) SaveDatabaseBackupSchedule(ctx context.Context, resource, schedule string, in domain.HostingDatabaseBackupScheduleInput) (domain.HostingDatabaseBackupSchedule, error) {
	var zero domain.HostingDatabaseBackupSchedule
	if in.Validate() != nil || schedule == "" && in.ExpectedVersion != 0 || schedule != "" && in.ExpectedVersion < 1 {
		return zero, InvalidInput(domain.ErrInvalidDatabaseBackupSchedule)
	}
	if schedule != "" {
		if err := validateDestinationID(schedule, "schedule_id"); err != nil {
			return zero, err
		}
	}
	r, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if r.Kind != "database" {
		return zero, store.ErrNotFound
	}
	repo, err := s.databaseBackupScheduleRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("dbs")
	if err != nil {
		return zero, err
	}
	return repo.SaveDatabaseBackupSchedule(ctx, id, resource, actor.ID, schedule, in)
}

// ListDatabaseBackupSchedules lists authorized database backup schedules for a resource.
func (s *Service) ListDatabaseBackupSchedules(ctx context.Context, resource string) ([]domain.HostingDatabaseBackupSchedule, error) {
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.databaseBackupScheduleRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListDatabaseBackupSchedules(ctx, actor.ID, resource)
}

// ListDatabaseBackupOccurrences lists recorded occurrences of an authorized database backup
// schedule.
func (s *Service) ListDatabaseBackupOccurrences(ctx context.Context, resource, schedule string) ([]domain.HostingDatabaseBackupOccurrence, error) {
	if err := validateDestinationID(schedule, "schedule_id"); err != nil {
		return nil, err
	}
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.databaseBackupScheduleRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListDatabaseBackupOccurrences(ctx, actor.ID, resource, schedule)
}
