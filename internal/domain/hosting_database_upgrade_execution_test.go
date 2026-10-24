package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func databaseUpgradeFixture(t *testing.T, engine string, maximal bool) (HostingDatabaseUpgradeProof, HostingOperation) {
	t.Helper()
	capture, in := captureDomainFixture(t, engine)
	if maximal {
		s, r := capture.SourcePlan.Snapshot, capture.SourcePlan.Release
		s.ResourceID = "resource_" + strings.Repeat("r", 119)
		s.BindingID = "binding_" + strings.Repeat("b", 120)
		s.DestinationID = "destination_" + strings.Repeat("d", 116)
		s.ServerID = "server_" + strings.Repeat("s", 121)
		s.RunnerID = "runner_" + strings.Repeat("r", 121)
		s.RuntimeNamespace = "ncd_" + strings.Repeat("a", 64)
		r.DaemonID = "daemon_" + strings.Repeat("d", 121)
		r.Hash = ""
		r.Hash = HostingDocumentHash(r)
		storage := capture.Storage
		storage.ID, storage.VolumeName = HostingStorageIdentity(s.BindingID)
		storage.ProjectID = "project_" + strings.Repeat("p", 120)
		storage.ResourceID, storage.BindingID, storage.DestinationID = s.ResourceID, s.BindingID, s.DestinationID
		storage.CreatedBy = "owner_" + strings.Repeat("o", 122)
		storage.Hash = storage.DocumentHash()
		s.Storage = storage.Reference()
		recipe, err := HostingDatabaseRecipeFor(engine, s.Database.RecipeVersion)
		if err != nil {
			t.Fatal(err)
		}
		refs := append([]HostingDatabaseCredentialRef(nil), recipe.Credentials...)
		for i := range refs {
			refs[i].SecretID = HostingDatabaseSecretID(s.ResourceID, refs[i].Role)
			refs[i].Version = 9223372036854775807
		}
		s.Database.ConfigurationHash, err = HostingDatabaseConfigurationHash(s, refs)
		if err != nil {
			t.Fatal(err)
		}
		sp := HostingStoragePlan{Reference: s.Storage, VolumeName: storage.VolumeName, MountPath: storage.MountPath, CreationOperationID: "origin_" + strings.Repeat("a", 121), DaemonID: r.DaemonID}
		sp.CreationNonce = HostingStorageCreationNonce(sp.Reference, sp.CreationOperationID, r.DaemonID)
		db := HostingDatabasePlan{Reference: s.Database, NetworkName: HostingDatabaseNetworkName(s.BindingID), Credentials: refs}
		first, err := CompileHostingDatabasePlan(s, r, nil, sp, db)
		if err != nil {
			t.Fatal(err)
		}
		old := HostingHealthy{Snapshot: s, Release: r, OperationID: sp.CreationOperationID, ContainerID: strings.Repeat("a", 64), PlanHash: first.Hash, Storage: &HostingHealthyStorage{Plan: sp, CreationPlanHash: first.Hash}}
		plan, err := CompileHostingDatabasePlan(s, r, &old, sp, db)
		if err != nil {
			t.Fatal(err)
		}
		healthy := old
		healthy.OperationID = "source_" + strings.Repeat("b", 121)
		healthy.ContainerID = strings.Repeat("b", 64)
		healthy.PlanHash = plan.Hash
		receipt := capture.Creation.Receipt
		receipt.ID = "materialized_" + strings.Repeat("m", 115)
		m := HostingStorageMaterialization{StorageRef: s.Storage, State: "materialized", CreationOperationID: sp.CreationOperationID, CreationPlanHash: first.Hash, CreationNonce: sp.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &receipt}
		m.Labels = ExpectedHostingStorageLabels(storage, m)
		capture = HostingDatabaseNetworkCapturePlan{Version: HostingDatabaseNetworkCaptureVersion, OperationID: HostingDatabaseNetworkCaptureID(healthy.OperationID), SourceOperationID: healthy.OperationID, ProjectID: storage.ProjectID, ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, SourcePlan: plan, SourceHealthy: healthy, Storage: storage, Materialization: m, Creation: HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
		capture.Hash = capture.DocumentHash()
		in.OperationID = capture.OperationID
		in.RunID = "capture_" + capture.OperationID
		in.PlanHash = capture.Hash
		in.Origin.Binding, in.Origin.Destination, in.Origin.Daemon = s.BindingID, s.DestinationID, r.DaemonID
		in.Origin.Configuration, in.Origin.Storage = s.Database.ConfigurationHash, s.Storage
		in.Origin.OriginOperation, in.Origin.OriginPlan = sp.CreationOperationID, first.Hash
		in.Origin.VolumeNonce = sp.CreationNonce
	}
	receipt := HostingDatabaseNetworkOriginReceipt{HostingReceipt: HostingReceipt{ID: "origin_receipt", BodyHash: HostingDocumentHash(in), CreatedAt: in.ObservedAt}, OperationID: capture.OperationID, SourceOperationID: capture.SourceOperationID, PlanHash: capture.Hash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: capture.RunnerID}
	proof := HostingDatabaseUpgradeProof{Version: HostingDatabaseUpgradeVersion, CapturePlan: capture, OriginReceipt: receipt, PreviousPlan: capture.SourcePlan, PreviousHealthy: capture.SourceHealthy}
	proof.Hash = proof.DocumentHash()
	op := HostingOperation{ID: "hop_upgrade", ProjectID: capture.ProjectID, ResourceID: capture.ResourceID, BindingID: capture.BindingID, State: "queued", Snapshot: proof.PreviousPlan.Snapshot, CreatedBy: capture.Storage.CreatedBy, CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.CreatedAt, DatabaseUpgrade: &proof}
	if proof.Validate() != nil || ValidateHostingDatabaseUpgradeOperation(op) != nil {
		t.Fatal("invalid upgrade fixture")
	}
	return proof, op
}

func prepareDatabaseUpgrade(t *testing.T, op HostingOperation) HostingOperation {
	t.Helper()
	p := op.DatabaseUpgrade
	input, err := HostingDatabaseUpgradePlanInput(*p)
	if err != nil {
		t.Fatal(err)
	}
	db := *p.PreviousPlan.Database
	db.Network = nil
	plan, err := CompileHostingDatabasePlanV2(op.Snapshot, p.PreviousPlan.Release, p.PreviousHealthy, p.PreviousPlan, *p.PreviousPlan.Storage, db, input)
	if err != nil {
		t.Fatal(err)
	}
	release := plan.Release
	op.Release, op.Plan, op.State = &release, &plan, "prepared"
	return op
}

func cloneDatabaseUpgrade(t *testing.T, p HostingDatabaseUpgradeProof) HostingDatabaseUpgradeProof {
	t.Helper()
	raw, err := json.Marshal(p)
	var copy HostingDatabaseUpgradeProof
	if err != nil || json.Unmarshal(raw, &copy) != nil {
		t.Fatal("upgrade clone failed")
	}
	return copy
}

func TestDatabaseUpgradeEmptyTransitionAndMaintenance(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			proof, op := databaseUpgradeFixture(t, engine, false)
			prepared := prepareDatabaseUpgrade(t, op)
			if ValidateHostingDatabaseUpgradeOperation(prepared) != nil || prepared.Plan.Database.Network.MembershipRevision != 1 || len(prepared.Plan.Database.Network.Members) != 0 {
				t.Fatal("initial empty upgrade rejected")
			}
			old := proof.PreviousHealthy
			old.OperationID, old.ContainerID = "hop_upgraded", strings.Repeat("e", 64)
			old.PlanHash = prepared.Plan.Hash
			proof.PreviousPlan, proof.PreviousHealthy = *prepared.Plan, old
			// A retained empty v2 predecessor keeps its accepted revision.
			proof.PreviousPlan.Database.Network.MembershipRevision = 7
			rehashDatabaseNetwork(&proof.PreviousPlan)
			proof.PreviousHealthy.PlanHash = proof.PreviousPlan.Hash
			proof.Hash = proof.DocumentHash()
			op.DatabaseUpgrade, op.ID = &proof, "hop_maintenance"
			next := prepareDatabaseUpgrade(t, op)
			if ValidateHostingDatabaseUpgradeOperation(next) != nil || next.Plan.Database.Network.MembershipRevision != 7 || next.Plan.Database.Network.Predecessor.PlanHash != proof.PreviousPlan.Hash || next.Plan.Database.Network.OriginHash != proof.OriginReceipt.OriginHash {
				t.Fatal("empty maintenance lost exact predecessor/origin/revision")
			}
			in, err := HostingDatabaseUpgradePlanInput(proof)
			if err != nil || in.Members == nil || len(in.Members) != 0 || len(in.Retired) != 0 {
				t.Fatal("upgrade acquired membership or retirement proof")
			}
		})
	}
}

