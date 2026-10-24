package domain

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func retirementMember(source HostingDatabaseNetworkMemberProof, name, container, endpoint string, revision int64) HostingDatabaseNetworkMemberProof {
	source.ApplicationHealthy.OperationID = "app_" + name
	source.ApplicationHealthy.ContainerID = strings.Repeat(container, 64)
	source.ApplicationHealthy.Snapshot.HostPort = 21002
	source.EndpointPlan.OperationID = "attach_" + name
	source.EndpointPlan.Target.ContainerID = source.ApplicationHealthy.ContainerID
	source.EndpointPlan.Target.ContainerOperationID = source.ApplicationHealthy.OperationID
	source.EndpointPlan.MembershipRevision = revision
	source.EndpointPlan.Hash = source.EndpointPlan.DocumentHash()
	source.Receipt = databaseBindingReceipt(source.EndpointPlan, "attached")
	source.Receipt.ReceiptID = "attached_" + name
	source.Receipt.Observation.Endpoint.EndpointID = strings.Repeat(endpoint, 64)
	source.Receipt.Hash = source.Receipt.DocumentHash()
	return source
}

func retirementProof(attached HostingDatabaseNetworkMemberProof, revision int64) HostingDatabaseNetworkRetirementProof {
	p := attached.EndpointPlan
	p.OperationID = "retire_" + attached.ApplicationHealthy.OperationID
	p.Action, p.ExpectedEndpointID = "retire", attached.Receipt.Observation.Endpoint.EndpointID
	p.MembershipRevision = revision
	p.Hash = p.DocumentHash()
	proof := HostingDatabaseNetworkRetirementProof{Attached: attached, RetirePlan: p}
	retirementReceipts(&proof)
	return proof
}

func retirementReceipts(proof *HostingDatabaseNetworkRetirementProof) {
	proof.RetirePlan.Hash = proof.RetirePlan.DocumentHash()
	proof.RetireIntent = databaseBindingReceipt(proof.RetirePlan, "retire_intent")
	proof.RetireIntent.ReceiptID = proof.RetirePlan.OperationID + "_intent"
	proof.RetireIntent.Hash = proof.RetireIntent.DocumentHash()
	proof.Detached = databaseBindingReceipt(proof.RetirePlan, "detached")
	proof.Detached.ReceiptID = proof.RetirePlan.OperationID + "_detached"
	proof.Detached.Hash = proof.Detached.DocumentHash()
}

func retirementFixture(t *testing.T, engine string, two bool) (HostingApplyPlan, HostingHealthy, HostingDatabaseNetworkPlanInput) {
	t.Helper()
	original, healthy, in := databaseNetworkFixture(t, engine)
	in.Members[0].ApplicationHealthy.Snapshot.HostPort = 21001
	if two {
		in.Members = append(in.Members, retirementMember(in.Members[0], "b", "4", "5", in.MembershipRevision))
	}
	p := compileDatabaseNetwork(t, original, healthy, in)
	healthy.Snapshot, healthy.PlanHash, healthy.OperationID = p.Snapshot, p.Hash, "database_baseline"
	return p, healthy, in
}

func retirementCompile(p HostingApplyPlan, healthy HostingHealthy, in HostingDatabaseNetworkPlanInput) (HostingApplyPlan, error) {
	s, db := p.Snapshot, *p.Database
	s.ResourceGeneration++
	db.Network = nil
	return CompileHostingDatabasePlanV2(s, p.Release, healthy, p, *p.Storage, db, in)
}

