package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Fixed on accepted 54a3 with its original v1 implementation, before adding Network.
func TestDatabaseNetworkV1GoldenBytesAndStaging(t *testing.T) {
	for _, fixture := range []struct {
		engine, recipe string
		platform       int
		plan, bytes    string
	}{
		{"postgresql", HostingDatabasePostgresRecipe, 0, "sha256:e9d88a311652db93b452754eed07f28d1447502bc3f4bbce6e68cf8b8360b14e", "a1c3c09755633f6bb5358ff71e752e2e3c76f0af0f274d7f2c97a45653eb34f2"},
		{"postgresql", HostingDatabasePostgresRecipe, 1, "sha256:55cb4564beb3054b3c470697133bbcf8aca4d1a2547d08e7cf4200b0e011599c", "ac596163f62248bcfbea8e341e0801266413c2e69c4afd97577544cbd006404d"},
		{"redis", HostingDatabaseRedisRecipe, 0, "sha256:2f9993b0d67f6d63ede3bfd8fa5d79eda4c3d1d6e4406d48a1ac882963e73e16", "60b37073c4b1c47c7392d5217fd3532cb2ca4596a8b127fba2625cdd2024a6cf"},
		{"redis", HostingDatabaseRedisRecipe, 1, "sha256:ff21d037e08040e6c33b43aabc667dd804812711d52a9ac62e28363133a65ca5", "1ccec27d44eae1ecdf8c3bceac64c96c4a541ba477d99f2c776d497aeea70f7c"},
	} {
		t.Run(fixture.engine+string(rune('0'+fixture.platform)), func(t *testing.T) {
			s, r, storage, db := databaseExecutionFixture(t, fixture.engine, fixture.recipe, fixture.platform)
			p, err := CompileHostingDatabasePlan(s, r, nil, storage, db)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(p)
			if err != nil || p.Hash != fixture.plan || fmt.Sprintf("%x", sha256.Sum256(raw)) != fixture.bytes || bytes.Contains(raw, []byte(`"network":`)) {
				t.Fatal("existing v1 bytes or hash changed", err)
			}
			p.Database.Network = &HostingDatabaseNetworkPlan{}
			p.Hash = ""
			p.Hash = HostingDocumentHash(p)
			if ValidateHostingDatabasePlan(p) == nil || HostingPlanFamilyMatches(s, p) || p.Database.Validate(s, r) == nil {
				t.Fatal("v1 or generic route accepted a network descriptor")
			}
			if _, err := CompileHostingDatabasePlan(s, r, nil, storage, *p.Database); err == nil {
				t.Fatal("v1 compiler accepted v2 descriptor")
			}
		})
	}
}

func databaseNetworkFixture(t *testing.T, engine string) (HostingApplyPlan, HostingHealthy, HostingDatabaseNetworkPlanInput) {
	t.Helper()
	b, app, original, endpoint := databaseBindingFixture(t, engine)
	healthy := HostingHealthy{Snapshot: original.Snapshot, Release: original.Release, OperationID: original.Storage.CreationOperationID, ContainerID: strings.Repeat("2", 64), PlanHash: original.Hash, Storage: &HostingHealthyStorage{Plan: *original.Storage, CreationPlanHash: original.Hash}}
	origin := HostingDatabaseNetworkOrigin{Version: 1, Binding: original.Snapshot.BindingID, Destination: original.Snapshot.DestinationID, Daemon: original.Release.DaemonID, Configuration: original.Snapshot.Database.ConfigurationHash, OriginOperation: original.Storage.CreationOperationID, OriginPlan: original.Hash, Nonce: strings.Repeat("a", 32), Storage: original.Snapshot.Storage, VolumeNonce: original.Storage.CreationNonce, VolumeCreatedAt: "2026-10-24T00:00:00Z", NetworkID: endpoint.Target.NetworkID, InitReady: true}
	proof := HostingDatabaseNetworkMemberProof{Binding: b, EndpointPlan: endpoint, Receipt: databaseBindingReceipt(endpoint, "attached"), ApplicationHealthy: app, DatabasePlan: original, ApplicationProjectID: "project", DatabaseProjectID: "project"}
	return original, healthy, HostingDatabaseNetworkPlanInput{Origin: origin, MembershipRevision: endpoint.MembershipRevision, Members: []HostingDatabaseNetworkMemberProof{proof}}
}

