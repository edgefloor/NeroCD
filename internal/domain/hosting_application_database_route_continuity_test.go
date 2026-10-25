package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestApplicationDatabaseRouteContinuityUsesPersistedHealthyValueIdentity(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			snapshot, release, input := applicationDatabaseFixture(t, engine, true)
			previous := input.Members[0].ApplicationHealthy
			id, volume := HostingStorageIdentity(snapshot.BindingID)
			storage := HostingStorage{ID: id, ProjectID: input.ApplicationProjectID, ResourceID: snapshot.ResourceID, BindingID: snapshot.BindingID, DestinationID: snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now().UTC()}
			storage.Hash = storage.DocumentHash()
			snapshot.Storage, snapshot.PolicyVersion = storage.Reference(), HostingApplicationDatabaseStoragePolicyVersion
			descriptor := HostingStoragePlan{Reference: storage.Reference(), VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: input.OperationID, CreationNonce: HostingStorageCreationNonce(storage.Reference(), input.OperationID, release.DaemonID), DaemonID: release.DaemonID}
			plan, err := CompileHostingApplicationDatabaseStoragePlan(snapshot, release, &previous, nil, input, descriptor)
			if err != nil {
				t.Fatal(err)
			}
			healthy := HostingHealthy{Snapshot: snapshot, Release: release, PlanHash: plan.Hash, OperationID: input.OperationID, ContainerID: strings.Repeat("c", 64), Storage: &HostingHealthyStorage{Plan: descriptor, CreationPlanHash: plan.Hash}}
			if ValidateHostingHealthyStorage(healthy) != nil {
				t.Fatal("invalid settled storage fixture")
			}
			raw, err := json.Marshal(healthy)
			if err != nil {
				t.Fatal(err)
			}
			var bindingHealthy, membershipHealthy HostingHealthy
			if json.Unmarshal(raw, &bindingHealthy) != nil || json.Unmarshal(raw, &membershipHealthy) != nil || bindingHealthy.Storage == nil || membershipHealthy.Storage == nil || bindingHealthy.Storage == membershipHealthy.Storage || HostingDocumentHash(bindingHealthy) != HostingDocumentHash(membershipHealthy) {
				t.Fatal("fixture did not independently decode identical persisted storage")
			}
			op := HostingOperation{ID: "next_storage", RunID: "run_next_storage", Snapshot: snapshot, ApplicationDatabase: &HostingApplicationDatabaseExecutionProof{Members: []HostingDatabaseNetworkMemberEvidence{{ApplicationHealthy: membershipHealthy}}}}
			op.Snapshot.HostPort++
			continuation := HostingApplicationDatabaseRouteContinuity{Version: HostingApplicationDatabaseRouteContinuityVersion, OperationID: op.ID, ProxyOperationID: "appdb_proxy_" + op.ID, FinalizeRunID: "finalize_" + op.ID, ProxyID: "proxy", LoadedSnapshotHash: HostingDocumentHash("loaded"), PreviousHealthy: bindingHealthy, Request: HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 3, ExpectedLoadedProxyOperationID: "loaded_proxy", Routes: []HostingRouteSwitchSelection{{RouteID: "route", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: healthy.OperationID}}}}
			continuation.Hash = continuation.DocumentHash()
			if err = continuation.Validate(op); err != nil {
				t.Fatal("equal persisted healthy storage rejected by pointer identity", err)
			}
			for name, mutate := range map[string]func(*HostingHealthyStorage){
				"reference":        func(s *HostingHealthyStorage) { s.Plan.Reference.Hash = HostingDocumentHash("foreign storage") },
				"origin_operation": func(s *HostingHealthyStorage) { s.Plan.CreationOperationID = "foreign_operation" },
				"origin_plan":      func(s *HostingHealthyStorage) { s.CreationPlanHash = HostingDocumentHash("foreign plan") },
				"nonce":            func(s *HostingHealthyStorage) { s.Plan.CreationNonce = strings.Repeat("f", 32) },
				"daemon":           func(s *HostingHealthyStorage) { s.Plan.DaemonID = "foreign_daemon" },
				"volume":           func(s *HostingHealthyStorage) { s.Plan.VolumeName = "foreign_volume" },
				"mount":            func(s *HostingHealthyStorage) { s.Plan.MountPath = "/other/uploads" },
			} {
				t.Run(name, func(t *testing.T) {
					bad := continuation
					var detached HostingHealthy
					if json.Unmarshal(raw, &detached) != nil {
						t.Fatal("decode negative fixture")
					}
					mutate(detached.Storage)
					bad.PreviousHealthy = detached
					bad.Hash = bad.DocumentHash()
					if bad.Validate(op) == nil {
						t.Fatal("self-rehashed continuation admitted changed storage identity")
					}
				})
			}
		})
	}
}

