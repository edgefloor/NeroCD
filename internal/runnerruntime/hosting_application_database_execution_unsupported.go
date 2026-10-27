//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeConnectedImageClaim(*attemptSupervisor, *runner.AttemptJournal, *attemptReporter, func(context.Context) error, string, string, domain.ClaimedRun, string, bool, *RuntimeEnvironmentConfig, *DatabaseExecutionConfig, ...*RegistryPullConfig) error {
	return errors.New("connected database execution requires native Linux arm64")
}

func reconcileApplicationDatabaseJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	for _, e := range j.Snapshot().Hosting {
		if e.ApplicationDatabase != nil {
			return errors.New("connected database journal unsupported on this platform")
		}
	}
	return nil
}

func replaySavedDatabaseMaintenanceCompletions(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	for _, e := range j.Snapshot().Hosting {
		if e.DatabaseMaintenance != nil {
			return errors.New("database maintenance journal unsupported on this platform")
		}
	}
	return nil
}
