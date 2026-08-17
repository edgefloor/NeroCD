package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestExecuteProcessStreamsAndExitCode(t *testing.T) {
	var mu sync.Mutex
	var events []ProcessEvent
	result, err := ExecuteProcess(t.Context(), domain.ProcessSpec{Command: []string{"sh", "-c", "echo out; echo err >&2; exit 7"}}, func(event ProcessEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || result.TimedOut || result.Canceled {
		t.Fatalf("unexpected result: %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := ""
	for _, event := range events {
		joined += event.Stream + ":" + event.Message + "\n"
	}
	if !strings.Contains(joined, "stdout:out") || !strings.Contains(joined, "stderr:err") {
		t.Fatalf("expected stdout and stderr events, got:\n%s", joined)
	}
}

func TestExecuteProcessTimeout(t *testing.T) {
	result, err := ExecuteProcess(context.Background(), domain.ProcessSpec{Command: []string{"sh", "-c", "sleep 2"}, TimeoutSeconds: 1}, func(ProcessEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode == 0 {
		t.Fatalf("expected timed out non-zero result, got %#v", result)
	}
}

func TestExecuteProcessReturnsWhenEmitBlocksAfterBoundedDrain(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	callbackFinished := make(chan struct{})
	resultCh := make(chan ProcessResult, 1)
	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		result, err := ExecuteProcess(t.Context(), domain.ProcessSpec{Command: []string{"sh", "-c", "echo blocked-callback"}}, func(event ProcessEvent) {
			if event.Message != "blocked-callback" {
				return
			}
			close(entered)
			<-release
			close(callbackFinished)
		})
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("emit callback was not entered")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteProcess remained blocked in emit callback")
	}
	result := <-resultCh
	if result.ExitCode != 0 || result.Canceled || result.TimedOut {
		t.Fatalf("unexpected result: %#v", result)
	}
	if elapsed := time.Since(start); elapsed < processOutputDrainTimeout+processOutputCloseTimeout-200*time.Millisecond {
		t.Fatalf("returned before bounded drain windows elapsed: %v", elapsed)
	}
	close(release)
	select {
	case <-callbackFinished:
	case <-time.After(time.Second):
		t.Fatal("blocked emit callback did not finish after release")
	}
}

func TestReceiveProcessWaitHasBoundedDeadline(t *testing.T) {
	never := make(chan error)
	start := time.Now()
	outcome, received := receiveProcessWait(never, 30*time.Millisecond)
	if received || outcome.received {
		t.Fatalf("unexpected wait outcome: %#v received=%v", outcome, received)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Fatalf("unexpected wait duration: %v", elapsed)
	}
}
