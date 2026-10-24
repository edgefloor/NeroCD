//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type databaseCoordinatorEngine struct {
	f            *databaseFake
	beforeApply  func()
	observeCalls int
}

func (e *databaseCoordinatorEngine) Close() error { return nil }
func (e *databaseCoordinatorEngine) PrepareDatabase(ctx context.Context, s domain.HostingSnapshot, c DatabaseEngineConfig, before func(context.Context) error) (domain.HostingRelease, error) {
	if err := before(ctx); err != nil {
		return domain.HostingRelease{}, err
	}
	return e.f.plan.Release, nil
}
func (e *databaseCoordinatorEngine) ensureOwnedVolumeWithAuthority(ctx context.Context, r hostingOwnedVolumeRequest, before ...func(context.Context) error) (hostingOwnedVolumeEvidence, error) {
	if len(before) != 1 {
		return hostingOwnedVolumeEvidence{}, errors.New("authority missing")
	}
	if err := before[0](ctx); err != nil {
		return hostingOwnedVolumeEvidence{}, err
	}
	return e.f.e.ensureOwnedVolumeWithAuthority(ctx, r, before...)
}
func (e *databaseCoordinatorEngine) ApplyDatabase(ctx context.Context, id string, p domain.HostingApplyPlan, r hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, c DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	if e.beforeApply != nil {
		e.beforeApply()
	}
	return e.f.e.ApplyDatabase(ctx, id, p, r, v, credentials, c, func(ctx context.Context) error {
		if err := before(ctx); err != nil {
			return err
		}
		return e.f.before(ctx)
	})
}
func (e *databaseCoordinatorEngine) ObserveDatabase(ctx context.Context, op domain.HostingOperation, credentials *DatabaseCredentials, c DatabaseEngineConfig) (domain.HostingObservation, error) {
	e.observeCalls++
	return e.f.e.ObserveDatabase(ctx, op, credentials, c)
}

type databaseCoordinatorController struct {
	*storageCoordinatorController
	dbmu                             sync.Mutex
	credentials                      int
	deliveryIDs                      []string
	deliveryUntil                    time.Time
	failCredentials, lostCredentials bool
	mutateMetadata                   func(*hostingdatabaseprotocol.Metadata)
}

