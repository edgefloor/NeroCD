package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nerocd/internal/runner"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

type runtimeLogClientFixture struct {
	claim  func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error)
	append func(context.Context, domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error)
}

func (f runtimeLogClientFixture) ClaimRuntimeLogSessions(ctx context.Context, in domain.HostingRuntimeLogClaimInput, _ ...runnerclient.RuntimeLogTransport) (domain.HostingRuntimeLogClaims, error) {
	return f.claim(ctx, in)
}
func (f runtimeLogClientFixture) AppendRuntimeLogs(ctx context.Context, in domain.HostingRuntimeLogBatch, _ ...runnerclient.RuntimeLogTransport) (domain.HostingRuntimeLogAck, error) {
	return f.append(ctx, in)
}

type runtimeLogSourceFixture struct {
	verify func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error)
	open   func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error)
}

func (f runtimeLogSourceFixture) Verify(ctx context.Context, t domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
	return f.verify(ctx, t)
}
func (f runtimeLogSourceFixture) Open(ctx context.Context, t domain.HostingRuntimeLogTarget) (io.ReadCloser, error) {
	return f.open(ctx, t)
}
func (runtimeLogSourceFixture) Close() {}
func runtimeLogClaimFixture() domain.HostingRuntimeLogClaim {
	return domain.HostingRuntimeLogClaim{SessionID: "rls_test", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), EpochExpiresAt: time.Now().Add(9 * time.Second), DemandExpiresAt: time.Now().Add(29 * time.Second), Target: runtimeLogTargetFixture(), NextSequence: 1, NextBatchSequence: 1}
}
func TestRuntimeLogClaimsValidateBeforeDocker(t *testing.T) {
	for _, mode := range []string{"valid", "runner", "short-id", "namespace", "duplicate", "too-many", "epoch", "expired", "demand", "batch", "cursor", "nil"} {
		t.Run(mode, func(t *testing.T) {
			claim := runtimeLogClaimFixture()
			page := domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{claim}, NextCursor: claim.SessionID, EndOfSweep: true}
			switch mode {
			case "runner":
				page.Targets[0].Target.RunnerID = "foreign"
			case "short-id":
				page.Targets[0].Target.ContainerID = "short"
			case "namespace":
				page.Targets[0].Target.RuntimeNamespace = "../a"
			case "duplicate":
				page.Targets = append(page.Targets, claim)
			case "too-many":
				page.Targets = append(page.Targets, claim, claim, claim, claim)
			case "epoch":
				page.Targets[0].EpochExpiresAt = time.Now().Add(time.Minute)
			case "expired":
				page.Targets[0].EpochExpiresAt = time.Now()
			case "demand":
				page.Targets[0].DemandExpiresAt = time.Now()
			case "batch":
				page.Targets[0].NextBatchSequence = 0
			case "cursor":
				page.NextCursor = "rls_a"
			case "nil":
				page.Targets = nil
			}
			now := time.Now()
			grants, err := validateRuntimeLogClaims(page, claim.Target.RunnerID, claim.InstanceID, now, now)
			if (err == nil) != (mode == "valid") {
				t.Fatal("malformed authority accepted", err)
			}
			if mode == "valid" && (!grants[0].deadline.Before(claim.EpochExpiresAt) || grants[0].deadline.Sub(now) > 9*time.Second) {
				t.Fatal("watchdog did not reserve authority")
			}
		})
	}
	var touched atomic.Int32
	c := runtimeLogCollector{runner: "runner_a", instance: strings.Repeat("a", 32), client: runtimeLogClientFixture{claim: func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
		return domain.HostingRuntimeLogClaims{Targets: nil}, nil
	}}, source: runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
		touched.Add(1)
		return runtimeLogSourceIdentity{}, nil
	}}}
	_ = c.round(t.Context())
	if touched.Load() != 0 {
		t.Fatal("invalid claim page reached Docker")
	}
}
func TestRuntimeLogImmutableRetryAndIndependentEmptyBatchCounter(t *testing.T) {
	claim := runtimeLogClaimFixture()
	var bodies []domain.HostingRuntimeLogBatch
	c := runtimeLogCollector{client: runtimeLogClientFixture{append: func(_ context.Context, b domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			return domain.HostingRuntimeLogAck{}, &runnerclient.APIError{Err: io.ErrUnexpectedEOF}
		}
		return domain.HostingRuntimeLogAck{}, nil
	}}}
	if !c.publish(t.Context(), time.Now().Add(time.Second), claim, []domain.HostingRuntimeLogEvent{}, "following") {
		t.Fatal("response loss did not retry")
	}
	if len(bodies) != 2 || !reflect.DeepEqual(bodies[0], bodies[1]) || bodies[0].ExpectedSequence != 0 || bodies[0].BatchSequence != 1 {
		t.Fatal("pending canonical batch changed")
	}
	claim.NextBatchSequence++
	if !c.publish(t.Context(), time.Now().Add(time.Second), claim, []domain.HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "output\n"}}, "following") {
		t.Fatal("output failed")
	}
	if bodies[2].ExpectedSequence != 0 || bodies[2].BatchSequence != 2 {
		t.Fatal("empty batch advanced event sequence")
	}
	calls := 0
	c.client = runtimeLogClientFixture{append: func(context.Context, domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
		calls++
		return domain.HostingRuntimeLogAck{}, &runnerclient.APIError{StatusCode: 409}
	}}
	if c.publish(t.Context(), time.Now().Add(time.Second), claim, []domain.HostingRuntimeLogEvent{}, "following") || calls != 1 {
		t.Fatal("conflict retried")
	}
}
func TestRuntimeLogRenewalKeepsOneReaderAndLocalCounters(t *testing.T) {
	claim := runtimeLogClaimFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	var opens, verifies atomic.Int32
	published := make(chan domain.HostingRuntimeLogBatch, 8)
	c := runtimeLogCollector{token: "private", source: runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
		verifies.Add(1)
		return runtimeLogSourceIdentity{Driver: "local"}, nil
	}, open: func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error) {
		opens.Add(1)
		return reader, nil
	}}, client: runtimeLogClientFixture{append: func(_ context.Context, b domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
		published <- b
		return domain.HostingRuntimeLogAck{}, nil
	}}}
	updates := make(chan runtimeLogGrant, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.session(ctx, runtimeLogGrant{claim, time.Now().Add(3 * time.Second)}, updates)
	}()
	select {
	case first := <-published:
		if first.BatchSequence != 1 || len(first.Events) != 0 {
			t.Error("empty initial batch invalid")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("following batch missing")
	}
	updates <- runtimeLogGrant{claim, time.Now().Add(4 * time.Second)} // Deliberately stale counters.
	if _, err := writer.Write(runtimeLogFrame(1, "output\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-published:
		if second.BatchSequence != 2 || second.ExpectedSequence != 0 || len(second.Events) != 1 {
			t.Error("renewal overwrote batch/event counters")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second batch missing")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not close and join")
	}
	if opens.Load() != 1 || verifies.Load() < 2 {
		t.Fatal("renewal duplicated reader or skipped exact identity")
	}
}
func TestRuntimeLogAuthorityAndStalledAppendJoin(t *testing.T) {
	for _, mode := range []string{"expiry", "cancelled-append", "renew-mismatch", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			claim := runtimeLogClaimFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			entered := make(chan struct{}, 1)
			states := make(chan string, 4)
			var count atomic.Int32
			source := runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
				if mode == "unsupported" {
					return runtimeLogSourceIdentity{}, errRuntimeLogDriver
				}
				if mode == "renew-mismatch" && count.Add(1) > 1 {
					return runtimeLogSourceIdentity{}, errors.New("daemon mismatch")
				}
				return runtimeLogSourceIdentity{Driver: "json-file"}, nil
			}, open: func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error) { return reader, nil }}
			c := runtimeLogCollector{source: source, client: runtimeLogClientFixture{append: func(ctx context.Context, b domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
				states <- b.State
				select {
				case entered <- struct{}{}:
				default:
				}
				if mode == "cancelled-append" {
					<-ctx.Done()
					return domain.HostingRuntimeLogAck{}, ctx.Err()
				}
				return domain.HostingRuntimeLogAck{}, nil
			}}}
			updates := make(chan runtimeLogGrant, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				c.session(ctx, runtimeLogGrant{claim, time.Now().Add(1500 * time.Millisecond)}, updates)
			}()
			if mode == "renew-mismatch" {
				updates <- runtimeLogGrant{claim, time.Now().Add(5 * time.Second)}
			}
			if mode == "cancelled-append" {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("append not started")
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("authority loss or cancellation failed to join")
			}
			if mode == "unsupported" {
				select {
				case state := <-states:
					if state != "unsupported_driver" {
						t.Fatal("unsupported driver hidden")
					}
				default:
					t.Fatal("missing unsupported state")
				}
			}
			_ = reader.Close()
		})
	}
}
func TestRuntimeLogQueueBound(t *testing.T) {
	queue := runtimeLogQueue{events: make(chan domain.HostingRuntimeLogEvent, 64)}
	event := domain.HostingRuntimeLogEvent{Message: strings.Repeat("x", 16384)}
	accepted := 0
	for queue.push(t.Context(), event) == nil {
		accepted++
	}
	if accepted != 3 || queue.bytes.Load() > 64<<10 {
		t.Fatal("redacted queue exceeded byte budget")
	}
}

