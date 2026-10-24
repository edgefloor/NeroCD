//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type automaticCaptureTestClient struct {
	*captureExecutionTestClient
	selections int
	status     int
}

func (c *automaticCaptureTestClient) ClaimNextHostingDatabaseNetworkCapture(ctx context.Context, runnerID string) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	c.selections++
	if ctx.Err() != nil || runnerID != c.claim.Lease.RunnerID || len(c.j.Snapshot().DatabaseNetworkCapture) != 0 {
		c.t.Error("selection escaped identity, cancellation or retained hold")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		c.t.Error("unbounded automatic selection")
	}
	if c.status != 0 {
		return domain.HostingDatabaseNetworkCaptureClaim{}, &runnerclient.APIError{StatusCode: c.status, Err: errors.New("private_selection_canary")}
	}
	return c.claim, nil
}

func automaticCaptureConfig(f *captureExecutionFixture) Config {
	return Config{Server: "https://controller.example", Token: "automatic_token", RunnerID: f.client.claim.Lease.RunnerID, JournalDir: f.path, Execute: true, Database: &DatabaseExecutionConfig{StateRoot: f.db.config.stateRoot, SecretRoot: f.db.config.secretRoot, NetworkCapture: true}}
}

func automaticCaptureFixture(t *testing.T, f *captureExecutionFixture, client *automaticCaptureTestClient, open captureRuntimeObserverFactory, preflight func(context.Context) error) automaticDatabaseNetworkCaptureDispatcher {
	t.Helper()
	d, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(automaticCaptureConfig(f), f.j, client, open, preflight)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAutomaticCaptureSerialSelectionAndOrdinaryAdmission(t *testing.T) {
	for _, status := range []int{0, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "redis")
			client := &automaticCaptureTestClient{captureExecutionTestClient: f.client, status: status}
			opened, closed, preflights, ordinary := 0, 0, 0, 0
			d := automaticCaptureFixture(t, f, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
				opened++
				entries := f.j.Snapshot().DatabaseNetworkCapture
				if len(entries) != 2 || entries[0].Claim == nil || entries[1].Plan == nil {
					t.Error("host opened before durable claim and plan")
				}
				return f.observer, func() error { closed++; return nil }, nil
			}, func(context.Context) error { preflights++; return nil })
			if err := d.Reconcile(t.Context()); err != nil || client.selections != 0 || opened != 0 {
				t.Fatal("startup selected or opened host before replay")
			}
			_, outcome, err := claimRunnerIteration(t.Context(), d, func(context.Context) (domain.ClaimedRun, error) { ordinary++; return domain.ClaimedRun{}, nil })
			switch status {
			case 0:
				if err != nil || outcome != captureDispatchCompleted || opened != 1 || closed != 1 || f.observer.calls != 1 || preflights != 0 || ordinary != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
					t.Fatal("selected capture did not settle serially", err)
				}
			case http.StatusNotFound:
				if err != nil || outcome != captureDispatchEmpty || preflights != 1 || ordinary != 1 || opened != 0 {
					t.Fatal("empty selection bypassed host admission", err)
				}
			case http.StatusConflict:
				if err != nil || outcome != captureDispatchContended || ordinary != 0 || preflights != 0 || opened != 0 {
					t.Fatal("contention fell through to ordinary work")
				}
			default:
				if err == nil || ordinary != 0 || opened != 0 || strings.Contains(err.Error(), "canary") {
					t.Fatal("selection failure escaped fail-closed boundary")
				}
			}
			if client.claimCalls != 0 || client.selections != 1 {
				t.Fatal("automatic path used exact-ID selection or repeated selection")
			}
		})
	}
}

func TestAutomaticCaptureRetainedOriginReplaysWithoutDockerBeforeSelection(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	f.client.lostAck = true
	if err := f.execute(t.Context()); err == nil || len(f.j.Snapshot().DatabaseNetworkCapture) != 3 {
		t.Fatal("missing retained origin fixture")
	}
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client, status: http.StatusNotFound}
	f.client.lostAck = false
	preflights, ordinary := 0, 0
	d := automaticCaptureFixture(t, f, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Error("retained origin opened Docker")
		return nil, nil, errors.New("forbidden")
	}, func(context.Context) error { preflights++; return errors.New("daemon_unavailable") })
	if err := d.Reconcile(t.Context()); err != nil || client.selections != 0 || preflights != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 || f.observer.calls != 1 {
		t.Fatal("origin replay required host or selected replacement", err)
	}
	if len(client.requests) != 2 || client.requests[0] != client.requests[1] {
		t.Fatal("origin replay changed exact publication")
	}
	_, _, err := claimRunnerIteration(t.Context(), d, func(context.Context) (domain.ClaimedRun, error) { ordinary++; return domain.ClaimedRun{}, nil })
	if err == nil || ordinary != 0 || preflights != 1 {
		t.Fatal("ordinary admission bypassed failed host preflight")
	}
}

