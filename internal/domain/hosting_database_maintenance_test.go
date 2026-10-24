package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func databaseMaintenanceFixture(t *testing.T, engine string, count int) (HostingDatabaseMaintenanceProof, HostingOperation) {
	t.Helper()
	app, capture := applicationExecutionFixture(t, engine)
	in, c, ep := executionCandidate(t, app)
	members := make([]HostingDatabaseNetworkMemberProof, 0, count)
	for i := 0; i < count; i++ {
		h := HostingHealthy{Snapshot: app.Snapshot, Release: *app.Release, OperationID: app.ID, ContainerID: c.ContainerID, PlanHash: app.Plan.Hash}
		endpoint := ep
		if i == 1 {
			h.OperationID = "application_retained"
			h.ContainerID = strings.Repeat("6", 64)
			h.PlanHash = "sha256:" + strings.Repeat("7", 64)
			endpoint.OperationID = h.OperationID
			endpoint.Target.ContainerOperationID = h.OperationID
			endpoint.Target.ContainerID = h.ContainerID
			endpoint.Target.ContainerPlanHash = h.PlanHash
			endpoint.Hash = endpoint.DocumentHash()
		}
		receipt := databaseBindingReceipt(endpoint, "attached")
		receipt.ReceiptID += "_" + h.OperationID
		receipt.Observation.Endpoint.EndpointID = strings.Repeat(string(rune('8'+i)), 64)
		receipt.Hash = receipt.DocumentHash()
		members = append(members, HostingDatabaseNetworkMemberProof{Binding: in.Binding, EndpointPlan: endpoint, Receipt: receipt, ApplicationHealthy: h, DatabasePlan: in.BindingDatabasePlan, ApplicationProjectID: in.ApplicationProjectID, DatabaseProjectID: in.DatabaseProjectID})
	}
	revision := int64(1)
	if count > 0 {
		revision = 2
	}
	network := HostingDatabaseNetworkPlanInput{Origin: in.Origin, MembershipRevision: revision, Members: members, Retired: []HostingDatabaseNetworkRetirementProof{}}
	previous := compileDatabaseNetwork(t, in.DatabasePlan, in.DatabaseHealthy, network)
	healthy := in.DatabaseHealthy
	healthy.OperationID = "database_current"
	healthy.PlanHash = previous.Hash
	healthy.Snapshot = previous.Snapshot
	m, r := networkEvidence(members, nil)
	proof := HostingDatabaseMaintenanceProof{Version: HostingDatabaseMaintenanceVersion, CapturePlan: capture.CapturePlan, OriginReceipt: capture.OriginReceipt, PreviousPlan: previous, PreviousHealthy: healthy, MembershipRevision: revision, Members: m, Retired: r}
	proof.Hash = proof.DocumentHash()
	s := previous.Snapshot
	s.ResourceGeneration++
	plan, err := CompileHostingDatabaseMaintenancePlan(s, previous.Release, proof)
	if err != nil {
		t.Fatal("maintenance fixture", err)
	}
	release := previous.Release
	op := HostingOperation{ID: "database_maintenance", ProjectID: proof.CapturePlan.ProjectID, ResourceID: s.ResourceID, BindingID: s.BindingID, Snapshot: s, Release: &release, Plan: &plan, DatabaseMaintenance: &proof}
	if ValidateHostingDatabaseMaintenanceOperation(op) != nil {
		t.Fatal("maintenance operation fixture")
	}
	return proof, op
}