func compileDatabaseNetwork(t *testing.T, previous HostingApplyPlan, healthy HostingHealthy, input HostingDatabaseNetworkPlanInput) HostingApplyPlan {
	t.Helper()
	snapshot := previous.Snapshot
	snapshot.ResourceGeneration++
	db := *previous.Database
	db.Network = nil
	p, err := CompileHostingDatabasePlanV2(snapshot, previous.Release, healthy, previous, *previous.Storage, db, input)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func rehashDatabaseNetwork(p *HostingApplyPlan) {
	p.Database.Network.Hash = p.Database.Network.DocumentHash()
	p.Hash = ""
	p.Hash = HostingDocumentHash(*p)
}

func TestDatabaseNetworkTransitionsAndCopies(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			original, healthy, in := databaseNetworkFixture(t, engine)
			p := compileDatabaseNetwork(t, original, healthy, in)
			if ValidateHostingDatabasePlanV2(p) != nil || ValidateHostingDatabasePlanTransition(p, original, in) != nil || ValidateHostingDatabasePlan(p) == nil || HostingPlanFamilyMatches(p.Snapshot, p) || p.Snapshot.HostPort != 0 {
				t.Fatal("v2 escaped staging or valid contextual plan rejected")
			}
			if p.Database.Network.Predecessor.CompilerVersion != HostingDatabaseCompilerVersion || p.Database.Network.Predecessor.Alias != "" {
				t.Fatal("v1 predecessor acquired an alias or wrong compiler")
			}
			var roundtrip HostingApplyPlan
			raw, err := json.Marshal(p)
			if err != nil || json.Unmarshal(raw, &roundtrip) != nil || ValidateHostingDatabasePlanTransition(roundtrip, original, in) != nil {
				t.Fatal("v2 round trip failed", err)
			}
			nextHealthy := healthy
			nextHealthy.OperationID, nextHealthy.ContainerID = "next_database", strings.Repeat("3", 64)
			nextHealthy.Snapshot, nextHealthy.PlanHash = p.Snapshot, p.Hash
			q := compileDatabaseNetwork(t, p, nextHealthy, in)
			if ValidateHostingDatabasePlanTransition(q, p, in) != nil || q.Database.Network.Predecessor.CompilerVersion != HostingDatabaseNetworkCompilerVersion || q.Database.Network.Predecessor.Alias != HostingDatabaseBindingAlias(q.Snapshot.BindingID) || q.Database.Network.OriginHash != p.Database.Network.OriginHash {
				t.Fatal("v2 original predecessor or stable origin lost")
			}
			in.Members[0].Binding.EnvironmentNames[0] = "mutated"
			healthy.Storage.CreationPlanHash = "mutated"
			if p.PreviousHealthy.Storage.CreationPlanHash != original.Hash || p.Database.Network.Members[0].BindingHash == "mutated" {
				t.Fatal("compiled plan aliases caller proof or healthy storage")
			}
			q.Database.Network.Members[0].ReceiptID = "changed"
			if p.Database.Network.Members[0].ReceiptID == "changed" {
				t.Fatal("successive compiler outputs share members")
			}
		})
	}
}

