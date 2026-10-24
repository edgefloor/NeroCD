package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestApplicationDatabasePlanValidatesPersistedEmptyRetention(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, v2 := range []bool{false, true} {
			name := engine + "/v1"
			if v2 {
				name = engine + "/v2"
			}
			t.Run(name, func(t *testing.T) {
				s, r, in := applicationDatabaseFixture(t, engine, v2)
				previous := in.Members[0].ApplicationHealthy
				p, err := CompileHostingApplicationDatabasePlan(s, r, &previous, nil, in)
				if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil {
					t.Fatal("valid source plan rejected", err)
				}
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				// A persisted empty array has the same canonical document and hash
				// as omitted retention, but decodes to a nonnil empty Go slice.
				wire := bytes.Replace(raw, []byte(`"snapshot":`), []byte(`"retained_releases":[],"snapshot":`), 1)
				var persisted HostingApplyPlan
				if err = json.Unmarshal(wire, &persisted); err != nil {
					t.Fatal(err)
				}
				canonical, err := json.Marshal(persisted)
				if err != nil || persisted.RetainedReleases == nil || !bytes.Equal(raw, canonical) {
					t.Fatal("fixture did not preserve canonical document identity", err)
				}
				if err = ValidateHostingApplicationDatabasePlan(persisted, in); err != nil {
					t.Fatal("persisted equivalent plan rejected", err)
				}
			})
		}
	}
}

func TestApplicationDatabasePlanFinalSerializedSizeBound(t *testing.T) {
	const limit = 48 << 10
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			s, r, in := applicationDatabaseFixture(t, engine, true)
			seed, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(seed)
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []int{limit, limit + 1} {
				// Padding one snapshot string isolates the aggregate plan bound.
				// The ordinary retained plan still fits before the DB descriptor.
				snapshot := s
				snapshot.RuntimeNamespace = strings.Repeat("n", len(s.RuntimeNamespace)+target-len(raw))
				ordinary, err := compileRetainedHostingPlan(snapshot, r, nil, nil, HostingApplicationDatabasePolicyVersion, HostingApplicationDatabaseCompilerVersion)
				if err != nil {
					t.Fatal("ordinary precursor unexpectedly exceeded its bound", err)
				}
				ordinaryRaw, err := json.Marshal(ordinary)
				if err != nil || len(ordinaryRaw) >= limit {
					t.Fatal("fixture did not isolate appended descriptor size", err)
				}
				persisted := seed
				persisted.Snapshot = snapshot
				persisted.Hash = ""
				persisted.Hash = HostingDocumentHash(persisted)
				finalRaw, err := json.Marshal(persisted)
				if err != nil || len(finalRaw) != target {
					t.Fatal("fixture missed exact serialized boundary", err)
				}
				compiled, compileErr := CompileHostingApplicationDatabasePlan(snapshot, r, nil, nil, in)
				validateErr := ValidateHostingApplicationDatabasePlan(persisted, in)
				if target == limit {
					if compileErr != nil || validateErr != nil || compiled.Hash != persisted.Hash {
						t.Fatal("plan at final serialized limit rejected", compileErr, validateErr)
					}
				} else {
					if compileErr == nil || validateErr == nil {
						t.Fatalf("oversized final plan accepted: ordinary=%d final=%d compile_error=%v validation_error=%v", len(ordinaryRaw), len(finalRaw), compileErr, validateErr)
					}
					if !reflect.DeepEqual(compiled, HostingApplyPlan{}) {
						t.Fatal("failed compilation returned a usable oversized plan")
					}
				}
			}
		})
	}
}

func applicationDatabaseFixture(t *testing.T, engine string, v2 bool) (HostingSnapshot, HostingRelease, HostingApplicationDatabasePlanInput) {
	t.Helper()
	database, healthy, network := databaseNetworkFixture(t, engine)
	proof := network.Members[0]
	proof.ApplicationHealthy.Snapshot.HostPort = 21001
	network.Members[0] = proof
	in := HostingApplicationDatabasePlanInput{OperationID: "application_replace", CreateNonce: strings.Repeat("b", 32), Binding: proof.Binding, ApplicationProjectID: "project", DatabaseProjectID: "project", BindingApplicationSnapshot: proof.ApplicationHealthy.Snapshot, BindingDatabasePlan: database, DatabasePlan: database, DatabaseHealthy: healthy, Origin: network.Origin, MembershipRevision: network.MembershipRevision, Members: network.Members}
	if v2 {
		in.DatabasePlan = compileDatabaseNetwork(t, database, healthy, network)
		in.DatabaseHealthy.Snapshot = in.DatabasePlan.Snapshot
		in.DatabaseHealthy.Release = in.DatabasePlan.Release
		in.DatabaseHealthy.PlanHash = in.DatabasePlan.Hash
		in.DatabaseHealthy.OperationID = "database_replace"
	}
	s := in.BindingApplicationSnapshot
	s.PolicyVersion = HostingApplicationDatabasePolicyVersion
	s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: in.Binding.ID, BindingHash: in.Binding.Hash}
	s.HostPort++
	return s, proof.ApplicationHealthy.Release, in
}