func TestDatabaseNetworkRetirementTransitions(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			baseline, healthy, in := retirementFixture(t, engine, true)
			a, b := in.Members[0], in.Members[1]
			in.MembershipRevision++
			in.Members = []HostingDatabaseNetworkMemberProof{b}
			in.Retired = []HostingDatabaseNetworkRetirementProof{retirementProof(a, in.MembershipRevision)}
			one, err := retirementCompile(baseline, healthy, in)
			if err != nil || ValidateHostingDatabasePlanTransition(one, baseline, in) != nil || len(one.Database.Network.Members) != 1 {
				t.Fatal("retiring one of two members failed", err)
			}
			again, err := retirementCompile(baseline, healthy, in)
			if err != nil || again.Hash != one.Hash {
				t.Fatal("historical proof replay against the same baseline was not deterministic", err)
			}
			withoutProof := in
			withoutProof.Retired = nil
			if _, err := retirementCompile(baseline, healthy, withoutProof); err == nil || ValidateHostingDatabasePlanTransition(one, baseline, withoutProof) == nil {
				t.Fatal("compiler or contextual validator forgot a member without retirement proof")
			}
			healthy.Snapshot, healthy.PlanHash, healthy.OperationID = one.Snapshot, one.Hash, "database_one_member"
			in.MembershipRevision++
			in.Members = nil
			in.Retired = []HostingDatabaseNetworkRetirementProof{retirementProof(b, in.MembershipRevision)}
			empty, err := retirementCompile(one, healthy, in)
			if err != nil || ValidateHostingDatabasePlanTransition(empty, one, in) != nil || len(empty.Database.Network.Members) != 0 {
				t.Fatal("last settled endpoint could not retire to an empty set", err)
			}
			raw, err := json.Marshal(empty)
			var decoded HostingApplyPlan
			if err != nil || json.Unmarshal(raw, &decoded) != nil || ValidateHostingDatabasePlanTransition(decoded, one, in) != nil || strings.Contains(string(raw), `"retired"`) || strings.Contains(string(raw), `"Retired"`) {
				t.Fatal("retirement proof changed the persisted plan format", err)
			}
			in.Retired = nil
			healthy.Snapshot, healthy.PlanHash, healthy.OperationID = empty.Snapshot, empty.Hash, "database_empty"
			maintained, err := retirementCompile(empty, healthy, in)
			if err != nil || maintained.Database.Network.MembershipRevision != empty.Database.Network.MembershipRevision {
				t.Fatal("unchanged empty set lost exact revision maintenance", err)
			}
			in.MembershipRevision++
			in.Members = []HostingDatabaseNetworkMemberProof{retirementMember(a, "c", "6", "7", in.MembershipRevision)}
			later, err := retirementCompile(empty, healthy, in)
			if err != nil || ValidateHostingDatabasePlanTransition(later, empty, in) != nil || len(later.Database.Network.Members) != 1 || later.Database.Network.OriginHash != baseline.Database.Network.OriginHash {
				t.Fatal("a new settled attachment after retirement changed origin or failed", err)
			}
		})
	}
}

