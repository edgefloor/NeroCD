package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// The commit response waits for both real authority workers to join. A legacy
// reporter-only drain lets terminal observation cancel the ACK instead.
func TestStorageCompletionAcknowledgementAllowsNextClaim(t *testing.T) {
	for _, worker := range []string{"watcher", "renewer"} {
		t.Run(worker, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			replacement := domain.ClaimedRun{Run: domain.TaskRun{ID: "run_replacement", ProjectID: c.op.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: "hop_replacement", HostingMode: "deploy"}}, Lease: c.lease}
			replacement.Lease.ID, replacement.Lease.RunID, replacement.Lease.Fence = "lease_replacement", replacement.Run.ID, "fence_replacement"
			replacement.PrimitivePlan.RunID = replacement.Run.ID
			parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s := newAttemptSupervisorFrom(parent, c.lease)
			defer s.Close()
			entered, committed := make(chan struct{}), make(chan struct{})
			var enteredOnce sync.Once
			var watcher *leaseWatcher
			var renewer *leaseRenewer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: c.lease.RunnerID})
				case "/api/v1/runners/lease", "/api/v1/runners/renew":
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-r.Context().Done():
						return
					case <-parent.Done():
						return
					case <-committed:
					}
					if r.URL.Path == "/api/v1/runners/renew" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					terminal := c.lease
					terminal.Status = "succeeded"
					_ = json.NewEncoder(w).Encode(terminal)
				case "/api/v1/runners/hosting/complete":
					rec := httptest.NewRecorder()
					c.serve(rec, r)
					if rec.Code != http.StatusOK {
						t.Error("completion did not commit")
						w.WriteHeader(rec.Code)
						return
					}
					close(committed)
					select {
					case <-watcher.done:
					case <-parent.Done():
						return
					}
					select {
					case <-renewer.done:
					case <-parent.Done():
						return
					}
					_, _ = w.Write(rec.Body.Bytes())
				case "/api/v1/runners/claim":
					_ = json.NewEncoder(w).Encode(replacement)
				default:
					c.serve(w, r)
				}
			}))
			defer server.Close()
			watchInterval, renewInterval := time.Hour, time.Hour
			if worker == "watcher" {
				watchInterval = time.Nanosecond
			} else {
				renewInterval = time.Nanosecond
			}
			watcher = startLeaseWatcher(s.Context(), server.URL, "token", c.lease, watchInterval, s.cancel)
			renewer = startLeaseRenewer(s.Context(), s, server.URL, "token", c.lease, renewInterval)
			defer renewer.Stop()
			defer watcher.Stop()
			select {
			case <-entered:
			case <-parent.Done():
				t.Fatal("watcher did not enter authority handshake")
			}
			reporter := startAttemptReporter(s.Context(), s, j, server.URL, "token", c.a.RunID, c.lease)
			defer reporter.Stop()
			engine := &storageCoordinatorEngine{volume: f.engine.ensureOwnedVolume, apply: func(ctx context.Context, id string, p domain.HostingApplyPlan, r hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, verifying ...func() error) domain.HostingResult {
				select {
				case <-watcher.done:
					t.Error("watcher stopped before writer effects")
				default:
				}
				select {
				case <-renewer.done:
					t.Error("renewer stopped before writer effects")
				default:
				}
				if c.hasPhase("result") {
					t.Error("result preceded writer effects")
				}
				return f.engine.ApplyWithOwnedStorage(ctx, id, p, r, v, verifying...)
			}}
			client := runnerclient.New(server.URL, "token")
			if err := executeHostingStoragePrepared(s, j, databaseCompletionDrain(s, reporter, watcher, renewer), client, engine, c.a, c.lease, c.op); err != nil {
				t.Fatalf("committed storage completion stopped runner before replacement claim: %v", err)
			}
			if s.Context().Err() != nil {
				t.Fatal("own completion canceled its supervisor")
			}
			if c.completeCalls != 1 || c.applyCalls != 1 {
				t.Fatal("completion duplicated writer or result")
			}
			if len(j.Snapshot().Hosting) != 0 {
				t.Fatal("settled completion left journal records")
			}
			next, err := client.Claim(parent)
			if err != nil || next.Run.ID != replacement.Run.ID || next.Lease != replacement.Lease {
				t.Fatalf("replacement claim failed: %v", err)
			}
		})
	}
}

