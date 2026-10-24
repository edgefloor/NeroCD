//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func (f *captureExecutionFixture) restart(t *testing.T) {
	t.Helper()
	if err := f.j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := runner.OpenAttemptJournal(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.j.AttemptJournal = j
}

func TestDatabaseNetworkCaptureReplayLostCommittedAckAfterExpiry(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	f.client.claim.Lease.ExpiresAt = time.Now().UTC().Add(3 * time.Second)
	f.client.lostAck = true
	if err := f.execute(t.Context()); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("lost ACK was accepted or leaked", err)
	}
	chain := f.j.Snapshot().DatabaseNetworkCapture
	if len(chain) != 3 || f.observer.calls != 1 || len(f.client.requests) != 1 {
		t.Fatal("lost ACK did not retain exact origin")
	}
	original := *chain[2].Origin
	attempt := chain[0].Attempt
	f.restart(t)
	if !reflect.DeepEqual(f.j.Snapshot().DatabaseNetworkCapture, chain) {
		t.Fatal("restart changed immutable capture evidence")
	}
	timer := time.NewTimer(time.Until(attempt.ExpiresAt) + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("expiry wait canceled")
	}
	plans, claims := f.client.planCalls, f.client.claimCalls
	if err := replayDatabaseNetworkCapture(t.Context(), f.j, f.client, nil, f.client.claim.Lease.RunnerID, f.client.claim.Capture.ID); err != nil {
		t.Fatal(err)
	}
	if f.observer.calls != 1 || f.client.planCalls != plans || f.client.claimCalls != claims || len(f.client.requests) != 2 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
		t.Fatal("origin replay resampled, refreshed authority or failed to retire")
	}
	if captureExecutionCanonical(t, f.client.requests[0]) != captureExecutionCanonical(t, f.client.requests[1]) || !reflect.DeepEqual(original, f.client.requests[1]) {
		t.Fatal("replay changed original request bytes/time/fence/receipt")
	}
}

func TestDatabaseNetworkCaptureReplayLiveOriginalAttempt(t *testing.T) {
	for _, phase := range []string{"claim", "plan"} {
		t.Run(phase, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "redis")
			claim := f.client.claim
			attempt := journalAttemptIdentity(claim.Lease.RunID, claim.Lease, nil)
			if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: attempt, Phase: "claim", Claim: &claim}); err != nil {
				t.Fatal(err)
			}
			if phase == "plan" {
				p := claim.Capture
				if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: attempt, Phase: "plan", Plan: &p}); err != nil {
					t.Fatal(err)
				}
			}
			f.restart(t)
			// A newer remote expiry is deliberately not imported into the saved attempt.
			f.client.claim.Lease.ExpiresAt = claim.Lease.ExpiresAt.Add(time.Minute)
			f.observer.hook = func(ctx context.Context, p domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
				if deadline, ok := ctx.Deadline(); !ok || !deadline.Before(attempt.ExpiresAt) {
					t.Fatal("restart extended original observation window")
				}
				return f.observer.actual.ObserveDatabaseNetworkOrigin(ctx, p, before)
			}
			if err := f.execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			if f.client.claimCalls != 0 || f.observer.calls != 1 || len(f.client.requests) != 1 || f.client.requests[0].HostingAuthority != captureExecutionAuthority(claim) {
				t.Fatal("restart replaced original authority")
			}
		})
	}
}

func TestDatabaseNetworkCaptureReplayExpiredPreOriginRemainsHeld(t *testing.T) {
	for _, phase := range []string{"claim", "plan"} {
		t.Run(phase, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "postgresql")
			claim := f.client.claim
			claim.Lease.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
			claim.Lease.ExpiresAt = claim.Lease.CreatedAt.Add(time.Minute)
			attempt := journalAttemptIdentity(claim.Lease.RunID, claim.Lease, nil)
			if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: attempt, Phase: "claim", Claim: &claim}); err != nil {
				t.Fatal(err)
			}
			if phase == "plan" {
				p := claim.Capture
				if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: attempt, Phase: "plan", Plan: &p}); err != nil {
					t.Fatal(err)
				}
			}
			f.restart(t)
			before := f.j.Snapshot()
			// execute must inspect this chain instead of asking for a replacement claim.
			if err := f.execute(t.Context()); err == nil {
				t.Fatal("expired pre-origin capture was accepted")
			}
			if f.client.claimCalls != 0 || f.client.planCalls != 0 || f.observer.calls != 0 || len(f.client.requests) != 0 || !reflect.DeepEqual(before, f.j.Snapshot()) {
				t.Fatal("expired chain was discarded, reclaimed or observed")
			}
		})
	}
}