func TestApplicationDatabaseCompileAndRoundTrip(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, v2 := range []bool{false, true} {
			name := engine + "/v1"
			if v2 {
				name = engine + "/v2"
			}
			t.Run(name, func(t *testing.T) {
				s, r, in := applicationDatabaseFixture(t, engine, v2)
				previous := in.Members[0].ApplicationHealthy
				p, err := CompileHostingApplicationDatabasePlan(s, r, &previous, nil, in)
				if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil {
					t.Fatal("valid combined plan rejected", err)
				}
				if p.ApplicationDatabase.Binding.ID != s.ApplicationDatabase.BindingID || p.ApplicationDatabase.Binding.Hash != s.ApplicationDatabase.BindingHash || p.ApplicationDatabase.DatabasePlanHash != in.DatabasePlan.Hash || p.ApplicationDatabase.Origin != in.Origin || p.ApplicationDatabase.CreateNonce != in.CreateNonce || p.ApplicationDatabase.OperationID != in.OperationID || !p.RetainPrevious || len(p.ApplicationDatabase.PreviousMembers) != 1 {
					t.Fatal("combined descriptor omitted exact deployment or retained network identity")
				}
				if HostingPlanFamilyMatches(s, p) {
					t.Fatal("unqualified runtime admitted combined plan")
				}
				if _, err := CompileRetainedHostingPlan(s, r, &previous, nil); err == nil {
					t.Fatal("ordinary compiler admitted combined policy")
				}
				raw, err := json.Marshal(p)
				var decoded HostingApplyPlan
				if err != nil || json.Unmarshal(raw, &decoded) != nil || ValidateHostingApplicationDatabasePlan(decoded, in) != nil || !reflect.DeepEqual(p, decoded) {
					t.Fatal("combined plan round trip changed immutable identity", err)
				}
				if strings.Contains(string(raw), `"password":`) || strings.Contains(string(raw), `"lease_id":`) || strings.Contains(string(raw), `"fence":`) {
					t.Fatal("combined document contains plaintext credentials or live lease authority")
				}
				// The candidate remains desired input. Its operation must not occur
				// in previously settled endpoint/healthy records.
				if p.PreviousHealthy.OperationID == in.OperationID || strings.Contains(string(raw), `"container_operation_id":"`+in.OperationID+`"`) {
					t.Fatal("candidate acquired synthetic healthy endpoint evidence")
				}
			})
		}
	}
}

func TestApplicationDatabaseRejectsDatabaseOperationCollision(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, version := range []string{"v1", "v2"} {
			for _, source := range []string{"healthy", "origin"} {
				t.Run(engine+"/"+version+"/"+source, func(t *testing.T) {
					s, r, in := applicationDatabaseFixture(t, engine, version == "v2")
					p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
					if err != nil {
						t.Fatal("valid distinct application operation rejected", err)
					}
					in.OperationID = in.DatabaseHealthy.OperationID
					if source == "origin" {
						in.OperationID = in.Origin.OriginOperation
					}
					if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in); err == nil {
						t.Error("compiler accepted an existing database operation as the application operation")
					}
					// Rehash a previously valid plan so rejection must depend on
					// operation scope, not a stale descriptor or plan hash.
					p.ApplicationDatabase.OperationID = in.OperationID
					p.ApplicationDatabase.Hash = p.ApplicationDatabase.DocumentHash()
					p.Hash = ""
					p.Hash = HostingDocumentHash(p)
					if ValidateHostingApplicationDatabasePlan(p, in) == nil {
						t.Error("validator accepted a self-rehashed database operation collision")
					}
				})
			}
		}
	}
}

func TestApplicationDatabaseAllowsEqualNonceValuesAcrossScopes(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, version := range []string{"v1", "v2"} {
			t.Run(engine+"/"+version, func(t *testing.T) {
				s, r, in := applicationDatabaseFixture(t, engine, version == "v2")
				in.CreateNonce = in.Origin.Nonce
				p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
				if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil {
					t.Fatal("equal nonce values rejected despite distinct application and database operations", err)
				}
				if p.ApplicationDatabase.CreateNonce != p.ApplicationDatabase.Origin.Nonce {
					t.Fatal("compiler changed independently scoped nonce values")
				}
			})
		}
	}
}