func TestAutomaticCaptureExpiredAndMixedEvidenceHoldOrdinaryWork(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	f.client.claim.Lease.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
	f.client.claim.Lease.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	at := journalAttemptIdentity(f.client.claim.Lease.RunID, f.client.claim.Lease, nil)
	if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "claim", Claim: &f.client.claim}); err != nil {
		t.Fatal(err)
	}
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client}
	d := automaticCaptureFixture(t, f, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("expired chain opened host")
		return nil, nil, nil
	}, func(context.Context) error { t.Fatal("held chain preflighted ordinary work"); return nil })
	if d.Reconcile(t.Context()) == nil {
		t.Fatal("expired chain was replaced or retired")
	}
	ordinary := 0
	_, _, err := claimRunnerIteration(t.Context(), d, func(context.Context) (domain.ClaimedRun, error) { ordinary++; return domain.ClaimedRun{}, nil })
	if err == nil || ordinary != 0 || client.selections != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 1 {
		t.Fatal("unresolved capture bypassed hold")
	}
	if captureRuntimeJournalCompatible(runner.JournalSnapshot{Events: []runner.JournalEvent{{}}, DatabaseNetworkCapture: f.j.Snapshot().DatabaseNetworkCapture}, f.client.claim.Capture.ID) {
		t.Fatal("mixed evidence admitted")
	}
}

func TestAutomaticCaptureLostSelectionResponseUsesSameLiveClaim(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client, status: http.StatusServiceUnavailable}
	opens := 0
	d := automaticCaptureFixture(t, f, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		opens++
		return f.observer, func() error { return nil }, nil
	}, func(context.Context) error { return nil })
	if _, err := d.Poll(t.Context()); err == nil || opens != 0 || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 {
		t.Fatal("lost claim response invented authority")
	}
	client.status = 0
	if _, err := d.Poll(t.Context()); err != nil || client.selections != 2 || client.claimCalls != 0 || opens != 1 || f.observer.calls != 1 {
		t.Fatal("same live claim did not resume once", err)
	}
}

func TestAutomaticCaptureCancellationJoinsAndRetainsPlan(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client}
	entered, joined, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.observer.hook = func(ctx context.Context, _ domain.HostingDatabaseNetworkCapturePlan, _ func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
		close(entered)
		<-ctx.Done()
		close(joined)
		return domain.HostingDatabaseNetworkOrigin{}, ctx.Err()
	}
	d := automaticCaptureFixture(t, f, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		return f.observer, func() error { close(closed); return nil }, nil
	}, func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := d.Poll(ctx); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not join")
	}
	select {
	case <-joined:
	default:
		t.Fatal("observer lifetime escaped")
	}
	select {
	case <-closed:
	default:
		t.Fatal("observer resource escaped")
	}
	if len(f.j.Snapshot().DatabaseNetworkCapture) != 2 || len(client.requests) != 0 {
		t.Fatal("cancellation lost plan or published origin")
	}
}

func TestAutomaticCaptureConfigAndUnsupportedSelectionFailClosed(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	c := automaticCaptureConfig(f)
	if validateAutomaticDatabaseNetworkCaptureConfig(c) != nil {
		t.Fatal("valid configuration rejected")
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.JournalDir = "" }, func(c *Config) { c.Token = "" }, func(c *Config) { c.Execute = false }, func(c *Config) { c.CompleteStatus = "succeeded" }, func(c *Config) { c.Server = "http://127.0.0.1:1" }, func(c *Config) { c.DatabaseNetworkCapture = &DatabaseNetworkCaptureExecutionConfig{} }} {
		bad := c
		mutate(&bad)
		if validateAutomaticDatabaseNetworkCaptureConfig(bad) == nil {
			t.Fatal("incompatible automatic configuration accepted")
		}
	}
	if _, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(c, f.j, f.client, openCaptureRuntimeObserver, func(context.Context) error { return nil }); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal("missing transport silently fell back")
	}
}

type automaticCaptureMixedJournal struct{ databaseNetworkCaptureJournal }

func (j automaticCaptureMixedJournal) Snapshot() runner.JournalSnapshot {
	s := j.databaseNetworkCaptureJournal.Snapshot()
	s.Events = []runner.JournalEvent{{}}
	return s
}

