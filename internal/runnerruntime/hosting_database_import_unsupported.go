//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func executeDatabaseImportClaim(*attemptSupervisor, *runner.AttemptJournal, func(context.Context) error, string, string, domain.ClaimedRun, string, *DatabaseExecutionConfig) error {
	return errors.New("external import requires qualified Linux")
}
func reconcileDatabaseImportJournal(_ context.Context, _ Config, j *runner.AttemptJournal) error {
	if len(j.Snapshot().DatabaseImports) == 0 {
		return nil
	}
	return errors.New("external import requires qualified Linux")
}
