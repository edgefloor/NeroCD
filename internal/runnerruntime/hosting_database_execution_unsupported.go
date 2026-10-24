//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func validateDatabasePlatformConfig(DatabaseExecutionConfig) error {
	return errors.New("database execution requires native Linux arm64")
}
func executeDatabaseClaim(*attemptSupervisor, *runner.AttemptJournal, *attemptReporter, string, string, domain.ClaimedRun, string, *DatabaseExecutionConfig) error {
	return errors.New("database execution unsupported on this platform")
}
func reconcileHostingDatabaseJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	for _, entry := range j.Snapshot().Hosting {
		if runner.HostingJournalUsesDatabase(entry) {
			return errors.New("database journal unsupported on this platform")
		}
	}
	return nil
}

// ValidateDatabaseExecution rejects unsupported host platforms.
func ValidateDatabaseExecution(context.Context, DatabaseExecutionConfig, string) error {
	return errors.New("database execution requires native Linux arm64")
}
