package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func captureDomainFixture(t *testing.T, engine string) (HostingDatabaseNetworkCapturePlan, HostingDatabaseNetworkOriginRequest) {
	t.Helper()
	recipe := HostingDatabasePostgresRecipe
	if engine == "redis" {
		recipe = HostingDatabaseRedisRecipe
	}
	s, r, sp, db := databaseExecutionFixture(t, engine, recipe, 1)
	st := HostingStorage{ID: s.Storage.StorageID, ProjectID: "project", ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: sp.VolumeName, MountPath: sp.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner", CreatedAt: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
	st.Hash = st.DocumentHash()
	s.Storage = st.Reference()
	sp.Reference = s.Storage
	sp.CreationNonce = HostingStorageCreationNonce(s.Storage, sp.CreationOperationID, r.DaemonID)
	s.Database.ConfigurationHash, _ = HostingDatabaseConfigurationHash(s, db.Credentials)
	db.Reference = s.Database
	plan, err := CompileHostingDatabasePlan(s, r, nil, sp, db)
	if err != nil {
		t.Fatal(err)
	}
	h := HostingHealthy{OperationID: sp.CreationOperationID, Snapshot: s, Release: r, PlanHash: plan.Hash, ContainerID: strings.Repeat("a", 64), Storage: &HostingHealthyStorage{Plan: sp, CreationPlanHash: plan.Hash}}
	receipt := HostingReceipt{ID: "materialized", BodyHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: st.CreatedAt}
	m := HostingStorageMaterialization{StorageRef: s.Storage, State: "materialized", CreationOperationID: sp.CreationOperationID, CreationPlanHash: plan.Hash, CreationNonce: sp.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &receipt}
	m.Labels = ExpectedHostingStorageLabels(st, m)
	p := HostingDatabaseNetworkCapturePlan{Version: HostingDatabaseNetworkCaptureVersion, OperationID: HostingDatabaseNetworkCaptureID(h.OperationID), SourceOperationID: h.OperationID, ProjectID: st.ProjectID, ResourceID: s.ResourceID, BindingID: s.BindingID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID, SourcePlan: plan, SourceHealthy: h, Storage: st, Materialization: m, Creation: HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: st.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	in := HostingDatabaseNetworkOriginRequest{HostingAuthority: HostingAuthority{OperationID: p.OperationID, RunID: "capture_" + p.OperationID, LeaseID: "lease_capture", Attempt: 1, Fence: "fence_capture"}, ReceiptID: "captured", PlanHash: p.Hash, ObservedAt: st.CreatedAt, Origin: HostingDatabaseNetworkOrigin{Version: 1, Binding: s.BindingID, Destination: s.DestinationID, Daemon: r.DaemonID, Configuration: s.Database.ConfigurationHash, OriginOperation: m.CreationOperationID, OriginPlan: m.CreationPlanHash, Nonce: strings.Repeat("c", 32), Storage: s.Storage, VolumeNonce: m.CreationNonce, VolumeCreatedAt: st.CreatedAt.Format(time.RFC3339Nano), NetworkID: strings.Repeat("d", 64), InitReady: true}}
	if p.Validate() != nil || in.Validate() != nil || p.ValidateOrigin(in.Origin) != nil {
		t.Fatal("invalid capture fixture")
	}
	return p, in
}

func TestDatabaseNetworkCaptureKnownIdentityAndAttestedFields(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			p, in := captureDomainFixture(t, engine)
			for _, change := range []func(*HostingDatabaseNetworkOrigin){func(o *HostingDatabaseNetworkOrigin) { o.Daemon = "foreign" }, func(o *HostingDatabaseNetworkOrigin) { o.OriginPlan = "sha256:" + strings.Repeat("e", 64) }, func(o *HostingDatabaseNetworkOrigin) { o.Configuration = "sha256:" + strings.Repeat("e", 64) }, func(o *HostingDatabaseNetworkOrigin) { o.VolumeNonce = strings.Repeat("f", 32) }, func(o *HostingDatabaseNetworkOrigin) { o.InitReady = false }, func(o *HostingDatabaseNetworkOrigin) { o.Storage.Generation++ }, func(o *HostingDatabaseNetworkOrigin) { o.NetworkID = "tag" }, func(o *HostingDatabaseNetworkOrigin) { o.VolumeCreatedAt = "" }} {
				o := in.Origin
				change(&o)
				if p.ValidateOrigin(o) == nil {
					t.Fatal("foreign or malformed origin accepted")
				}
			}
			// Previously unknown observations may differ; the controller cannot verify Docker.
			o := in.Origin
			o.NetworkID = strings.Repeat("e", 64)
			o.Nonce = strings.Repeat("f", 32)
			o.VolumeCreatedAt = "2026-10-23T00:00:00Z"
			if p.ValidateOrigin(o) != nil {
				t.Fatal("invented controller-known observation")
			}
			raw, _ := json.Marshal(p)
			var got HostingDatabaseNetworkCapturePlan
			if json.Unmarshal(raw, &got) != nil || got.Hash != p.Hash {
				t.Fatal("plan round trip failed")
			}
			for _, change := range []func(*HostingDatabaseNetworkCapturePlan){
				func(c *HostingDatabaseNetworkCapturePlan) { c.Materialization.DaemonID = "foreign" },
				func(c *HostingDatabaseNetworkCapturePlan) {
					c.Materialization.CreationPlanHash = "sha256:" + strings.Repeat("f", 64)
				},
				func(c *HostingDatabaseNetworkCapturePlan) { c.Creation.Driver = "foreign" },
				func(c *HostingDatabaseNetworkCapturePlan) {
					c.Creation.Receipt.BodyHash = "sha256:" + strings.Repeat("f", 64)
				},
				func(c *HostingDatabaseNetworkCapturePlan) {
					c.Storage.MountPath = "/foreign"
					c.Storage.Hash = c.Storage.DocumentHash()
				},
			} {
				var changed HostingDatabaseNetworkCapturePlan
				if json.Unmarshal(raw, &changed) != nil {
					t.Fatal("capture clone failed")
				}
				change(&changed)
				changed.Hash = changed.DocumentHash()
				if changed.Validate() == nil {
					t.Fatal("self-rehashed source/materialization mismatch accepted")
				}
			}
			p.Creation.Receipt.ID = "different"
			p.Hash = p.DocumentHash()
			if p.Validate() == nil {
				t.Fatal("materialization receipt substitution accepted")
			}
		})
	}
}