func TestDatabaseNetworkTwoEndpointsSameBindingAndCanonicalOrder(t *testing.T) {
	original, healthy, in := databaseNetworkFixture(t, "postgresql")
	second := in.Members[0]
	second.ApplicationHealthy.ContainerID = strings.Repeat("4", 64)
	second.ApplicationHealthy.OperationID = "app_replacement"
	second.EndpointPlan.OperationID = "attach_replacement"
	second.EndpointPlan.Target.ContainerID = second.ApplicationHealthy.ContainerID
	second.EndpointPlan.Target.ContainerOperationID = second.ApplicationHealthy.OperationID
	second.EndpointPlan.Hash = second.EndpointPlan.DocumentHash()
	second.Receipt = databaseBindingReceipt(second.EndpointPlan, "attached")
	second.Receipt.ReceiptID = "replacement_receipt"
	second.Receipt.Observation.Endpoint.EndpointID = strings.Repeat("5", 64)
	second.Receipt.Hash = second.Receipt.DocumentHash()
	in.Members = append(in.Members, second)
	p := compileDatabaseNetwork(t, original, healthy, in)
	slices.Reverse(in.Members)
	q := compileDatabaseNetwork(t, original, healthy, in)
	if p.Hash != q.Hash || len(p.Database.Network.Members) != 2 || p.Database.Network.Members[0].BindingID != p.Database.Network.Members[1].BindingID {
		t.Fatal("same binding replacement or canonical caller ordering rejected")
	}
	slices.Reverse(q.Database.Network.Members)
	rehashDatabaseNetwork(&q)
	if ValidateHostingDatabasePlanV2(q) == nil {
		t.Fatal("noncanonical descriptor accepted")
	}
	for name, change := range map[string]func(*HostingDatabaseNetworkPlanInput){
		"third":     func(v *HostingDatabaseNetworkPlanInput) { v.Members = append(v.Members, v.Members[0]) },
		"duplicate": func(v *HostingDatabaseNetworkPlanInput) { v.Members[1] = v.Members[0] },
		"endpoint_duplicate": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[1].Receipt.Observation.Endpoint.EndpointID = v.Members[0].Receipt.Observation.Endpoint.EndpointID
			v.Members[1].Receipt.Hash = v.Members[1].Receipt.DocumentHash()
		},
		"mixed_binding": func(v *HostingDatabaseNetworkPlanInput) {
			b := &v.Members[1].Binding
			b.Generation++
			b.Hash = b.DocumentHash()
			v.Members[1].EndpointPlan.BindingHash = b.Hash
			v.Members[1].EndpointPlan.Hash = v.Members[1].EndpointPlan.DocumentHash()
			v.Members[1].Receipt.BindingHash, v.Members[1].Receipt.PlanHash = b.Hash, v.Members[1].EndpointPlan.Hash
			v.Members[1].Receipt.Hash = v.Members[1].Receipt.DocumentHash()
		},
		"mixed_app_binding": func(v *HostingDatabaseNetworkPlanInput) {
			m := &v.Members[1]
			m.Binding.ApplicationResourceID, m.Binding.ApplicationBindingID = "other_app", "other_app_binding"
			m.ApplicationHealthy.Snapshot.ResourceID, m.ApplicationHealthy.Snapshot.BindingID = "other_app", "other_app_binding"
			m.Binding.ID = HostingDatabaseBindingID(m.Binding.ApplicationBindingID, m.Binding.DatabaseBindingID)
			m.Binding.Hash = m.Binding.DocumentHash()
			m.EndpointPlan.BindingHash = m.Binding.Hash
			m.EndpointPlan.Hash = m.EndpointPlan.DocumentHash()
			m.Receipt.BindingHash, m.Receipt.PlanHash = m.Binding.Hash, m.EndpointPlan.Hash
			m.Receipt.Hash = m.Receipt.DocumentHash()
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, value := databaseNetworkFixture(t, "postgresql")
			value.Members = append(value.Members, second)
			value.Members[1].Receipt.Observation = &HostingDatabaseBindingEndpointObservation{Endpoint: second.Receipt.Observation.Endpoint, Present: true}
			change(&value)
			if _, err := CompileHostingDatabasePlanV2(original.Snapshot, original.Release, healthy, original, *original.Storage, *original.Database, value); err == nil {
				t.Fatal("invalid bounded membership accepted")
			}
		})
	}
}

