package domain

import (
	"encoding/json"
	"testing"
)

func TestHealthPolicyRejectsUnknownServerConfiguration(t *testing.T) {
	var policy HealthPolicy
	if err := json.Unmarshal([]byte(`{"url":"https://health.example/","allowed_hosts":["health.example"],"unexpected_allow_all":true}`), &policy); err == nil {
		t.Fatal("unknown health policy control was accepted")
	}
	if err := json.Unmarshal([]byte(`{"url":"https://health.example/","allowed_hosts":["health.example"],"allowed_cidrs":["10.0.0.0/8"]}`), &policy); err != nil {
		t.Fatalf("known health policy was rejected: %v", err)
	}
}

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
