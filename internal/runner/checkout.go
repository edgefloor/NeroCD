package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
	"nerocd/internal/source"
)

// ExecuteCheckout performs plan in an isolated directory beneath workRoot.
func ExecuteCheckout(ctx context.Context, plan domain.CheckoutPlan, workRoot string, emit func(ProcessEvent)) (string, error) {
	repository := plan.Repository
	repoURL := strings.TrimSpace(repository.URL)
	if repoURL == "" {
		return "", errors.New("checkout repository url is required")
	}
	if err := source.ValidateRepositoryURL(repoURL); err != nil {
		return "", err
	}
	dest := strings.TrimSpace(plan.DestPath)
	if dest == "" {
		dest = "workspace"
	}
	dest, err := cleanRelativePath(dest, "checkout dest_path")
	if err != nil {
		return "", err
	}
	workRoot = strings.TrimSpace(workRoot)
	if workRoot == "" {
		workRoot = os.TempDir()
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return "", err
	}
	destPath := filepath.Join(workRoot, dest)
	if err := os.RemoveAll(destPath); err != nil {
		return "", err
	}

	args := []string{"clone", "--depth", "1"}
	if ref := strings.TrimSpace(repository.Ref); ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, destPath)
	emit(ProcessEvent{Stream: domain.LogSystem, Message: "Runner checking out repository"})
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			emit(ProcessEvent{Stream: domain.LogSystem, Message: line})
		}
	}
	if err != nil {
		return "", err
	}
	return destPath, nil
}