func TestDatabaseUpgradeRejectsChangedProofAndPreparedPlan(t *testing.T) {
	original, base := databaseUpgradeFixture(t, "postgresql", false)
	for name, change := range map[string]func(*HostingDatabaseUpgradeProof){
		"capture":    func(p *HostingDatabaseUpgradeProof) { p.CapturePlan.OperationID = "hnc_" + strings.Repeat("f", 32) },
		"receipt":    func(p *HostingDatabaseUpgradeProof) { p.OriginReceipt.PlanHash = "sha256:" + strings.Repeat("f", 64) },
		"origin":     func(p *HostingDatabaseUpgradeProof) { p.OriginReceipt.Origin.NetworkID = strings.Repeat("f", 64) },
		"source":     func(p *HostingDatabaseUpgradeProof) { p.OriginReceipt.SourceOperationID = "foreign" },
		"runner":     func(p *HostingDatabaseUpgradeProof) { p.OriginReceipt.RunnerID = "foreign" },
		"target":     func(p *HostingDatabaseUpgradeProof) { p.PreviousHealthy.Snapshot.DestinationID = "foreign" },
		"healthy":    func(p *HostingDatabaseUpgradeProof) { p.PreviousHealthy.ContainerID = strings.Repeat("f", 64) },
		"plan":       func(p *HostingDatabaseUpgradeProof) { p.PreviousPlan.Hash = "sha256:" + strings.Repeat("f", 64) },
		"secret_ref": func(p *HostingDatabaseUpgradeProof) { p.PreviousPlan.Database.Credentials[0].Version++ },
		"time": func(p *HostingDatabaseUpgradeProof) {
			p.OriginReceipt.ObservedAt = p.OriginReceipt.CreatedAt.Add(time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := cloneDatabaseUpgrade(t, original)
			change(&p)
			p.Hash = p.DocumentHash()
			if p.Validate() == nil {
				t.Fatal("foreign proof accepted")
			}
		})
	}
	for name, change := range map[string]func(*HostingOperation){
		"generation": func(op *HostingOperation) { op.Snapshot.ResourceGeneration++ },
		"binding":    func(op *HostingOperation) { op.BindingID = "foreign" },
		"resource":   func(op *HostingOperation) { op.ResourceID = "foreign" },
		"project":    func(op *HostingOperation) { op.ProjectID = "foreign" },
		"release":    func(op *HostingOperation) { op.Release.ImageID = "sha256:" + strings.Repeat("f", 64) },
		"predecessor": func(op *HostingOperation) {
			op.Plan.Database.Network.Predecessor.PlanHash = "sha256:" + strings.Repeat("f", 64)
			rehashDatabaseNetwork(op.Plan)
		},
		"member": func(op *HostingOperation) {
			op.Plan.Database.Network.Members = append(op.Plan.Database.Network.Members, HostingDatabaseNetworkMember{})
			rehashDatabaseNetwork(op.Plan)
		},
		"proof_absent": func(op *HostingOperation) { op.DatabaseUpgrade = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := cloneDatabaseUpgrade(t, original)
			op := base
			op.DatabaseUpgrade = &p
			op = prepareDatabaseUpgrade(t, op)
			change(&op)
			if ValidateHostingDatabaseUpgradeOperation(op) == nil {
				t.Fatal("foreign operation accepted")
			}
		})
	}
}