func TestDatabaseNetworkRejectsIncompleteOrForeignProof(t *testing.T) {
	for name, change := range map[string]func(*HostingDatabaseNetworkPlanInput){
		"intent": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].Receipt = databaseBindingReceipt(v.Members[0].EndpointPlan, "attach_intent")
		},
		"uncertain": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].Receipt = databaseBindingReceipt(v.Members[0].EndpointPlan, "uncertain")
		},
		"detached": func(v *HostingDatabaseNetworkPlanInput) {
			p := &v.Members[0].EndpointPlan
			p.Action, p.ExpectedEndpointID = "retire", strings.Repeat("1", 64)
			p.Hash = p.DocumentHash()
			v.Members[0].Receipt = databaseBindingReceipt(*p, "detached")
		},
		"receipt_hash_only": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].Receipt = HostingDatabaseBindingEndpointReceipt{Hash: v.Members[0].Receipt.Hash}
		},
		"project": func(v *HostingDatabaseNetworkPlanInput) { v.Members[0].ApplicationProjectID = "foreign" },
		"app_storage": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].ApplicationHealthy.Storage = &HostingHealthyStorage{}
		},
		"app_database": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].ApplicationHealthy.Snapshot.Database = v.Members[0].Binding.Database
		},
		"app_config": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].ApplicationHealthy.Snapshot.RuntimeConfiguration.Generation++
		},
		"app_release": func(v *HostingDatabaseNetworkPlanInput) {
			v.Members[0].ApplicationHealthy.Release.ImageID = "sha256:" + strings.Repeat("0", 64)
		},
		"credential": func(v *HostingDatabaseNetworkPlanInput) {
			m := &v.Members[0]
			m.Binding.Credential.Version++
			m.Binding.Hash = m.Binding.DocumentHash()
			m.EndpointPlan.BindingHash = m.Binding.Hash
			m.EndpointPlan.Hash = m.EndpointPlan.DocumentHash()
			m.Receipt.BindingHash, m.Receipt.PlanHash = m.Binding.Hash, m.EndpointPlan.Hash
			m.Receipt.Hash = m.Receipt.DocumentHash()
		},
		"future_original_target": func(v *HostingDatabaseNetworkPlanInput) {
			m := &v.Members[0]
			m.DatabasePlan.Snapshot.ResourceGeneration++
			m.DatabasePlan.Hash = ""
			m.DatabasePlan.Hash = HostingDocumentHash(m.DatabasePlan)
			m.Binding.DatabaseResourceGeneration++
			m.Binding.Hash = m.Binding.DocumentHash()
			m.EndpointPlan.BindingHash = m.Binding.Hash
			m.EndpointPlan.Hash = m.EndpointPlan.DocumentHash()
			m.Receipt.BindingHash, m.Receipt.PlanHash = m.Binding.Hash, m.EndpointPlan.Hash
			m.Receipt.Hash = m.Receipt.DocumentHash()
		},
		"network":          func(v *HostingDatabaseNetworkPlanInput) { v.Origin.NetworkID = strings.Repeat("0", 64) },
		"daemon":           func(v *HostingDatabaseNetworkPlanInput) { v.Origin.Daemon = "foreign" },
		"configuration":    func(v *HostingDatabaseNetworkPlanInput) { v.Origin.Configuration = "sha256:" + strings.Repeat("0", 64) },
		"origin":           func(v *HostingDatabaseNetworkPlanInput) { v.Origin.OriginPlan = "sha256:" + strings.Repeat("0", 64) },
		"origin_operation": func(v *HostingDatabaseNetworkPlanInput) { v.Origin.OriginOperation = "foreign" },
		"volume":           func(v *HostingDatabaseNetworkPlanInput) { v.Origin.VolumeNonce = strings.Repeat("0", 32) },
		"uninitialized":    func(v *HostingDatabaseNetworkPlanInput) { v.Origin.InitReady = false },
		"created":          func(v *HostingDatabaseNetworkPlanInput) { v.Origin.VolumeCreatedAt = "" },
		"nonce":            func(v *HostingDatabaseNetworkPlanInput) { v.Origin.Nonce = "" },
		"revision":         func(v *HostingDatabaseNetworkPlanInput) { v.MembershipRevision = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			original, healthy, in := databaseNetworkFixture(t, "redis")
			change(&in)
			if _, err := CompileHostingDatabasePlanV2(original.Snapshot, original.Release, healthy, original, *original.Storage, *original.Database, in); err == nil {
				t.Fatal("foreign or incomplete proof accepted")
			}
		})
	}
}

