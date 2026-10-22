package hostingbuild

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancellationJoinsTraversalBeforeCleanup(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "one"), []byte("one"))
	writeTestFile(t, filepath.Join(dir, "two"), []byte("two"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	exited := make(chan struct{})
	cleanupStarted := make(chan struct{})
	visited := 0
	go func() {
		err := scanTree(newScanBudget(ctx), dir, func(_ string, info fs.FileInfo) error {
			if info.IsDir() {
				return nil
			}
			visited++
			if visited != 1 {
				return nil
			}
			close(entered)
			// Handshake pauses at an entry, without a large tree or resource stress.
			<-ctx.Done()
			// The callback succeeds. The traversal itself must notice cancellation.
			return nil
		})
		close(exited)
		done <- err
	}()
	t.Cleanup(func() { cancel(); <-exited })
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("traversal did not enter fixture")
	}
	joined := make(chan error, 1)
	go func() { err := joinMonitor(cancel, done); close(cleanupStarted); joined <- err }()
	select {
	case err := <-joined:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("join: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled traversal blocked cleanup")
	}
	select {
	case <-exited:
	default:
		t.Fatal("cleanup could outlive traversal join")
	}
	select {
	case <-cleanupStarted:
	default:
		t.Fatal("cleanup did not start after join")
	}
	if visited != 1 {
		t.Fatalf("visited %d files after cancellation", visited)
	}
	// Removing state after the join is safe because no scanner is left alive.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}
func TestScanWorkBudgetAndCancellation(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "one"), []byte("one"))
	budget := newScanBudget(t.Context())
	budget.remaining = 3
	if err := scanTree(budget, dir, func(string, fs.FileInfo) error { return nil }); err == nil {
		t.Fatal("finite scan budget not enforced")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := usage(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("usage cancellation: %v", err)
	}
}