func databaseCoordinatorFixture(t *testing.T, kind string) (*databaseFake, *runner.AttemptJournal, string, *databaseCoordinatorController, *databaseCoordinatorEngine) {
	t.Helper()
	f, _ := databaseFixture(t, kind)
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, &mountedApplyFixture{plan: f.plan, request: f.req})
	c.a.OperationID = f.req.OriginOperationID
	c.a.RunID = "run_" + f.req.OriginOperationID
	c.lease.RunID = c.a.RunID
	c.op.RunID = c.a.RunID
	c.op.ID = f.req.OriginOperationID
	return f, j, dir, &databaseCoordinatorController{storageCoordinatorController: c, deliveryUntil: time.Now().Add(40 * time.Second)}, &databaseCoordinatorEngine{f: f}
}
func (c *databaseCoordinatorController) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/runners/hosting/status" {
		c.mu.Lock()
		revoked := c.revoked
		c.mu.Unlock()
		if revoked {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	if r.URL.Path != "/api/v1/runners/hosting/database-credentials" {
		c.storageCoordinatorController.serve(w, r)
		return
	}
	c.dbmu.Lock()
	defer c.dbmu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	in, err := hostingdatabaseprotocol.ReadRequest(r.Body)
	if err != nil {
		c.t.Error(err)
		w.WriteHeader(400)
		return
	}
	if in.Mode == "deploy" && (!c.materialized || !c.hasPhase("apply_intent") || c.op.State != "prepared" || c.applyCalls != 0) {
		c.t.Error("credentials escaped materialization-before-apply ordering")
	}
	c.credentials++
	c.deliveryIDs = append(c.deliveryIDs, in.DeliveryID)
	if c.failCredentials {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("protected_canary_response"))
		return
	}
	if c.lostCredentials && c.credentials == 1 {
		w.WriteHeader(503)
		return
	}
	m := hostingdatabaseprotocol.Metadata{RunnerID: c.lease.RunnerID, Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: c.op.Plan.Database.Reference, References: c.op.Plan.Database.Credentials, UseUntil: c.deliveryUntil, RequestHash: hostingdatabaseprotocol.RequestHash(c.lease.RunnerID, in), Purpose: hostingdatabaseprotocol.Purpose}
	if c.mutateMetadata != nil {
		c.mutateMetadata(&m)
	}
	entries := make([]hostingdatabaseprotocol.Credential, len(m.References))
	for i, ref := range m.References {
		entries[i] = hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32)))}
	}
	protected, err := hostingdatabaseprotocol.NewCredentials(m, entries)
	if err != nil {
		c.t.Error(err)
		w.WriteHeader(500)
		return
	}
	defer protected.Close()
	w.Header().Set("Cache-Control", "no-store")
	if err = hostingdatabaseprotocol.WriteCredentials(w, protected); err != nil {
		c.t.Error(err)
	}
}
func runDatabaseCoordinator(t *testing.T, f *databaseFake, j *runner.AttemptJournal, c *databaseCoordinatorController, e *databaseCoordinatorEngine, server string) error {
	t.Helper()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	return executeDatabaseDeployment(s, j, func(context.Context) error { return nil }, runnerclient.New(server, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config)
}
func databaseReplayConfig(server string, c *databaseCoordinatorController) Config {
	return Config{Server: server, Token: "fixture_token", RunnerID: c.lease.RunnerID, Database: &DatabaseExecutionConfig{TrustedLoopbackHTTP: true}}
}
func TestDatabaseCoordinatorRealEngineOrderingAndCredentials(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			f, j, _, c, e := databaseCoordinatorFixture(t, kind)
			c.lostCredentials = true
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err != nil {
				t.Fatal(err)
			}
			if c.credentials != 2 || c.deliveryIDs[0] != c.deliveryIDs[1] || c.applyCalls != 1 || c.completeCalls != 1 || f.creates != 1 || f.starts != 1 || len(j.Snapshot().Hosting) != 0 {
				t.Fatalf("sequence incomplete: credentials=%d apply=%d create=%d journal=%d", c.credentials, c.applyCalls, f.creates, len(j.Snapshot().Hosting))
			}
		})
	}
}
func TestDatabaseCoordinatorFailureBoundariesRetainEvidence(t *testing.T) {
	for _, mode := range []string{"materialization_ack", "credentials", "apply_ack", "completion_ack", "revoked", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f, j, dir, c, e := databaseCoordinatorFixture(t, "redis")
			switch mode {
			case "materialization_ack":
				c.lostMaterialized = true
			case "credentials":
				c.failCredentials = true
			case "apply_ack":
				c.lostApply = true
			case "completion_ack":
				c.lostComplete = true
			case "revoked":
				e.beforeApply = func() { c.mu.Lock(); c.revoked = true; c.op.State = "reconciliation_required"; c.mu.Unlock() }
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			defer s.Close()
			if mode == "canceled" {
				e.beforeApply = s.cancel
			}
			err := executeDatabasePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config)
			if err == nil || len(j.Snapshot().Hosting) == 0 {
				t.Fatal("failure lost uncertainty/evidence")
			}
			if mode != "completion_ack" && (f.creates != 0 || f.starts != 0) {
				t.Fatal("writer started beyond failed authority boundary")
			}
			if mode == "materialization_ack" && c.credentials != 0 {
				t.Fatal("fetched credentials before materialization ACK")
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, "journal.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, canary := range []string{"fixture_token", "protected_canary_response", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))} {
				if strings.Contains(string(raw), canary) || strings.Contains(err.Error(), canary) {
					t.Fatal("protected material escaped")
				}
			}
		})
	}
}
func TestDatabaseCoordinatorCredentialConversionOwnsAndBoundsValues(t *testing.T) {
	f, _ := databaseFixture(t, "redis")
	a := domain.HostingAuthority{OperationID: f.req.OriginOperationID, RunID: "run_" + f.req.OriginOperationID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	in := domain.HostingDatabaseCredentialRequest{Authority: a, DeliveryID: strings.Repeat("a", 32), Mode: "deploy", PlanHash: f.plan.Hash}
	until := time.Now().Add(20 * time.Second)
	m := hostingdatabaseprotocol.Metadata{RunnerID: f.plan.Snapshot.RunnerID, Authority: a, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: f.plan.Database.Reference, References: f.plan.Database.Credentials, RequestHash: hostingdatabaseprotocol.RequestHash(f.plan.Snapshot.RunnerID, in), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: until}
	entries := make([]hostingdatabaseprotocol.Credential, len(m.References))
	for i, ref := range m.References {
		entries[i] = hostingdatabaseprotocol.Credential{Reference: ref, Value: []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32)))}
	}
	p, err := hostingdatabaseprotocol.NewCredentials(m, entries)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	credentials, err := convertDatabaseCredentials(p, in, m.RunnerID, f.plan, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentials.Close)
	p.Close()
	if credentials.until != until {
		t.Fatal("original use-until extended")
	}
	if err = credentials.use(t.Context(), f.plan, func(_ context.Context, values []DatabaseCredentialValue) error {
		if len(values) != len(entries) || !bytes.Equal(values[0].value, entries[0].Value) {
			return errors.New("owned copy changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err = hostingdatabaseprotocol.NewCredentials(m, entries)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	in.DeliveryID = strings.Repeat("b", 32)
	if bad, err := convertDatabaseCredentials(p, in, m.RunnerID, f.plan, time.Now().Add(time.Minute)); err == nil || bad != nil {
		t.Fatal("accepted wrong retained delivery identity")
	}
	if strings.Contains(fmt.Sprintf("%+v", credentials), string(entries[0].Value)) {
		t.Fatal("formatted credentials leaked")
	}
}
func TestDatabaseFamiliesCannotEnterImageOrStorageCoordinator(t *testing.T) {
	_, j, _, c, _ := databaseCoordinatorFixture(t, "redis")
	if validateHostingPlanResponse(c.op, c.a, c.lease.RunnerID, "deploy") == nil || storageOperationMatches(c.op, c.a, c.op.Snapshot.Storage) {
		t.Fatal("database accepted by legacy executor")
	}
	for _, phase := range []string{"prepared", "apply_intent", "result"} {
		entry := runner.JournalHosting{ID: phase, Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: phase, CreatedAt: time.Now().UTC()}
		databaseEntry(&entry, c.op)
		switch phase {
		case "prepared":
			entry.Prepared = &domain.HostingPreparedRequest{HostingAuthority: c.a, ReceiptID: phase, Release: *c.op.Release}
		case "apply_intent":
			entry.Plan = c.op.Plan
			entry.DeliveryID = strings.Repeat("a", 32)
		case "result":
			entry.Result = &domain.HostingCompleteRequest{HostingAuthority: c.a, ReceiptID: phase, Result: domain.HostingResult{Outcome: "failed", Uncertain: true}}
		}
		if _, err := j.AppendHosting(entry); err != nil {
			t.Fatal(err)
		}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	if err := reconcileHostingJournalWithLocalBuild(server.URL, "token", j, "", false, t.Context()); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || len(j.Snapshot().Hosting) != 3 {
		t.Fatal("legacy recovery touched database evidence")
	}
}

func TestDatabaseCoordinatorJournalsPreparationBeforeExactACK(t *testing.T) {
	f, j, _, c, e := databaseCoordinatorFixture(t, "postgresql")
	c.op.State = "preparing"
	c.op.Plan = nil
	c.op.Release = nil
	preparedCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/plan":
			var a domain.HostingAuthority
			if json.NewDecoder(r.Body).Decode(&a) != nil || a != c.a {
				t.Error("pre-pull authority changed")
			}
			_ = json.NewEncoder(w).Encode(c.op)
			return
		case "/api/v1/runners/hosting/prepared":
			var in domain.HostingPreparedRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a || in.Release != f.plan.Release || !c.hasPhase("prepared") {
				t.Error("preparation escaped exact durable receipt")
			}
			preparedCalls++
			c.op.Plan = &f.plan
			c.op.Release = &f.plan.Release
			c.op.State = "prepared"
			c.op.PreparedReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Release), CreatedAt: time.Now().UTC()}
			if preparedCalls == 1 {
				w.WriteHeader(503)
				return
			}
			_ = json.NewEncoder(w).Encode(c.op)
			return
		}
		c.serve(w, r)
	}))
	defer server.Close()
	if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err != nil {
		t.Fatal(err)
	}
	if preparedCalls != 2 || f.creates != 1 || len(j.Snapshot().Hosting) != 0 {
		t.Fatal("preparation retry lost receipt or duplicated writer")
	}
}

