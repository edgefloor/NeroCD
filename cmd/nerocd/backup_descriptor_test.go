//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupStagingRetainsAdmittedDirectory(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root := privateBackupTestDir(t)
			ancestor := filepath.Join(root, "ancestor")
			output := filepath.Join(ancestor, "output")
			if err := os.MkdirAll(output, 0700); err != nil {
				t.Fatal(err)
			}
			parent, err := openSecureBackupDirectory(output)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parent.Close() })
			moved := substituteBackupAncestor(t, ancestor, replacement)
			staging, err := createBackupDirectoryAt(parent, ".staging")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = staging.Close() })
			dump, err := createBackupFileAt(staging, "database.dump")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dump.Close() })
			previous := backupCommand
			t.Cleanup(func() { backupCommand = previous })
			var command *exec.Cmd
			backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
				if tool != "pg_dump" || strings.Contains(strings.Join(args, " "), "--file") {
					t.Fatal("dump must use its admitted output descriptor")
				}
				command = exec.CommandContext(ctx, "sh", "-c", "printf 'original database dump'")
				return command
			}
			if err := runBackupToolIO(t.Context(), "pg_dump", "postgres://owner@localhost/test", nil, dump, "--format=custom"); err != nil {
				t.Fatal(err)
			}
			if command.Stdout != dump {
				t.Fatal("dump was not passed directly to the child")
			}
			if err := dump.Sync(); err != nil {
				t.Fatal(err)
			}
			checksum, err := checksumOpenBackupFile(dump, "database.dump")
			if err != nil {
				t.Fatal(err)
			}
			manifest := backupManifest{Version: 1, CreatedAt: time.Now(), ApplicationVersion: "test", SchemaIdentity: "test", Database: "test", Files: []backupFile{checksum}}
			if err := writeBackupManifestAt(staging, manifest); err != nil {
				t.Fatal(err)
			}
			if err := publishBackupDirectoryAt(parent, ".staging", "backup-test"); err != nil {
				t.Fatal(err)
			}
			// A durability error after rename must not let deferred cleanup erase the
			// published archive, even when the old staging name no longer exists.
			removeBackupStagingAt(parent, staging, ".staging")
			if _, err := verifyBackupArchive(filepath.Join(moved, "output", "backup-test")); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement output received backup data: %v, %v", entries, err)
			}
			info, err := os.Stat(filepath.Join(moved, "output", "backup-test", "database.dump"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("dump is not private: %v", err)
			}
		})
	}
}

func substituteBackupAncestor(t *testing.T, ancestor, replacement string) string {
	t.Helper()
	moved := ancestor + "-original"
	if err := os.Rename(ancestor, moved); err != nil {
		t.Fatal(err)
	}
	target := ancestor
	if replacement == "symlink" {
		target = ancestor + "-replacement"
	}
	if err := os.MkdirAll(filepath.Join(target, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	if replacement == "symlink" {
		if err := os.Symlink(target, ancestor); err != nil {
			t.Fatal(err)
		}
	}
	return moved
}

func TestBackupFailureCleansPinnedStagingAfterAncestorReplacement(t *testing.T) {
	root := privateBackupTestDir(t)
	ancestor := filepath.Join(root, "ancestor")
	output := filepath.Join(ancestor, "output")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_MODE", "development")
	previous := backupCommand
	t.Cleanup(func() { backupCommand = previous })
	var moved string
	backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
		moved = substituteBackupAncestor(t, ancestor, "symlink")
		return exec.CommandContext(ctx, "sh", "-c", "printf 'private dump'; exit 1")
	}
	err := backupDatabaseContext(t.Context(), []string{"--database-url", "postgres://owner@127.0.0.1:1/test?connect_timeout=1", "--output-dir", output})
	if err == nil || !strings.Contains(err.Error(), "pg_dump failed") {
		t.Fatalf("unexpected failure: %v", err)
	}
	for _, path := range []string{output, filepath.Join(moved, "output")} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("dump or staging remains at %s: %v, %v", path, entries, err)
		}
	}
}

func TestRestoreUsesVerifiedDumpAfterFilesystemSubstitution(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink", "dump"} {
		t.Run(replacement, func(t *testing.T) {
			root := privateBackupTestDir(t)
			ancestor := filepath.Join(root, "ancestor")
			archive := filepath.Join(ancestor, "archive")
			if err := os.MkdirAll(archive, 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("checksum verified database archive")
			if err := os.WriteFile(filepath.Join(archive, "database.dump"), original, 0600); err != nil {
				t.Fatal(err)
			}
			checksum := sha256.Sum256(original)
			manifest := backupManifest{Version: 1, CreatedAt: time.Now(), ApplicationVersion: "test", SchemaIdentity: "test", Database: "test", Files: []backupFile{{Path: "database.dump", SHA256: hex.EncodeToString(checksum[:]), Bytes: int64(len(original))}}}
			directory, err := openSecureBackupDirectory(archive)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			if err := writeBackupManifestAt(directory, manifest); err != nil {
				t.Fatal(err)
			}
			_, dump, err := openVerifiedBackupArchive(archive)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dump.Close() })
			if replacement == "dump" {
				if err := os.Rename(filepath.Join(archive, "database.dump"), filepath.Join(archive, "original.dump")); err != nil {
					t.Fatal(err)
				}
			} else {
				substituteBackupAncestor(t, ancestor, replacement)
				if err := os.Mkdir(archive, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(archive, "database.dump"), []byte("unverified replacement archive"), 0600); err != nil {
				t.Fatal(err)
			}
			previous := backupCommand
			t.Cleanup(func() { backupCommand = previous })
			var command *exec.Cmd
			backupCommand = func(ctx context.Context, tool string, args ...string) *exec.Cmd {
				if tool != "pg_restore" || len(args) != 2 || args[1] != "--single-transaction" {
					t.Fatal("restore must not receive a dump pathname", args)
				}
				command = exec.CommandContext(ctx, "sh", "-c", "cat")
				return command
			}
			var restored bytes.Buffer
			if err := runBackupToolIO(t.Context(), "pg_restore", "postgres://owner@localhost/test", dump, &restored, "--single-transaction"); err != nil {
				t.Fatal(err)
			}
			if command.Stdin != dump || !bytes.Equal(restored.Bytes(), original) {
				t.Fatalf("restore did not read the verified descriptor: %q", restored.String())
			}
		})
	}
}

func TestBackupFileCreationDoesNotFollowExistingEntry(t *testing.T) {
	root := privateBackupTestDir(t)
	directory, err := openSecureBackupDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "database.dump")); err != nil {
		t.Fatal(err)
	}
	if file, err := createBackupFileAt(directory, "database.dump"); err == nil {
		_ = file.Close()
		t.Fatal("existing symlink was accepted")
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("victim changed: %q, %v", raw, err)
	}
}