func TestRuntimeLogReplayFailureStartsNoCollector(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plan := imageTestPlan(t)
	authority := runner.AttemptIdentity{RunID: "run_logs_replay", LeaseID: "lease", Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err = journal.AppendHosting(runner.JournalHosting{ID: "intent", Attempt: authority, OperationID: "hop_replay", Phase: "apply_intent", Plan: &plan, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_a", "capabilities": []string{domain.HostingRuntimeLogsCapability}})
		case "/api/v1/runners/hosting/status":
			_ = json.NewEncoder(w).Encode(domain.HostingOperation{ID: "hop_replay", Plan: &plan, MutationHold: true})
		default:
			t.Error("unexpected replay traffic", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	var claimed atomic.Int32
	settings := &HostingRuntimeLogsConfig{TrustedLoopbackHTTP: true, client: runtimeLogClientFixture{claim: func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
		claimed.Add(1)
		return domain.HostingRuntimeLogClaims{}, errors.New("unexpected collector")
	}}}
	if err = RunContext(t.Context(), Config{Server: server.URL, Token: "token", RunnerID: "runner_a", Execute: true, WorkDir: t.TempDir(), JournalDir: journalPath, HostingRuntimeLogs: settings}); err == nil || claimed.Load() != 0 {
		t.Fatal("failed replay started log collection")
	}
}
func TestRuntimeLogRuntimeErrorJoinsClaimAndCooldown(t *testing.T) {
	started, joined := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_a", "capabilities": []string{domain.HostingRuntimeLogsCapability}})
		case "/api/v1/runners/heartbeat":
			select {
			case <-started:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(503)
		default:
			t.Error("unexpected runtime traffic", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	settings := &HostingRuntimeLogsConfig{TrustedLoopbackHTTP: true, source: runtimeLogSourceFixture{}, client: runtimeLogClientFixture{claim: func(ctx context.Context, _ domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
		close(started)
		<-ctx.Done()
		close(joined)
		return domain.HostingRuntimeLogClaims{}, ctx.Err()
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := RunContext(ctx, Config{Server: server.URL, Token: "token", RunnerID: "runner_a", Execute: true, WorkDir: t.TempDir(), JournalDir: filepath.Join(t.TempDir(), "journal"), HostingRuntimeLogs: settings}); err == nil {
		t.Fatal("heartbeat failure lost")
	}
	select {
	case <-joined:
	default:
		t.Fatal("runtime returned before claim cancellation joined")
	}
}

func TestRuntimeLogSharedTargetSessionsAndFourReaderLimit(t *testing.T) {
	claim := runtimeLogClaimFixture()
	second := claim
	second.SessionID = "rls_test2"
	now := time.Now()
	page := domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{claim, second}, NextCursor: second.SessionID, EndOfSweep: true}
	if _, err := validateRuntimeLogClaims(page, claim.Target.RunnerID, claim.InstanceID, now, now); err != nil {
		t.Fatal("independent viewer sessions cannot share a settled target", err)
	}
	for i := 2; i < 4; i++ {
		next := claim
		next.SessionID = fmt.Sprintf("rls_test%d", i+1)
		page.Targets = append(page.Targets, next)
		page.NextCursor = next.SessionID
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var opened atomic.Int32
	ready := make(chan struct{})
	done := make(chan struct{})
	c := runtimeLogCollector{runner: claim.Target.RunnerID, instance: claim.InstanceID, client: runtimeLogClientFixture{claim: func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
		return page, nil
	}, append: func(context.Context, domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
		return domain.HostingRuntimeLogAck{}, nil
	}}, source: runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
		return runtimeLogSourceIdentity{Driver: "local"}, nil
	}, open: func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error) {
		reader, _ := io.Pipe()
		if opened.Add(1) == 4 {
			close(ready)
		}
		return reader, nil
	}}}
	go func() { defer close(done); _ = c.round(ctx) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("four bounded readers did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded group did not join")
	}
	if opened.Load() != 4 {
		t.Fatal("collector exceeded reader limit")
	}
}
func TestRuntimeLogLostRenewalResponseIncludedInCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		claim := runtimeLogClaimFixture()
		reader, writer := io.Pipe()
		defer func() { _ = writer.Close() }()
		calls := 0
		c := runtimeLogCollector{runner: claim.Target.RunnerID, instance: claim.InstanceID, source: runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
			return runtimeLogSourceIdentity{Driver: "local"}, nil
		}, open: func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error) { return reader, nil }}, client: runtimeLogClientFixture{claim: func(ctx context.Context, _ domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
			calls++
			if calls > 1 {
				<-ctx.Done()
				return domain.HostingRuntimeLogClaims{}, &runnerclient.APIError{Err: io.ErrUnexpectedEOF}
			}
			return domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{claim}, NextCursor: claim.SessionID, EndOfSweep: true}, nil
		}, append: func(context.Context, domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
			return domain.HostingRuntimeLogAck{}, nil
		}}}
		delay := c.round(t.Context())
		if calls < 2 || delay < 9*time.Second {
			t.Fatal("cooldown ignored an accepted renewal hidden by response loss")
		}
	})
}

