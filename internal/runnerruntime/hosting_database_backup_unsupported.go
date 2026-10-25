//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeDatabaseBackupClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *DatabaseExecutionConfig) error {
	return errors.New("database backup requires native Linux arm64")
}
func reconcileDatabaseBackupJournal(_ context.Context, _ Config, journal *runner.AttemptJournal) error {
	if len(journal.Snapshot().DatabaseBackup) == 0 {
		return nil
	}
	return errors.New("database backup requires native Linux arm64")
}