func TestApplicationDatabaseBindingExactReference(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			s, _, in := applicationDatabaseFixture(t, engine, false)
			b, proof := in.Binding, in.Members[0]
			h := proof.ApplicationHealthy
			h.Snapshot = s
			if b.ValidateTargets("project", s, "project", in.BindingDatabasePlan) != nil || proof.EndpointPlan.ValidateApplication(b, h) != nil {
				t.Fatal("exact combined ref rejected by binding validators")
			}
			for name, change := range map[string]func(*HostingSnapshot){
				"missing":        func(v *HostingSnapshot) { v.ApplicationDatabase = HostingApplicationDatabaseRef{} },
				"wrong_id":       func(v *HostingSnapshot) { v.ApplicationDatabase.BindingID = "foreign_binding" },
				"wrong_hash":     func(v *HostingSnapshot) { v.ApplicationDatabase.BindingHash = "sha256:" + strings.Repeat("0", 64) },
				"malformed":      func(v *HostingSnapshot) { v.ApplicationDatabase.BindingHash = "bad" },
				"old_policy":     func(v *HostingSnapshot) { v.PolicyVersion = HostingRuntimePolicyVersion },
				"runtime_gen":    func(v *HostingSnapshot) { v.RuntimeConfiguration.Generation++ },
				"runtime_hash":   func(v *HostingSnapshot) { v.RuntimeConfiguration.Hash = "sha256:" + strings.Repeat("0", 64) },
				"destination":    func(v *HostingSnapshot) { v.DestinationID = "foreign" },
				"server":         func(v *HostingSnapshot) { v.ServerID = "foreign" },
				"runner":         func(v *HostingSnapshot) { v.RunnerID = "foreign" },
				"app_generation": func(v *HostingSnapshot) { v.ResourceGeneration++ },
			} {
				t.Run(name, func(t *testing.T) {
					bad := s
					change(&bad)
					h.Snapshot = bad
					if b.ValidateTargets("project", bad, "project", in.BindingDatabasePlan) == nil || proof.EndpointPlan.ValidateApplication(b, h) == nil {
						t.Fatal("binding validator admitted mismatched combined ref or target")
					}
				})
			}
			if b.ValidateTargets("project", in.BindingApplicationSnapshot, "project", in.BindingDatabasePlan) != nil {
				t.Fatal("explicit original ordinary runtime capture stopped validating")
			}
		})
	}
}

func TestApplicationDatabaseRejectsFamilySubstitution(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, family := range []string{"storage", "local_build", "route_continuity", "database", "local_build_release", "ordinary_policy", "missing_runtime", "wrong_compiler"} {
			t.Run(engine+"/"+family, func(t *testing.T) {
				s, r, in := applicationDatabaseFixture(t, engine, false)
				switch family {
				case "storage":
					s.Storage = in.DatabasePlan.Snapshot.Storage
				case "local_build":
					s.LocalBuild.SourceGeneration = 1
				case "route_continuity":
					s.StorageRouteContinuity.Hash = "sha256:" + strings.Repeat("1", 64)
				case "database":
					s.Database = in.Binding.Database
				case "local_build_release":
					var err error
					r, err = NewHostingLocalBuildRelease(localBuildArtifactFixture())
					if err != nil {
						t.Fatal(err)
					}
				case "ordinary_policy":
					s.PolicyVersion = HostingRuntimePolicyVersion
				case "missing_runtime":
					s.RuntimeConfiguration = HostingRuntimeConfigurationRef{}
				case "wrong_compiler":
					p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
					if err != nil {
						t.Fatal(err)
					}
					p.CompilerVersion = HostingRuntimeCompilerVersion
					p.Hash = ""
					p.Hash = HostingDocumentHash(p)
					if ValidateHostingApplicationDatabasePlan(p, in) == nil {
						t.Fatal("mixed compiler accepted")
					}
					return
				}
				if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in); err == nil {
					t.Fatal("combined compiler accepted out-of-scope family")
				}
			})
		}
	}
}