func TestRuntimeLogQueueFullChannelBackpressureAndCancel(t *testing.T) {
	for _, mode := range []string{"drain", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			queue := runtimeLogQueue{events: make(chan domain.HostingRuntimeLogEvent, 64)}
			event := domain.HostingRuntimeLogEvent{Message: "redacted short line\n"}
			for range 64 {
				if err := queue.push(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			entered, done := make(chan struct{}), make(chan error, 1)
			go func() { close(entered); done <- queue.push(ctx, event) }()
			<-entered
			select {
			case <-done:
				t.Fatal("full event channel dropped source instead of applying backpressure")
			case <-time.After(20 * time.Millisecond):
			}
			if queue.bytes.Load() > 64<<10 {
				t.Fatal("pending event escaped byte reservation")
			}
			if mode == "cancel" {
				cancel()
			} else {
				queue.take(<-queue.events)
			}
			select {
			case err := <-done:
				if (err != nil) != (mode == "cancel") {
					t.Fatal("wrong bounded handoff result", err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked queue send did not wake")
			}
			if queue.bytes.Load() > 64<<10 {
				t.Fatal("queue grew beyond byte budget")
			}
		})
	}
}

type runtimeLogCloseProof struct {
	*io.PipeReader
	closed atomic.Bool
}

func (r *runtimeLogCloseProof) Close() error { r.closed.Store(true); return r.PipeReader.Close() }

func TestRuntimeLogRenewalRejectionImmediatelyJoinsAndDiscardsBufferedOutput(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				claim := runtimeLogClaimFixture()
				reader, writer := io.Pipe()
				defer func() { _ = writer.Close() }()
				body := &runtimeLogCloseProof{PipeReader: reader}
				rejectedSignal, pendingStarted, buffered, producerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				releasePending := make(chan struct{})
				go func() { <-rejectedSignal; time.Sleep(25 * time.Millisecond); close(releasePending) }()
				go func() {
					defer close(producerDone)
					time.Sleep(1500 * time.Millisecond)
					if _, err := writer.Write(runtimeLogFrame(1, "first pending output\n")); err != nil {
						return
					}
					<-pendingStarted
					if _, err := writer.Write(runtimeLogFrame(1, "buffered output must be discarded\n")); err == nil {
						close(buffered)
					}
				}()
				var rejected atomic.Bool
				var appendAfterRejection atomic.Bool
				calls := 0
				pendingID := ""
				var rejectedAt time.Time
				c := runtimeLogCollector{runner: claim.Target.RunnerID, instance: claim.InstanceID, source: runtimeLogSourceFixture{verify: func(context.Context, domain.HostingRuntimeLogTarget) (runtimeLogSourceIdentity, error) {
					return runtimeLogSourceIdentity{Driver: "local"}, nil
				}, open: func(context.Context, domain.HostingRuntimeLogTarget) (io.ReadCloser, error) { return body, nil }}, client: runtimeLogClientFixture{claim: func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
					calls++
					if calls == 1 {
						return domain.HostingRuntimeLogClaims{Targets: []domain.HostingRuntimeLogClaim{claim}, NextCursor: claim.SessionID, EndOfSweep: true}, nil
					}
					<-buffered
					if rejected.CompareAndSwap(false, true) {
						rejectedAt = time.Now()
						close(rejectedSignal)
					}
					return domain.HostingRuntimeLogClaims{}, &runnerclient.APIError{StatusCode: status}
				}, append: func(ctx context.Context, batch domain.HostingRuntimeLogBatch) (domain.HostingRuntimeLogAck, error) {
					if rejected.Load() && batch.BatchID != pendingID {
						appendAfterRejection.Store(true)
					}
					if pendingID == "" && len(batch.Events) > 0 {
						pendingID = batch.BatchID
						close(pendingStarted)
						select {
						case <-ctx.Done():
							return domain.HostingRuntimeLogAck{}, ctx.Err()
						case <-releasePending:
						}
					}
					return domain.HostingRuntimeLogAck{}, nil
				}}}
				delay := c.round(t.Context())
				returnedAt := time.Now()
				<-producerDone
				<-releasePending
				if !rejected.Load() || !body.closed.Load() {
					t.Fatal("rejected renewal returned without closing and joining active reader")
				}
				if returnedAt.Sub(rejectedAt) != 0 {
					t.Error("renewal rejection waited for append timeout or authority watchdog")
				}
				if appendAfterRejection.Load() {
					t.Error("queued output was published after renewal rejection")
				}
				if delay < 10*time.Second || delay > 11*time.Second {
					t.Error("rejection discarded conservative completed-attempt cooldown")
				}
			})
		})
	}
}
func TestRuntimeLogRejectionCooldownIsParentCancellable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		claimed, done := make(chan struct{}), make(chan struct{})
		c := runtimeLogCollector{instance: strings.Repeat("a", 32), client: runtimeLogClientFixture{claim: func(context.Context, domain.HostingRuntimeLogClaimInput) (domain.HostingRuntimeLogClaims, error) {
			close(claimed)
			return domain.HostingRuntimeLogClaims{}, &runnerclient.APIError{StatusCode: http.StatusForbidden}
		}}}
		go func() { defer close(done); c.run(ctx) }()
		<-claimed
		synctest.Wait()
		started := time.Now()
		cancel()
		<-done
		if time.Since(started) != 0 {
			t.Fatal("parent shutdown waited for rejection cooldown")
		}
	})
}
