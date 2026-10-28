package runnerruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func createAttemptWorkspace(workDir string) (string, func(), error) {
	if strings.TrimSpace(workDir) == "" {
		return "", nil, errors.New("attempt workspace root is required")
	}
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return "", nil, err
	}
	path, err := filepath.Abs(workDir)
	if err != nil {
		return "", nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", nil, err
	}
	attempt, err := os.MkdirTemp(path, "nerocd-attempt-")
	if err != nil {
		_ = root.Close()
		return "", nil, err
	}
	return attempt, func() {
		_ = root.RemoveAll(filepath.Base(attempt))
		_ = root.Close()
	}, nil
}
