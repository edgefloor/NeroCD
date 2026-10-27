package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestRunContinuesAfterAcknowledgedImagePreparationFailure(t *testing.T) {
	for _, mode := range []string{"worker", "once", "lost_ack", "missing_ack", "changed_ack", "changed_target", "changed_snapshot", "uncertain", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			once := mode == "once"
			// Fail the real preparation path before any Docker process or workload mutation.
			t.Setenv("PATH", t.TempDir())
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			now := time.Now().UTC()
			lease := domain.RunLease{ID: "lease_prepare_failure", RunID: "run_prepare_failure", RunnerID: "runner", Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
			a := hostingAuthority(lease.RunID, "prepare_failure", lease)
			snapshot := imageTestPlan(t).Snapshot
			snapshot.RunnerID = lease.RunnerID
			op := domain.HostingOperation{ID: a.OperationID, RunID: a.RunID, BindingID: snapshot.BindingID, Snapshot: snapshot, State: "preparing", TaskMode: "deploy"}
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: a.OperationID, HostingMode: "deploy"}}, Lease: lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: a.RunID}}
			var claims, completed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/identity":
					_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": []string{domain.RunTypeImageDeploy}})
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
				case "/api/v1/runners/telemetry":
					w.WriteHeader(http.StatusNoContent)
				case "/api/v1/runners/claim":
					if claims.Add(1) == 1 {
						_ = json.NewEncoder(w).Encode(claim)
					} else {
						cancel()
						w.WriteHeader(http.StatusNotFound)
					}
				case "/api/v1/runners/renew", "/api/v1/runners/lease":
					_ = json.NewEncoder(w).Encode(lease)
				case "/api/v1/runners/hosting/plan":
					_ = json.NewEncoder(w).Encode(op)
				case "/api/v1/runners/events/batch":
					var in runnerclient.EventBatchRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil {
						t.Error("event decode")
						w.WriteHeader(400)
						return
					}
					ack := runnerclient.EventBatchAck{}
					for _, event := range in.Events {
						ack.Events = append(ack.Events, domain.RunLog{RunID: in.RunID, LeaseID: in.LeaseID, Attempt: in.Attempt, EventKey: event.EventKey, RequestedSequence: event.Sequence, Sequence: event.Sequence, Stream: event.Stream, Message: event.Message})
					}
					_ = json.NewEncoder(w).Encode(ack)
				case "/api/v1/runners/hosting/complete":
					var in domain.HostingCompleteRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != a || in.Result != (domain.HostingResult{Outcome: "failed"}) {
						t.Error("failure authority/result changed")
						w.WriteHeader(400)
						return
					}
					completed.Add(1)
					finished := op
					finished.State = "failed"
					finished.Result = &in.Result
					finished.CompleteReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Result), CreatedAt: time.Now().UTC()}
					switch mode {
					case "lost_ack":
						w.WriteHeader(http.StatusConflict)
						return
					case "missing_ack":
						finished.CompleteReceipt = nil
					case "changed_ack":
						finished.CompleteReceipt.BodyHash = domain.HostingDocumentHash("other result")
					case "changed_target":
						finished.ID = "other_operation"
					case "changed_snapshot":
						finished.Snapshot.ResourceGeneration++
					case "uncertain":
						finished.MutationHold = true
					case "canceled":
						cancel()
					}
					_ = json.NewEncoder(w).Encode(finished)
				default:
					t.Errorf("unexpected mutation or request: %s", r.URL.Path)
					w.WriteHeader(409)
				}
			}))
			defer server.Close()
			dir := filepath.Join(t.TempDir(), "journal")
			err := RunContext(ctx, Config{Server: server.URL, Token: "token", RunnerID: "runner", WorkDir: t.TempDir(), JournalDir: dir, Execute: true, Once: once, PollInterval: time.Millisecond, CancelPollInterval: time.Hour, ImageDockerEndpoint: "unix:///nonexistent-prepare-failure.sock"})
			wantClaims := int32(1)
			if mode == "worker" {
				wantClaims = 2
			}
			if completed.Load() != 1 || claims.Load() != wantClaims || err == nil {
				t.Fatalf("settled preparation stopped worker: completions=%d claims=%d want=%d err=%v", completed.Load(), claims.Load(), wantClaims, err)
			}
			if errors.Is(err, errSettledImagePreparationFailure) != once {
				t.Fatalf("unsafe failure classified as settled: %s: %v", mode, err)
			}
			j, e := runner.OpenAttemptJournal(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if err := j.Close(); err != nil {
					t.Error(err)
				}
			}()
			retained := mode != "worker" && mode != "once"
			if (len(j.Snapshot().Hosting) != 0) != retained || len(j.Snapshot().Events) != 0 {
				t.Fatal("failure acknowledgement changed durable evidence retention")
			}
		})
	}
}

func TestContinuousImagePreparationFailureBoundary(t *testing.T) {
	for _, test := range []struct {
		name                 string
		err                  error
		once, canceled, want bool
	}{
		{name: "settled", err: errSettledImagePreparationFailure, want: true},
		{name: "wrapped settled", err: fmt.Errorf("completed: %w", errSettledImagePreparationFailure), want: true},
		{name: "once preserves failure", err: errSettledImagePreparationFailure, once: true},
		{name: "cancellation stops", err: errSettledImagePreparationFailure, canceled: true},
		{name: "same text is not proof", err: errors.New(errSettledImagePreparationFailure.Error())},
		{name: "authority loss", err: errLeaseAuthorityLost},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "unconfirmed completion", err: errors.New("completion unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			if settledImagePreparationMayContinue(ctx, test.once, test.err) != test.want {
				t.Fatal("continuous/once/cancellation settlement boundary changed")
			}
		})
	}
}
