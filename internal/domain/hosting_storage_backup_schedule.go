package domain

import (
	"errors"
	"time"
)

// HostingStorageBackupScheduleVersion identifies the storage backup schedule document format.
const HostingStorageBackupScheduleVersion = "hosting.storage-backup.schedule.v1"

// ErrInvalidStorageBackupSchedule reports invalid storage backup schedule input.
var ErrInvalidStorageBackupSchedule = errors.New("invalid storage backup schedule")

// HostingStorageBackupScheduleInput uses a whole-document compare-and-swap.
// Creation requires expected_version=0; updates retain the original schedule ID.
type HostingStorageBackupScheduleInput struct {
	RemoteTarget               *BackupTargetSelection `json:"remote_target,omitempty"`
	RequestID                  string                 `json:"request_id"`
	ExpectedResourceGeneration int64                  `json:"expected_resource_generation"`
	ExpectedVersion            int64                  `json:"expected_version"`
	Frequency                  string                 `json:"frequency"`
	Timezone                   string                 `json:"timezone"`
	Enabled                    bool                   `json:"enabled"`
}

// Validate checks the storage backup schedule input fields and invariants.
func (in HostingStorageBackupScheduleInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.ExpectedVersion < 0 || in.RemoteTarget != nil && in.RemoteTarget.Validate() != nil {
		return ErrInvalidStorageBackupSchedule
	}
	_, _, _, err := backupSchedule(in.Frequency, in.Timezone)
	if err != nil {
		return ErrInvalidStorageBackupSchedule
	}
	return nil
}

// HostingStorageBackupSchedule contains only safe resource-scoped metadata.
type HostingStorageBackupSchedule struct {
	RemoteTarget    *BackupTargetSelection `json:"remote_target,omitempty"`
	ID              string                 `json:"id"`
	Version         string                 `json:"version"`
	ProjectID       string                 `json:"project_id"`
	ResourceID      string                 `json:"resource_id"`
	Revision        int64                  `json:"revision"`
	Frequency       string                 `json:"frequency"`
	Timezone        string                 `json:"timezone"`
	Enabled         bool                   `json:"enabled"`
	LastScheduledAt *time.Time             `json:"last_scheduled_at,omitempty"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

// HostingStorageBackupOccurrence retains the admitted configuration even after
// edits or disablement. Operation state is projected from the linked operation.
type HostingStorageBackupOccurrence struct {
	ID               string    `json:"id"`
	ScheduleID       string    `json:"schedule_id"`
	ProjectID        string    `json:"project_id"`
	ResourceID       string    `json:"resource_id"`
	ScheduleRevision int64     `json:"schedule_revision"`
	ScheduledAt      time.Time `json:"scheduled_at"`
	Frequency        string    `json:"frequency"`
	Timezone         string    `json:"timezone"`
	BackupID         string    `json:"backup_id,omitempty"`
	OperationID      string    `json:"operation_id,omitempty"`
	State            string    `json:"state"`
	Reason           string    `json:"reason"`
	OwnershipHeld    bool      `json:"ownership_held"`
	CreatedAt        time.Time `json:"created_at"`
}