func TestAutomaticCaptureOrdinaryRecoveryKeepsHostPrerequisite(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client}
	preflights := 0
	d, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(automaticCaptureConfig(f), automaticCaptureMixedJournal{f.j}, client, openCaptureRuntimeObserver, func(context.Context) error { preflights++; return errors.New("host unavailable") })
	if err != nil {
		t.Fatal(err)
	}
	if d.Reconcile(t.Context()) == nil || preflights != 1 || client.selections != 0 {
		t.Fatal("ordinary journal recovery bypassed host preflight")
	}
}

func TestAutomaticCaptureRejectsInvalidSelectedAuthorityBeforeHost(t *testing.T) {
	for _, mode := range []string{"expired", "runner", "attempt", "capture_id", "capture_state"} {
		t.Run(mode, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "postgresql")
			config := automaticCaptureConfig(f)
			client := &automaticCaptureTestClient{captureExecutionTestClient: f.client}
			switch mode {
			case "expired":
				client.claim.Lease.ExpiresAt = time.Now().Add(-time.Second)
			case "runner":
				client.claim.Capture.Plan.RunnerID = "other"
			case "attempt":
				client.claim.Lease.Attempt = 0
			case "capture_id":
				client.claim.Capture.ID = "other"
			case "capture_state":
				client.claim.Capture.State = "queued"
			}
			d, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(config, f.j, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
				t.Fatal("invalid selection opened host")
				return nil, nil, nil
			}, func(context.Context) error { t.Fatal("invalid selection reached ordinary preflight"); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Poll(t.Context()); err == nil || len(f.j.Snapshot().DatabaseNetworkCapture) != 0 || client.planCalls != 0 {
				t.Fatal("invalid selected authority escaped validation")
			}
		})
	}
}

func TestAutomaticCaptureCannotUseGenericExecutionOrCompletion(t *testing.T) {
	claim := domain.ClaimedRun{Run: domain.TaskRun{RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseNetworkCapture}}}
	if _, _, err := claimRunnerIteration(t.Context(), nil, func(context.Context) (domain.ClaimedRun, error) { return claim, nil }); err == nil {
		t.Fatal("capture entered generic completion branch")
	}
	if err := executeClaimWithOptionsContext(t.Context(), "https://controller.invalid", "canary", claim, t.TempDir(), time.Millisecond, nil, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{}); err == nil {
		t.Fatal("capture entered generic execution branch")
	}
}

func TestAutomaticCaptureCancellationDuringHostPreflightPreventsOrdinaryClaim(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	client := &automaticCaptureTestClient{captureExecutionTestClient: f.client, status: http.StatusNotFound}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := automaticCaptureFixture(t, f, client, openCaptureRuntimeObserver, func(context.Context) error { cancel(); return nil })
	_, _, err := claimRunnerIteration(ctx, d, func(context.Context) (domain.ClaimedRun, error) {
		t.Error("canceled preflight admitted ordinary claim")
		return domain.ClaimedRun{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("host preflight lost cancellation", err)
	}
}

func TestAutomaticCaptureCombinedArchivePreflightFollowsRetainedPublication(t *testing.T) {
	for _, archiveAvailable := range []bool{false, true} {
		f := newCaptureExecutionFixture(t, "postgresql")
		f.client.lostAck = true
		if err := f.execute(t.Context()); err == nil {
			t.Fatal("missing retained origin")
		}
		f.client.lostAck = false
		client := &automaticCaptureTestClient{captureExecutionTestClient: f.client, status: http.StatusNotFound}
		config := automaticCaptureConfig(f)
		config.StorageArchive = &StorageArchiveExecutionConfig{}
		var order []string
		d, err := newAutomaticDatabaseNetworkCaptureDispatcherWith(config, f.j, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
			t.Fatal("retained publication opened observer")
			return nil, nil, nil
		}, func(context.Context) error { order = append(order, "database_host"); return nil })
		if err != nil {
			t.Fatal(err)
		}
		err = reconcileAutomaticCaptureStartup(t.Context(), config, d, func(context.Context) error {
			if len(f.j.Snapshot().DatabaseNetworkCapture) != 0 || len(client.requests) != 2 || client.selections != 0 {
				t.Error("archive Docker preflight preceded retained origin replay")
			}
			order = append(order, "archive_host")
			if !archiveAvailable {
				return errors.New("archive daemon unavailable")
			}
			return nil
		})
		if (err == nil) != archiveAvailable {
			t.Fatal("archive preflight failure was ignored", err)
		}
		if archiveAvailable {
			order = append(order, "archive_recovery")
			if strings.Join(order, ",") != "archive_host,database_host,archive_recovery" {
				t.Fatal("combined recovery bypassed host prerequisites", order)
			}
		} else if strings.Join(order, ",") != "archive_host" {
			t.Fatal("failed archive preflight admitted ordinary work", order)
		}
	}
}
