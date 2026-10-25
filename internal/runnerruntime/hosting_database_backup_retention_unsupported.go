//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeDatabaseBackupRetentionClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *DatabaseExecutionConfig) error {
	return errors.New("backup retention requires native Linux arm64")
}
func reconcileDatabaseBackupRetentionJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().DatabaseBackupRetentions) == 0 {
		return nil
	}
	return errors.New("backup retention requires native Linux arm64")
}
