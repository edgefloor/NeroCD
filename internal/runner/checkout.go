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

	configDir, err := os.MkdirTemp(workRoot, "nerocd-checkout-config-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(configDir) }()
	args := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "clone", "--template=" + configDir, "--depth", "1"}
	if ref := strings.TrimSpace(repository.Ref); ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, destPath)
	emit(ProcessEvent{Stream: domain.LogSystem, Message: "Runner checking out repository"})
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = configDir
	cmd.Env = append(processEnvironment(nil), "HOME="+configDir, "XDG_CONFIG_HOME="+configDir,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_ALLOW_PROTOCOL=https:http")
	// Git diagnostics can echo URLs, helper output, and credentials. Keep them
	// out of durable runner events, including when clone fails.
	if err := cmd.Run(); err != nil {
		return "", errors.New("repository checkout failed")
	}
	return destPath, nil
}
