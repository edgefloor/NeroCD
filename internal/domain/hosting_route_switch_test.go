package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func switchSnapshot() HostingProxySnapshot {
	a := HostingProxyRouteTarget{RouteID: "route_a", ProjectID: "project", ResourceID: "resource", BindingID: "binding", ReleaseOperationID: "release_b", Hostname: "app.example.test", PathPrefix: "/", HostPort: 19002}
	b := a
	b.RouteID = "route_other"
	b.ResourceID = "other"
	b.BindingID = "other_binding"
	b.Hostname = "other.example.test"
	b.ReleaseOperationID = "release_other"
	prior := a
	prior.ReleaseOperationID = "release_a"
	prior.HostPort = 19001
	return HostingProxySnapshot{ProxyID: "proxy", ServerID: "server", DestinationID: "destination", RunnerID: "runner", Generation: 5, Configuration: HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []HostingProxyRouteTarget{b, a}, VerifiedSwitch: &HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: 3, PriorAppliedProxyOperationID: "operation_a", Routes: []HostingVerifiedSwitchRoute{{RouteID: a.RouteID, PriorVersion: 7, PriorTarget: prior}}}}
}

func TestVerifiedRouteSwitch(t *testing.T) {
	t.Run("switch_legacy_bytes", func(t *testing.T) {
		snapshot := switchSnapshot()
		snapshot.VerifiedSwitch = nil
		plan, err := CompileHostingProxyPlan(snapshot, 3, "daemon")
		if err != nil {
			t.Fatal(err)
		}
		legacySnapshot := struct {
			ProxyID       string                    `json:"proxy_id"`
			ServerID      string                    `json:"server_id"`
			DestinationID string                    `json:"destination_id"`
			RunnerID      string                    `json:"runner_id"`
			Generation    int64                     `json:"generation"`
			Configuration HostingProxyConfiguration `json:"configuration"`
			Routes        []HostingProxyRouteTarget `json:"routes"`
		}{snapshot.ProxyID, snapshot.ServerID, snapshot.DestinationID, snapshot.RunnerID, snapshot.Generation, snapshot.Configuration, plan.Snapshot.Routes}
		legacy := struct {
			Snapshot           any    `json:"snapshot"`
			PreviousGeneration int64  `json:"previous_generation"`
			CompilerVersion    string `json:"compiler_version"`
			DaemonID           string `json:"daemon_id"`
			Hash               string `json:"hash"`
		}{legacySnapshot, 3, HostingProxyCompilerVersion, "daemon", ""}
		if plan.Hash != HostingDocumentHash(legacy) {
			t.Fatal("nil descriptor changed legacy hash")
		}
		legacy.Hash = plan.Hash
		old, _ := json.Marshal(legacy)
		current, _ := json.Marshal(plan)
		if !bytes.Equal(old, current) {
			t.Fatal("nil descriptor changed legacy bytes")
		}
	})
	t.Run("exact_scope", func(t *testing.T) {
		snapshot := switchSnapshot()
		plan, err := CompileHostingProxyPlan(snapshot, 3, "daemon")
		if err != nil {
			t.Fatal(err)
		}
		snapshot.VerifiedSwitch.Routes[0].PriorTarget.HostPort++
		if plan.Snapshot.VerifiedSwitch.Routes[0].PriorTarget.HostPort != 19001 {
			t.Fatal("descriptor aliases input")
		}
		for _, mutate := range []func(*HostingProxySnapshot){
			func(s *HostingProxySnapshot) { s.VerifiedSwitch.Version = 2 },
			func(s *HostingProxySnapshot) { s.VerifiedSwitch.ExpectedLoadedGeneration = 4 },
			func(s *HostingProxySnapshot) { s.VerifiedSwitch.Routes[0].PriorVersion = 0 },
			func(s *HostingProxySnapshot) { s.VerifiedSwitch.Routes[0].PriorTarget.Hostname = "other.example.test" },
			func(s *HostingProxySnapshot) { s.VerifiedSwitch.Routes[0].PriorTarget.ResourceID = "foreign" },
			func(s *HostingProxySnapshot) {
				s.VerifiedSwitch.Routes = append(s.VerifiedSwitch.Routes, s.VerifiedSwitch.Routes[0])
			},
		} {
			bad := switchSnapshot()
			mutate(&bad)
			if _, err = CompileHostingProxyPlan(bad, 3, "daemon"); err == nil {
				t.Fatal("invalid switch scope accepted")
			}
		}
		prior := HostingProxyPriorRoutes(plan.Snapshot)
		if prior[0].ReleaseOperationID != "release_a" || prior[1] != plan.Snapshot.Routes[1] {
			t.Fatal("prior route set changed unrelated route")
		}
		input := HostingRouteSwitchInput{ExpectedLoadedGeneration: 3, TargetReleaseOperationID: "release_b", Routes: []HostingRouteSwitchSelection{{RouteID: "b", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "a"}, {RouteID: "a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "a"}}}
		got, err := CanonicalHostingRouteSwitchInput(input)
		if err != nil || got.Routes[0].RouteID != "a" || input.Routes[0].RouteID != "b" {
			t.Fatal("input canonicalization aliases input")
		}
	})
	t.Run("tls_and_rollback", func(t *testing.T) {
		plan, err := CompileHostingProxyPlan(switchSnapshot(), 3, "daemon")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		result := HostingProxyResult{PlanHash: plan.Hash, Outcome: "rolled_back", LoadedGeneration: 3}
		for _, r := range plan.Snapshot.Routes {
			result.Routes = append(result.Routes, HostingProxyRouteObservation{RouteID: r.RouteID, HTTPReady: true, HTTPSReady: true, Certificate: HostingProxyCertificate{Hostname: r.Hostname, Status: "trusted", Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Hour)}})
		}
		if err = ValidateHostingVerifiedSwitchResult(plan, result, now); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*HostingProxyResult){func(r *HostingProxyResult) { r.Routes[0].HTTPSReady = false }, func(r *HostingProxyResult) { r.Routes[0].Certificate.ExpiresAt = now.Add(-time.Second) }, func(r *HostingProxyResult) { r.Routes[1] = r.Routes[0] }, func(r *HostingProxyResult) { r.LoadedGeneration = 5 }} {
			bad := result
			bad.Routes = append([]HostingProxyRouteObservation(nil), result.Routes...)
			mutate(&bad)
			if err = ValidateHostingVerifiedSwitchResult(plan, bad, now); err == nil {
				t.Fatal("invalid rollback evidence accepted")
			}
		}
		result.Routes[0].Certificate.ExpiresAt = now.Add(-time.Second)
		if err = ValidateHostingVerifiedSwitchResult(plan, result, time.Time{}); err != nil {
			t.Fatal("historical TLS evidence was treated as fresh")
		}
		if !reflect.DeepEqual(plan.Snapshot.VerifiedSwitch.RouteIDs(), []string{"route_a"}) {
			t.Fatal("selected IDs changed")
		}
	})
}