func TestApplicationDatabaseRejectsIncompleteOrForeignProof(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for name, change := range map[string]func(*HostingApplicationDatabasePlanInput){
			"implicit_capture": func(v *HostingApplicationDatabasePlanInput) {
				v.BindingApplicationSnapshot.PolicyVersion = HostingApplicationDatabasePolicyVersion
				v.BindingApplicationSnapshot.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: v.Binding.ID, BindingHash: v.Binding.Hash}
			},
			"app_project":         func(v *HostingApplicationDatabasePlanInput) { v.ApplicationProjectID = "foreign" },
			"database_project":    func(v *HostingApplicationDatabasePlanInput) { v.DatabaseProjectID = "foreign" },
			"missing_capture":     func(v *HostingApplicationDatabasePlanInput) { v.BindingApplicationSnapshot = HostingSnapshot{} },
			"missing_original_db": func(v *HostingApplicationDatabasePlanInput) { v.BindingDatabasePlan = HostingApplyPlan{} },
			"bad_db_hash": func(v *HostingApplicationDatabasePlanInput) {
				v.DatabasePlan.Hash = "sha256:" + strings.Repeat("0", 64)
			},
			"db_unsettled": func(v *HostingApplicationDatabasePlanInput) { v.DatabaseHealthy = HostingHealthy{} },
			"db_wrong_plan": func(v *HostingApplicationDatabasePlanInput) {
				v.DatabaseHealthy.PlanHash = "sha256:" + strings.Repeat("0", 64)
			},
			"db_storage_origin": func(v *HostingApplicationDatabasePlanInput) {
				v.DatabaseHealthy.Storage.CreationPlanHash = "sha256:" + strings.Repeat("0", 64)
			},
			"not_initialized": func(v *HostingApplicationDatabasePlanInput) { v.Origin.InitReady = false },
			"wrong_network":   func(v *HostingApplicationDatabasePlanInput) { v.Origin.NetworkID = strings.Repeat("0", 64) },
			"wrong_origin": func(v *HostingApplicationDatabasePlanInput) {
				v.Origin.OriginPlan = "sha256:" + strings.Repeat("0", 64)
			},
			"origin_nonce":  func(v *HostingApplicationDatabasePlanInput) { v.Origin.Nonce = "bad" },
			"origin_daemon": func(v *HostingApplicationDatabasePlanInput) { v.Origin.Daemon = "foreign" },
			"app_nonce":     func(v *HostingApplicationDatabasePlanInput) { v.CreateNonce = strings.Repeat("A", 32) },
			"app_operation": func(v *HostingApplicationDatabasePlanInput) { v.OperationID = "/foreign" },
			"synthetic_candidate": func(v *HostingApplicationDatabasePlanInput) {
				v.OperationID = v.Members[0].ApplicationHealthy.OperationID
			},
			"revision_zero":  func(v *HostingApplicationDatabasePlanInput) { v.MembershipRevision = 0 },
			"stale_revision": func(v *HostingApplicationDatabasePlanInput) { v.MembershipRevision-- },
			"intent_only": func(v *HostingApplicationDatabasePlanInput) {
				v.Members[0].Receipt = databaseBindingReceipt(v.Members[0].EndpointPlan, "attach_intent")
			},
			"uncertain": func(v *HostingApplicationDatabasePlanInput) {
				v.Members[0].Receipt = databaseBindingReceipt(v.Members[0].EndpointPlan, "uncertain")
			},
			"cross_project":    func(v *HostingApplicationDatabasePlanInput) { v.Members[0].ApplicationProjectID = "foreign" },
			"duplicate_member": func(v *HostingApplicationDatabasePlanInput) { v.Members = append(v.Members, v.Members[0]) },
			"wrong_binding": func(v *HostingApplicationDatabasePlanInput) {
				v.Members[0].Binding.Generation++
				v.Members[0].Binding.Hash = v.Members[0].Binding.DocumentHash()
			},
		} {
			t.Run(name+map[bool]string{false: "/v1", true: "/v2"}[v2], func(t *testing.T) {
				s, r, in := applicationDatabaseFixture(t, "postgresql", v2)
				change(&in)
				if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in); err == nil {
					t.Fatal("incomplete, unsettled or foreign historical proof accepted")
				}
			})
		}
	}
}

