//go:build linux

package runner

import (
	"testing"
	"time"
)

func waitForPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _, exists, err := linuxProcessState(pid)
		if err != nil {
			t.Fatal(err)
		}
		if !exists || state == "Z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child PID %d is still runnable", pid)
}

func TestParseLinuxProcessStatUsesFinalCommandParenthesis(t *testing.T) {
	state, pgrp, err := parseLinuxProcessStat("42 (command ) with spaces) S 1 777 0 0")
	if err != nil || state != "S" || pgrp != 777 {
		t.Fatalf("state=%q pgrp=%d err=%v", state, pgrp, err)
	}
}