func TestApplicationDatabaseRouteIntentCopiesAndPinsCompleteSelections(t *testing.T) {
	in := HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 3, ExpectedLoadedProxyOperationID: "proxy_loaded", Routes: []HostingRouteSwitchSelection{{RouteID: "route_b", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "previous"}, {RouteID: "route_a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "previous"}}}
	got, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(in)
	if err != nil || got.Routes[0].RouteID != "route_a" || in.Routes[0].RouteID != "route_b" {
		t.Fatal("canonicalization lost owned route order", err)
	}
	got.Routes[0].ExpectedVersion = 9
	if in.Routes[1].ExpectedVersion != 1 {
		t.Fatal("canonical intent aliases caller selections")
	}
	for name, change := range map[string]func(*HostingApplicationDatabaseRouteContinuityInput){"missing_loaded": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.ExpectedLoadedProxyOperationID = "" }, "zero_generation": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.ExpectedLoadedGeneration = 0 }, "no_routes": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.Routes = nil }, "duplicate": func(v *HostingApplicationDatabaseRouteContinuityInput) { v.Routes[1] = v.Routes[0] }, "missing_predecessor": func(v *HostingApplicationDatabaseRouteContinuityInput) {
		v.Routes[0].ExpectedSelectedReleaseOperationID = ""
	}} {
		t.Run(name, func(t *testing.T) {
			bad := in
			bad.Routes = append([]HostingRouteSwitchSelection(nil), in.Routes...)
			change(&bad)
			if _, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(bad); err == nil {
				t.Fatal("incomplete intent admitted")
			}
		})
	}
}

func TestApplicationDatabaseYieldClosedAuthorityAndSafeProjection(t *testing.T) {
	h := "sha256:" + strings.Repeat("a", 64)
	y := HostingApplicationDatabaseYieldRequest{HostingAuthority: HostingAuthority{OperationID: "operation", RunID: "run_operation", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Version: HostingApplicationDatabaseRouteContinuityVersion, ReceiptID: "yield", PlanHash: h, StageHash: h, CandidateHash: h, ContinuationHash: h}
	raw, err := json.Marshal(y)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingApplicationDatabaseYieldRequest
	if json.Unmarshal(raw, &decoded) != nil || decoded != y {
		t.Fatal("yield tuple changed on round trip")
	}
	projection, err := json.Marshal(y.Receipt())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"lease_id", "attempt", "fence", "run_id"} {
		if bytes.Contains(projection, []byte(forbidden)) {
			t.Fatal("public yield disclosed authority", forbidden)
		}
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"version":`), []byte(`"extra":true,"version":`), 1), bytes.Replace(raw, []byte(`"attempt":1`), []byte(`"attempt":1,"attempt":1`), 1), bytes.Replace(raw, []byte(`"fence":"fence"`), []byte(`"fence":null`), 1), bytes.Replace(raw, []byte(`"receipt_id"`), []byte(`"Receipt_ID"`), 1)} {
		if json.Unmarshal(bad, &decoded) == nil {
			t.Fatal("ambiguous yield document admitted")
		}
	}
	y.Attempt = math.MaxInt32 + 1
	if y.Validate() == nil {
		t.Fatal("attempt overflow admitted")
	}
}
