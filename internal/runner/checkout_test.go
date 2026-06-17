package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"nerocd/internal/domain"
)

func TestExecuteCheckoutClonesLocalRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	source := t.TempDir()
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.local")
	runGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")

	dest, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: source}, DestPath: "repo"}, t.TempDir(), func(ProcessEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("checkout did not clone README.md: %v", err)
	}
}

func TestExecuteCheckoutRejectsUnsafeDestPath(t *testing.T) {
	_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "file:///tmp/repo"}, DestPath: "../repo"}, t.TempDir(), func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsafe dest_path error")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}
