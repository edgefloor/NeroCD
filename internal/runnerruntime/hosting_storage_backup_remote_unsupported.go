//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeStorageBackupTransferClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *StorageArchiveExecutionConfig) error {
	return errors.New("remote storage backups require native Linux arm64")
}
func reconcileStorageBackupTransferJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().StorageBackupTransfers) == 0 {
		return nil
	}
	return errors.New("remote storage backups require native Linux arm64")
}
