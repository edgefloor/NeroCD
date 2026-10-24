//go:build unix

package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestDatabaseReplayLostCompletionACKHasNoEngineWork(t *testing.T) {
	f, j, dir, c, e := databaseCoordinatorFixture(t, "postgresql")
	c.lostComplete = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err == nil {
		t.Fatal("lost ACK was accepted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	j, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	c.journal = j
	c.lostComplete = false
	beforeCreates, beforeCredentials := f.creates, c.credentials
	opens := 0
	err = reconcileDatabaseJournalWithEngine(t.Context(), databaseReplayConfig(server.URL, c), j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { opens++; return e, f.config, nil })
	if err != nil {
		t.Fatal(err)
	}
	if opens != 0 || f.creates != beforeCreates || c.credentials != beforeCredentials || len(j.Snapshot().Hosting) != 0 {
		t.Fatal("completion replay touched engine or retained settled evidence")
	}
}
func TestDatabaseReplayPreparedUsesOriginalDeliveryAndMaterialization(t *testing.T) {
	for _, lost := range []string{"materialized", "credentials"} {
		t.Run(lost, func(t *testing.T) {
			f, j, _, c, e := databaseCoordinatorFixture(t, "redis")
			if lost == "materialized" {
				c.lostMaterialized = true
			} else {
				c.failCredentials = true
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err == nil {
				t.Fatal("expected interrupted preparation")
			}
			c.lostMaterialized = false
			c.failCredentials = false
			err := reconcileDatabaseJournalWithEngine(t.Context(), databaseReplayConfig(server.URL, c), j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { return e, f.config, nil })
			if err != nil {
				t.Fatal(err)
			}
			if f.creates != 1 || f.starts != 1 || c.applyCalls != 1 || len(j.Snapshot().Hosting) != 0 {
				t.Fatal("prepared replay duplicated or failed writer")
			}
			if lost == "credentials" && (len(c.deliveryIDs) != 2 || c.deliveryIDs[0] != c.deliveryIDs[1]) {
				t.Fatal("replay minted replacement delivery")
			}
		})
	}
}
func TestDatabaseReplayApplyingNeverRefetchesOrReapplies(t *testing.T) {
	f, j, _, c, e := databaseCoordinatorFixture(t, "redis")
	c.lostApply = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err == nil {
		t.Fatal("expected admission response loss")
	}
	c.lostApply = false
	opens := 0
	before := c.credentials
	err := reconcileDatabaseJournalWithEngine(t.Context(), databaseReplayConfig(server.URL, c), j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { opens++; return e, f.config, nil })
	if err == nil || opens != 0 || c.credentials != before || c.applyCalls != 1 || f.creates != 0 || len(j.Snapshot().Hosting) == 0 {
		t.Fatal("uncertain admitted operation was reapplied or erased")
	}
	c.op.ReconcileRequested = true
	c.op.Result = &domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: f.plan.Hash}
	if err = reconcileDatabaseJournalWithEngine(t.Context(), databaseReplayConfig(server.URL, c), j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { opens++; return e, f.config, nil }); err == nil || c.observeCalls != 0 || opens != 0 {
		t.Fatal("missing durable candidate was synthesized")
	}
}
func TestDatabaseReplayExpiredOriginalWindowAndDisabledConfig(t *testing.T) {
	f, j, _, c, e := databaseCoordinatorFixture(t, "redis")
	entry := runner.JournalHosting{ID: "original_intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, DeliveryID: strings.Repeat("a", 32), CreatedAt: time.Now().Add(-2 * time.Minute)}
	databaseEntry(&entry, c.op)
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; c.serve(w, r) }))
	defer server.Close()
	cfg := databaseReplayConfig(server.URL, c)
	cfg.Database = nil
	open := func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
		t.Error("expired/disabled recovery opened engine")
		return e, f.config, nil
	}
	if err := reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, open); err == nil || calls != 0 {
		t.Fatal("disabled database recovery proceeded")
	}
	cfg.Database = &DatabaseExecutionConfig{TrustedLoopbackHTTP: true}
	if err := reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, open); err == nil || c.credentials != 0 || c.applyCalls != 0 {
		t.Fatal("expired original window refreshed credentials or admission")
	}
}
func TestDatabaseObservationUsesNewAuthorityAndReadOnlyCredentials(t *testing.T) {
	f, j, _, c, e := databaseCoordinatorFixture(t, "postgresql")
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	if err := runDatabaseCoordinator(t, f, j, c, e, server.URL); err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Close()
	op := c.op
	op.TaskMode = "observe"
	op.ReconcileRequested = true
	op.ReconciliationRunID = "observe_" + op.ID
	lease := c.lease
	lease.RunID = op.ReconciliationRunID
	lease.ID = "lease_observe"
	lease.Fence = "fence_observe"
	a := hostingAuthority(lease.RunID, op.ID, lease)
	beforeCreates, beforeStarts := f.creates, f.starts
	observations := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/reconciliation" {
			var observation domain.HostingObservation
			if json.NewDecoder(r.Body).Decode(&observation) != nil || observation.HostingAuthority != a || !observation.Ready || observation.ContainerID != op.Result.ContainerID {
				t.Error("observation changed authority/candidate")
			}
			observations++
			_ = json.NewEncoder(w).Encode(op)
			return
		}
		c.serve(w, r)
	}))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), lease)
	defer s.Close()
	if err := executeDatabaseObservation(s, runnerclient.New(server.URL, "fixture_token"), e, a, op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || e.observeCalls != 1 || f.creates != beforeCreates || f.starts != beforeStarts {
		t.Fatal("observation changed daemon writer")
	}
	// Old deployment authority cannot turn an observation into credential refresh.
	if err := executeDatabaseObservation(s, runnerclient.New(server.URL, "fixture_token"), e, c.a, op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config); err == nil {
		t.Fatal("old authority observed")
	}
	op.Result = nil
	if err := executeDatabaseObservation(s, runnerclient.New(server.URL, "fixture_token"), e, a, op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config); err == nil {
		t.Fatal("missing result observed")
	}
}
func TestDatabaseCurrentAuthorityRejectsFenceAndPlanChanges(t *testing.T) {
	_, _, _, c, _ := databaseCoordinatorFixture(t, "redis")
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	for _, mode := range []string{"status forbidden", "changed snapshot", "changed plan", "missing hold"} {
		t.Run(mode, func(t *testing.T) {
			op := c.op
			op.State = "applying"
			op.MutationHold = true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request domain.HostingStatusRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostingAuthority != c.a {
					t.Error("current authority changed")
				}
				switch mode {
				case "status forbidden":
					w.WriteHeader(403)
					return
				case "changed snapshot":
					op.Snapshot.RunnerID = "foreign"
				case "changed plan":
					plan := *op.Plan
					plan.Hash = "sha256:" + strings.Repeat("f", 64)
					op.Plan = &plan
				case "missing hold":
					op.MutationHold = false
				}
				_ = json.NewEncoder(w).Encode(op)
			}))
			defer server.Close()
			if err := databaseCurrentAuthority(s, runnerclient.New(server.URL, "token"), c.a, c.op, "applying")(t.Context()); err == nil {
				t.Fatal("mutation accepted stale authority")
			}
		})
	}
}