func TestDatabaseNetworkCaptureClosedDocumentsAndGenericDenial(t *testing.T) {
	_, in := captureDomainFixture(t, "postgresql")
	raw, _ := json.Marshal(in)
	for _, bad := range []string{"null", string(raw) + "{}", strings.Replace(string(raw), `"receipt_id":"captured"`, `"receipt_id":null`, 1), strings.Replace(string(raw), `"receipt_id":"captured"`, `"receipt_id":"captured","receipt_id":"other"`, 1), strings.Replace(string(raw), `"receipt_id":"captured"`, `"unknown":true,"receipt_id":"captured"`, 1), strings.Replace(string(raw), `"receipt_id":"captured"`, `"receipt_id":"`+strings.Repeat("a", HostingDatabaseBindingJSONLimit)+`"`, 1)} {
		var got HostingDatabaseNetworkOriginRequest
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatal("open/null/duplicate/oversized document accepted")
		}
	}
	spec := RunSpec{Type: RunTypeDatabaseNetworkCapture, HostingOperationID: in.OperationID, HostingMode: "capture", Inputs: map[string]any{}}
	run := TaskRun{ID: in.RunID, RunSpec: spec}
	if ValidateHostingDatabaseNetworkCaptureTask(run) != nil {
		t.Fatal("canonical task denied")
	}
	if !ContainsHostingExecution(RunSpec{Type: RunTypeDatabaseNetworkCapture}, Workflow{}) {
		t.Fatal("bare generic submission bypass")
	}
	if !ContainsHostingExecution(RunSpec{}, Workflow{Steps: []WorkflowStep{{RunSpec: RunSpec{Type: RunTypeDatabaseNetworkCapture}}}}) {
		t.Fatal("nested generic submission bypass")
	}
	run.RunSpec.Process = &ProcessSpec{}
	if ValidateHostingDatabaseNetworkCaptureTask(run) == nil {
		t.Fatal("expanded task accepted")
	}
}

func TestDatabaseNetworkCaptureEquivalentPersistedEvidence(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			p, _ := captureDomainFixture(t, engine)
			p.Creation.Receipt.CreatedAt = p.Creation.Receipt.CreatedAt.In(time.FixedZone("same instant", 0))
			p.Hash = p.DocumentHash()
			if p.Validate() != nil {
				t.Fatal("equivalent receipt instant rejected")
			}
			raw, err := json.Marshal(p)
			var persisted HostingDatabaseNetworkCapturePlan
			if err != nil || json.Unmarshal(raw, &persisted) != nil || persisted.Validate() != nil {
				t.Fatal("persisted equivalent evidence rejected", err)
			}
			persisted.Creation.Receipt.CreatedAt = persisted.Creation.Receipt.CreatedAt.Add(time.Nanosecond)
			persisted.Hash = persisted.DocumentHash()
			if persisted.Validate() == nil {
				t.Fatal("different receipt instant accepted")
			}
		})
	}
}