func TestDatabaseNetworkRetirementRejectsAlteredProof(t *testing.T) {
	changes := map[string]func(*HostingDatabaseNetworkRetirementProof){
		"target": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.ContainerID = strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"image": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.ImageID = "sha256:" + strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"release": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.ReleaseHash = "sha256:" + strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"container_plan": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.ContainerPlanHash = "sha256:" + strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"container_target_operation": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.ContainerOperationID = "foreign"
			retirementReceipts(v)
		},
		"network_name": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.NetworkName = "foreign"
			retirementReceipts(v)
		},
		"network": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.NetworkID = strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"daemon": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.Target.DaemonID = "foreign"
			retirementReceipts(v)
		},
		"endpoint": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.ExpectedEndpointID = strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"binding": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.BindingHash = "sha256:" + strings.Repeat("0", 64)
			retirementReceipts(v)
		},
		"action": func(v *HostingDatabaseNetworkRetirementProof) { v.RetirePlan.Action = "attach"; retirementReceipts(v) },
		"attach_operation": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.OperationID = v.Attached.EndpointPlan.OperationID
			retirementReceipts(v)
		},
		"container_operation": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.OperationID = v.Attached.ApplicationHealthy.OperationID
			retirementReceipts(v)
		},
		"old_revision": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.MembershipRevision--
			retirementReceipts(v)
		},
		"future_revision": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetirePlan.MembershipRevision++
			retirementReceipts(v)
		},
		"missing_intent": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetireIntent = HostingDatabaseBindingEndpointReceipt{}
		},
		"intent_only": func(v *HostingDatabaseNetworkRetirementProof) { v.Detached = v.RetireIntent },
		"uncertain": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Detached = databaseBindingReceipt(v.RetirePlan, "uncertain")
		},
		"presence": func(v *HostingDatabaseNetworkRetirementProof) { v.Detached.Observation.Present = true },
		"wrong_detached_endpoint": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Detached.Observation.Endpoint.EndpointID = strings.Repeat("0", 64)
		},
		"wrong_intent_endpoint": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetireIntent.Intent.EndpointID = strings.Repeat("0", 64)
		},
		"same_receipt": func(v *HostingDatabaseNetworkRetirementProof) { v.Detached.ReceiptID = v.RetireIntent.ReceiptID },
		"intent_reuses_attached": func(v *HostingDatabaseNetworkRetirementProof) {
			v.RetireIntent.ReceiptID = v.Attached.Receipt.ReceiptID
		},
		"detached_reuses_attached": func(v *HostingDatabaseNetworkRetirementProof) { v.Detached.ReceiptID = v.Attached.Receipt.ReceiptID },
		"wrong_receipt_operation":  func(v *HostingDatabaseNetworkRetirementProof) { v.Detached.OperationID = "foreign" },
		"wrong_receipt_plan": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Detached.PlanHash = "sha256:" + strings.Repeat("0", 64)
		},
		"wrong_receipt_revision": func(v *HostingDatabaseNetworkRetirementProof) { v.Detached.MembershipRevision++ },
		"attached_intent": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Attached.Receipt = databaseBindingReceipt(v.Attached.EndpointPlan, "attach_intent")
		},
		"attached_wrong_project": func(v *HostingDatabaseNetworkRetirementProof) { v.Attached.ApplicationProjectID = "foreign" },
		"attached_wrong_generation": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Attached.ApplicationHealthy.Snapshot.ResourceGeneration++
		},
		"attached_wrong_original": func(v *HostingDatabaseNetworkRetirementProof) {
			v.Attached.DatabasePlan.Hash = "sha256:" + strings.Repeat("0", 64)
		},
	}
	for _, engine := range []string{"postgresql", "redis"} {
		for name, change := range changes {
			t.Run(engine+"/"+name, func(t *testing.T) {
				baseline, healthy, in := retirementFixture(t, engine, false)
				member := baseline.Database.Network.Members[0]
				in.MembershipRevision++
				proof := retirementProof(in.Members[0], in.MembershipRevision)
				if ValidateHostingDatabaseNetworkRetirement(proof, member, in.MembershipRevision-1, in.MembershipRevision) != nil {
					t.Fatal("valid retirement fixture rejected")
				}
				change(&proof)
				proof.RetireIntent.Hash = proof.RetireIntent.DocumentHash()
				proof.Detached.Hash = proof.Detached.DocumentHash()
				if ValidateHostingDatabaseNetworkRetirement(proof, member, in.MembershipRevision-1, in.MembershipRevision) == nil {
					t.Fatal("altered or unsettled retirement proof accepted")
				}
				in.Members = nil
				in.Retired = []HostingDatabaseNetworkRetirementProof{proof}
				if _, err := retirementCompile(baseline, healthy, in); err == nil {
					t.Fatal("compiler accepted altered retirement evidence")
				}
			})
		}
	}
}

