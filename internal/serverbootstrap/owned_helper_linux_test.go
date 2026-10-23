//go:build linux

package serverbootstrap

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedUnitState(t *testing.T) {
	path := "/etc/systemd/system/nerocd-agent-" + strings.Repeat("a", 32) + ".service"
	valid := "ActiveState=active\nInvocationID=" + strings.Repeat("b", 32) + "\nJob=0\nFragmentPath=" + path + "\nDropInPaths=\nLoadState=loaded"
	active, _, pending, code := parseUnitState(valid, path)
	if code != "" || !active || pending {
		t.Fatal("valid loaded unit rejected")
	}
	for name, output := range map[string]string{
		"foreign fragment": strings.Replace(valid, path, "/usr/lib/systemd/system/foreign.service", 1),
		"exact dropin":     strings.Replace(valid, "DropInPaths=", "DropInPaths="+path+".d/override.conf", 1),
		"type wide dropin": strings.Replace(valid, "DropInPaths=", "DropInPaths=/etc/systemd/system/service.d/override.conf", 1),
		"unloaded":         strings.Replace(valid, "LoadState=loaded", "LoadState=not-found", 1),
		"duplicate":        valid + "\nActiveState=active",
		"unknown":          valid + "\nUnexpected=value",
		"missing":          "ActiveState=active",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, code := parseUnitState(output, path); code == "" {
				t.Fatal("changed or invalid unit configuration admitted")
			}
		})
	}
}

func TestOwnedStopEvidence(t *testing.T) {
	result, code, status, err := parseStopEvidence("Result=exit-code\nExecMainCode=1\nExecMainStatus=1")
	if err != nil || result != "exit-code" || code != 1 || status != 1 {
		t.Fatalf("stop failure evidence changed: %s %d %d %v", result, code, status, err)
	}
	for _, output := range []string{"", "Result=success", "Result=raw arbitrary output\nExecMainCode=1\nExecMainStatus=1", "Result=success\nResult=signal\nExecMainCode=1\nExecMainStatus=1", "Result=success\nExecMainCode=7\nExecMainStatus=1", "Result=success\nExecMainCode=1\nExecMainStatus=256"} {
		if _, _, _, err := parseStopEvidence(output); err == nil {
			t.Fatalf("invalid stop evidence admitted: %q", output)
		}
	}
}

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
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := removeOwnedAt(fd, "link", false, 0600); err != OwnershipConflict {
		t.Fatalf("anchored unlink admitted replacement symlink: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("foreign symlink was removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("foreign symlink target was removed")
	}
}
