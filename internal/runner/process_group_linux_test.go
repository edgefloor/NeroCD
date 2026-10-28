//go:build linux

package runner

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestLinuxProcessStateReadOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		err     error
		exists  bool
		wantErr error
	}{
		{name: "running", data: "42 (worker) S 1 777 0 0", exists: true},
		{name: "removed before open", err: &os.PathError{Op: "open", Path: "/proc/42/stat", Err: syscall.ENOENT}},
		{name: "exited during read", err: &os.PathError{Op: "read", Path: "/proc/42/stat", Err: syscall.ESRCH}},
		{name: "permission denied", err: &os.PathError{Op: "open", Path: "/proc/42/stat", Err: syscall.EACCES}, wantErr: syscall.EACCES},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, group, exists, err := linuxProcessStateWithReader(42, func(path string) ([]byte, error) {
				if path != "/proc/42/stat" {
					t.Fatalf("unexpected process path %q", path)
				}
				return []byte(tc.data), tc.err
			})
			if exists != tc.exists || !errors.Is(err, tc.wantErr) {
				t.Fatalf("exists=%t err=%v, want exists=%t err=%v", exists, err, tc.exists, tc.wantErr)
			}
			if exists && (state != "S" || group != 777) {
				t.Fatalf("state=%q group=%d, want state=S group=777", state, group)
			}
		})
	}
}

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