func TestDatabaseUpgradeClosedJSONAndOrdinaryBytes(t *testing.T) {
	proof, op := databaseUpgradeFixture(t, "postgresql", false)
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	for index, bad := range [][]byte{[]byte("null"), []byte("[]"), append(bytes.Clone(raw), []byte("{}")...), bytes.Replace(raw, []byte(`"version":`), []byte(`"unknown":true,"version":`), 1), bytes.Replace(raw, []byte(`"version":`), []byte(`"version":"duplicate","version":`), 1), bytes.Replace(raw, []byte(`"previous_healthy":`), []byte(`"previous_healthy":null,"discarded":`), 1), append(append([]byte("{"), bytes.Repeat([]byte(" "), HostingDatabaseUpgradeProofJSONLimit)...), raw[1:]...)} {
		var decoded HostingDatabaseUpgradeProof
		if json.Unmarshal(bad, &decoded) == nil {
			t.Fatalf("malformed proof case %d accepted", index)
		}
	}
	input := HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: proof.CapturePlan.OperationID, ExpectedOriginHash: proof.OriginReceipt.OriginHash, IdempotencyKey: "upgrade_1"}
	encoded, err := json.Marshal(input)
	var decoded HostingDatabaseUpgradeInput
	if err != nil || json.Unmarshal(encoded, &decoded) != nil || decoded != input {
		t.Fatal("canonical upgrade input rejected")
	}
	for _, bad := range []string{`null`, `{}`, string(encoded) + `{}`, strings.Replace(string(encoded), `"capture_id":`, `"capture_id":null,"unknown":`, 1), strings.Replace(string(encoded), `"expected_generation":1`, `"expected_generation":1,"expected_generation":2`, 1), strings.Replace(string(encoded), `"idempotency_key":`, `"command":[],"idempotency_key":`, 1), "{" + strings.Repeat(" ", 4097) + string(encoded[1:])} {
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatal("open or malformed upgrade input accepted")
		}
	}
	op.DatabaseUpgrade = nil
	withNil, err := json.Marshal(op)
	if err != nil || bytes.Contains(withNil, []byte(`"database_upgrade"`)) || ValidateHostingDatabaseUpgradeOperation(op) != nil {
		t.Fatal("ordinary operation wire changed")
	}
	// Reconstruct the prior exported wire structure by removing only the new field.
	typ := reflect.TypeOf(op)
	fields := make([]reflect.StructField, 0, typ.NumField()-1)
	for i := 1; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i))
	}
	old := reflect.New(reflect.StructOf(fields)).Elem()
	value := reflect.ValueOf(op)
	for i := range fields {
		old.Field(i).Set(value.Field(i + 1))
	}
	again, err := json.Marshal(old.Interface())
	if err != nil || !bytes.Equal(withNil, again) {
		t.Fatal("ordinary nil sidecar changed serialization")
	}
}

