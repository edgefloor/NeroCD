package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestApplicationDatabaseStorageInputRequiresClosedExplicitAcknowledgment(t *testing.T) {
	valid := `{"expected_storage_generation":1,"expected_storage_hash":"sha256:` + strings.Repeat("a", 64) + `","acknowledge_interruption":true}`
	for _, raw := range []string{valid, strings.Replace(valid, "true", "false", 1), strings.Replace(valid, `"expected_storage_generation":1`, `"expected_storage_generation":2`, 1), strings.Replace(valid, `"acknowledge_interruption":true`, `"acknowledge_interruption":true,"volume_name":"foreign"`, 1), strings.Replace(valid, `"acknowledge_interruption":true`, `"acknowledge_interruption":true,"acknowledge_interruption":true`, 1)} {
		var in HostingApplicationDatabaseStorageInput
		if err := json.Unmarshal([]byte(raw), &in); (err == nil) != (raw == valid) {
			t.Fatal("closed interruption input", err)
		}
	}
}

func TestApplicationDatabaseStorageComposedPlanPreservesBindingAndVolume(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			s, release, input := applicationDatabaseFixture(t, engine, true)
			previous := input.Members[0].ApplicationHealthy
			id, volume := HostingStorageIdentity(s.BindingID)
			storage := HostingStorage{ID: id, ProjectID: input.ApplicationProjectID, ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now()}
			storage.Hash = storage.DocumentHash()
			s.Storage, s.PolicyVersion = storage.Reference(), HostingApplicationDatabaseStoragePolicyVersion
			descriptor := HostingStoragePlan{Reference: s.Storage, VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: input.OperationID, DaemonID: release.DaemonID}
			descriptor.CreationNonce = HostingStorageCreationNonce(s.Storage, input.OperationID, release.DaemonID)
			plan, err := CompileHostingApplicationDatabaseStoragePlan(s, release, &previous, nil, input, descriptor)
			if err != nil || ValidateHostingApplicationDatabasePlan(plan, input) != nil || plan.Storage == nil || *plan.Storage != descriptor || plan.RetainPrevious || plan.CompilerVersion != HostingApplicationDatabaseStorageCompilerVersion {
				t.Fatal("composed plan rejected or lost exclusive volume policy", err)
			}
			if _, err = CompileHostingApplicationDatabasePlan(s, release, &previous, nil, input); err == nil {
				t.Fatal("old compiler silently accepted storage")
			}
			if _, err = CompileHostingOwnedStoragePlan(s, release, &previous, nil, descriptor); err == nil {
				t.Fatal("ordinary storage compiler accepted connected composition")
			}
			wrong := plan
			wrong.Storage = nil
			wrong.Hash = ""
			wrong.Hash = HostingDocumentHash(wrong)
			if ValidateHostingApplicationDatabasePlan(wrong, input) == nil {
				t.Fatal("self-rehashed plan dropped volume")
			}
			healthy := HostingHealthy{Snapshot: s, Release: release, PlanHash: plan.Hash, OperationID: input.OperationID, ContainerID: strings.Repeat("c", 64), Storage: &HostingHealthyStorage{Plan: descriptor, CreationPlanHash: plan.Hash}}
			if ValidateHostingHealthyStorage(healthy) != nil {
				t.Fatal("composed healthy storage rejected")
			}
		})
	}
}

func TestApplicationDatabaseStorageStateRequiresExplicitCorrelations(t *testing.T) {
	state := HostingApplicationDatabaseStorageState{Version: HostingApplicationDatabaseStorageVersion, Phase: "stopped", ReceiptID: "writer_stopped", StateHash: "sha256:" + strings.Repeat("a", 64), RecoveryActions: []string{}, RecoveryState: "none"}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingApplicationDatabaseStorageState
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("closed valid state rejected")
	}
	for _, field := range []string{"stage_hash", "candidate_hash", "previous_state_hash", "recovery_actions", "recovery_action", "recovery_state"} {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			t.Fatal("fixture")
		}
		delete(object, field)
		changed, _ := json.Marshal(object)
		if json.Unmarshal(changed, &decoded) == nil {
			t.Fatal("missing required safe correlation accepted", field)
		}
	}
	state.StageHash = "sha256:" + strings.Repeat("b", 64)
	if state.Validate() == nil {
		t.Fatal("stage without candidate accepted")
	}
	state.StageHash = ""
	state.RecoveryAction = "resume_predecessor"
	if state.Validate() == nil {
		t.Fatal("recovery action without explicit state accepted")
	}
}

