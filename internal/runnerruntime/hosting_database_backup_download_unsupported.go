//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeDatabaseBackupDownloadClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, *DatabaseExecutionConfig) error {
	return errors.New("database backup downloads require a native supported runner")
}