func TestApplicationDatabaseTamperAndOwnedCopies(t *testing.T) {
	s, r, in := applicationDatabaseFixture(t, "postgresql", true)
	previous := in.Members[0].ApplicationHealthy
	p, err := CompileHostingApplicationDatabasePlan(s, r, &previous, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*HostingApplyPlan){
		"binding": func(v *HostingApplyPlan) { v.ApplicationDatabase.Binding.Generation++ },
		"database_plan": func(v *HostingApplyPlan) {
			v.ApplicationDatabase.DatabasePlanHash = "sha256:" + strings.Repeat("0", 64)
		},
		"network_origin":      func(v *HostingApplyPlan) { v.ApplicationDatabase.Origin.Nonce = strings.Repeat("c", 32) },
		"create_nonce":        func(v *HostingApplyPlan) { v.ApplicationDatabase.CreateNonce = strings.Repeat("c", 32) },
		"operation":           func(v *HostingApplyPlan) { v.ApplicationDatabase.OperationID = "foreign" },
		"membership_revision": func(v *HostingApplyPlan) { v.ApplicationDatabase.MembershipRevision++ },
		"previous_members":    func(v *HostingApplyPlan) { v.ApplicationDatabase.PreviousMembers = nil },
		"retain_previous":     func(v *HostingApplyPlan) { v.RetainPrevious = false },
		"previous_port":       func(v *HostingApplyPlan) { v.PreviousHealthy.Snapshot.HostPort++ },
		"previous_namespace":  func(v *HostingApplyPlan) { v.PreviousHealthy.Snapshot.RuntimeNamespace = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(p)
			var bad HostingApplyPlan
			if err != nil || json.Unmarshal(raw, &bad) != nil {
				t.Fatal("invalid fixture", err)
			}
			change(&bad)
			bad.ApplicationDatabase.Binding.Hash = bad.ApplicationDatabase.Binding.DocumentHash()
			bad.ApplicationDatabase.Hash = bad.ApplicationDatabase.DocumentHash()
			bad.Hash = ""
			bad.Hash = HostingDocumentHash(bad)
			if ValidateHostingApplicationDatabasePlan(bad, in) == nil {
				t.Fatal("self-rehashed substituted combined plan accepted")
			}
		})
	}
	hash := p.Hash
	in.Binding.EnvironmentNames[0] = "foreign"
	in.Members[0].Receipt.Observation.Present = false
	in.Members[0].ApplicationHealthy.ContainerID = "foreign"
	previous.ContainerID = "foreign"
	in.DatabaseHealthy.Storage.CreationPlanHash = "foreign"
	if p.ApplicationDatabase.Binding.EnvironmentNames[0] == "foreign" || p.PreviousHealthy.ContainerID == "foreign" || p.ApplicationDatabase.PreviousMembers[0].Endpoint.Target.ContainerID == "foreign" {
		t.Fatal("compiled plan aliases caller-owned mutable input")
	}
	p.Hash = ""
	if HostingDocumentHash(p) != hash {
		t.Fatal("input mutation changed compiled hash")
	}
}

