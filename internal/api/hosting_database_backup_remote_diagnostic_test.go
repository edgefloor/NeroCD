package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDatabaseRemoteDiagnosticClosedMessagesAndRoutes(t *testing.T) {
	messages := map[string]string{
		"remote backup authority unavailable":                "remote_plan_authority",
		"invalid PostgreSQL remote backup request":           "remote_response",
		"remote backup credentials unavailable":              "remote_credentials",
		"remote backup receipt request failed":               "remote_receipt_transport",
		"remote backup evidence requires reconciliation":     "remote_evidence_held",
		"invalid protected remote backup network policy":     "remote_network_policy",
		"remote backup network denied":                       "remote_network_denied",
		"remote database backups require native Linux arm64": "remote_platform",
	}
	for _, code := range []string{"access_denied", "network_denied", "remote_unavailable", "object_mismatch", "archive_unavailable", "interrupted", "authority_denied"} {
		messages["remote backup transfer: "+code] = "remote_" + code
	}
	canary := "private_secret_https://private.invalid/?token=private"
	for message, want := range messages {
		if got := databaseBackupTerminalClass([]byte("{\"event\":\"private\"}\n" + message + "\ntime=fixture level=INFO msg=private\n")); got != want {
			t.Fatal("closed remote terminal class missing")
		}
		for _, raw := range []string{message + ": " + canary, message + "\n" + canary, canary + message} {
			if databaseBackupTerminalClass([]byte(raw)) != "unknown" {
				t.Fatal("remote classification reflected appended private text")
			}
		}
	}
	for _, raw := range []string{canary, "remote backup transfer: " + canary, "remote backup transfer: ", strings.Repeat("x", (1<<20)+1)} {
		if databaseBackupTerminalClass([]byte(raw)) != "unknown" {
			t.Fatal("unknown remote message became evidence")
		}
	}
	var counts databaseRunnerAPICounts
	for _, phase := range []string{"plan", "credentials", "mutation"} {
		path := "/api/v1/runners/hosting/database-backup-transfer/" + phase
		counts.record("POST", path, 200)
		counts.record("POST", path+"?token="+canary, 200)
		counts.record("GET", path, 200)
	}
	want := "POST /api/v1/runners/hosting/database-backup-transfer/plan 200=1,POST /api/v1/runners/hosting/database-backup-transfer/credentials 200=1,POST /api/v1/runners/hosting/database-backup-transfer/mutation 200=1"
	if counts.summary() != want {
		t.Fatal("remote counters accepted an unknown method or path")
	}
}

func TestDatabaseRemoteDiagnosticProjectionAndObserverNeverReflectPrivateInput(t *testing.T) {
	canary := "ncr_remote_private_canary"
	counts := &databaseRunnerAPICounts{}
	got := databaseRemoteValidationProjection(canary, canary, canary, canary, canary, canary, true, true, false, true, false, []byte(canary), databaseRunnerExit{Exited: true, Code: 1000, Kind: canary}, counts)
	raw, err := json.Marshal(got)
	if err != nil || bytes.Contains(raw, []byte(canary)) || got.LastState != "unknown" || got.StoredState != "unknown" || got.FailureCode != "unknown" || got.Reason != "unknown" || got.RunStatus != "unknown" || got.LeaseStatus != "unknown" || got.TerminalClass != "unknown" || got.RunnerCode != -1 || got.RunLinked != "true" || got.LeaseLinked != "false" || got.IntentReceipt != "present" || got.TerminalReceipt != "absent" {
		t.Fatal("remote diagnostic projection escaped its closed vocabulary")
	}
	got = databaseRemoteValidationProjection("dispatched", "applying", "", "runner_exited", "running", "expired", false, true, true, true, true, nil, databaseRunnerExit{Exited: true, Code: 1}, counts)
	if got.LastState != "dispatched" || got.StoredState != "unknown" || got.RunLinked != "unknown" || got.LeaseLinked != "unknown" || got.IntentReceipt != "unknown" || got.TerminalReceipt != "unknown" || got.RunnerCode != 1 {
		t.Fatal("missing SQL evidence inferred receipt or authority presence")
	}
	var output bytes.Buffer
	f := &databaseRunnerFixture{logs: &databaseRunnerLog{data: []byte(canary)}, failureOutput: &output, lastExit: databaseRunnerExit{Exited: true, Code: 1}}
	f.reportDatabaseRemoteValidationFailure(databaseRemoteTransfer{ID: canary, State: canary, FailureCode: canary}, canary)
	if !strings.HasPrefix(output.String(), "DATABASE_BACKUP_REMOTE_FAILURE_EVIDENCE ") || bytes.Contains(output.Bytes(), []byte(canary)) {
		t.Fatal("remote failure observer reflected private data")
	}
}

func TestDatabaseRemoteValidationWaitRetainsLastStateAndStopsOnRunnerExit(t *testing.T) {
	t.Run("already_exited", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		p := &databaseRunnerProcess{done: done, exit: databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}}
		calls := 0
		started := time.Now()
		last, reason := waitDatabaseRemoteValidation(t.Context(), p, databaseRemoteTransfer{State: "dispatched"}, func(context.Context) (databaseRemoteTransfer, error) {
			calls++
			return databaseRemoteTransfer{}, errors.New("private error")
		})
		if calls != 0 || reason != "runner_exited" || last.State != "dispatched" || time.Since(started) > time.Second {
			t.Fatal("exited runner did not fail promptly with retained state")
		}
	})
	t.Run("exit_cancels_poll", func(t *testing.T) {
		done := make(chan struct{})
		p := &databaseRunnerProcess{done: done, exit: databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}}
		calls := 0
		last, reason := waitDatabaseRemoteValidation(t.Context(), p, databaseRemoteTransfer{State: "queued"}, func(ctx context.Context) (databaseRemoteTransfer, error) {
			calls++
			if calls == 1 {
				return databaseRemoteTransfer{State: "dispatched"}, nil
			}
			close(done)
			<-ctx.Done()
			return databaseRemoteTransfer{}, errors.New("private poll error")
		})
		if reason != "runner_exited" || last.State != "dispatched" || calls != 2 {
			t.Fatal("poll cancellation discarded the last validation state")
		}
	})
	for _, state := range []string{"failed", "uncertain", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			last, reason := waitDatabaseRemoteValidation(ctx, nil, databaseRemoteTransfer{State: "queued"}, func(context.Context) (databaseRemoteTransfer, error) {
				return databaseRemoteTransfer{State: state}, nil
			})
			want := "terminal_operation"
			if state == "succeeded" {
				want = ""
			}
			if last.State != state || reason != want {
				t.Fatal("remote terminal state was not retained")
			}
		})
	}
}
