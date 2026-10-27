//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeStorageBackupDownloadClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, *StorageArchiveExecutionConfig) error {
	return errors.New("storage archive downloads require a native supported runner")
}