func TestDatabaseMaintenancePreservesMembersAndOriginalReferences(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, count := range []int{0, 1, 2} {
			t.Run(engine+string(rune('0'+count)), func(t *testing.T) {
				proof, op := databaseMaintenanceFixture(t, engine, count)
				in, err := HostingDatabaseMaintenancePlanInput(proof)
				if err != nil || len(in.Members) != count || ValidateHostingDatabasePlanTransition(*op.Plan, proof.PreviousPlan, in) != nil || len(op.Plan.Database.Network.Members) != count {
					t.Fatal("members changed", err)
				}
				owned, err := NewHostingDatabaseMaintenanceProof(proof.CapturePlan, proof.OriginReceipt, proof.PreviousPlan, proof.PreviousHealthy, in)
				if err != nil || owned.Hash != proof.Hash {
					t.Fatal("owned maintenance constructor changed evidence", err)
				}
				foreign := in
				foreign.Origin.NetworkID = strings.Repeat("f", 64)
				if _, err := NewHostingDatabaseMaintenanceProof(proof.CapturePlan, proof.OriginReceipt, proof.PreviousPlan, proof.PreviousHealthy, foreign); err == nil {
					t.Fatal("constructor replaced a foreign origin")
				}
				if *op.Plan.Storage != *proof.PreviousPlan.Storage || op.Plan.Database.Network.NetworkID != proof.OriginReceipt.Origin.NetworkID || op.Plan.Release != proof.PreviousPlan.Release || op.Plan.Snapshot.Database != proof.PreviousPlan.Snapshot.Database {
					t.Fatal("stable database references changed")
				}
				raw, err := json.Marshal(proof)
				var persisted HostingDatabaseMaintenanceProof
				if err != nil || json.Unmarshal(raw, &persisted) != nil || persisted.Hash != proof.Hash {
					t.Fatal("maintenance round trip rejected", err)
				}
				// Omitted and empty retention have identical canonical JSON and identity.
				persisted.CapturePlan.SourcePlan.RetainedReleases = []HostingHealthy{}
				persisted.PreviousPlan.RetainedReleases = []HostingHealthy{}
				persisted.PreviousPlan.Database.Credentials = append([]HostingDatabaseCredentialRef{}, persisted.PreviousPlan.Database.Credentials...)
				if persisted.DocumentHash() != proof.Hash || persisted.Validate() != nil {
					t.Fatal("canonical persisted originals rejected")
				}
				emptyUpgrade := HostingDatabaseUpgradeProof{Version: HostingDatabaseUpgradeVersion, CapturePlan: proof.CapturePlan, OriginReceipt: proof.OriginReceipt, PreviousPlan: proof.PreviousPlan, PreviousHealthy: proof.PreviousHealthy}
				emptyUpgrade.Hash = emptyUpgrade.DocumentHash()
				if count > 0 && emptyUpgrade.Validate() == nil {
					t.Fatal("member-aware proof broadened empty upgrade")
				}
				queued := op
				queued.Plan = nil
				queued.Release = nil
				if ValidateHostingDatabaseMaintenanceOperation(queued) != nil {
					t.Fatal("queued maintenance rejected")
				}
				if ValidateHostingDatabaseUpgradeOperation(op) == nil {
					t.Fatal("legacy validator accepted maintenance")
				}
				t.Logf("serialized maintenance proof=%d plan=%d members=%d", len(raw), len(mustContractJSON(t, op.Plan)), count)
			})
		}
	}
}

func mustContractJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDatabaseMaintenanceRetirementAndForgedProofs(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			proof, op := databaseMaintenanceFixture(t, engine, 2)
			old := proof.Members[0]
			retire := old.EndpointPlan
			retire.OperationID = "retire_member"
			retire.Action = "retire"
			retire.ExpectedEndpointID = old.Receipt.Observation.Endpoint.EndpointID
			retire.MembershipRevision = 3
			retire.Hash = retire.DocumentHash()
			proof.Members = proof.Members[1:]
			proof.MembershipRevision = 3
			proof.Retired = []HostingDatabaseNetworkRetirementEvidence{{Attached: old, RetirePlan: retire, RetireIntent: databaseBindingReceipt(retire, "retire_intent"), Detached: databaseBindingReceipt(retire, "detached")}}
			proof.Hash = proof.DocumentHash()
			if proof.Validate() != nil {
				t.Fatal("exact retirement rejected")
			}
			plan, err := CompileHostingDatabaseMaintenancePlan(op.Snapshot, *op.Release, proof)
			if err != nil || len(plan.Database.Network.Members) != 1 {
				t.Fatal("retirement maintenance rejected", err)
			}
			raw := mustContractJSON(t, proof)
			t.Logf("serialized retired proof=%d plan=%d", len(raw), len(mustContractJSON(t, plan)))
			for name, change := range map[string]func(*HostingDatabaseMaintenanceProof){
				"missing_retirement":  func(p *HostingDatabaseMaintenanceProof) { p.Retired = []HostingDatabaseNetworkRetirementEvidence{} },
				"stale_revision":      func(p *HostingDatabaseMaintenanceProof) { p.MembershipRevision = 2 },
				"foreign_origin":      func(p *HostingDatabaseMaintenanceProof) { p.OriginReceipt.Origin.NetworkID = strings.Repeat("f", 64) },
				"foreign_predecessor": func(p *HostingDatabaseMaintenanceProof) { p.PreviousHealthy.ContainerID = "foreign" },
				"forged_retirement": func(p *HostingDatabaseMaintenanceProof) {
					p.Retired[0].Detached.Observation.Present = true
					p.Retired[0].Detached.Hash = p.Retired[0].Detached.DocumentHash()
				},
				"v1_predecessor": func(p *HostingDatabaseMaintenanceProof) {
					p.PreviousPlan = p.CapturePlan.SourcePlan
					p.PreviousHealthy = p.CapturePlan.SourceHealthy
				},
			} {
				t.Run(name, func(t *testing.T) {
					var p HostingDatabaseMaintenanceProof
					if json.Unmarshal(raw, &p) != nil {
						t.Fatal("clone")
					}
					change(&p)
					p.Hash = p.DocumentHash()
					if p.Validate() == nil {
						t.Fatal("forged maintenance accepted")
					}
				})
			}
			for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"version":`), []byte(`"Version":`), 1), bytes.Replace(raw, []byte(`"version":`), []byte(`"unknown":true,"version":`), 1), bytes.Replace(raw, []byte(`"version":`), []byte(`"version":"duplicate","version":`), 1), append(append([]byte("{"), bytes.Repeat([]byte(" "), HostingApplicationDatabaseProofJSONLimit)...), raw[1:]...)} {
				if json.Unmarshal(bad, new(HostingDatabaseMaintenanceProof)) == nil {
					t.Fatal("open or oversized maintenance proof accepted")
				}
			}
			op.DatabaseMaintenance = &proof
			op.Plan = &plan
			mixed := op
			mixed.ApplicationDatabase = &HostingApplicationDatabaseExecutionProof{}
			if ValidateHostingDatabaseMaintenanceOperation(mixed) == nil {
				t.Fatal("mixed maintenance accepted")
			}
			foreign := op
			foreign.ProjectID = "foreign"
			if ValidateHostingDatabaseMaintenanceOperation(foreign) == nil {
				t.Fatal("cross-project maintenance accepted")
			}
			// Returned transient proof is owned by the consumer.
			input, err := HostingDatabaseMaintenancePlanInput(proof)
			if err != nil {
				t.Fatal(err)
			}
			input.Members[0].Binding.EnvironmentNames[0] = "foreign"
			if proof.Validate() != nil {
				t.Fatal("input conversion aliased retained evidence")
			}
		})
	}
}

func TestDatabaseMaintenanceMaximumRetirementProof(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			proof, op := databaseMaintenanceFixture(t, engine, 2)
			for _, member := range proof.Members {
				plan := member.EndpointPlan
				plan.OperationID = "retire_" + member.ApplicationHealthy.OperationID
				plan.Action = "retire"
				plan.ExpectedEndpointID = member.Receipt.Observation.Endpoint.EndpointID
				plan.MembershipRevision = 3
				plan.Hash = plan.DocumentHash()
				intent := databaseBindingReceipt(plan, "retire_intent")
				intent.ReceiptID += "_" + plan.OperationID
				intent.Hash = intent.DocumentHash()
				detached := databaseBindingReceipt(plan, "detached")
				detached.ReceiptID += "_" + plan.OperationID
				detached.Hash = detached.DocumentHash()
				proof.Retired = append(proof.Retired, HostingDatabaseNetworkRetirementEvidence{Attached: member, RetirePlan: plan, RetireIntent: intent, Detached: detached})
			}
			proof.Members = []HostingDatabaseNetworkMemberEvidence{}
			proof.MembershipRevision = 3
			proof.Hash = proof.DocumentHash()
			plan, err := CompileHostingDatabaseMaintenancePlan(op.Snapshot, *op.Release, proof)
			if err != nil || len(plan.Database.Network.Members) != 0 {
				t.Fatal("complete exact retirement rejected", err)
			}
			raw := mustContractJSON(t, proof)
			var persisted HostingDatabaseMaintenanceProof
			if len(raw) > HostingApplicationDatabaseProofJSONLimit || json.Unmarshal(raw, &persisted) != nil {
				t.Fatal("maximum retirement proof exceeded serialized contract")
			}
			envelope := struct {
				Plan  HostingApplyPlan                `json:"plan"`
				Proof HostingDatabaseMaintenanceProof `json:"proof"`
			}{plan, persisted}
			size := len(mustContractJSON(t, envelope))
			if size > HostingApplicationDatabaseJournalJSONLimit {
				t.Fatal("maintenance journal bound exceeded")
			}
			t.Logf("serialized two-retirement proof=%d final plan=%d composed=%d", len(raw), len(mustContractJSON(t, plan)), size)
		})
	}
}