func TestDatabaseNetworkRejectsSelfRehashedTransitions(t *testing.T) {
	for name, change := range map[string]func(*HostingApplyPlan){
		"predecessor_hash": func(p *HostingApplyPlan) {
			p.Database.Network.Predecessor.PlanHash = "sha256:" + strings.Repeat("0", 64)
		},
		"predecessor_version": func(p *HostingApplyPlan) { p.Database.Network.Predecessor.CompilerVersion = "foreign" },
		"predecessor_alias":   func(p *HostingApplyPlan) { p.Database.Network.Predecessor.Alias = "foreign" },
		"alias":               func(p *HostingApplyPlan) { p.Database.Network.Alias = "foreign" },
		"origin":              func(p *HostingApplyPlan) { p.Database.Network.OriginHash = "sha256:" + strings.Repeat("0", 64) },
		"network":             func(p *HostingApplyPlan) { p.Database.Network.NetworkID = strings.Repeat("0", 64) },
		"daemon": func(p *HostingApplyPlan) {
			p.Release.DaemonID = "foreign"
			p.Release.Hash = ""
			p.Release.Hash = HostingDocumentHash(p.Release)
		},
		"platform": func(p *HostingApplyPlan) {
			recipe, err := HostingDatabaseRecipeFor(p.Snapshot.Database.Engine, p.Snapshot.Database.RecipeVersion)
			if err != nil {
				t.Fatal(err)
			}
			platform := recipe.Platforms[0]
			p.Release.OS, p.Release.Architecture, p.Release.Variant = platform.OS, platform.Architecture, platform.Variant
			p.Release.CanonicalImage = strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest
			p.Release.Hash = ""
			p.Release.Hash = HostingDocumentHash(p.Release)
		},
		"image": func(p *HostingApplyPlan) {
			p.Release.ImageID = "sha256:" + strings.Repeat("0", 64)
			p.Release.Hash = ""
			p.Release.Hash = HostingDocumentHash(p.Release)
		},
		"snapshot": func(p *HostingApplyPlan) { p.PreviousHealthy.Snapshot.ResourceGeneration++ },
		"healthy_origin": func(p *HostingApplyPlan) {
			p.PreviousHealthy.Storage.CreationPlanHash = "sha256:" + strings.Repeat("0", 64)
		},
		"member_hash": func(p *HostingApplyPlan) {
			p.Database.Network.Members[0].ReceiptHash = "sha256:" + strings.Repeat("0", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			original, healthy, in := databaseNetworkFixture(t, "postgresql")
			p := compileDatabaseNetwork(t, original, healthy, in)
			change(&p)
			rehashDatabaseNetwork(&p)
			if ValidateHostingDatabasePlanTransition(p, original, in) == nil {
				t.Fatal("rehashed foreign transition accepted")
			}
		})
	}
}

func TestDatabaseNetworkEmptyFirstTransitionAndProofCorrespondence(t *testing.T) {
	original, healthy, in := databaseNetworkFixture(t, "postgresql")
	in.Members = nil
	p := compileDatabaseNetwork(t, original, healthy, in)
	if len(p.Database.Network.Members) != 0 || p.Database.Network.Members == nil {
		t.Fatal("empty first transition not canonical")
	}
	if ValidateHostingDatabasePlanTransition(p, original, in) != nil {
		t.Fatal("healthy initialized empty first transition rejected")
	}
	_, _, withMember := databaseNetworkFixture(t, "postgresql")
	if ValidateHostingDatabasePlanTransition(p, original, withMember) == nil {
		t.Fatal("omitted proof membership accepted")
	}
	next := healthy
	next.Snapshot, next.PlanHash = p.Snapshot, p.Hash
	db := *p.Database
	db.Network = nil
	if _, err := CompileHostingDatabasePlanV2(p.Snapshot, p.Release, next, p, *p.Storage, db, in); err != nil {
		t.Fatal("unchanged empty v2 maintenance rejected", err)
	}
	wrong := original
	wrong.Hash = "sha256:" + strings.Repeat("0", 64)
	if ValidateHostingDatabasePlanTransition(p, wrong, in) == nil {
		t.Fatal("unvalidated original predecessor accepted")
	}
}