func TestApplicationDatabaseLegacyPlanIsolation(t *testing.T) {
	s, r, in := applicationDatabaseFixture(t, "postgresql", true)
	combined, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	for name, original := range map[string]HostingApplyPlan{"database_v1": in.BindingDatabasePlan, "database_v2": in.DatabasePlan} {
		t.Run(name, func(t *testing.T) {
			for _, rehash := range []bool{false, true} {
				p := original
				p.ApplicationDatabase = combined.ApplicationDatabase
				if rehash {
					p.Hash = ""
					p.Hash = HostingDocumentHash(p)
				}
				if ValidateHostingDatabasePlan(p) == nil || ValidateHostingDatabasePlanV2(p) == nil || HostingPlanFamilyMatches(p.Snapshot, p) {
					t.Fatal("old database plan accepted combined descriptor")
				}
				p = original
				p.Snapshot.ApplicationDatabase = s.ApplicationDatabase
				if rehash {
					p.Hash = ""
					p.Hash = HostingDocumentHash(p)
				}
				if ValidateHostingDatabasePlan(p) == nil || ValidateHostingDatabasePlanV2(p) == nil || ValidateHostingDatabaseSnapshot(p.Snapshot) == nil {
					t.Fatal("old database snapshot accepted combined reference")
				}
			}
		})
	}
	ordinary := in.BindingApplicationSnapshot
	ordinary.ApplicationDatabase = s.ApplicationDatabase
	if ValidateHostingRuntimeConfigurationRef(ordinary) == nil {
		t.Fatal("old runtime policy accepted nonzero combined reference")
	}
	ordinary.ApplicationDatabase = HostingApplicationDatabaseRef{}
	p, err := CompileRetainedHostingPlan(ordinary, r, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.ApplicationDatabase = combined.ApplicationDatabase
	if HostingPlanFamilyMatches(ordinary, p) {
		t.Fatal("generic validator ignored new descriptor on old family")
	}
	previous := in.Members[0].ApplicationHealthy
	previous.Snapshot = s
	if _, err := CompileRetainedHostingPlan(ordinary, r, &previous, nil); err == nil {
		t.Fatal("ordinary family retained a combined predecessor")
	}
	if _, err := CompileHostingCleanupPlan(previous, "cleanup"); err == nil {
		t.Fatal("ordinary cleanup compiler admitted the unqualified combined family")
	}
}

func TestApplicationDatabaseMembershipAndRetention(t *testing.T) {
	s, r, in := applicationDatabaseFixture(t, "postgresql", true)
	// A new, separately settled retained endpoint may extend the current v2 set.
	second := in.Members[0]
	second.ApplicationHealthy.OperationID = "application_older"
	second.ApplicationHealthy.ContainerID = strings.Repeat("0", 64)
	second.ApplicationHealthy.Snapshot.HostPort = 21003
	second.EndpointPlan.OperationID = "binding_attach_older"
	second.EndpointPlan.Target.ContainerID = second.ApplicationHealthy.ContainerID
	second.EndpointPlan.Target.ContainerOperationID = second.ApplicationHealthy.OperationID
	second.EndpointPlan.MembershipRevision++
	second.EndpointPlan.Hash = second.EndpointPlan.DocumentHash()
	second.Receipt = databaseBindingReceipt(second.EndpointPlan, "attached")
	second.Receipt.ReceiptID = "receipt_older"
	second.Receipt.Observation.Endpoint.EndpointID = strings.Repeat("3", 64)
	second.Receipt.Hash = second.Receipt.DocumentHash()
	in.Members = append(in.Members, second)
	previous := in.Members[0].ApplicationHealthy
	retained := []HostingHealthy{second.ApplicationHealthy}
	if _, err := CompileHostingApplicationDatabasePlan(s, r, &previous, retained, in); err == nil {
		t.Fatal("membership addition accepted without a higher revision")
	}
	in.MembershipRevision++
	p, err := CompileHostingApplicationDatabasePlan(s, r, &previous, retained, in)
	if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil {
		t.Fatal("fully proven membership addition rejected", err)
	}
	if p.ApplicationDatabase.PreviousMembers[0].Endpoint.Target.ContainerID != second.ApplicationHealthy.ContainerID {
		t.Fatal("members were not ordered canonically")
	}
	reversed := in
	reversed.Members = []HostingDatabaseNetworkMemberProof{second, in.Members[0]}
	p2, err := CompileHostingApplicationDatabasePlan(s, r, &previous, retained, reversed)
	if err != nil || p2.Hash != p.Hash {
		t.Fatal("member proof order changed canonical plan", err)
	}
	retained[0].ContainerID = "foreign"
	in.Members[1].EndpointPlan.Target.ContainerID = "foreign"
	if p.RetainedReleases[0].ContainerID == "foreign" || p.ApplicationDatabase.PreviousMembers[0].Endpoint.Target.ContainerID == "foreign" {
		t.Fatal("compiled retention or members alias input slices")
	}

	for name, change := range map[string]func(*HostingApplicationDatabasePlanInput){
		"omitted_at_current_revision": func(v *HostingApplicationDatabasePlanInput) { v.Members = nil },
		"omitted_at_new_revision": func(v *HostingApplicationDatabasePlanInput) {
			v.Members = nil
			v.MembershipRevision++
		},
		"unchanged_set_new_revision": func(v *HostingApplicationDatabasePlanInput) { v.MembershipRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, in := applicationDatabaseFixture(t, "postgresql", true)
			change(&in)
			if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in); err == nil {
				t.Fatal("retained set or expected revision changed without proof")
			}
		})
	}

	s, r, in = applicationDatabaseFixture(t, "redis", false)
	in.Members = nil
	p, err = CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
	if err != nil || p.PreviousHealthy != nil || len(p.ApplicationDatabase.PreviousMembers) != 0 {
		t.Fatal("initial stopped candidate required invented healthy evidence", err)
	}
	missing := previous
	if _, err := CompileHostingApplicationDatabasePlan(s, r, &missing, nil, in); err == nil {
		t.Fatal("previous healthy metadata accepted without settled membership proof")
	}
}

func retirementApplicationInput(baseline HostingApplyPlan, healthy HostingHealthy, in HostingDatabaseNetworkPlanInput) HostingApplicationDatabasePlanInput {
	attached := in.Members[0]
	return HostingApplicationDatabasePlanInput{OperationID: "candidate_b", CreateNonce: strings.Repeat("b", 32), Binding: attached.Binding, ApplicationProjectID: attached.ApplicationProjectID, DatabaseProjectID: attached.DatabaseProjectID, BindingApplicationSnapshot: attached.ApplicationHealthy.Snapshot, BindingDatabasePlan: attached.DatabasePlan, DatabasePlan: baseline, DatabaseHealthy: healthy, Origin: in.Origin, MembershipRevision: in.MembershipRevision, Members: slices.Clone(in.Members)}
}

