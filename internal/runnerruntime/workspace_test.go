package runnerruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttemptWorkspacesAreIsolatedAndCleanupConfined(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	first, cleanFirst, err := createAttemptWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanFirst()
	second, cleanSecond, err := createAttemptWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanSecond()
	if first == second {
		t.Fatal("attempt workspace reused")
	}
	target := filepath.Join(outside, "keep")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(first, "repository-link")); err != nil {
		t.Fatal(err)
	}
	cleanFirst()
	if _, err := os.Stat(target); err != nil {
		t.Fatal("cleanup traversed repository symlink", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("cleanup removed another attempt", err)
	}
}
