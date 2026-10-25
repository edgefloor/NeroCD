//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeDatabaseBackupTransferClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *DatabaseExecutionConfig) error {
	return errors.New("remote database backups require native Linux arm64")
}
func reconcileDatabaseBackupTransferJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().DatabaseBackupTransfers) == 0 {
		return nil
	}
	return errors.New("remote database backups require native Linux arm64")
}
