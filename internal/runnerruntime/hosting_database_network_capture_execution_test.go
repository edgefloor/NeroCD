//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

type captureExecutionTestJournal struct {
	*runner.AttemptJournal
	failPhase  string
	failRetire bool
}

func (j *captureExecutionTestJournal) AppendDatabaseNetworkCapture(e runner.JournalDatabaseNetworkCapture) (runner.JournalDatabaseNetworkCapture, error) {
	if e.Phase == j.failPhase {
		return runner.JournalDatabaseNetworkCapture{}, errors.New("private_journal_path_canary")
	}
	return j.AttemptJournal.AppendDatabaseNetworkCapture(e)
}
func (j *captureExecutionTestJournal) RetireDatabaseNetworkCapture(a runner.AttemptIdentity) error {
	if j.failRetire {
		return errors.New("private_journal_path_canary")
	}
	return j.AttemptJournal.RetireDatabaseNetworkCapture(a)
}

type captureExecutionTestObserver struct {
	actual databaseNetworkCaptureObserver
	calls  int
	hook   func(context.Context, domain.HostingDatabaseNetworkCapturePlan, func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error)
}

func (o *captureExecutionTestObserver) ObserveDatabaseNetworkOrigin(ctx context.Context, p domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
	o.calls++
	if o.hook != nil {
		return o.hook(ctx, p, before)
	}
	return o.actual.ObserveDatabaseNetworkOrigin(ctx, p, before)
}

type captureExecutionTestClient struct {
	t                     *testing.T
	j                     *captureExecutionTestJournal
	claim                 domain.HostingDatabaseNetworkCaptureClaim
	claimCalls, planCalls int
	requests              []domain.HostingDatabaseNetworkOriginRequest
	ack                   *domain.HostingDatabaseNetworkOriginReceipt
	lostAck, revoked      bool
	planHook              func(context.Context, int) (domain.HostingDatabaseNetworkCapture, error)
	ackHook               func(*domain.HostingDatabaseNetworkOriginReceipt)
}

func (c *captureExecutionTestClient) ClaimHostingDatabaseNetworkCapture(ctx context.Context, runnerID, id string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	c.t.Helper()
	c.claimCalls++
	if len(c.j.Snapshot().DatabaseNetworkCapture) != 0 {
		c.t.Fatal("claim before inspecting retained journal")
	}
	if _, ok := ctx.Deadline(); !ok {
		c.t.Fatal("unbounded claim")
	}
	if ctx.Err() != nil {
		return domain.HostingDatabaseNetworkCaptureClaim{}, errors.New("private_bearer_canary")
	}
	return c.claim, nil
}
func (c *captureExecutionTestClient) HostingDatabaseNetworkCapturePlan(ctx context.Context, runnerID string, a domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error) {
	c.t.Helper()
	c.planCalls++
	chain := c.j.Snapshot().DatabaseNetworkCapture
	if len(chain) == 0 || chain[0].Phase != "claim" || a != captureExecutionAuthority(c.claim) || runnerID != c.claim.Lease.RunnerID {
		c.t.Fatal("plan lacked durable original claim")
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.After(c.claim.Lease.ExpiresAt) {
		c.t.Fatal("plan escaped original expiry")
	}
	if c.planHook != nil {
		return c.planHook(ctx, c.planCalls)
	}
	if c.revoked || ctx.Err() != nil {
		return domain.HostingDatabaseNetworkCapture{}, errors.New("private_fence_canary")
	}
	return c.claim.Capture, nil
}
func (c *captureExecutionTestClient) RecordHostingDatabaseNetworkOrigin(ctx context.Context, runnerID string, in domain.HostingDatabaseNetworkOriginRequest, p domain.HostingDatabaseNetworkCapturePlan) (domain.HostingDatabaseNetworkOriginReceipt, error) {
	c.t.Helper()
	chain := c.j.Snapshot().DatabaseNetworkCapture
	if len(chain) != 3 || chain[2].Origin == nil || !reflect.DeepEqual(*chain[2].Origin, in) || p.Hash != c.claim.Capture.Plan.Hash || runnerID != p.RunnerID {
		c.t.Fatal("origin published before exact durable request")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		c.t.Fatal("unbounded origin request")
	}
	c.requests = append(c.requests, in)
	if c.revoked || ctx.Err() != nil {
		return domain.HostingDatabaseNetworkOriginReceipt{}, errors.New("private_bearer_canary")
	}
	if c.ack == nil {
		v := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: time.Now().UTC()}, OperationID: in.OperationID, SourceOperationID: p.SourceOperationID, PlanHash: p.Hash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: runnerID}
		c.ack = &v
	}
	if c.lostAck && len(c.requests) == 1 {
		return domain.HostingDatabaseNetworkOriginReceipt{}, errors.New("private_transport_canary")
	}
	v := *c.ack
	if c.ackHook != nil {
		c.ackHook(&v)
	}
	return v, nil
}

type captureExecutionFixture struct {
	j        *captureExecutionTestJournal
	client   *captureExecutionTestClient
	observer *captureExecutionTestObserver
	db       *databaseFake
	path     string
}

