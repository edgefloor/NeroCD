//go:build linux || darwin

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestExecuteProcessCancellationKillsBackgroundChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resultCh := make(chan ProcessResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := ExecuteProcess(ctx, domain.ProcessSpec{Command: []string{"/bin/sh", "-c", "sleep 30 & echo $! > \"$1\"; wait", "sh", pidFile}}, func(ProcessEvent) {})
		resultCh <- result
		errCh <- err
	}()
	pid := waitForChildPID(t, pidFile)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteProcess did not return after cancellation")
	}
	result := <-resultCh
	if !result.Canceled || result.TimedOut {
		t.Fatalf("unexpected result: %#v", result)
	}
	waitForPIDGone(t, pid)
}

func TestExecuteProcessCancellationEscalatesToKillForTermIgnoringGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resultCh := make(chan ProcessResult, 1)
	errCh := make(chan error, 1)
	command := "trap '' TERM; /bin/sh -c 'trap \"\" TERM; echo $$ > \"$1\"; while :; do sleep 1; done' sh \"$1\" & wait"
	go func() {
		result, err := ExecuteProcess(ctx, domain.ProcessSpec{Command: []string{"/bin/sh", "-c", command, "sh", pidFile}}, func(ProcessEvent) {})
		resultCh <- result
		errCh <- err
	}()
	pid := waitForChildPID(t, pidFile)
	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteProcess did not return after TERM/KILL escalation")
	}
	result := <-resultCh
	if !result.Canceled || result.TimedOut {
		t.Fatalf("unexpected result: %#v", result)
	}
	if elapsed := time.Since(start); elapsed < processTerminationGrace-100*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected bounded TERM grace before KILL, elapsed=%v", elapsed)
	}
	waitForPIDGone(t, pid)
}

func TestExecuteProcessCancellationEscalatesAfterLeaderExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resultCh := make(chan ProcessResult, 1)
	errCh := make(chan error, 1)
	command := "/bin/sh -c 'trap \"\" TERM; echo $$ > \"$1\"; while :; do sleep 1; done' sh \"$1\" & wait"
	go func() {
		result, err := ExecuteProcess(ctx, domain.ProcessSpec{Command: []string{"/bin/sh", "-c", command, "sh", pidFile}}, func(ProcessEvent) {})
		resultCh <- result
		errCh <- err
	}()
	pid := waitForChildPID(t, pidFile)
	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteProcess did not return after leader exit and group escalation")
	}
	result := <-resultCh
	if !result.Canceled || result.TimedOut {
		t.Fatalf("unexpected result: %#v", result)
	}
	if elapsed := time.Since(start); elapsed < processTerminationGrace-100*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected escalation after leader exited, elapsed=%v", elapsed)
	}
	waitForPIDGone(t, pid)
}

func TestExecuteProcessTimeoutEscalatesAfterLeaderExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	command := "/bin/sh -c 'trap \"\" TERM; echo $$ > \"$1\"; while :; do sleep 1; done' sh \"$1\" & wait"
	start := time.Now()
	result, err := ExecuteProcess(context.Background(), domain.ProcessSpec{Command: []string{"/bin/sh", "-c", command, "sh", pidFile}, TimeoutSeconds: 1}, func(ProcessEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.Canceled {
		t.Fatalf("unexpected result: %#v", result)
	}
	if elapsed := time.Since(start); elapsed < time.Second+processTerminationGrace-100*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("expected timeout plus TERM grace, elapsed=%v", elapsed)
	}
	waitForPIDGone(t, waitForChildPID(t, pidFile))
}

func TestExecuteProcessNaturalLeaderExitCleansIgnoringBackgroundChild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	readyFile := filepath.Join(dir, "child.ready")
	child := "/bin/sh -c 'trap \"\" TERM; echo $$ > \"$1\"; : > \"$2\"; while :; do sleep 1; done' sh \"$1\" \"$2\" &"
	leader := child + " while [ ! -f \"$2\" ]; do sleep 0.01; done; exit 0"
	start := time.Now()
	result, err := ExecuteProcess(context.Background(), domain.ProcessSpec{Command: []string{"/bin/sh", "-c", leader, "sh", pidFile, readyFile}}, func(ProcessEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Canceled || result.TimedOut {
		t.Fatalf("natural leader exit semantics changed: %#v", result)
	}
	if elapsed := time.Since(start); elapsed < processTerminationGrace-100*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("expected TERM grace and KILL cleanup after natural exit, elapsed=%v", elapsed)
	}
	if _, err := os.Stat(readyFile); err != nil {
		t.Fatalf("leader exited before child was ready: %v", err)
	}
	waitForPIDGone(t, waitForChildPID(t, pidFile))
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid child PID %q: %v", contents, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not write PID")
	return 0
}