func TestDatabaseNetworkEmptyV2Maintenance(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			original, healthy, in := databaseNetworkFixture(t, engine)
			in.Members = nil
			first := compileDatabaseNetwork(t, original, healthy, in)
			previous := healthy
			previous.Snapshot, previous.PlanHash, previous.OperationID = first.Snapshot, first.Hash, "empty_database_previous"
			maintained := compileDatabaseNetwork(t, first, previous, in)
			if ValidateHostingDatabasePlanV2(maintained) != nil || ValidateHostingDatabasePlanTransition(maintained, first, in) != nil || maintained.Database.Network.MembershipRevision != first.Database.Network.MembershipRevision || maintained.Database.Network.OriginHash != first.Database.Network.OriginHash || maintained.Database.Network.NetworkID != first.Database.Network.NetworkID || maintained.Database.Network.Alias != first.Database.Network.Alias || len(maintained.Database.Network.Members) != 0 || HostingPlanFamilyMatches(maintained.Snapshot, maintained) {
				t.Fatal("empty maintenance changed identity or escaped v2 staging")
			}
			for name, change := range map[string]func(*HostingDatabaseNetworkPlanInput){
				"revision_advanced": func(v *HostingDatabaseNetworkPlanInput) { v.MembershipRevision++ },
				"revision_lowered":  func(v *HostingDatabaseNetworkPlanInput) { v.MembershipRevision-- },
				"origin":            func(v *HostingDatabaseNetworkPlanInput) { v.Origin.Nonce = strings.Repeat("b", 32) },
				"network":           func(v *HostingDatabaseNetworkPlanInput) { v.Origin.NetworkID = strings.Repeat("0", 64) },
				"uninitialized":     func(v *HostingDatabaseNetworkPlanInput) { v.Origin.InitReady = false },
			} {
				t.Run(name, func(t *testing.T) {
					changed := in
					change(&changed)
					db := *first.Database
					db.Network = nil
					if _, err := CompileHostingDatabasePlanV2(maintained.Snapshot, first.Release, previous, first, *first.Storage, db, changed); err == nil {
						t.Fatal("empty maintenance accepted changed proof identity or revision")
					}
				})
			}
			for _, predecessorAlias := range []bool{false, true} {
				changed := maintained
				db, network := *maintained.Database, *maintained.Database.Network
				changed.Database, db.Network = &db, &network
				if predecessorAlias {
					network.Predecessor.Alias = "foreign"
				} else {
					network.Alias = "foreign"
				}
				rehashDatabaseNetwork(&changed)
				if ValidateHostingDatabasePlanV2(changed) == nil || ValidateHostingDatabasePlanTransition(changed, first, in) == nil {
					t.Fatal("empty maintenance accepted changed alias")
				}
			}
			_, _, memberProof := databaseNetworkFixture(t, engine)
			nonempty := compileDatabaseNetwork(t, original, healthy, memberProof)
			previous.Snapshot, previous.PlanHash = nonempty.Snapshot, nonempty.Hash
			for _, advanceRevision := range []bool{false, true} {
				changed := maintained
				db, network := *maintained.Database, *maintained.Database.Network
				changed.Database, db.Network = &db, &network
				changed.PreviousHealthy = &previous
				network.Predecessor.PlanHash = nonempty.Hash
				emptyProof := in
				if advanceRevision {
					network.MembershipRevision++
					emptyProof.MembershipRevision++
				}
				rehashDatabaseNetwork(&changed)
				if ValidateHostingDatabasePlanV2(changed) != nil {
					t.Fatal("disappearance regression did not reach contextual proof validation")
				}
				if ValidateHostingDatabasePlanTransition(changed, nonempty, emptyProof) == nil {
					t.Fatal("nonempty membership disappeared without retirement proof")
				}
				inputDB := *nonempty.Database
				inputDB.Network = nil
				if _, err := CompileHostingDatabasePlanV2(changed.Snapshot, nonempty.Release, previous, nonempty, *nonempty.Storage, inputDB, emptyProof); err == nil {
					t.Fatal("compiler forgot existing endpoints")
				}
			}
		})
	}
}

