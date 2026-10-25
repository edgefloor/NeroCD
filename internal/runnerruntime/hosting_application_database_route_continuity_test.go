//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func connectedRouteDescriptor(t *testing.T, f *connectedExecutionFixture) {
	t.Helper()
	c := domain.HostingApplicationDatabaseRouteContinuity{Version: domain.HostingApplicationDatabaseRouteContinuityVersion, OperationID: f.op.ID, ProxyOperationID: "appdb_proxy_" + f.op.ID, FinalizeRunID: "finalize_" + f.op.ID, ProxyID: "proxy", LoadedSnapshotHash: "sha256:" + strings.Repeat("a", 64), PreviousHealthy: f.op.ApplicationDatabase.Members[0].ApplicationHealthy, Request: domain.HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 4, ExpectedLoadedProxyOperationID: "proxy_loaded", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: f.op.ApplicationDatabase.Members[0].ApplicationHealthy.OperationID}}}}
	c.Hash = c.DocumentHash()
	if err := c.Validate(f.op); err != nil {
		t.Fatal(err)
	}
	f.op.ApplicationDatabaseRouteContinuity = &c
}

func TestConnectedRouteYieldReplayAndFinalizeNeverPrepares(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newConnectedExecutionFixture(t, engine, true)
			connectedRouteDescriptor(t, f)
			e := &connectedExecutionEngine{f: f}
			lost := true
			yieldCalls := 0
			var original domain.HostingApplicationDatabaseYieldRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/hosting/database-binding/yield" {
					f.serve(w, r)
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				yieldCalls++
				var request domain.HostingApplicationDatabaseYieldRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || f.stage == nil || request.ValidateStage(f.op, *f.stage) != nil || !f.has("application_yield") {
					t.Error("yield escaped durable ready evidence")
					w.WriteHeader(409)
					return
				}
				if original.ReceiptID != "" && original != request {
					t.Error("yield replay changed original tuple/receipt")
				}
				original = request
				receipt := request.Receipt()
				f.op.ApplicationDatabaseYield = &receipt
				if lost {
					w.WriteHeader(409)
					return
				}
				_ = json.NewEncoder(w).Encode(f.op)
			}))
			defer server.Close()
			if err := f.run(t, e, server.URL); err == nil {
				t.Fatal("lost yield ACK accepted")
			}
			if yieldCalls != 1 || e.retirements != 0 || f.complete != 0 || f.op.State != "applying" || !f.op.MutationHold || f.j.Depth() == 0 {
				t.Fatal("yield settled original operation or retired predecessor")
			}
			reopenRouteJournal(t, f)
			beforeCreates, beforeStarts, beforeConfigure := e.creates, e.starts, e.configure
			first := f.j.Snapshot().Hosting[0]
			lost = false
			// Receipt replay must finish before opening Docker or probing current prepare authority.
			config := f.config(server.URL)
			config.Database.RouteContinuity = true
			if err := reconcileApplicationDatabaseAttempt(runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true}), config, runnerclient.New(server.URL, "fixture"), f.j, first, func() (applicationDatabaseExecutionEngine, error) {
				t.Fatal("yield ACK replay opened Docker")
				return nil, nil
			}); err != nil {
				t.Fatal("historical yield replay", err)
			}
			if yieldCalls != 2 || f.j.Depth() != 0 || e.retirements != 0 || f.generic != 0 {
				t.Fatal("yield replay did not clear only original exchange")
			}
			originalRun := f.op.RunID
			f.op.TaskMode = "finalize"
			f.lease = domain.RunLease{ID: "finalize_lease", RunID: f.op.ApplicationDatabaseRouteContinuity.FinalizeRunID, RunnerID: f.lease.RunnerID, Attempt: 1, Fence: "finalize_fence", Status: domain.LeaseActive, ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now()}
			f.a = hostingAuthority(f.lease.RunID, f.op.ID, f.lease)
			ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(ctx, f.lease)
			defer s.Close()
			f.lose = "complete"
			if err := executeApplicationDatabaseFinalize(s, f.j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture"), e, f.a, f.lease, f.op); err == nil {
				t.Fatal("lost final Complete ACK accepted")
			}
			if e.creates != beforeCreates || e.starts != beforeStarts || e.configure != beforeConfigure || e.retirements != 1 || f.op.RunID != originalRun || f.generic != 0 {
				t.Fatalf("finalize repeated prepare or changed original identity creates=%d/%d starts=%d/%d configure=%d/%d retirements=%d run=%s/%s journal=%d", e.creates, beforeCreates, e.starts, beforeStarts, e.configure, beforeConfigure, e.retirements, f.op.RunID, originalRun, f.j.Depth())
			}
			if f.j.Depth() == 0 {
				t.Fatal("lost final receipt not retained")
			}
			reopenRouteJournal(t, f)
			first = f.j.Snapshot().Hosting[0]
			f.lose = ""
			if err := reconcileApplicationDatabaseAttempt(ctx, config, runnerclient.New(server.URL, "fixture"), f.j, first, func() (applicationDatabaseExecutionEngine, error) {
				t.Fatal("final Complete replay opened Docker")
				return nil, nil
			}); err != nil {
				t.Fatal("final Complete replay", err)
			}
			if e.retirements != 1 || f.j.Depth() != 0 || f.op.State != "succeeded" || f.op.MutationHold {
				t.Fatal("finalize receipt replay repeated retirement or failed settlement")
			}
		})
	}
}