func TestApplicationDatabaseStorageEffectDoesNotExtendOldProtocol(t *testing.T) {
	grant := HostingApplicationDatabaseStorageEffectAuthorization{HostingApplicationDatabaseEffectAuthorization: HostingApplicationDatabaseEffectAuthorization{AuthorizationID: strings.Repeat("a", 32), OperationID: "operation", PlanHash: "sha256:" + strings.Repeat("b", 64), Action: "stop_predecessor", LeaseID: "lease", Attempt: 1, Fence: "fence", MembershipRevision: 2, UseUntil: time.Now().Add(time.Second)}, StateHash: "sha256:" + strings.Repeat("c", 64)}
	if grant.Validate() != nil || grant.HostingApplicationDatabaseEffectAuthorization.Validate() == nil {
		t.Fatal("new storage action silently widened old effect protocol")
	}
	raw, _ := json.Marshal(grant)
	var decoded HostingApplicationDatabaseStorageEffectAuthorization
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("closed storage grant rejected")
	}
	for _, bad := range []string{strings.Replace(string(raw), `"state_hash":`, `"unknown":true,"state_hash":`, 1), strings.Replace(string(raw), `"action":"stop_predecessor"`, `"action":"delete_volume"`, 1)} {
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatal("open or invalid storage grant accepted")
		}
	}
}

func TestApplicationDatabaseStorageProxyScopeRequiresExactStoppedEvidence(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	id, _ := HostingStorageIdentity("application_binding")
	snapshot := HostingProxySnapshot{Generation: 2, Routes: []HostingProxyRouteTarget{{RouteID: "application_route", ResourceID: "application", ReleaseOperationID: "replacement", HostPort: 18082}, {RouteID: "unrelated_route", ResourceID: "unrelated", ReleaseOperationID: "unchanged", HostPort: 18083}}, VerifiedSwitch: &HostingVerifiedSwitch{Version: 1}}
	proof := HostingProxyStorageSwitch{Version: HostingProxyStorageSwitchVersion, Action: "switch", OperationID: "replacement", ContinuationHash: hash, StorageRef: HostingStorageRef{StorageID: id, Generation: 1, Hash: hash}, StorageOriginHash: hash, WriterStateHash: hash, CandidateHash: hash, RouteScopeHash: HostingDocumentHash(snapshot.Routes), PriorGeneration: 1, PriorEngineHash: strings.Repeat("a", 64), PriorRequiredRouteIDs: []string{}}
	proof.Hash = proof.DocumentHash()
	if proof.Validate(snapshot, 1) != nil {
		t.Fatal("exact interrupted scope rejected")
	}
	for _, mutate := range []func(*HostingProxyStorageSwitch){
		func(p *HostingProxyStorageSwitch) { p.WriterStateHash = "" },
		func(p *HostingProxyStorageSwitch) { p.StorageOriginHash = "" },
		func(p *HostingProxyStorageSwitch) { p.ContinuationHash = "" },
		func(p *HostingProxyStorageSwitch) { p.CandidateHash = "" },
		func(p *HostingProxyStorageSwitch) { p.PriorGeneration = 2 },
		func(p *HostingProxyStorageSwitch) { p.PriorRequiredRouteIDs = []string{"foreign_route"} },
		func(p *HostingProxyStorageSwitch) { p.Action = "complete_replacement" },
	} {
		bad := proof
		mutate(&bad)
		bad.Hash = bad.DocumentHash()
		if bad.Validate(snapshot, 1) == nil {
			t.Fatal("incomplete self-rehashed storage proxy proof accepted")
		}
	}
	snapshot.Routes[1].HostPort++
	if proof.Validate(snapshot, 1) == nil {
		t.Fatal("unrelated route changed under stopped predecessor allowance")
	}
}
