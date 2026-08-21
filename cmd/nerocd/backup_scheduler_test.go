package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupScheduleBackoffIsBoundedAndMonotonic(t *testing.T) {
	interval := 15 * 60
	previous := time.Duration(0)
	for failures := 0; failures < 12; failures++ {
		gotFailures, delay := backupScheduleBackoff(interval, failures)
		if gotFailures < 1 || gotFailures > 8 || delay < time.Minute || delay > time.Duration(interval)*time.Second || delay < previous {
			t.Fatalf("failures=%d -> count=%d delay=%s", failures, gotFailures, delay)
		}
		previous = delay
	}
	if gotFailures, delay := backupScheduleBackoff(60, 8); gotFailures != 8 || delay != time.Minute {
		t.Fatalf("capped backoff=(%d,%s)", gotFailures, delay)
	}
}

func TestRotateSecureBackupsKeepsOnlyCanonicalNewestDirectories(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backup-20260101T000000Z-a", "backup-20260102T000000Z-b", "backup-20260103T000000Z-c"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"database.dump", "manifest.json"} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := rotateSecureBackups(root, 2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "backup-20260102T000000Z-b" || entries[1].Name() != "backup-20260103T000000Z-c" {
		t.Fatalf("rotation entries=%v", entries)
	}
}

func TestRotateSecureBackupsFailsClosedOnUnexpectedOrUnsafeEntry(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "backup-20260101T000000Z-a")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"database.dump", "manifest.json", "unexpected"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := rotateSecureBackups(root, 1); err != nil {
		t.Fatal(err) // retention has nothing to remove, so no archive is touched.
	}
	second := filepath.Join(root, "backup-20260102T000000Z-b")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"database.dump", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(second, file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := rotateSecureBackups(root, 1); err == nil {
		t.Fatal("unexpected archive content was removed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("unsafe archive unexpectedly deleted: %v", err)
	}
}

func TestBackupDatabaseContextHonorsSupervisorCancellation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	t.Setenv("NEROCD_MODE", "development")
	previous := backupCommand
	backupCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 1")
	}
	t.Cleanup(func() { backupCommand = previous })
	err = backupDatabaseContext(ctx, []string{"--database-url", "postgres://user:password@db/nerocd", "--output-dir", root})
	if err == nil || ctx.Err() != context.Canceled {
		t.Fatalf("canceled backup error=%v context=%v", err, ctx.Err())
	}
}