func TestDatabaseNetworkCaptureReplayAckRetiresLocallyAfterFailure(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	f.j.failRetire = true
	if err := f.execute(t.Context()); err == nil {
		t.Fatal("retirement failure lost")
	}
	if len(f.j.Snapshot().DatabaseNetworkCapture) != 4 {
		t.Fatal("ACK was not durable before retirement")
	}
	f.restart(t)
	f.j.failRetire = false
	f.client.revoked = true
	plans, claims, requests := f.client.planCalls, f.client.claimCalls, len(f.client.requests)
	id := f.client.claim.Capture.ID
	if err := replayDatabaseNetworkCapture(t.Context(), f.j, f.client, f.observer, "foreign_runner", id); err == nil || len(f.j.Snapshot().DatabaseNetworkCapture) != 4 {
		t.Fatal("foreign configured identity retired capture")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := replayDatabaseNetworkCapture(ctx, f.j, nil, nil, f.client.claim.Lease.RunnerID, id); err != nil {
		t.Fatal("persisted ACK failed local retirement", err)
	}
	if f.client.planCalls != plans || f.client.claimCalls != claims || len(f.client.requests) != requests || f.observer.calls != 1 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
		t.Fatal("ACK replay contacted server or repeated observation")
	}
}

func TestDatabaseNetworkCaptureReplayRevocationRetainsOriginalOrigin(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	f.client.lostAck = true
	if err := f.execute(t.Context()); err == nil {
		t.Fatal("missing lost ACK")
	}
	f.restart(t)
	before := f.j.Snapshot()
	f.client.revoked = true
	if err := f.replay(t.Context()); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("revoked replay accepted or leaked", err)
	}
	if !reflect.DeepEqual(before, f.j.Snapshot()) || f.observer.calls != 1 {
		t.Fatal("revocation discarded or resampled origin")
	}
	if err := filepath.WalkDir(f.path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"private_bearer_canary", "private_transport_canary", "private_journal_path_canary"} {
			if strings.Contains(string(raw), secret) {
				t.Error("private diagnostic persisted in journal")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseNetworkCaptureExecutionPinnedExpiryCancelsObserver(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	f.client.claim.Lease.ExpiresAt = time.Now().UTC().Add(1500 * time.Millisecond)
	f.observer.hook = func(ctx context.Context, _ domain.HostingDatabaseNetworkCapturePlan, _ func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
		<-ctx.Done()
		return domain.HostingDatabaseNetworkOrigin{}, ctx.Err()
	}
	if err := f.execute(t.Context()); err == nil {
		t.Fatal("pinned expiry was ignored")
	}
	chain := f.j.Snapshot().DatabaseNetworkCapture
	if len(chain) != 2 || !chain[0].Attempt.ExpiresAt.Equal(f.client.claim.Lease.ExpiresAt) || len(f.client.requests) != 0 {
		t.Fatal("pinned expiry changed or published origin")
	}
}

func TestDatabaseNetworkCaptureExecutionCancellationJoinsHTTP(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	entered, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer close(exited)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := runnerclient.New(server.URL, "private_bearer_canary")
	client.HTTP = server.Client()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	joined := false
	go func() {
		done <- executeDatabaseNetworkCapture(ctx, f.j, client, f.observer, f.client.claim.Lease.RunnerID, f.client.claim.Capture.ID)
	}()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("owned HTTP coordinator did not join")
			}
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Error("owned HTTP handler did not join")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request did not enter")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal("HTTP cancellation lost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP coordinator did not join")
	}
	if f.observer.calls != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
		t.Fatal("canceled claim acquired durable capture or observation")
	}
}

func TestDatabaseNetworkCaptureReplayNoChainDoesNotClaim(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	if err := f.replay(t.Context()); err != nil || f.client.claimCalls != 0 || f.observer.calls != 0 {
		t.Fatal("empty replay selected new work", err)
	}
	// Keep this explicit compile-time consumer assertion beside replay coverage.
	var _ databaseNetworkCaptureClient = runnerclient.Client{}
	var _ databaseNetworkCaptureJournal = (*runner.AttemptJournal)(nil)
	var _ databaseNetworkCaptureObserver = (*databaseNetworkOriginObserver)(nil)
}
