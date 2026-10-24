//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
)

func openStorageArchiveEngine(context.Context, StorageArchiveExecutionConfig, string) (storageArchiveExecutionEngine, error) {
	return nil, errors.New("storage archive execution requires native Linux arm64")
}