func TestDatabaseNetworkRetirementExactRemovedSet(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			baseline, healthy, in := retirementFixture(t, engine, true)
			a, b := in.Members[0], in.Members[1]
			in.MembershipRevision++
			ra, rb := retirementProof(a, in.MembershipRevision), retirementProof(b, in.MembershipRevision)
			in.Members = nil
			in.Retired = []HostingDatabaseNetworkRetirementProof{ra, rb}
			p, err := retirementCompile(baseline, healthy, in)
			if err != nil || ValidateHostingDatabasePlanTransition(p, baseline, in) != nil {
				t.Fatal("complete two-member retirement failed", err)
			}
			slices.Reverse(in.Retired)
			q, err := retirementCompile(baseline, healthy, in)
			if err != nil || p.Hash != q.Hash {
				t.Fatal("retirement proof order changed the canonical plan", err)
			}
			for name, change := range map[string]func(*HostingDatabaseNetworkPlanInput){
				"missing":       func(v *HostingDatabaseNetworkPlanInput) { v.Retired = v.Retired[:1] },
				"duplicate":     func(v *HostingDatabaseNetworkPlanInput) { v.Retired[1] = v.Retired[0] },
				"extraneous":    func(v *HostingDatabaseNetworkPlanInput) { v.Retired = append(v.Retired, v.Retired[0]) },
				"still_present": func(v *HostingDatabaseNetworkPlanInput) { v.Members = []HostingDatabaseNetworkMemberProof{a, b} },
				"no_advance":    func(v *HostingDatabaseNetworkPlanInput) { v.MembershipRevision-- },
				"other_member": func(v *HostingDatabaseNetworkPlanInput) {
					v.Retired[0] = retirementProof(retirementMember(a, "foreign", "8", "9", 7), v.MembershipRevision)
				},
				"shared_retire_receipt": func(v *HostingDatabaseNetworkPlanInput) {
					v.Retired[1].Detached.ReceiptID = v.Retired[0].Detached.ReceiptID
					v.Retired[1].Detached.Hash = v.Retired[1].Detached.DocumentHash()
				},
				"receipt_reuses_other_attached": func(v *HostingDatabaseNetworkPlanInput) {
					v.Retired[0].Detached.ReceiptID = v.Retired[1].Attached.Receipt.ReceiptID
					v.Retired[0].Detached.Hash = v.Retired[0].Detached.DocumentHash()
				},
				"same_endpoint_new_receipt": func(v *HostingDatabaseNetworkPlanInput) {
					changed := a
					changed.Receipt.ReceiptID = "different_attached_receipt"
					changed.Receipt.Hash = changed.Receipt.DocumentHash()
					v.Members = []HostingDatabaseNetworkMemberProof{changed}
				},
			} {
				t.Run(name, func(t *testing.T) {
					value := in
					value.Retired = slices.Clone(in.Retired)
					change(&value)
					if _, err := retirementCompile(baseline, healthy, value); err == nil || ValidateHostingDatabasePlanTransition(p, baseline, value) == nil {
						t.Fatal("compiler or validator accepted an inexact removed member set")
					}
				})
			}
			// The transient evidence must not alias any returned persisted state.
			saved := p.Hash
			in.Retired[0].Attached.Binding.EnvironmentNames[0] = "changed"
			in.Retired[0].Detached.Observation.Present = true
			p.Hash = ""
			if HostingDocumentHash(p) != saved {
				t.Fatal("output aliases transient retirement proof")
			}
			if reflect.DeepEqual(p.Database.Network.Members, baseline.Database.Network.Members) {
				t.Fatal("retired members remained in compiled output")
			}
		})
	}
}

func TestDatabaseNetworkRetirementExactMemberAndRevisionBounds(t *testing.T) {
	baseline, _, in := retirementFixture(t, "postgresql", false)
	member := baseline.Database.Network.Members[0]
	proof := retirementProof(in.Members[0], in.MembershipRevision+2)
	for _, revisions := range [][2]int64{{0, 9}, {7, 7}, {7, 8}, {9, 10}, {10, 11}} {
		if ValidateHostingDatabaseNetworkRetirement(proof, member, revisions[0], revisions[1]) == nil {
			t.Fatalf("retirement outside baseline/result revision bounds accepted: %v", revisions)
		}
	}
	if ValidateHostingDatabaseNetworkRetirement(proof, member, 8, 10) != nil {
		t.Fatal("retirement newer than attachment and baseline but within result was rejected")
	}
	for _, change := range []func(*HostingDatabaseNetworkMember){
		func(v *HostingDatabaseNetworkMember) { v.BindingID = "foreign" },
		func(v *HostingDatabaseNetworkMember) { v.BindingHash = "sha256:" + strings.Repeat("0", 64) },
		func(v *HostingDatabaseNetworkMember) { v.EndpointPlanHash = "sha256:" + strings.Repeat("0", 64) },
		func(v *HostingDatabaseNetworkMember) { v.ReceiptID = "foreign" },
		func(v *HostingDatabaseNetworkMember) { v.ReceiptHash = "sha256:" + strings.Repeat("0", 64) },
		func(v *HostingDatabaseNetworkMember) { v.Endpoint.EndpointID = strings.Repeat("0", 64) },
	} {
		changed := member
		change(&changed)
		if ValidateHostingDatabaseNetworkRetirement(proof, changed, 8, 10) == nil {
			t.Fatal("retirement projection did not equal the exact original baseline member")
		}
	}
}

