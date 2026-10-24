//go:build !unix

package runnerruntime

import (
	"errors"

	"nerocd/internal/runner"
)

func newAutomaticDatabaseNetworkCaptureDispatcher(c Config, _ *runner.AttemptJournal) (automaticDatabaseNetworkCaptureDispatcher, error) {
	if c.Database != nil && c.Database.NetworkCapture {
		return nil, errors.New("automatic database capture requires native Linux arm64")
	}
	return nil, nil
}