func newCaptureExecutionFixture(t *testing.T, engine string) *captureExecutionFixture {
	t.Helper()
	db, observer, p, _ := databaseOriginObserverFixture(t, engine)
	path := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &captureExecutionTestJournal{AttemptJournal: j}
	t.Cleanup(func() {
		if err := wrapped.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	capture := domain.HostingDatabaseNetworkCapture{ID: p.OperationID, RunID: "capture_" + p.OperationID, State: "running", CreatedBy: "owner", Plan: p, CreatedAt: now.Add(-time.Second), UpdatedAt: now}
	claim := domain.HostingDatabaseNetworkCaptureClaim{Capture: capture, Lease: domain.RunLease{ID: "capture_lease", RunID: capture.RunID, RunnerID: p.RunnerID, Status: domain.LeaseActive, Attempt: 1, Fence: "private_fence_canary", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	f := &captureExecutionFixture{j: wrapped, db: db, path: path, observer: &captureExecutionTestObserver{actual: observer}}
	f.client = &captureExecutionTestClient{t: t, j: wrapped, claim: claim}
	return f
}
func (f *captureExecutionFixture) execute(ctx context.Context) error {
	return executeDatabaseNetworkCapture(ctx, f.j, f.client, f.observer, f.client.claim.Lease.RunnerID, f.client.claim.Capture.ID)
}
func (f *captureExecutionFixture) replay(ctx context.Context) error {
	return replayDatabaseNetworkCapture(ctx, f.j, f.client, f.observer, f.client.claim.Lease.RunnerID, f.client.claim.Capture.ID)
}

func TestDatabaseNetworkCaptureExecutionBothEnginesReadOnly(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, engine)
			before := observerTree(t, f.db.config.stateRoot, f.db.config.secretRoot)
			if err := f.execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			if f.client.claimCalls != 1 || f.observer.calls != 1 || len(f.client.requests) != 1 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
				t.Fatal("capture did not settle exactly once")
			}
			assertObserverReadOnly(t, f.db, before)
		})
	}
}

func TestDatabaseNetworkCaptureExecutionPersistenceBeforeEachBoundary(t *testing.T) {
	for _, phase := range []string{"claim", "plan", "origin", "ack"} {
		t.Run(phase, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "postgresql")
			f.j.failPhase = phase
			err := f.execute(t.Context())
			if err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatal("persistence failure lost or exposed diagnostic", err)
			}
			wantDepth := map[string]int{"claim": 0, "plan": 1, "origin": 2, "ack": 3}[phase]
			if len(f.j.Snapshot().DatabaseNetworkCapture) != wantDepth {
				t.Fatal("unexpected durable phase after failure")
			}
			if (phase == "claim" || phase == "plan") && f.observer.calls != 0 {
				t.Fatal("observation preceded durable claim/plan")
			}
			if phase != "ack" && len(f.client.requests) != 0 {
				t.Fatal("publication preceded durable origin")
			}
			if phase == "claim" && f.client.planCalls != 0 {
				t.Fatal("plan request preceded durable claim")
			}
			if phase == "ack" {
				f.j.failPhase = ""
				if err := f.replay(t.Context()); err != nil || f.observer.calls != 1 || len(f.client.requests) != 2 {
					t.Fatal("ACK persistence retry repeated observation", err)
				}
			}
		})
	}
}

func TestDatabaseNetworkCaptureExecutionRejectsSubstitutedIdentity(t *testing.T) {
	for _, mode := range []string{"claim_id", "claim_runner", "plan", "origin", "ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "redis")
			id := f.client.claim.Capture.ID
			runnerID := f.client.claim.Lease.RunnerID
			switch mode {
			case "claim_id":
				f.client.claim.Capture.ID = "hnc_" + strings.Repeat("0", 32)
			case "claim_runner":
				f.client.claim.Lease.RunnerID = "foreign"
			case "plan":
				f.client.planHook = func(context.Context, int) (domain.HostingDatabaseNetworkCapture, error) {
					v := f.client.claim.Capture
					v.UpdatedAt = v.UpdatedAt.Add(time.Second)
					return v, nil
				}
			case "origin":
				f.observer.hook = func(ctx context.Context, p domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
					v, err := f.observer.actual.ObserveDatabaseNetworkOrigin(ctx, p, before)
					v.Daemon = "foreign"
					return v, err
				}
			case "ack":
				f.client.ackHook = func(v *domain.HostingDatabaseNetworkOriginReceipt) { v.BodyHash = "sha256:" + strings.Repeat("0", 64) }
			}
			if err := executeDatabaseNetworkCapture(t.Context(), f.j, f.client, f.observer, runnerID, id); err == nil {
				t.Fatal("substituted exchange accepted")
			}
			if mode != "ack" && len(f.client.requests) != 0 {
				t.Fatal("substitution reached publication")
			}
			if (mode == "claim_id" || mode == "claim_runner" || mode == "plan") && f.observer.calls != 0 {
				t.Fatal("substitution reached observer")
			}
		})
	}
}

func TestDatabaseNetworkCaptureExecutionRevocationDuringObservation(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	f.client.planHook = func(_ context.Context, n int) (domain.HostingDatabaseNetworkCapture, error) {
		if n >= 3 {
			return domain.HostingDatabaseNetworkCapture{}, errors.New("private_revocation_canary")
		}
		return f.client.claim.Capture, nil
	}
	if err := f.execute(t.Context()); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("revoked observation accepted", err)
	}
	if len(f.client.requests) != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 2 {
		t.Fatal("revocation published or discarded retained attempt")
	}
}

func TestDatabaseNetworkCaptureExecutionCancellationJoinsObserver(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	entered, exited := make(chan struct{}), make(chan struct{})
	f.observer.hook = func(ctx context.Context, _ domain.HostingDatabaseNetworkCapturePlan, _ func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return domain.HostingDatabaseNetworkOrigin{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.execute(ctx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("owned coordinator did not join during cleanup")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not enter")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coordinator did not join")
	}
	select {
	case <-exited:
	default:
		t.Fatal("observer outlived coordinator")
	}
	if len(f.client.requests) != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 2 {
		t.Fatal("cancellation published or discarded attempt")
	}
}

func captureExecutionCanonical(t *testing.T, in domain.HostingDatabaseNetworkOriginRequest) string {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