func reopenRouteJournal(t *testing.T, f *connectedExecutionFixture) {
	t.Helper()
	before := f.j.Snapshot()
	if err := f.j.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Version != 16 {
		t.Fatal("continuation did not discriminate journal16")
	}
	oldDir := filepath.Join(t.TempDir(), "old-journal")
	if err = os.Mkdir(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(oldDir, "journal.json"), bytes.Replace(raw, []byte(`"version":16`), []byte(`"version":15`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if old, err := runner.OpenAttemptJournal(oldDir); err == nil {
		_ = old.Close()
		t.Fatal("journal15 accepted continuation or finalize evidence")
	}
	j, err := runner.OpenAttemptJournal(f.dir)
	if err != nil {
		t.Fatal("journal16 restart", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	f.j = j
	if domain.HostingDocumentHash(before) != domain.HostingDocumentHash(j.Snapshot()) {
		t.Fatal("restart changed durable continuation tuple or receipts")
	}
}

func TestConnectedRouteInterruptedFinalizeIntentNeverRepeatsRetirement(t *testing.T) {
	f := newConnectedExecutionFixture(t, "redis", true)
	connectedRouteDescriptor(t, f)
	e := &connectedExecutionEngine{f: f}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/hosting/database-binding/yield" {
			f.serve(w, r)
			return
		}
		var in domain.HostingApplicationDatabaseYieldRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || f.stage == nil || in.ValidateStage(f.op, *f.stage) != nil {
			t.Error("invalid ready yield")
			w.WriteHeader(409)
			return
		}
		receipt := in.Receipt()
		f.op.ApplicationDatabaseYield = &receipt
		_ = json.NewEncoder(w).Encode(f.op)
	}))
	defer server.Close()
	if err := f.run(t, e, server.URL); err != nil {
		t.Fatal(err)
	}
	f.op.TaskMode = "finalize"
	f.lease.ID = "finalize_lease"
	f.lease.RunID = f.op.ApplicationDatabaseRouteContinuity.FinalizeRunID
	f.lease.Fence = "finalize_fence"
	f.a = hostingAuthority(f.lease.RunID, f.op.ID, f.lease)
	f.deny = "retire"
	ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, f.lease)
	defer s.Close()
	if err := executeApplicationDatabaseFinalize(s, f.j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture"), e, f.a, f.lease, f.op); err == nil {
		t.Fatal("retirement denial accepted")
	}
	if e.retirements != 0 || f.op.State != "applying" || !f.op.MutationHold || !f.has("application_retirement") {
		t.Fatal("interrupted intent lost durable hold or repeated effect")
	}
	reopenRouteJournal(t, f)
	config := f.config(server.URL)
	config.Database.RouteContinuity = true
	if err := reconcileApplicationDatabaseAttempt(ctx, config, runnerclient.New(server.URL, "fixture"), f.j, f.j.Snapshot().Hosting[0], func() (applicationDatabaseExecutionEngine, error) {
		t.Fatal("interrupted retirement reopened Docker")
		return nil, nil
	}); err == nil {
		t.Fatal("interrupted retirement invented retry authority")
	}
	if e.retirements != 0 || f.j.Depth() == 0 || f.complete != 0 {
		t.Fatal("interrupted finalize repeated retirement or discarded evidence")
	}
}
