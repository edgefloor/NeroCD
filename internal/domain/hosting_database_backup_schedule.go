package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Persisted IANA names must work in the minimal server image.

	"github.com/robfig/cron/v3"
)

// HostingDatabaseBackupScheduleVersion identifies the database backup schedule document format.
const HostingDatabaseBackupScheduleVersion = "postgresql.logical-local.schedule.v1"

// HostingDatabaseMySQLBackupScheduleVersion identifies a native MySQL local schedule.
const HostingDatabaseMySQLBackupScheduleVersion = "mysql.logical-local.schedule.v1"

// HostingDatabaseBackupComponentScheduleVersion identifies a Compose component schedule.
const HostingDatabaseBackupComponentScheduleVersion = "postgresql.compose-logical-local.schedule.v1"

// HostingDatabaseMySQLBackupComponentScheduleVersion identifies a local Compose MySQL schedule.
const HostingDatabaseMySQLBackupComponentScheduleVersion = "mysql.compose-logical-local.schedule.v1"

// HostingDatabaseBackupComponentScheduleVersionFor maps saved component engine identity.
func HostingDatabaseBackupComponentScheduleVersionFor(backupVersion string) string {
	switch backupVersion {
	case HostingDatabaseBackupComponentVersion:
		return HostingDatabaseBackupComponentScheduleVersion
	case HostingDatabaseMySQLBackupComponentVersion:
		return HostingDatabaseMySQLBackupComponentScheduleVersion
	default:
		return ""
	}
}

// ErrInvalidDatabaseBackupSchedule reports invalid database backup schedule input.
var ErrInvalidDatabaseBackupSchedule = errors.New("invalid database backup schedule")

// HostingDatabaseBackupScheduleVersionFor admits only qualified native recipes.
func HostingDatabaseBackupScheduleVersionFor(engine, recipe string) string {
	switch HostingDatabaseBackupVersionFor(engine, recipe) {
	case HostingDatabaseBackupVersion:
		return HostingDatabaseBackupScheduleVersion
	case HostingDatabaseMySQLBackupVersion:
		return HostingDatabaseMySQLBackupScheduleVersion
	default:
		return ""
	}
}

// HostingDatabaseBackupScheduleInput uses a whole-document compare-and-swap.
// Creation requires expected_version=0; updates retain the original schedule ID.
type HostingDatabaseBackupScheduleInput struct {
	Component                   string                 `json:"component,omitempty"`
	ExpectedDeployedOperationID string                 `json:"expected_deployed_operation_id,omitempty"`
	RemoteTarget                *BackupTargetSelection `json:"remote_target,omitempty"`
	RequestID                   string                 `json:"request_id"`
	ExpectedResourceGeneration  int64                  `json:"expected_resource_generation"`
	ExpectedVersion             int64                  `json:"expected_version"`
	Frequency                   string                 `json:"frequency"`
	Timezone                    string                 `json:"timezone"`
	Enabled                     bool                   `json:"enabled"`
}

var backupScheduleAliases = map[string]string{
	"every_minute": "* * * * *", "hourly": "0 * * * *", "daily": "0 0 * * *", "weekly": "0 0 * * 0", "monthly": "0 0 1 * *", "yearly": "0 0 1 1 *",
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *",
}
var backupScheduleFixedHours = regexp.MustCompile(`^[0-9,\-]+$`)

func backupSchedule(frequency, zone string) (cron.Schedule, string, *time.Location, error) {
	if len(frequency) < 1 || len(frequency) > 128 || strings.TrimSpace(frequency) != frequency || len(zone) < 1 || len(zone) > 128 || strings.TrimSpace(zone) != zone || strings.ContainsAny(zone, "\x00\r\n\\") || strings.HasPrefix(zone, "/") || strings.Contains(zone, "..") {
		return nil, "", nil, ErrInvalidDatabaseBackupSchedule
	}
	expression := frequency
	if alias, ok := backupScheduleAliases[frequency]; ok {
		expression = alias
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, "", nil, ErrInvalidDatabaseBackupSchedule
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return nil, "", nil, ErrInvalidDatabaseBackupSchedule
	}
	parsed, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expression)
	if err != nil {
		return nil, "", nil, ErrInvalidDatabaseBackupSchedule
	}
	return parsed, fields[1], location, nil
}

// Validate checks the database backup schedule input fields and invariants.
func (in HostingDatabaseBackupScheduleInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.ExpectedVersion < 0 || in.RemoteTarget != nil && in.RemoteTarget.Validate() != nil {
		return ErrInvalidDatabaseBackupSchedule
	}
	if in.Component == "" {
		if in.ExpectedDeployedOperationID != "" {
			return ErrInvalidDatabaseBackupSchedule
		}
	} else if !stackName.MatchString(in.Component) || (in.ExpectedDeployedOperationID != "" && !hostingProxyID.MatchString(in.ExpectedDeployedOperationID)) || ((in.Enabled || in.ExpectedVersion == 0) && in.ExpectedDeployedOperationID == "") {
		return ErrInvalidDatabaseBackupSchedule
	}
	_, _, _, err := backupSchedule(in.Frequency, in.Timezone)
	return err
}

// NewestHostingDatabaseBackupOccurrence considers at most eleven minute slots,
// never enumerating an outage backlog. The inclusive ten-minute comparison uses
// the actual tick time, as upstream does, rather than rounding its lower bound.
func NewestHostingDatabaseBackupOccurrence(frequency, zone string, now time.Time, cursor *time.Time) (time.Time, bool, error) {
	schedule, hours, location, err := backupSchedule(frequency, zone)
	if err != nil {
		return time.Time{}, false, err
	}
	for candidate := now.UTC().Truncate(time.Minute); !candidate.Before(now.Add(-10 * time.Minute)); candidate = candidate.Add(-time.Minute) {
		local := candidate.In(location)
		if !schedule.Next(local.Add(-time.Minute)).Equal(candidate) {
			continue
		}
		if cursor != nil {
			if !candidate.After(*cursor) {
				return time.Time{}, false, nil
			}
			if backupScheduleFixedHours.MatchString(hours) && cursor.In(location).Format("2006-01-02 15:04") == local.Format("2006-01-02 15:04") {
				return time.Time{}, false, nil
			}
		}
		return candidate, true, nil
	}
	return time.Time{}, false, nil
}

// HostingDatabaseBackupSchedule contains only safe resource-scoped metadata.
type HostingDatabaseBackupSchedule struct {
	Component       string                 `json:"component,omitempty"`
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

// HostingDatabaseBackupOccurrence retains the admitted configuration even after
// edits or disablement. Operation state is projected from the linked operation.
type HostingDatabaseBackupOccurrence struct {
	Component        string    `json:"component,omitempty"`
	ID               string    `json:"id"`
	ScheduleID       string    `json:"schedule_id"`
	ProjectID        string    `json:"project_id"`
	ResourceID       string    `json:"resource_id"`
	ScheduleRevision int64     `json:"schedule_revision"`
	ScheduledAt      time.Time `json:"scheduled_at"`
	Frequency        string    `json:"frequency"`
	Timezone         string    `json:"timezone"`
	OperationID      string    `json:"operation_id,omitempty"`
	State            string    `json:"state"`
	Reason           string    `json:"reason"`
	OwnershipHeld    bool      `json:"ownership_held"`
	CreatedAt        time.Time `json:"created_at"`
}
