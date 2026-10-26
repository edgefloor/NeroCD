//go:build !unix

package runnerruntime

import (
	"context"
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeStorageBackupRetentionClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *StorageArchiveExecutionConfig) error {
	return errors.New("backup retention requires native Linux arm64")
}
func reconcileStorageBackupRetentionJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().StorageBackupRetentions) == 0 {
		return nil
	}
	return errors.New("backup retention requires native Linux arm64")
}