func TestDatabaseNetworkV2OriginalProofAndRevision(t *testing.T) {
	original, healthy, in := databaseNetworkFixture(t, "redis")
	p := compileDatabaseNetwork(t, original, healthy, in)
	next := healthy
	next.Snapshot, next.PlanHash, next.OperationID = p.Snapshot, p.Hash, "database_replacement"
	db := *p.Database
	db.Network = nil
	// The full original v1 proof remains valid across v2 predecessors. A v2 plan
	// substituted as a new binding target is deliberately unsupported in this slice.
	wrong := in
	wrong.Members = slices.Clone(in.Members)
	wrong.Members[0].DatabasePlan = p
	if _, err := CompileHostingDatabasePlanV2(p.Snapshot, p.Release, next, p, *p.Storage, db, wrong); err == nil {
		t.Fatal("new binding target silently routed through v1 validation")
	}
	changed := in
	changed.Members = slices.Clone(in.Members)
	changed.Members[0].Receipt.ReceiptID = "fresh_receipt"
	changed.Members[0].Receipt.Hash = changed.Members[0].Receipt.DocumentHash()
	if _, err := CompileHostingDatabasePlanV2(p.Snapshot, p.Release, next, p, *p.Storage, db, changed); err == nil {
		t.Fatal("changed membership accepted without revision advance")
	}
	changed.MembershipRevision++
	q := compileDatabaseNetwork(t, p, next, changed)
	if ValidateHostingDatabasePlanTransition(q, p, changed) != nil {
		t.Fatal("revision advance with full observed proof rejected")
	}
	q.Database.Network.Predecessor.Alias = ""
	rehashDatabaseNetwork(&q)
	if ValidateHostingDatabasePlanTransition(q, p, changed) == nil {
		t.Fatal("v2 predecessor lost its original alias")
	}
	changed.Origin.Nonce = strings.Repeat("b", 32)
	if _, err := CompileHostingDatabasePlanV2(p.Snapshot, p.Release, next, p, *p.Storage, db, changed); err == nil {
		t.Fatal("v2 silently replaced retained network origin")
	}
}

func TestDatabaseNetworkOriginBytesAndClosedDecoding(t *testing.T) {
	original, healthy, in := databaseNetworkFixture(t, "redis")
	raw, err := json.Marshal(in.Origin)
	if err != nil {
		t.Fatal(err)
	}
	// Literal field ordering is the existing v1 retained engine record wire format.
	want := fmtOriginRecord(in.Origin)
	if string(raw) != want || in.Origin.DocumentHash() != HostingDocumentHash(json.RawMessage(want)) {
		t.Fatal("typed origin drifted from retained v1 record bytes")
	}
	p := compileDatabaseNetwork(t, original, healthy, in)
	raw, err = json.Marshal(p.Database.Network)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range [][]byte{bytes.Replace(raw, []byte(`"version":`), []byte(`"foreign":1,"version":`), 1), bytes.Replace(raw, []byte(`"members":[`), []byte(`"members":null,"duplicate":[`), 1), append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"alias":"duplicate"}`)...)} {
		var decoded HostingDatabaseNetworkPlan
		if json.Unmarshal(changed, &decoded) == nil {
			t.Fatal("open/null/duplicate network descriptor decoded")
		}
	}
	var decoded HostingDatabaseNetworkPlan
	if json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded, *p.Database.Network) {
		t.Fatal("closed canonical network roundtrip failed")
	}
}

func fmtOriginRecord(o HostingDatabaseNetworkOrigin) string {
	// This independent wire struct is deliberately the engine's v1 field order.
	raw, err := json.Marshal(struct {
		Version         int               `json:"version"`
		Binding         string            `json:"binding"`
		Destination     string            `json:"destination"`
		Daemon          string            `json:"daemon"`
		Configuration   string            `json:"configuration"`
		OriginOperation string            `json:"origin_operation"`
		OriginPlan      string            `json:"origin_plan"`
		Nonce           string            `json:"nonce"`
		Storage         HostingStorageRef `json:"storage"`
		VolumeNonce     string            `json:"volume_nonce"`
		VolumeCreatedAt string            `json:"volume_created_at"`
		NetworkID       string            `json:"network_id"`
		InitReady       bool              `json:"init_ready"`
	}{o.Version, o.Binding, o.Destination, o.Daemon, o.Configuration, o.OriginOperation, o.OriginPlan, o.Nonce, o.Storage, o.VolumeNonce, o.VolumeCreatedAt, o.NetworkID, o.InitReady})
	if err != nil {
		return ""
	}
	return string(raw)
}