func TestDatabaseCoordinatorPersistsResultBeforeDrainAndCompletesAfterEventsACK(t *testing.T) {
	f, j, _, c, e := databaseCoordinatorFixture(t, "redis")
	eventCalls := 0
	eventKey := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/events/batch" {
			var batch runnerEventBatchRequest
			if json.NewDecoder(r.Body).Decode(&batch) != nil || len(batch.Events) != 1 || len(j.Snapshot().Events) != 1 {
				t.Error("event sent without durable append")
				w.WriteHeader(400)
				return
			}
			if eventKey != "" && eventKey != batch.Events[0].EventKey {
				t.Error("event retry identity changed")
			}
			eventKey = batch.Events[0].EventKey
			eventCalls++
			if eventCalls == 1 {
				w.WriteHeader(503)
				return
			}
			writeRunnerEventBatchAck(t, w, batch)
			return
		}
		if r.URL.Path == "/api/v1/runners/hosting/complete" && len(j.Snapshot().Events) != 0 {
			t.Error("completion preceded event ACK")
		}
		c.serve(w, r)
	}))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "fixture_token", c.a.RunID, c.lease)
	defer reporter.Stop()
	if err := reporter.Emit(domain.LogSystem, "Database preparation recorded", 4); err != nil {
		t.Fatal(err)
	}
	drain := func(ctx context.Context) error {
		if !c.hasPhase("result") {
			t.Error("result was not durable before event drain")
		}
		return reporter.WaitEmpty(ctx)
	}
	if err := executeDatabasePrepared(s, j, drain, runnerclient.New(server.URL, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config); err != nil {
		t.Fatal(err)
	}
	if eventCalls < 2 || c.completeCalls != 1 || len(j.Snapshot().Events) != 0 {
		t.Fatal("event/result acknowledgement sequence incomplete")
	}
}