func TestStorageCompletionDrainLostACKReplaysExactResult(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.lostComplete = true
	var bodies [][]byte
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			bodies = append(bodies, append([]byte(nil), body...))
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.serve(w, r)
	}))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	watcher := startLeaseWatcher(s.Context(), server.URL, "token", c.lease, time.Hour, s.cancel)
	defer watcher.Stop()
	renewer := startLeaseRenewer(s.Context(), s, server.URL, "token", c.lease, time.Hour)
	defer renewer.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "token", c.a.RunID, c.lease)
	defer reporter.Stop()
	err := executeHostingStoragePrepared(s, j, databaseCompletionDrain(s, reporter, watcher, renewer), runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
	if err == nil || !c.hasPhase("result") || s.Context().Err() != nil {
		t.Fatal("lost acknowledgement discarded result or canceled supervisor")
	}
	select {
	case <-watcher.done:
	default:
		t.Fatal("watcher not joined before completion")
	}
	select {
	case <-renewer.done:
	default:
		t.Fatal("renewer not joined before completion")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	c.journal = reopened
	c.lostComplete = false
	if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", reopened, "", t.Context(), func(string) (hostingStorageEngine, error) {
		t.Error("saved result replay opened engine")
		return nil, errors.New("forbidden")
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || c.applyCalls != 1 || c.completeCalls != 2 || len(reopened.Snapshot().Hosting) != 0 {
		t.Fatal("replay changed result, repeated writer or retained settled evidence")
	}
}

func TestStorageCompletionDrainRetainsCanceledOrUncertainEffects(t *testing.T) {
	for _, mode := range []string{"canceled_before_completion", "uncertain_result"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			defer s.Close()
			watcher := startLeaseWatcher(s.Context(), server.URL, "token", c.lease, time.Hour, s.cancel)
			defer watcher.Stop()
			renewer := startLeaseRenewer(s.Context(), s, server.URL, "token", c.lease, time.Hour)
			defer renewer.Stop()
			reporter := startAttemptReporter(s.Context(), s, j, server.URL, "token", c.a.RunID, c.lease)
			defer reporter.Stop()
			drain := databaseCompletionDrain(s, reporter, watcher, renewer)
			if mode == "canceled_before_completion" {
				drain = func(ctx context.Context) error {
					s.cancel()
					return databaseCompletionDrain(s, reporter, watcher, renewer)(ctx)
				}
			}
			engine := &storageCoordinatorEngine{volume: f.engine.ensureOwnedVolume, apply: func(ctx context.Context, id string, p domain.HostingApplyPlan, r hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, verifying ...func() error) domain.HostingResult {
				result := f.engine.ApplyWithOwnedStorage(ctx, id, p, r, v, verifying...)
				if mode == "uncertain_result" {
					result.Uncertain = true
					result.Outcome = "failed"
				}
				return result
			}}
			err := executeHostingStoragePrepared(s, j, drain, runnerclient.New(server.URL, "token"), engine, c.a, c.lease, c.op)
			if err == nil || len(j.Snapshot().Hosting) == 0 || !f.created || c.applyCalls != 1 {
				t.Fatal("unsettled effects were erased or writer was repeated")
			}
			if mode == "canceled_before_completion" && (c.completeCalls != 0 || !c.hasPhase("result")) {
				t.Fatal("revoked completion published or lost durable result")
			}
			if mode == "uncertain_result" && (c.completeCalls != 1 || !c.op.MutationHold) {
				t.Fatal("uncertain completion released hold")
			}
		})
	}
}

func TestStorageCompletionPreparedRecoveryJoinsWatcherBeforeCommit(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	prepared := domain.HostingPreparedRequest{HostingAuthority: c.a, ReceiptID: attemptMutationKey("hosting_prepared", c.a.LeaseID, c.a.Attempt, "release"), Release: *c.op.Release}
	entry := runner.JournalHosting{ID: prepared.ReceiptID, Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "prepared", Prepared: &prepared, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	entered, drained := make(chan struct{}), make(chan struct{})
	var leaseRequests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: c.lease.RunnerID})
		case "/api/v1/runners/lease":
			mu.Lock()
			leaseRequests++
			first := leaseRequests == 1
			mu.Unlock()
			if first {
				c.serve(w, r)
				return
			} // Replay's initial authority probe.
			close(entered)
			select {
			case <-r.Context().Done():
				close(drained)
				return
			case <-parent.Done():
				return
			}
		case "/api/v1/runners/hosting/complete":
			// Wait for server-side cancellation delivery as well as the local
			// watcher join. Their scheduling relative to this request can vary.
			select {
			case <-drained:
			case <-r.Context().Done():
				return
			case <-parent.Done():
				return
			}
			rec := httptest.NewRecorder()
			c.serve(rec, r)
			if rec.Code != http.StatusOK {
				t.Error("recovery completion did not commit")
				w.WriteHeader(rec.Code)
				return
			}
			_, _ = w.Write(rec.Body.Bytes())
		default:
			c.serve(w, r)
		}
	}))
	defer server.Close()
	engine := &storageCoordinatorEngine{volume: func(ctx context.Context, r hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
		select {
		case <-entered:
		case <-ctx.Done():
			return hostingOwnedVolumeEvidence{}, ctx.Err()
		}
		return f.engine.ensureOwnedVolume(ctx, r)
	}, apply: f.engine.ApplyWithOwnedStorage}
	if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", parent, func(string) (hostingStorageEngine, error) { return engine, nil }); err != nil {
		t.Fatalf("prepared recovery did not drain authority before completion: %v", err)
	}
	if c.applyCalls != 1 || c.completeCalls != 1 || len(j.Snapshot().Hosting) != 0 {
		t.Fatal("prepared recovery duplicated effects or left settled records")
	}
}