func TestApplicationDatabaseRepeatedReplacementWithRetirement(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			baseline, healthy, network := retirementFixture(t, engine, false)
			a := network.Members[0]
			in := retirementApplicationInput(baseline, healthy, network)
			s, r := in.BindingApplicationSnapshot, a.ApplicationHealthy.Release
			s.PolicyVersion = HostingApplicationDatabasePolicyVersion
			s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: in.Binding.ID, BindingHash: in.Binding.Hash}
			s.HostPort = 21002
			planB, err := CompileHostingApplicationDatabasePlan(s, r, &a.ApplicationHealthy, nil, in)
			if err != nil || ValidateHostingApplicationDatabasePlan(planB, in) != nil {
				t.Fatal("A to B desired deployment failed", err)
			}
			// Supply separately settled B evidence only after compiling its desired
			// plan. Neither compiler creates this observed healthy/attached record.
			b := retirementMember(a, "b", "4", "5", in.MembershipRevision+1)
			b.ApplicationHealthy.Snapshot, b.ApplicationHealthy.Release = planB.Snapshot, planB.Release
			b.ApplicationHealthy.OperationID, b.ApplicationHealthy.PlanHash = in.OperationID, planB.Hash
			b.EndpointPlan.OperationID = in.OperationID
			b.EndpointPlan.Target.ContainerOperationID = b.ApplicationHealthy.OperationID
			b.EndpointPlan.Target.ContainerPlanHash = planB.Hash
			b.EndpointPlan.Hash = b.EndpointPlan.DocumentHash()
			b.Receipt.Observation.Endpoint.Target = b.EndpointPlan.Target
			b.Receipt.OperationID = b.EndpointPlan.OperationID
			b.Receipt.PlanHash, b.Receipt.Hash = b.EndpointPlan.Hash, ""
			b.Receipt.Hash = b.Receipt.DocumentHash()
			in.MembershipRevision += 2
			retiredA := retirementProof(a, in.MembershipRevision)
			// B attaches itself and retires A within its own live operation.
			// C's later intent is distinct from this now-settled history.
			retiredA.RetirePlan.OperationID = b.ApplicationHealthy.OperationID
			retirementReceipts(&retiredA)
			in.OperationID, in.Members, in.Retired = "candidate_c", []HostingDatabaseNetworkMemberProof{b}, []HostingDatabaseNetworkRetirementProof{retiredA}
			s.HostPort++
			planC, err := CompileHostingApplicationDatabasePlan(s, r, &b.ApplicationHealthy, nil, in)
			if err != nil || ValidateHostingApplicationDatabasePlan(planC, in) != nil || len(planC.ApplicationDatabase.PreviousMembers) != 1 || planC.ApplicationDatabase.DatabasePlanHash != baseline.Hash {
				t.Fatal("B to C against the older database baseline failed", err)
			}
			for name, change := range map[string]func(*HostingApplicationDatabasePlanInput){
				"missing_retirement":   func(v *HostingApplicationDatabasePlanInput) { v.Retired = nil },
				"duplicate_retirement": func(v *HostingApplicationDatabasePlanInput) { v.Retired = append(v.Retired, v.Retired[0]) },
				"no_advance": func(v *HostingApplicationDatabasePlanInput) {
					v.MembershipRevision = baseline.Database.Network.MembershipRevision
				},
				"uncertain": func(v *HostingApplicationDatabasePlanInput) {
					v.Retired[0].Detached = databaseBindingReceipt(v.Retired[0].RetirePlan, "uncertain")
				},
				"extraneous_member": func(v *HostingApplicationDatabasePlanInput) { v.Retired[0] = retirementProof(b, v.MembershipRevision) },
			} {
				t.Run(name, func(t *testing.T) {
					value := in
					value.Retired = slices.Clone(in.Retired)
					change(&value)
					if _, err := CompileHostingApplicationDatabasePlan(s, r, &b.ApplicationHealthy, nil, value); err == nil || ValidateHostingApplicationDatabasePlan(planC, value) == nil {
						t.Fatal("compiler or validator accepted missing or altered app retirement proof")
					}
				})
			}
			// C becomes independently settled, then the database is replaced using
			// the same retained A retirement proof against its still-old baseline.
			c := retirementMember(b, "c", "6", "7", in.MembershipRevision+1)
			c.ApplicationHealthy.Snapshot, c.ApplicationHealthy.Release = planC.Snapshot, planC.Release
			c.ApplicationHealthy.OperationID, c.ApplicationHealthy.PlanHash = in.OperationID, planC.Hash
			c.EndpointPlan.Target.ContainerOperationID = c.ApplicationHealthy.OperationID
			c.EndpointPlan.Target.ContainerPlanHash = planC.Hash
			c.EndpointPlan.Hash = c.EndpointPlan.DocumentHash()
			c.Receipt.Observation.Endpoint.Target = c.EndpointPlan.Target
			c.Receipt.PlanHash, c.Receipt.Hash = c.EndpointPlan.Hash, ""
			c.Receipt.Hash = c.Receipt.DocumentHash()
			network.MembershipRevision = in.MembershipRevision + 2
			network.Members, network.Retired = []HostingDatabaseNetworkMemberProof{c}, []HostingDatabaseNetworkRetirementProof{retiredA}
			nextDB, err := retirementCompile(baseline, healthy, network)
			if err != nil || ValidateHostingDatabasePlanTransition(nextDB, baseline, network) != nil || nextDB.Database.Network.Members[0].Endpoint.Target.ContainerID != c.ApplicationHealthy.ContainerID {
				t.Fatal("database replacement lost repeated application membership", err)
			}
			// The retirement proof is obsolete against a baseline that already
			// forgot A. Historical replay never substitutes for a different effect.
			healthy.Snapshot, healthy.PlanHash = nextDB.Snapshot, nextDB.Hash
			if _, err := retirementCompile(nextDB, healthy, network); err == nil {
				t.Fatal("retirement proof accepted against a different settled baseline")
			}
		})
	}
}