// Trust the fixture CA while preserving certificate verification. Requests still
// use production client construction, supervisor, watcher, reporter, and replay.
func databaseRedirectFixture(t *testing.T, passPath string) (string, <-chan string, *atomic.Int32) {
	t.Helper()
	leaks := &atomic.Int32{}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks.Add(1); _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(destination.Close)
	paths := make(chan string, 16)
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if r.URL.Path == passPath {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Redirect(w, r, destination.URL+"/leak", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	previous := http.DefaultTransport
	trusted := source.Client().Transport.(*http.Transport).Clone()
	http.DefaultTransport = trusted
	t.Cleanup(func() { http.DefaultTransport = previous; trusted.CloseIdleConnections() })
	return source.URL, paths, leaks
}
func TestDatabaseTransportPolicyReachesExecutionAndLifecycle(t *testing.T) {
	for _, mode := range []string{"execution preflight", "renewal", "cancellation watcher", "event reporter", "journaled completion", "generic replay", "database replay"} {
		t.Run(mode, func(t *testing.T) {
			_, j, _, c, _ := databaseCoordinatorFixture(t, "redis")
			passPath := ""
			if mode == "renewal" {
				passPath = "/api/v1/runners/heartbeat"
			}
			server, paths, leaks := databaseRedirectFixture(t, passPath)
			parent := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{})
			s := newAttemptSupervisorFrom(parent, c.lease)
			defer s.Close()
			expected := ""
			var err error
			switch mode {
			case "execution preflight":
				expected = "/api/v1/runners/renew"
				claim := domain.ClaimedRun{Run: domain.TaskRun{ID: c.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy, HostingOperationID: c.a.OperationID, HostingMode: "deploy"}}, Lease: c.lease}
				// The entrypoint must attach policy to an untagged parent before renewal.
				err = executeClaimWithOptionsContext(t.Context(), server, "database_bearer_canary", claim, t.TempDir(), time.Millisecond, j, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{database: &DatabaseExecutionConfig{}})
			case "renewal":
				expected = "/api/v1/runners/renew"
				worker := startLeaseRenewer(s.Context(), s, server, "database_bearer_canary", c.lease, time.Millisecond)
				select {
				case <-s.Context().Done():
					err = s.Context().Err()
				case <-time.After(3 * time.Second):
					t.Error("renewal did not stop on rejected redirect")
				}
				worker.Stop()
			case "cancellation watcher":
				expected = "/api/v1/runners/lease"
				worker := startLeaseWatcher(s.Context(), server, "database_bearer_canary", c.lease, time.Millisecond, s.cancel)
				select {
				case <-s.Context().Done():
					err = s.Context().Err()
				case <-time.After(3 * time.Second):
					t.Error("watcher did not stop on rejected redirect")
				}
				worker.Stop()
			case "event reporter":
				expected = "/api/v1/runners/events/batch"
				reporter := startAttemptReporter(s.Context(), s, j, server, "database_bearer_canary", c.a.RunID, c.lease)
				if emitErr := reporter.Emit(domain.LogSystem, "durable event", 4); emitErr != nil {
					reporter.Stop()
					t.Fatal(emitErr)
				}
				err = reporter.WaitEmpty(s.Context())
				reporter.Stop()
				if len(j.Snapshot().Events) != 1 {
					t.Error("redirect discarded durable event")
				}
			case "journaled completion":
				expected = "/api/v1/runners/complete"
				err = journaledCompletion(s, j, server, "database_bearer_canary", c.a.RunID, c.lease, "failed", nil)
				if len(j.Snapshot().Completions) != 1 {
					t.Error("redirect discarded durable completion")
				}
			case "generic replay":
				expected = "/api/v1/runners/lease"
				if _, appendErr := j.AppendEvent(runner.JournalEvent{ID: "pending", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), Sequence: 4, Stream: domain.LogSystem, Message: "pending", CreatedAt: time.Now().UTC()}); appendErr != nil {
					t.Fatal(appendErr)
				}
				err = reconcileRunnerJournalWithCounters(server, "database_bearer_canary", j, &runnerOperationalCounters{}, parent)
				if len(j.Snapshot().Events) != 1 {
					t.Error("redirect discarded replay evidence")
				}
			case "database replay":
				expected = "/api/v1/runners/hosting/status"
				entry := runner.JournalHosting{ID: "intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, DeliveryID: strings.Repeat("a", 32), CreatedAt: time.Now().UTC()}
				databaseEntry(&entry, c.op)
				if _, appendErr := j.AppendHosting(entry); appendErr != nil {
					t.Fatal(appendErr)
				}
				cfg := databaseReplayConfig(server, c)
				cfg.Database.TrustedLoopbackHTTP = false
				err = reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
					t.Error("redirect opened database engine")
					return nil, DatabaseEngineConfig{}, errors.New("unexpected engine")
				})
				if len(j.Snapshot().Hosting) != 1 {
					t.Error("redirect discarded database evidence")
				}
			}
			if err == nil {
				t.Fatal("redirect accepted")
			}
			found := false
		drainPaths:
			for {
				select {
				case path := <-paths:
					found = found || path == expected
				default:
					break drainPaths
				}
			}
			if !found {
				t.Fatalf("protected production call was not exercised: %s", expected)
			}
			if leaks.Load() != 0 {
				t.Fatal("authority reached HTTP redirect destination")
			}
			if strings.Contains(err.Error(), "database_bearer_canary") || strings.Contains(err.Error(), c.lease.Fence) {
				t.Fatal("error leaked authority")
			}
		})
	}
}