func TestRetirementRejectsForeignOriginalDatabaseContext(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, compiler := range []string{"database", "application"} {
			t.Run(engine+"/"+compiler, func(t *testing.T) {
				baseline, healthy, network := retirementFixture(t, engine, false)
				app := retirementApplicationInput(baseline, healthy, network)
				retired := retirementProof(network.Members[0], network.MembershipRevision+1)
				network.MembershipRevision++
				network.Members, network.Retired = nil, []HostingDatabaseNetworkRetirementProof{retired}
				app.MembershipRevision, app.Members, app.Retired = network.MembershipRevision, nil, slices.Clone(network.Retired)
				// Compile valid documents first, so contextual validation must reject
				// the changed transient proof without depending on a stale plan hash.
				dbPlan, err := retirementCompile(baseline, healthy, network)
				if err != nil {
					t.Fatal("valid database retirement rejected", err)
				}
				s, r := app.BindingApplicationSnapshot, retired.Attached.ApplicationHealthy.Release
				s.PolicyVersion, s.HostPort = HostingApplicationDatabasePolicyVersion, 21003
				s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: app.Binding.ID, BindingHash: app.Binding.Hash}
				appPlan, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, app)
				if err != nil {
					t.Fatal("valid application retirement rejected", err)
				}
				original := retired.Attached.DatabasePlan
				storage := *original.Storage
				storage.CreationOperationID = "foreign_database_creation"
				storage.CreationNonce = HostingStorageCreationNonce(storage.Reference, storage.CreationOperationID, storage.DaemonID)
				foreign, err := CompileHostingDatabasePlan(original.Snapshot, original.Release, nil, storage, *original.Database)
				if err != nil || ValidateHostingDatabasePlan(foreign) != nil {
					t.Fatal("substitution must be a locally valid original v1 plan", err)
				}
				retired.Attached.DatabasePlan = foreign
				if ValidateHostingDatabaseNetworkRetirement(retired, baseline.Database.Network.Members[0], baseline.Database.Network.MembershipRevision, network.MembershipRevision) != nil {
					t.Fatal("substitution did not isolate the missing contextual database check")
				}
				if compiler == "database" {
					network.Retired[0] = retired
					if _, err := retirementCompile(baseline, healthy, network); err == nil {
						t.Error("database compiler accepted a self-consistent foreign creation origin")
					}
					if ValidateHostingDatabasePlanTransition(dbPlan, baseline, network) == nil {
						t.Error("database validator accepted a foreign original database context")
					}
				} else {
					app.Retired[0] = retired
					if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, app); err == nil {
						t.Error("application compiler accepted a self-consistent foreign creation origin")
					}
					if ValidateHostingApplicationDatabasePlan(appPlan, app) == nil {
						t.Error("application validator accepted a foreign original database context")
					}
				}
			})
		}
	}
}

