package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingStorageBackupScheduleRepository retains schedules, historical requests
// and UTC occurrences; execution always uses HostingStorageArchiveRepository.
type HostingStorageBackupScheduleRepository interface {
	SaveStorageBackupSchedule(context.Context, string, string, string, string, domain.HostingStorageBackupScheduleInput) (domain.HostingStorageBackupSchedule, error)
	ListStorageBackupSchedules(context.Context, string, string) ([]domain.HostingStorageBackupSchedule, error)
	ListStorageBackupOccurrences(context.Context, string, string, string) ([]domain.HostingStorageBackupOccurrence, error)
}