func TestStoragePreparationFailureCompletionDoesNotCancelAcknowledgement(t *testing.T) {
	for _, phase := range []string{"prepare", "ensure_prepared_release"} {
		t.Run(phase, func(t *testing.T) {
			for _, worker := range []string{"watcher", "renewer"} {
				t.Run(worker, func(t *testing.T) { testStoragePreparationFailureCompletion(t, phase, worker, "ack") })
			}
		})
	}
}

func TestStoragePreparationFailureRetainsLostACKAndRevokedAuthority(t *testing.T) {
	for _, phase := range []string{"prepare", "ensure_prepared_release"} {
		t.Run(phase, func(t *testing.T) {
			for _, outcome := range []string{"lost_ack", "canceled_drain", "expired_drain"} {
				t.Run(outcome, func(t *testing.T) { testStoragePreparationFailureCompletion(t, phase, "watcher", outcome) })
			}
		})
	}
}

func testStoragePreparationFailureCompletion(t *testing.T, phase, worker, outcome string) {
	t.Helper()
	// An unavailable executable fails daemon inspection in the actual image
	// caller. No Docker process or workload mutation can start in either phase.
	t.Setenv("PATH", t.TempDir())
	f := mountedTestFixture(t, "")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	if phase == "prepare" {
		c.op.Plan = nil
		c.op.Release = nil
		c.op.State = "queued"
	}
	parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s := newAttemptSupervisorFrom(parent, c.lease)
	defer s.Close()
	entered, committed := make(chan struct{}), make(chan struct{})
	var enteredOnce, committedOnce sync.Once
	var bodyMu sync.Mutex
	var completionBodies [][]byte
	loseACK := outcome == "lost_ack"
	var watcher *leaseWatcher
	var renewer *leaseRenewer
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: c.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: c.a.OperationID, HostingMode: "deploy"}}, Lease: c.lease}
	claim.PrimitivePlan.RunID = claim.Run.ID
	replacement := claim
	replacement.Run.ID = "run_after_prepare_failure"
	replacement.Run.RunSpec.HostingOperationID = "hop_after_prepare_failure"
	replacement.Lease.ID, replacement.Lease.RunID, replacement.Lease.Fence = "lease_after_prepare_failure", replacement.Run.ID, "fence_after_prepare_failure"
	replacement.PrimitivePlan.RunID = replacement.Run.ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/plan":
			_ = json.NewEncoder(w).Encode(c.op)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: c.lease.RunnerID})
		case "/api/v1/runners/events/batch":
			var in runnerclient.EventBatchRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.RunID != c.a.RunID || in.LeaseID != c.a.LeaseID || in.Attempt != c.a.Attempt || in.Fence != c.a.Fence {
				t.Error("event authority changed")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			ack := runnerclient.EventBatchAck{}
			for _, event := range in.Events {
				ack.Events = append(ack.Events, domain.RunLog{RunID: in.RunID, LeaseID: in.LeaseID, Attempt: in.Attempt, EventKey: event.EventKey, RequestedSequence: event.Sequence, Sequence: event.Sequence, Stream: event.Stream, Message: event.Message})
			}
			_ = json.NewEncoder(w).Encode(ack)
		case "/api/v1/runners/lease", "/api/v1/runners/renew":
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-r.Context().Done():
				return
			case <-parent.Done():
				return
			case <-committed:
			}
			if r.URL.Path == "/api/v1/runners/renew" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			terminal := c.lease
			terminal.Status = "failed"
			_ = json.NewEncoder(w).Encode(terminal)
		case "/api/v1/runners/hosting/complete":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			bodyMu.Lock()
			completionBodies = append(completionBodies, append([]byte(nil), body...))
			dropACK := loseACK
			bodyMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			rec := httptest.NewRecorder()
			c.serve(rec, r)
			if rec.Code != http.StatusOK {
				t.Error("preparation failure completion did not commit")
				w.WriteHeader(rec.Code)
				return
			}
			committedOnce.Do(func() { close(committed) })
			select {
			case <-watcher.done:
			case <-parent.Done():
				return
			}
			select {
			case <-renewer.done:
			case <-parent.Done():
				return
			}
			if dropACK {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write(rec.Body.Bytes())
		case "/api/v1/runners/claim":
			_ = json.NewEncoder(w).Encode(replacement)
		default:
			c.serve(w, r)
		}
	}))
	defer server.Close()
	watchInterval, renewInterval := time.Hour, time.Hour
	if worker == "watcher" {
		watchInterval = time.Nanosecond
	} else {
		renewInterval = time.Nanosecond
	}
	watcher = startLeaseWatcher(s.Context(), server.URL, "token", c.lease, watchInterval, s.cancel)
	defer watcher.Stop()
	renewer = startLeaseRenewer(s.Context(), s, server.URL, "token", c.lease, renewInterval)
	defer renewer.Stop()
	select {
	case <-entered:
	case <-parent.Done():
		t.Fatal("authority worker did not enter handshake")
	}
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "token", c.a.RunID, c.lease)
	defer reporter.Stop()
	drain := databaseCompletionDrain(s, reporter, watcher, renewer)
	switch outcome {
	case "canceled_drain":
		drain = func(ctx context.Context) error {
			s.cancel()
			return databaseCompletionDrain(s, reporter, watcher, renewer)(ctx)
		}
	case "expired_drain":
		drain = func(context.Context) error {
			s.mu.Lock()
			s.expiry = time.Now().Add(-time.Second)
			s.mu.Unlock()
			requestContext, done, err := s.RequestContext()
			if done != nil {
				done()
			}
			if err == nil || requestContext != nil {
				t.Error("expired supervisor admitted a request")
			}
			return err
		}
	}
	err := executeImageClaim(s, j, reporter, drain, server.URL, "token", claim, "unix:///nonexistent-storage-fixture.sock", false, nil)
	if outcome == "canceled_drain" || outcome == "expired_drain" {
		if err == nil || c.completeCalls != 0 || !c.hasPhase("result") || c.createCalls != 0 || c.materializedCalls != 0 || c.applyCalls != 0 || f.created {
			t.Fatal("revoked authority published fresh completion, mutated storage or lost durable failure")
		}
		return
	}
	if outcome == "lost_ack" {
		if err == nil || !c.hasPhase("result") || c.completeCalls != 1 || s.Context().Err() != nil {
			t.Fatal("lost preparation acknowledgement discarded result or canceled supervisor")
		}
		bodyMu.Lock()
		loseACK = false
		bodyMu.Unlock()
		if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", parent, func(string) (hostingStorageEngine, error) {
			t.Error("failure receipt replay opened an engine")
			return nil, errors.New("forbidden")
		}); err != nil {
			t.Fatal(err)
		}
		bodyMu.Lock()
		exact := len(completionBodies) == 2 && bytes.Equal(completionBodies[0], completionBodies[1])
		bodyMu.Unlock()
		if !exact || c.completeCalls != 2 || len(j.Snapshot().Hosting) != 0 || c.createCalls != 0 || c.materializedCalls != 0 || c.applyCalls != 0 || f.created {
			t.Fatal("failure replay changed receipt, repeated effects or retained settled evidence")
		}
		next, err := runnerclient.New(server.URL, "token").Claim(parent)
		if err != nil || next.Run.ID != replacement.Run.ID || next.Lease != replacement.Lease {
			t.Fatalf("failure receipt replay blocked next claim: %v", err)
		}
		return
	}
	if s.Context().Err() != nil || errors.Is(err, errLeaseAuthorityLost) {
		t.Fatalf("committed preparation failure canceled its own acknowledgement: %v", err)
	}
	if phase == "prepare" {
		if err == nil || !strings.Contains(err.Error(), "image preparation failed; previous workload was not changed") {
			t.Fatalf("prepare did not return its settled failure: %v", err)
		}
	} else if err != nil {
		t.Fatalf("prepared release failure did not settle: %v", err)
	}
	if c.completeCalls != 1 || c.createCalls != 0 || c.materializedCalls != 0 || c.applyCalls != 0 || f.created || c.op.MutationHold || c.op.Result == nil || *c.op.Result != (domain.HostingResult{Outcome: "failed"}) || len(j.Snapshot().Hosting) != 0 || len(j.Snapshot().Events) != 0 {
		t.Fatal("preparation failure mutated storage, repeated completion or left unsettled evidence")
	}
	if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", parent, func(string) (hostingStorageEngine, error) {
		t.Error("settled preparation replay opened engine")
		return nil, errors.New("forbidden")
	}); err != nil {
		t.Fatal(err)
	}
	next, err := runnerclient.New(server.URL, "token").Claim(parent)
	if err != nil || next.Run.ID != replacement.Run.ID || next.Lease != replacement.Lease {
		t.Fatalf("next claim after settled failure was blocked: %v", err)
	}
}