func TestRetirementRedeployedDatabaseCapturePreservesInitialization(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			initial, healthy, network := databaseNetworkFixture(t, engine)
			s := initial.Snapshot
			s.ResourceGeneration++
			capture, err := CompileHostingDatabasePlan(s, initial.Release, &healthy, *initial.Storage, *initial.Database)
			if err != nil {
				t.Fatal("valid redeployed v1 capture failed", err)
			}
			attached := network.Members[0]
			attached.DatabasePlan = capture
			attached.Binding.DatabaseResourceGeneration = capture.Snapshot.ResourceGeneration
			attached.Binding.Hash = attached.Binding.DocumentHash()
			attached.ApplicationHealthy.Snapshot.HostPort = 21001
			attached.EndpointPlan.BindingHash = attached.Binding.Hash
			attached.EndpointPlan.Hash = attached.EndpointPlan.DocumentHash()
			attached.Receipt.BindingHash, attached.Receipt.PlanHash = attached.Binding.Hash, attached.EndpointPlan.Hash
			attached.Receipt.Hash = attached.Receipt.DocumentHash()
			network.Members[0] = attached
			healthy.Snapshot, healthy.PlanHash, healthy.OperationID = capture.Snapshot, capture.Hash, "database_redeployed"
			baseline := compileDatabaseNetwork(t, capture, healthy, network)
			healthy.Snapshot, healthy.PlanHash, healthy.OperationID = baseline.Snapshot, baseline.Hash, "database_current"
			app := retirementApplicationInput(baseline, healthy, network)
			network.MembershipRevision++
			network.Members = nil
			network.Retired = []HostingDatabaseNetworkRetirementProof{retirementProof(attached, network.MembershipRevision)}
			app.MembershipRevision, app.Members, app.Retired = network.MembershipRevision, nil, slices.Clone(network.Retired)
			if capture.PreviousHealthy == nil || capture.Hash == network.Origin.OriginPlan || baseline.Snapshot.ResourceGeneration <= capture.Snapshot.ResourceGeneration {
				t.Fatal("fixture does not preserve original initialization across advancing generations")
			}
			dbPlan, err := retirementCompile(baseline, healthy, network)
			if err != nil || ValidateHostingDatabasePlanTransition(dbPlan, baseline, network) != nil {
				t.Fatal("legitimate redeployed capture rejected after v2 generation advance", err)
			}
			s, r := app.BindingApplicationSnapshot, attached.ApplicationHealthy.Release
			s.PolicyVersion, s.HostPort = HostingApplicationDatabasePolicyVersion, 21003
			s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: app.Binding.ID, BindingHash: app.Binding.Hash}
			appPlan, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, app)
			if err != nil || ValidateHostingApplicationDatabasePlan(appPlan, app) != nil {
				t.Fatal("application retirement lost original redeployed capture provenance", err)
			}
			// This complete attachment is newer than the initial database plan.
			// Target equality deliberately ignores generations; contextual checking
			// must still reject a capture from the expected context's future.
			member := baseline.Database.Network.Members[0]
			if !databaseNetworkSameTarget(capture, initial) || ValidateHostingDatabaseNetworkRetirement(network.Retired[0], member, baseline.Database.Network.MembershipRevision, network.MembershipRevision) != nil {
				t.Fatal("future-generation test did not reach contextual validation")
			}
			if validateDatabaseNetworkRetirements(initial, network.Origin, []HostingDatabaseNetworkMember{member}, nil, baseline.Database.Network.MembershipRevision, network.MembershipRevision, network.Retired) == nil {
				t.Fatal("retired original capture from a future database generation accepted")
			}
			// Preserve the exact storage creation operation and nonce while forging
			// only its predecessor's retained initialization plan hash.
			predecessor := *capture.PreviousHealthy
			storage := *predecessor.Storage
			storage.CreationPlanHash = "sha256:" + strings.Repeat("0", 64)
			predecessor.Storage = &storage
			foreign, err := CompileHostingDatabasePlan(capture.Snapshot, capture.Release, &predecessor, *capture.Storage, *capture.Database)
			if err != nil || ValidateHostingDatabasePlan(foreign) != nil {
				t.Fatal("foreign retained provenance must be locally canonical", err)
			}
			network.Retired[0].Attached.DatabasePlan = foreign
			app.Retired[0].Attached.DatabasePlan = foreign
			if _, err := retirementCompile(baseline, healthy, network); err == nil || ValidateHostingDatabasePlanTransition(dbPlan, baseline, network) == nil {
				t.Fatal("database retirement accepted unrelated initialization provenance")
			}
			if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, app); err == nil || ValidateHostingApplicationDatabasePlan(appPlan, app) == nil {
				t.Fatal("application retirement accepted unrelated initialization provenance")
			}
		})
	}
}
