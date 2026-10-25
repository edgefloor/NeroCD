//go:build unix

package runnerruntime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
)

func TestApplicationStorageProxyPlansReconstructOrdinaryAAndVerifiedBAfterMaintenance(t *testing.T) {
	config := ProxyConfig{Directory: t.TempDir(), DockerBinary: "/usr/bin/docker", DockerSocket: "/var/run/docker.sock", VerifiedSwitch: true}
	snapshot := domain.HostingProxySnapshot{ProxyID: "proxy", ServerID: "server", DestinationID: "destination", RunnerID: "runner", Generation: 3, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []domain.HostingProxyRouteTarget{{RouteID: "route", ProjectID: "project", ResourceID: "application", BindingID: "binding", ReleaseOperationID: "storage_a", Hostname: "app.example.test", PathPrefix: "/", HostPort: 21001}}}
	settings := hostingproxy.Settings{Directory: filepath.Join(config.Directory, snapshot.ProxyID), DockerBinary: config.DockerBinary, DockerSocket: config.DockerSocket, Email: snapshot.Configuration.Email, CA: hostingproxy.CA(snapshot.Configuration.CA), ManagementPort: snapshot.Configuration.ManagementPort}
	identity := hostingproxy.Identity{ProxyID: snapshot.ProxyID, ServerID: snapshot.ServerID, DestinationID: snapshot.DestinationID, RunnerID: snapshot.RunnerID, DaemonID: "daemon"}
	engineRoutes := func(s domain.HostingProxySnapshot) []hostingproxy.Route {
		var out []hostingproxy.Route
		for _, r := range s.Routes {
			out = append(out, hostingproxy.Route{ID: r.RouteID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, HostPort: r.HostPort})
		}
		return out
	}
	prior, err := hostingproxy.Prepare(identity, settings, 3, 2, engineRoutes(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	storageID, _ := domain.HostingStorageIdentity("binding")
	var last domain.HostingProxyPlan
	for _, operation := range []string{"storage_b", "storage_c"} {
		before := snapshot.Routes[0]
		snapshot.Generation++
		snapshot.Routes = append([]domain.HostingProxyRouteTarget(nil), snapshot.Routes...)
		snapshot.Routes[0].ReleaseOperationID = operation
		snapshot.Routes[0].HostPort++
		snapshot.VerifiedSwitch = &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: snapshot.Generation - 1, PriorAppliedProxyOperationID: "prior_" + before.ReleaseOperationID, Routes: []domain.HostingVerifiedSwitchRoute{{RouteID: before.RouteID, PriorVersion: snapshot.Generation - 3, PriorTarget: before}}}
		required := []string{}
		if operation == "storage_c" {
			required = []string{"route"}
		}
		proof := domain.HostingProxyStorageSwitch{Version: domain.HostingProxyStorageSwitchVersion, Action: "switch", OperationID: operation, ContinuationHash: domain.HostingDocumentHash("continuation_" + operation), StorageRef: domain.HostingStorageRef{StorageID: storageID, Generation: 1, Hash: domain.HostingDocumentHash("storage")}, StorageOriginHash: domain.HostingDocumentHash("materialized_origin_a"), WriterStateHash: domain.HostingDocumentHash("stopped_" + before.ReleaseOperationID), CandidateHash: domain.HostingDocumentHash("ready_" + operation), RouteScopeHash: domain.HostingDocumentHash(snapshot.Routes), PriorGeneration: snapshot.Generation - 1, PriorPreviousGeneration: snapshot.Generation - 2, PriorEngineHash: prior.Hash(), PriorRequiredRouteIDs: required}
		proof.Hash = proof.DocumentHash()
		snapshot.StorageSwitch = &proof
		plan, err := domain.CompileHostingProxyPlan(snapshot, snapshot.Generation-1, identity.DaemonID)
		if err != nil {
			t.Fatal("compile persisted storage proxy", err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var persisted domain.HostingProxyPlan
		if json.Unmarshal(raw, &persisted) != nil || domain.ValidateHostingProxyPlan(persisted) != nil {
			t.Fatal("persisted plan did not validate")
		}
		reconstructed, failed, err := applicationStorageProxyPlans(config, persisted)
		if err != nil || failed != nil || reconstructed.Hash() != prior.Hash() || reconstructed.Generation() != prior.Generation() || reconstructed.StaticHash() != prior.StaticHash() {
			t.Fatal("adapter changed exact committed predecessor", operation, err)
		}
		if operation == "storage_c" {
			ordinary, err := hostingproxy.Prepare(identity, settings, 4, 3, reconstructed.Routes())
			if err != nil || ordinary.Hash() == reconstructed.Hash() {
				t.Fatal("verified B was silently reconstructed as ordinary")
			}
		}
		prior, err = hostingproxy.PrepareVerifiedSwitch(identity, settings, uint64(snapshot.Generation), uint64(snapshot.Generation-1), engineRoutes(snapshot), []string{"route"})
		if err != nil {
			t.Fatal(err)
		}
		last = persisted
	}
	for _, mode := range []string{"prior_hash", "prior_previous", "prior_required", "destination", "prior_route"} {
		t.Run(mode, func(t *testing.T) {
			bad := last
			proof := *last.Snapshot.StorageSwitch
			bad.Snapshot.StorageSwitch = &proof
			switch mode {
			case "prior_hash":
				proof.PriorEngineHash = strings.Repeat("f", 64)
			case "prior_previous":
				proof.PriorPreviousGeneration--
			case "prior_required":
				proof.PriorRequiredRouteIDs = []string{}
			case "destination":
				bad.Snapshot.DestinationID = "foreign"
			case "prior_route":
				v := *last.Snapshot.VerifiedSwitch
				v.Routes = append([]domain.HostingVerifiedSwitchRoute(nil), v.Routes...)
				v.Routes[0].PriorTarget.HostPort--
				bad.Snapshot.VerifiedSwitch = &v
			}
			proof.Hash = proof.DocumentHash()
			if _, _, err := applicationStorageProxyPlans(config, bad); err == nil {
				t.Fatal("changed persisted predecessor reconstructed")
			}
		})
	}
}
