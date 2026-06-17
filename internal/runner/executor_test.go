package runner

import (
	"context"
	"strings"
	"sync"
	"testing"

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
