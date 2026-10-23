//go:build linux

package serverbootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedFilesystemBoundaries(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned filesystem assertions run in the disposable target")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "receipt")
	if err := writeOwned(path, []byte("original"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeOwned(path, []byte("collision"), false); err == nil {
		t.Fatal("exclusive create replaced receipt")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("receipt changed after collision")
	}
	// The failed exclusive create deliberately leaves a pending file. It blocks
	// subsequent writes rather than guessing that it is safe to replace.
	if err := writeOwned(path, []byte("replace"), true); err == nil {
		t.Fatal("pending file did not fence another writer")
	}
	if err := os.Remove(path + ".pending"); err != nil {
		t.Fatal(err)
	}
	if err := writeOwned(path, []byte("completed"), true); err != nil {
		t.Fatal(err)
	}
	if data, err := readOwned(path, 32, 0600); err != nil || string(data) != "completed" {
		t.Fatal("exact owned replacement failed")
	}
	if _, err := readOwned(path, 2, 0600); err == nil {
		t.Fatal("oversized receipt admitted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwned(link, 32, 0600); err == nil {
		t.Fatal("symlink admitted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if requireOwned(path, false, 0600) == nil {
		t.Fatal("permissive ownership admitted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 1001, 1001); err != nil {
		t.Fatal(err)
	}
	if requireOwned(path, false, 0600) == nil {
		t.Fatal("foreign ownership admitted")
	}
}
