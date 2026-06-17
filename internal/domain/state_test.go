package domain

import "testing"

func TestIsTerminalRunStatus(t *testing.T) {
	terminal := []string{RunSucceeded, RunFailed, RunCanceled}
	for _, status := range terminal {
		if !IsTerminalRunStatus(status) {
			t.Fatalf("IsTerminalRunStatus(%q) = false, want true", status)
		}
	}

	nonTerminal := []string{RunQueued, RunRunning, RunWaitingApproval, ""}
	for _, status := range nonTerminal {
		if IsTerminalRunStatus(status) {
			t.Fatalf("IsTerminalRunStatus(%q) = true, want false", status)
		}
	}
}
