//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type captureRuntimeHTTP struct {
	mu      sync.Mutex
	claim   domain.HostingDatabaseNetworkCaptureClaim
	paths   []string
	bodies  [][]byte
	ack     *domain.HostingDatabaseNetworkOriginReceipt
	lostAck bool
	denied  atomic.Bool
}

func (h *captureRuntimeHTTP) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.paths = append(h.paths, r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer capture_runtime_canary" || r.URL.RawQuery != "" {
		t.Error("wrong transport identity")
	}
	if h.denied.Load() {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var out any
	switch r.URL.Path {
	case "/api/v1/runners/identity":
		out = map[string]any{"runner_id": h.claim.Lease.RunnerID, "capabilities": []string{domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseCapability, domain.RunTypeDatabaseDeploy, domain.HostingOwnedStorageCapability, domain.RunTypeComposeDeploy}}
	case "/api/v1/runners/database-network-captures/claim":
		var in struct {
			CaptureID string `json:"capture_id"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.CaptureID != h.claim.Capture.ID {
			t.Error("wrong exact-ID claim")
		}
		out = h.claim
	case "/api/v1/runners/database-network-captures/plan":
		out = h.claim.Capture
	case "/api/v1/runners/database-network-captures/origin":
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		h.bodies = append(h.bodies, raw)
		var in domain.HostingDatabaseNetworkOriginRequest
		if json.Unmarshal(raw, &in) != nil {
			t.Error("invalid origin")
			return
		}
		if h.ack == nil {
			v := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: time.Now().UTC()}, OperationID: in.OperationID, SourceOperationID: h.claim.Capture.Plan.SourceOperationID, PlanHash: in.PlanHash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: h.claim.Lease.RunnerID}
			h.ack = &v
		}
		if h.lostAck && len(h.bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		out = h.ack
	default:
		t.Error("generic route invoked")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Error(err)
	}
}
func captureRuntimeHTTPFixture(t *testing.T, f *captureExecutionFixture) (Config, runnerclient.Client, *captureRuntimeHTTP) {
	t.Helper()
	if err := f.j.Close(); err != nil {
		t.Fatal(err)
	}
	h := &captureRuntimeHTTP{claim: f.client.claim}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serve(t, w, r) }))
	t.Cleanup(server.Close)
	client := runnerclient.New(server.URL, "capture_runtime_canary")
	client.HTTP = server.Client()
	config := Config{DatabaseNetworkCapture: &DatabaseNetworkCaptureExecutionConfig{CaptureID: h.claim.Capture.ID}, Server: server.URL, Token: "capture_runtime_canary", RunnerID: h.claim.Lease.RunnerID, JournalDir: f.path, Execute: true}
	return config, client, h
}
func runCaptureRuntimeTest(ctx context.Context, c Config, client runnerclient.Client, open captureRuntimeObserverFactory) error {
	return runDatabaseNetworkCaptureRuntimeWith(ctx, c, client, func(ctx context.Context) (string, error) {
		return client.RuntimeExecutionIdentityWithOptions(ctx, runnerclient.RuntimeExecutionOptions{DatabaseNetworkCapture: true})
	}, open)
}
func captureRuntimeSnapshot(t *testing.T, path string) runner.JournalSnapshot {
	t.Helper()
	j, err := runner.OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	}()
	return j.Snapshot()
}

func TestDatabaseNetworkCaptureRuntimeBothEnginesExactReturn(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, engine)
			c, client, h := captureRuntimeHTTPFixture(t, f)
			opened, closed := 0, 0
			err := runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
				opened++
				return f.observer, func() error { closed++; return nil }, nil
			})
			if err != nil || opened != 1 || closed != 1 || f.observer.calls != 1 {
				t.Fatalf("exact capture did not settle/join: %v", err)
			}
			if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 0 {
				t.Fatal("settled chain retained")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.bodies) != 1 || h.paths[0] != "/api/v1/runners/identity" {
				t.Fatal("missing exact authenticated exchange")
			}
		})
	}
}

func TestDatabaseNetworkCaptureRuntimeLostAckRestartNoObserver(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	c, client, h := captureRuntimeHTTPFixture(t, f)
	h.lostAck = true
	open := func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		return f.observer, func() error { return nil }, nil
	}
	if runCaptureRuntimeTest(context.Background(), c, client, open) == nil {
		t.Fatal("lost ACK reported success")
	}
	if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 3 {
		t.Fatal("lost ACK origin missing")
	}
	if err := runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("restart opened observer")
		return nil, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if f.observer.calls != 1 {
		t.Fatal("origin resampled")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	claims := 0
	for _, p := range h.paths {
		if strings.HasSuffix(p, "/claim") {
			claims++
		}
	}
	if claims != 1 || len(h.bodies) != 2 || !bytes.Equal(h.bodies[0], h.bodies[1]) {
		t.Fatal("restart changed request or reclaimed")
	}
}

func TestDatabaseNetworkCaptureRuntimeExpiredAndDefaultHold(t *testing.T) {
	for _, phase := range []string{"claim", "plan"} {
		t.Run(phase, func(t *testing.T) {
			f := newCaptureExecutionFixture(t, "redis")
			f.client.claim.Lease.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
			f.client.claim.Lease.ExpiresAt = time.Now().UTC().Add(-time.Minute)
			at := journalAttemptIdentity(f.client.claim.Lease.RunID, f.client.claim.Lease, nil)
			if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "claim", Claim: &f.client.claim}); err != nil {
				t.Fatal(err)
			}
			if phase == "plan" {
				if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "plan", Plan: &f.client.claim.Capture}); err != nil {
					t.Fatal(err)
				}
			}
			c, client, h := captureRuntimeHTTPFixture(t, f)
			if runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
				t.Fatal("expired work opened observer")
				return nil, nil, nil
			}) == nil {
				t.Fatal("expired chain cleared")
			}
			defaultConfig := Config{Server: c.Server, Token: c.Token, RunnerID: c.RunnerID, JournalDir: c.JournalDir, Execute: true, Once: true}
			if err := RunContext(context.Background(), defaultConfig); err == nil || !strings.Contains(err.Error(), "retained database network capture") {
				t.Fatalf("default skipped retained capture: %v", err)
			}
			if got := len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture); got != map[string]int{"claim": 1, "plan": 2}[phase] {
				t.Fatal("hold evidence changed")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.paths) != 1 {
				t.Fatal("expired/default work made another request")
			}
		})
	}
}

func TestDatabaseNetworkCaptureRuntimeIdentityAndModeDenials(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	c, client, h := captureRuntimeHTTPFixture(t, f)
	forbiddenOpen := func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("denied configuration observed host")
		return nil, nil, nil
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.CompleteStatus = "succeeded" }, func(c *Config) { c.Execute = false }, func(c *Config) { c.Database = &DatabaseExecutionConfig{} }, func(c *Config) { c.StorageArchive = &StorageArchiveExecutionConfig{} }, func(c *Config) { c.HostingReadiness = &HostingReadinessConfig{} }, func(c *Config) { c.RuntimeEnvironment = &RuntimeEnvironmentConfig{} }, func(c *Config) { c.JournalDir = "" }, func(c *Config) { c.Server = "http://127.0.0.1:1" }, func(c *Config) {
			c.DatabaseNetworkCapture = &DatabaseNetworkCaptureExecutionConfig{CaptureID: "foreign"}
		},
	} {
		bad := c
		change(&bad)
		if runCaptureRuntimeTest(context.Background(), bad, client, forbiddenOpen) == nil {
			t.Fatal("incompatible mode accepted")
		}
	}
	bad := c
	bad.RunnerID = "wrong_runner"
	if runCaptureRuntimeTest(context.Background(), bad, client, forbiddenOpen) == nil {
		t.Fatal("wrong identity accepted")
	}
	h.denied.Store(true)
	if runCaptureRuntimeTest(context.Background(), c, client, forbiddenOpen) == nil {
		t.Fatal("revoked identity accepted")
	}
	if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 0 {
		t.Fatal("denied invocation wrote journal")
	}
	// RunContext dispatches the exact-ID route before generic identity or polling.
	bad = c
	bad.Server = "http://127.0.0.1:1"
	if err := RunContext(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "verified HTTPS") {
		t.Fatalf("production dispatch missed capture gate: %v", err)
	}
}

func TestDatabaseNetworkCaptureRuntimeJournalCompatibility(t *testing.T) {
	id := "hnc_" + strings.Repeat("a", 32)
	for _, s := range []runner.JournalSnapshot{
		{Events: []runner.JournalEvent{{}}}, {Completions: []runner.JournalCompletion{{}}}, {Provenance: []runner.JournalProvenance{{}}}, {Hosting: []runner.JournalHosting{{}}}, {StorageArchive: []runner.JournalStorageArchive{{}}}, {Build: []runner.JournalBuild{{}}}, {BuildLogs: []runner.JournalBuildLog{{}}}, {BuildLogCursors: []runner.JournalBuildLogCursor{{}}}, {Proxy: []runner.JournalProxy{{}}}, {DatabaseNetworkCapture: []runner.JournalDatabaseNetworkCapture{{Attempt: runner.AttemptIdentity{RunID: "capture_other"}}}},
	} {
		if captureRuntimeJournalCompatible(s, id) {
			t.Fatal("incompatible retained family accepted")
		}
	}
	f := newCaptureExecutionFixture(t, "postgresql")
	at := journalAttemptIdentity(f.client.claim.Lease.RunID, f.client.claim.Lease, nil)
	if err := appendCaptureExecution(f.j, runner.JournalDatabaseNetworkCapture{Attempt: at, Phase: "claim", Claim: &f.client.claim}); err != nil {
		t.Fatal(err)
	}
	c, client, _ := captureRuntimeHTTPFixture(t, f)
	c.DatabaseNetworkCapture = &DatabaseNetworkCaptureExecutionConfig{CaptureID: id}
	if runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("incompatible journal opened host")
		return nil, nil, nil
	}) == nil {
		t.Fatal("other ID ignored")
	}
	if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 1 {
		t.Fatal("foreign retained chain deleted")
	}
}

func TestDatabaseNetworkCaptureRuntimeCancellationJoinsObserver(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	c, client, _ := captureRuntimeHTTPFixture(t, f)
	entered, joined, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.observer.hook = func(ctx context.Context, _ domain.HostingDatabaseNetworkCapturePlan, _ func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
		close(entered)
		<-ctx.Done()
		close(joined)
		return domain.HostingDatabaseNetworkOrigin{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runCaptureRuntimeTest(ctx, c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
			return f.observer, func() error { close(closed); return nil }, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observer not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime not joined")
	}
	select {
	case <-joined:
	default:
		t.Fatal("observer not joined")
	}
	select {
	case <-closed:
	default:
		t.Fatal("observer not closed")
	}
	if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 2 {
		t.Fatal("cancellation discarded plan")
	}
}

func TestDatabaseNetworkCaptureRuntimeRejectsUnqualifiedEngine(t *testing.T) {
	if o, closeObserver, err := openCaptureRuntimeObserver(context.Background(), DatabaseEngineConfig{}, "unix:///unavailable"); err == nil || o != nil || closeObserver != nil {
		t.Fatal("zero configuration opened observer")
	}
	c := Config{DatabaseNetworkCapture: &DatabaseNetworkCaptureExecutionConfig{CaptureID: "hnc_" + strings.Repeat("a", 32)}, Server: "https://controller.invalid", Token: "canary", RunnerID: "runner", JournalDir: filepath.Join(t.TempDir(), "journal"), Execute: true}
	if validateDatabaseNetworkCaptureRuntime(c) != nil {
		t.Fatal("well-formed explicit runtime configuration denied")
	}
	raw, err := json.Marshal(c.DatabaseNetworkCapture)
	if err == nil || len(raw) != 0 {
		t.Fatal("protected config serialized")
	}
}

func TestDatabaseNetworkCaptureRuntimePersistedAckRetiresLocally(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	f.j.failRetire = true
	if f.execute(context.Background()) == nil || len(f.j.Snapshot().DatabaseNetworkCapture) != 4 {
		t.Fatal("ACK retirement failure fixture did not retain receipt")
	}
	c, client, h := captureRuntimeHTTPFixture(t, f)
	if err := runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		t.Fatal("persisted ACK opened observer")
		return nil, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.paths) != 1 || h.paths[0] != "/api/v1/runners/identity" || len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 0 {
		t.Fatal("persisted ACK contacted capture endpoint or failed retirement")
	}
}

func TestDatabaseNetworkCaptureRuntimeRevocationDuringObserve(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	c, client, h := captureRuntimeHTTPFixture(t, f)
	closed := false
	f.observer.hook = func(ctx context.Context, _ domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (domain.HostingDatabaseNetworkOrigin, error) {
		h.denied.Store(true)
		return domain.HostingDatabaseNetworkOrigin{}, before(ctx)
	}
	if runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		return f.observer, func() error { closed = true; return nil }, nil
	}) == nil {
		t.Fatal("revocation ignored")
	}
	if !closed || len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 2 {
		t.Fatal("revocation did not close observer/retain original plan")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) != 0 {
		t.Fatal("revoked observation published")
	}
}

func TestDatabaseNetworkCaptureRuntimeCleanupFailurePreservesSettlement(t *testing.T) {
	f := newCaptureExecutionFixture(t, "postgresql")
	c, client, h := captureRuntimeHTTPFixture(t, f)
	err := runCaptureRuntimeTest(context.Background(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		return f.observer, func() error { return errors.New("private_cleanup_path_canary") }, nil
	})
	if err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("cleanup failure absent or leaked")
	}
	if len(captureRuntimeSnapshot(t, f.path).DatabaseNetworkCapture) != 0 {
		t.Fatal("cleanup error recreated settled journal")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) != 1 {
		t.Fatal("cleanup changed capture effect")
	}
}

func TestDatabaseNetworkCaptureRuntimeCancellationJoinsIdentityHTTP(t *testing.T) {
	entered, joined := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(joined) }))
	defer server.Close()
	client := runnerclient.New(server.URL, "capture_runtime_canary")
	client.HTTP = server.Client()
	c := Config{DatabaseNetworkCapture: &DatabaseNetworkCaptureExecutionConfig{CaptureID: "hnc_" + strings.Repeat("a", 32)}, Server: server.URL, Token: "capture_runtime_canary", RunnerID: "runner", JournalDir: filepath.Join(t.TempDir(), "journal"), Execute: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runCaptureRuntimeTest(ctx, c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
			t.Error("canceled HTTP opened observer")
			return nil, nil, errors.New("unexpected observer")
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("identity HTTP not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation not retained")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime HTTP did not join")
	}
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("identity handler did not join")
	}
}
