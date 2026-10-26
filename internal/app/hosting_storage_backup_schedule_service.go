package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func (s *Service) storageBackupScheduleRepository() (store.HostingStorageBackupScheduleRepository, error) {
	repo, ok := s.runs.(store.HostingStorageBackupScheduleRepository)
	if !ok {
		return nil, store.ErrStorageArchiveUnavailable
	}
	return repo, nil
}

// SaveStorageBackupSchedule creates or revises an authorized storage backup schedule.
func (s *Service) SaveStorageBackupSchedule(ctx context.Context, resource, schedule string, in domain.HostingStorageBackupScheduleInput) (domain.HostingStorageBackupSchedule, error) {
	var zero domain.HostingStorageBackupSchedule
	if in.Validate() != nil || schedule == "" && in.ExpectedVersion != 0 || schedule != "" && in.ExpectedVersion < 1 {
		return zero, InvalidInput(domain.ErrInvalidStorageBackupSchedule)
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
	if r.Kind != "application" {
		return zero, store.ErrNotFound
	}
	repo, err := s.storageBackupScheduleRepository()
	if err != nil {
		return zero, err
	}
	id, err := prefixedID("ssb")
	if err != nil {
		return zero, err
	}
	return repo.SaveStorageBackupSchedule(ctx, id, resource, actor.ID, schedule, in)
}

// ListStorageBackupSchedules lists authorized storage backup schedules for a resource.
func (s *Service) ListStorageBackupSchedules(ctx context.Context, resource string) ([]domain.HostingStorageBackupSchedule, error) {
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.storageBackupScheduleRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListStorageBackupSchedules(ctx, actor.ID, resource)
}

// ListStorageBackupOccurrences lists recorded occurrences of an authorized storage backup
// schedule.
func (s *Service) ListStorageBackupOccurrences(ctx context.Context, resource, schedule string) ([]domain.HostingStorageBackupOccurrence, error) {
	if err := validateDestinationID(schedule, "schedule_id"); err != nil {
		return nil, err
	}
	_, actor, err := s.resourceForDestinationRole(ctx, resource, domain.RoleViewer)
	if err != nil {
		return nil, err
	}
	repo, err := s.storageBackupScheduleRepository()
	if err != nil {
		return nil, err
	}
	return repo.ListStorageBackupOccurrences(ctx, actor.ID, resource, schedule)
}
