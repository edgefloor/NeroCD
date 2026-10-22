package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func proxyJournalFixture(t *testing.T) JournalProxy {
	t.Helper()
	now := time.Now().UTC()
	plan, err := domain.CompileHostingProxyPlan(domain.HostingProxySnapshot{ProxyID: "proxy_one", ServerID: "srv_one", DestinationID: "dest_one", RunnerID: "runner_one", Generation: 2, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []domain.HostingProxyRouteTarget{{RouteID: "route_one", ProjectID: "proj_one", ResourceID: "res_one", BindingID: "bind_one", ReleaseOperationID: "hop_one", Hostname: "web.nerocd.invalid", PathPrefix: "/", HostPort: 18080}}}, 1, "daemon_one")
	if err != nil {
		t.Fatal(err)
	}
	return JournalProxy{ID: "proxy_intent", Attempt: AttemptIdentity{RunID: "proxy_pop_one", LeaseID: "lease_one", Attempt: 1, Fence: "fence_one", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, OperationID: "pop_one", Phase: "apply_intent", Plan: plan, Preparation: domain.HostingProxyPreparation{DaemonID: "daemon_one", PlanHash: plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}, CreatedAt: now}
}

func TestProxyJournalV4PreservesImageCleanupAndNeverDowngrades(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "journal")
	journal, err := OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	now := time.Now().UTC()
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon_one"}
	release.Hash = domain.HostingDocumentHash(release)
	cleanupPlan, err := domain.CompileHostingCleanupPlan(domain.HostingHealthy{OperationID: "hop_old", ContainerID: strings.Repeat("c", 64), Snapshot: domain.HostingSnapshot{HostPort: 18081}, Release: release}, "hop_current")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := JournalHosting{ID: "cleanup_one", Attempt: AttemptIdentity{RunID: "cleanup_hop_one", LeaseID: "lease_cleanup", Attempt: 1, Fence: "fence_cleanup", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, OperationID: "hop_one", Phase: "cleanup_intent", CleanupPlan: &cleanupPlan, CreatedAt: now}
	if _, err = journal.AppendHosting(cleanup); err != nil || journal.state.Version != 3 {
		t.Fatalf("image v3 setup: %v", err)
	}
	entry := proxyJournalFixture(t)
	if _, err = journal.AppendProxy(entry); err != nil || journal.state.Version != 4 {
		t.Fatalf("proxy v4 upgrade: %v", err)
	}
	cleanup.ID = "cleanup_two"
	if _, err = journal.AppendHosting(cleanup); err != nil || journal.state.Version != 4 {
		t.Fatalf("later image append downgraded journal: version=%d %v", journal.state.Version, err)
	}
	if _, err = journal.AppendProxy(entry); err != nil || journal.Depth() != 3 {
		t.Fatalf("idempotent append: depth=%d %v", journal.Depth(), err)
	}
	changed := cloneJournalProxy(entry)
	changed.Preparation.EngineHash = strings.Repeat("c", 64)
	if _, err = journal.AppendProxy(changed); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("changed evidence reused ID: %v", err)
	}
	snapshot := journal.Snapshot()
	snapshot.Proxy[0].Plan.Snapshot.Routes[0].HostPort++
	if journal.Snapshot().Proxy[0].Plan.Snapshot.Routes[0].HostPort != 18080 {
		t.Fatal("snapshot aliases durable proxy route storage")
	}
	if err = journal.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("expired attempt discarded proxy intent")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var header struct{ Version int }
	if err = json.Unmarshal(raw, &header); err != nil || header.Version != 4 {
		t.Fatalf("old formats could accept proxy evidence: %s %v", raw, err)
	}
	journal, err = OpenAttemptJournal(directory)
	if err != nil || len(journal.Snapshot().Hosting) != 2 || len(journal.Snapshot().Proxy) != 1 {
		t.Fatalf("restart lost mixed image/proxy evidence: %v", err)
	}
}

func TestProxyJournalRejectsChangedAuthorityOrPlan(t *testing.T) {
	entry := proxyJournalFixture(t)
	for _, mutate := range []func(*JournalProxy){
		func(e *JournalProxy) { e.Attempt.RunID = "proxy_pop_foreign" },
		func(e *JournalProxy) { e.Plan.CompilerVersion = "old.v0" },
		func(e *JournalProxy) { e.Plan.Snapshot.Routes[0].HostPort++ },
		func(e *JournalProxy) { e.Preparation.DaemonID = "foreign_daemon" },
		func(e *JournalProxy) { e.Preparation.EngineHash = "private-key-material" },
		func(e *JournalProxy) { e.Phase = "unknown" },
	} {
		changed := cloneJournalProxy(entry)
		mutate(&changed)
		if err := validateJournalProxy(changed); err == nil {
			t.Fatalf("invalid journal accepted: %+v", changed)
		}
	}
	for _, version := range []int{1, 2, 3, 5} {
		if validateJournalState(journalState{Version: version, Proxy: []JournalProxy{entry}}) == nil {
			t.Fatalf("proxy evidence accepted in unsupported version %d", version)
		}
	}
}

func TestProxyJournalBoundsPublicObservationsButPreservesExpiredReceipt(t *testing.T) {
	entry := proxyJournalFixture(t)
	entry.Phase = "result"
	a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
	entry.Result = &domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: entry.ID, Result: domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: 2, PlanHash: entry.Plan.Hash, EngineHash: entry.Preparation.EngineHash, DaemonID: entry.Preparation.DaemonID, ContainerID: strings.Repeat("c", 64), Routes: []domain.HostingProxyRouteObservation{{RouteID: "route_one", HTTPReady: true, HTTPSReady: true, Certificate: domain.HostingProxyCertificate{Hostname: "web.nerocd.invalid", Issuer: "fixture", Status: "trusted", Fingerprint: strings.Repeat("d", 64), ExpiresAt: time.Now().Add(-time.Hour)}}}}}
	if err := validateJournalProxy(entry); err != nil {
		t.Fatalf("historical committed result must remain replayable: %v", err)
	}
	for _, mutate := range []func(*domain.HostingProxyResult){
		func(r *domain.HostingProxyResult) { r.ContainerID = "arbitrary" },
		func(r *domain.HostingProxyResult) { r.Routes[0].RouteID = "foreign" },
		func(r *domain.HostingProxyResult) { r.Routes = append(r.Routes, r.Routes[0]) },
		func(r *domain.HostingProxyResult) { r.Routes[0].Certificate.Hostname = "foreign.example.test" },
		func(r *domain.HostingProxyResult) { r.Routes[0].Certificate.Issuer = strings.Repeat("x", 1025) },
		func(r *domain.HostingProxyResult) { r.Routes[0].Certificate.Fingerprint = "not-a-digest" },
		func(r *domain.HostingProxyResult) { r.Routes[0].HTTPReady = false },
		func(r *domain.HostingProxyResult) { r.Routes[0].Certificate.Status = "self-signed" },
	} {
		changed := cloneJournalProxy(entry)
		mutate(&changed.Result.Result)
		if validateJournalProxy(changed) == nil {
			t.Fatal("invalid public certificate or route observation accepted")
		}
	}
}
