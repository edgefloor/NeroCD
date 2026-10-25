package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingDatabaseBackupScheduleRepository retains schedules, historical requests
// and UTC occurrences; execution always uses HostingDatabaseBackupRepository.
type HostingDatabaseBackupScheduleRepository interface {
	SaveDatabaseBackupSchedule(context.Context, string, string, string, string, domain.HostingDatabaseBackupScheduleInput) (domain.HostingDatabaseBackupSchedule, error)
	ListDatabaseBackupSchedules(context.Context, string, string) ([]domain.HostingDatabaseBackupSchedule, error)
	ListDatabaseBackupOccurrences(context.Context, string, string, string) ([]domain.HostingDatabaseBackupOccurrence, error)
}