func TestApplicationDatabaseRetirementToEmpty(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			baseline, healthy, network := retirementFixture(t, engine, true)
			in := retirementApplicationInput(baseline, healthy, network)
			s, r := in.BindingApplicationSnapshot, network.Members[0].ApplicationHealthy.Release
			s.PolicyVersion, s.HostPort = HostingApplicationDatabasePolicyVersion, 21003
			s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: in.Binding.ID, BindingHash: in.Binding.Hash}
			in.MembershipRevision++
			in.Members = nil
			for _, attached := range network.Members {
				in.Retired = append(in.Retired, retirementProof(attached, in.MembershipRevision))
			}
			p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
			if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil || len(p.ApplicationDatabase.PreviousMembers) != 0 || p.PreviousHealthy != nil {
				t.Fatal("retirement to empty required invented candidate health", err)
			}
			slices.Reverse(in.Retired)
			q, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
			if err != nil || p.Hash != q.Hash {
				t.Fatal("retirement proof order changed application plan", err)
			}
			in.Retired[0].Attached.Binding.EnvironmentNames[0] = "foreign"
			if p.ApplicationDatabase.Binding.EnvironmentNames[0] == "foreign" {
				t.Fatal("application plan aliases transient retirement evidence")
			}
		})
	}
}

func TestApplicationDatabaseRejectsHistoricalRetirementOperation(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			baseline, healthy, network := retirementFixture(t, engine, false)
			in := retirementApplicationInput(baseline, healthy, network)
			s, r := in.BindingApplicationSnapshot, network.Members[0].ApplicationHealthy.Release
			s.PolicyVersion, s.HostPort = HostingApplicationDatabasePolicyVersion, 21003
			s.ApplicationDatabase = HostingApplicationDatabaseRef{BindingID: in.Binding.ID, BindingHash: in.Binding.Hash}
			in.MembershipRevision++
			in.Retired = []HostingDatabaseNetworkRetirementProof{retirementProof(in.Members[0], in.MembershipRevision)}
			in.Members = nil
			p, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in)
			if err != nil || ValidateHostingApplicationDatabasePlan(p, in) != nil {
				t.Fatal("valid prospective operation distinct from historical retirement rejected", err)
			}
			in.OperationID = in.Retired[0].RetirePlan.OperationID
			if _, err := CompileHostingApplicationDatabasePlan(s, r, nil, nil, in); err == nil {
				t.Error("compiler accepted settled retirement operation as a new application intent")
			}
			p.ApplicationDatabase.OperationID = in.OperationID
			p.ApplicationDatabase.Hash = p.ApplicationDatabase.DocumentHash()
			p.Hash = ""
			p.Hash = HostingDocumentHash(p)
			if ValidateHostingApplicationDatabasePlan(p, in) == nil {
				t.Error("validator accepted self-rehashed historical retirement operation reuse")
			}
		})
	}
}