func TestDatabaseUpgradeInputAndCanonicalTimeIdentity(t *testing.T) {
	proof, _ := databaseUpgradeFixture(t, "postgresql", false)
	input := HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: proof.CapturePlan.OperationID, ExpectedOriginHash: proof.OriginReceipt.OriginHash, IdempotencyKey: "upgrade_1"}
	for name, change := range map[string]func(*HostingDatabaseUpgradeInput){
		"generation":  func(in *HostingDatabaseUpgradeInput) { in.ExpectedGeneration = 0 },
		"capture":     func(in *HostingDatabaseUpgradeInput) { in.CaptureID = "hnc_" + strings.Repeat("z", 32) },
		"origin":      func(in *HostingDatabaseUpgradeInput) { in.ExpectedOriginHash = "secret-canary" },
		"idempotency": func(in *HostingDatabaseUpgradeInput) { in.IdempotencyKey = "secret-canary\n" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := input
			change(&bad)
			err := bad.Validate()
			if err != ErrInvalidHostingDatabaseUpgrade || strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("input accepted or error disclosed input")
			}
		})
	}
	copy := proof.OriginReceipt
	copy.CreatedAt = copy.CreatedAt.In(time.FixedZone("local UTC representation", 0))
	copy.ObservedAt = copy.ObservedAt.In(time.FixedZone("local UTC representation", 0))
	if !databaseUpgradeDocumentEqual(copy, proof.OriginReceipt) {
		t.Fatal("equivalent timestamp representations changed document identity")
	}
	copy.ObservedAt = copy.ObservedAt.Add(time.Nanosecond)
	if databaseUpgradeDocumentEqual(copy, proof.OriginReceipt) {
		t.Fatal("changed timestamp preserved document identity")
	}
}

func TestDatabaseUpgradeEnvelopeSizes(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, maximal := range []bool{false, true} {
			proof, op := databaseUpgradeFixture(t, engine, maximal)
			op = prepareDatabaseUpgrade(t, op)
			if maximal {
				previous := proof.PreviousHealthy
				previous.OperationID = "upgraded_" + strings.Repeat("u", 119)
				previous.ContainerID = strings.Repeat("f", 64)
				previous.PlanHash = op.Plan.Hash
				proof.PreviousPlan, proof.PreviousHealthy = *op.Plan, previous
				proof.Hash = proof.DocumentHash()
				op.DatabaseUpgrade = &proof
				op.ID = "maintenance_" + strings.Repeat("m", 116)
				op = prepareDatabaseUpgrade(t, op)
			}
			capture, _ := json.Marshal(proof.CapturePlan)
			document, _ := json.Marshal(proof)
			envelope, _ := json.Marshal(op)
			t.Logf("engine=%s maximal=%t capture=%d proof=%d operation=%d", engine, maximal, len(capture), len(document), len(envelope))
			if len(capture) > HostingDatabaseBindingJSONLimit || len(document) > HostingDatabaseUpgradeProofJSONLimit || len(envelope) > 1<<20 {
				t.Fatal("bounded upgrade envelope exceeded existing transport")
			}
			var decoded HostingDatabaseUpgradeProof
			if json.Unmarshal(document, &decoded) != nil {
				t.Fatal("valid composed proof rejected by decoder bound")
			}
		}
	}
}
