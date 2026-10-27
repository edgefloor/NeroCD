package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStorageArchiveStrictPublicDocuments(t *testing.T) {
	request := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name, valid string
		out         func() any
	}{
		{"backup", `{"request_id":"` + request + `","expected_resource_generation":1}`, func() any { return new(HostingStorageBackupInput) }},
		{"restore", `{"request_id":"` + request + `","name":"restored uploads"}`, func() any { return new(HostingStorageRestoreInput) }},
		{"recovery", `{"request_id":"` + request + `","expected_plan_hash":"sha256:` + strings.Repeat("b", 64) + `"}`, func() any { return new(HostingStorageArchiveRecoveryInput) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if json.Unmarshal([]byte(tc.valid), tc.out()) != nil {
				t.Fatal("valid document refused")
			}
			for _, raw := range []string{`null`, `[]`, `{}`, tc.valid + `{}`, strings.Replace(tc.valid, `"request_id"`, `"Request_ID"`, 1), strings.Replace(tc.valid, `"request_id":`, `"request_id":"`+request+`","request_id":`, 1), strings.Replace(tc.valid, request, strings.Repeat("A", 32), 1), strings.Replace(tc.valid, `}`, `,"path":"/etc/passwd"}`, 1), strings.Replace(tc.valid, `"`+request+`"`, `null`, 1)} {
				if json.Unmarshal([]byte(raw), tc.out()) == nil {
					t.Fatal("ambiguous archive input accepted")
				}
			}
		})
	}
}

func archiveDomainPlan(t *testing.T) HostingStorageArchivePlan {
	t.Helper()
	id, volume := HostingStorageIdentity("binding")
	st := HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner", CreatedAt: time.Now().UTC()}
	st.Hash = st.DocumentHash()
	r := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	r.Hash = HostingDocumentHash(r)
	d := HostingStoragePlan{Reference: st.Reference(), VolumeName: volume, MountPath: st.MountPath, CreationOperationID: "origin", DaemonID: r.DaemonID}
	d.CreationNonce = HostingStorageCreationNonce(d.Reference, d.CreationOperationID, d.DaemonID)
	hash := "sha256:" + strings.Repeat("c", 64)
	h := HostingHealthy{Snapshot: HostingSnapshot{ResourceID: st.ResourceID, BindingID: st.BindingID, DestinationID: st.DestinationID, ServerID: "server", RunnerID: "runner", Storage: st.Reference(), PolicyVersion: HostingOwnedStoragePolicyVersion}, Storage: &HostingHealthyStorage{Plan: d, CreationPlanHash: hash}, OperationID: "origin", ContainerID: "container", Release: r, PlanHash: hash}
	m := HostingStorageMaterialization{StorageRef: st.Reference(), State: "materialized", CreationOperationID: d.CreationOperationID, CreationPlanHash: hash, CreationNonce: d.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &HostingReceipt{ID: "receipt", BodyHash: hash, CreatedAt: time.Now().UTC()}}
	m.Labels = ExpectedHostingStorageLabels(st, m)
	helper := HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: HostingStorageArchiveVersion, ExecutableSHA256: "sha256:" + strings.Repeat("d", 64), SourceCommit: strings.Repeat("f", 40), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	helper.Hash = HostingDocumentHash(helper)
	p := HostingStorageArchivePlan{OperationID: "archive", ProjectID: st.ProjectID, ServerID: "server", DestinationID: st.DestinationID, RunnerID: "runner", Mode: "backup", BackupID: "backup", SourceHealthy: h, SourceStorage: st, SourceMaterialization: m, Writers: []HostingHealthy{h}, Helper: helper, Limits: HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 20, FileBytes: 1 << 20, Members: 1000, Depth: 16, PathBytes: 255}, CompilerVersion: HostingStorageArchiveVersion, PolicyVersion: HostingStorageArchiveVersion}
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("valid archive fixture refused")
	}
	return p
}

func TestStorageArchivePlanIdentityAndExplicitLimits(t *testing.T) {
	p := archiveDomainPlan(t)
	for name, mutate := range map[string]func(*HostingStorageArchivePlan){
		"hash":   func(p *HostingStorageArchivePlan) { p.Hash = "" },
		"daemon": func(p *HostingStorageArchivePlan) { p.Helper.DaemonID = "foreign" },
		"unmaterialized": func(p *HostingStorageArchivePlan) {
			p.SourceMaterialization.State = "create_admitted"
			p.SourceMaterialization.MaterializedReceipt = nil
		},
		"archive_limit":           func(p *HostingStorageArchivePlan) { p.Limits.ArchiveBytes = HostingStorageArchiveMaxBytes + 1 },
		"data_limit":              func(p *HostingStorageArchivePlan) { p.Limits.DataBytes = 0 },
		"file_limit":              func(p *HostingStorageArchivePlan) { p.Limits.FileBytes = p.Limits.DataBytes + 1 },
		"members":                 func(p *HostingStorageArchivePlan) { p.Limits.Members = 0 },
		"depth":                   func(p *HostingStorageArchivePlan) { p.Limits.Depth = 0 },
		"path":                    func(p *HostingStorageArchivePlan) { p.Limits.PathBytes = 0 },
		"unknown_writer":          func(p *HostingStorageArchivePlan) { p.Writers[0].ContainerID = "foreign" },
		"duplicate_writer":        func(p *HostingStorageArchivePlan) { p.Writers = append(p.Writers, p.Writers[0]) },
		"wrong_family":            func(p *HostingStorageArchivePlan) { p.SourceHealthy.Snapshot.Database.Engine = "postgresql" },
		"restore_without_target":  func(p *HostingStorageArchivePlan) { p.Mode = "restore" },
		"recovery_without_origin": func(p *HostingStorageArchivePlan) { p.Mode = "recover" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var changed HostingStorageArchivePlan
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("fixture decode")
			}
			mutate(&changed)
			if name != "hash" {
				changed.Hash = ""
				changed.Hash = HostingDocumentHash(changed)
			}
			if changed.Validate() == nil {
				t.Fatal("changed archive identity or missing limit accepted")
			}
		})
	}
}

func TestStorageArchiveTaskRejectsGenericExecution(t *testing.T) {
	for mode, prefix := range map[string]string{"backup": "backup_", "restore": "restore_", "recover": "archive_recover_"} {
		r := TaskRun{ID: prefix + "operation", RunSpec: RunSpec{Type: RunTypeStorageArchive, HostingOperationID: "operation", HostingMode: mode}}
		if ValidateStorageArchiveTask(r) != nil {
			t.Fatal("typed archive task refused")
		}
		for _, mutate := range []func(*TaskRun){func(r *TaskRun) { r.ID = "run_operation" }, func(r *TaskRun) { r.RunSpec.HostingMode = "deploy" }, func(r *TaskRun) { r.RunSpec.Inputs = map[string]any{"path": "/"} }, func(r *TaskRun) { r.RunSpec.Workflow = &Workflow{} }, func(r *TaskRun) { r.RunSpec.HostingBuildID = "build" }} {
			bad := r
			mutate(&bad)
			if ValidateStorageArchiveTask(bad) == nil {
				t.Fatal("generic archive execution admitted")
			}
		}
		if !ContainsHostingExecution(RunSpec{Type: RunTypeStorageArchive}, Workflow{}) {
			t.Fatal("archive escaped internal-family guard")
		}
	}
}

func TestStorageArchiveHelperRequiresQualifiedLocalIdentity(t *testing.T) {
	h := archiveDomainPlan(t).Helper
	for _, change := range []func(*HostingStorageArchiveHelper){
		func(h *HostingStorageArchiveHelper) { h.Mode = "registry" },
		func(h *HostingStorageArchiveHelper) { h.ImageID = "archive:latest" },
		func(h *HostingStorageArchiveHelper) { h.ExecutableSHA256 = "" },
		func(h *HostingStorageArchiveHelper) { h.SourceCommit = strings.Repeat("A", 40) },
		func(h *HostingStorageArchiveHelper) { h.Architecture = "amd64" },
		func(h *HostingStorageArchiveHelper) { h.DaemonID = "" },
	} {
		bad := h
		change(&bad)
		bad.Hash = ""
		bad.Hash = HostingDocumentHash(bad)
		if bad.Validate() == nil {
			t.Fatal("unqualified local helper accepted")
		}
	}
	if (HostingStorageArchiveHelper{}).Validate() == nil {
		t.Fatal("missing helper configuration accepted")
	}
}

func TestStorageArchiveRestoreNeverAuthorizesSourceWriter(t *testing.T) {
	p := archiveDomainPlan(t)
	storage := p.SourceStorage
	storage.ResourceID = "restored_resource"
	storage.BindingID = "restored_binding"
	storage.ID, storage.VolumeName = HostingStorageIdentity(storage.BindingID)
	storage.Hash = storage.DocumentHash()
	d := HostingStoragePlan{Reference: storage.Reference(), VolumeName: storage.VolumeName, MountPath: storage.MountPath, CreationOperationID: p.OperationID, DaemonID: p.Helper.DaemonID}
	d.CreationNonce = HostingStorageCreationNonce(d.Reference, d.CreationOperationID, d.DaemonID)
	p.Mode = "restore"
	p.Target = &HostingStorageArchiveTarget{Resource: Resource{ID: storage.ResourceID, ProjectID: p.ProjectID, Kind: "application", Source: ResourceSource{Kind: "image", Image: ImageSource{Reference: p.SourceHealthy.Release.CanonicalImage}}}, Storage: storage, Plan: d}
	p.Blob = &HostingStorageArchiveBlob{BackupID: p.BackupID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("f", 64), SizeBytes: 1024, Format: HostingStorageArchiveFormat}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() == nil {
		t.Fatal("restore granted source stop authority")
	}
	p.Writers = nil
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("isolated restore target refused")
	}
	t.Run("source_binding_cannot_be_restore_target", func(t *testing.T) {
		bad := p
		target := *p.Target
		bad.Target = &target
		target.Storage = p.SourceStorage
		target.Storage.ResourceID = target.Resource.ID
		target.Storage.Hash = target.Storage.DocumentHash()
		target.Plan = HostingStoragePlan{Reference: target.Storage.Reference(), VolumeName: target.Storage.VolumeName, MountPath: target.Storage.MountPath, CreationOperationID: p.OperationID, DaemonID: p.Helper.DaemonID}
		target.Plan.CreationNonce = HostingStorageCreationNonce(target.Plan.Reference, target.Plan.CreationOperationID, target.Plan.DaemonID)
		bad.Hash = ""
		bad.Hash = HostingDocumentHash(bad)
		if bad.Validate() == nil {
			t.Fatal("restore accepted source binding and volume as new target")
		}
	})
	p.Mode = "recover"
	p.RecoveryOfOperationID = p.OperationID
	p.RecoveryEvidence = &HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: p.Hash}
	p.OperationID = "recovery_operation"
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("restore recovery rejected empty source writer obligations")
	}
	p.Writers = []HostingHealthy{p.SourceHealthy}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() == nil {
		t.Fatal("restore recovery gained source stop authority")
	}
}

func TestStorageArchiveRecoveryEvidenceCannotAuthorizeEligibleWriters(t *testing.T) {
	p := archiveDomainPlan(t)
	h := p.SourceHealthy
	state := HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: true}
	p.Mode = "recover"
	p.RecoveryOfOperationID = p.OperationID
	p.OperationID = "recovery"
	p.RecoveryEvidence = &HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: p.Hash, SnapshotReceiptID: "snapshot_receipt", Writers: []HostingStorageArchiveWriterState{state}, ResumeRequiredContainerIDs: []string{h.ContainerID}}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("exact running-only recovery snapshot refused")
	}
	for name, change := range map[string]func(*HostingStorageArchiveRecoveryEvidence){
		"unknown_writer": func(e *HostingStorageArchiveRecoveryEvidence) { e.Writers[0].ContainerID = "foreign" },
		"stopped_writer": func(e *HostingStorageArchiveRecoveryEvidence) { e.Writers[0].Running = false },
		"duplicate_resume": func(e *HostingStorageArchiveRecoveryEvidence) {
			e.ResumeRequiredContainerIDs = append(e.ResumeRequiredContainerIDs, e.ResumeRequiredContainerIDs...)
		},
		"missing_snapshot":         func(e *HostingStorageArchiveRecoveryEvidence) { e.SnapshotReceiptID = "" },
		"missing_observed_writers": func(e *HostingStorageArchiveRecoveryEvidence) { e.Writers = nil },
		"unknown_resume": func(e *HostingStorageArchiveRecoveryEvidence) {
			e.ResumeRequiredContainerIDs = []string{"merely_eligible"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var changed HostingStorageArchivePlan
			if json.Unmarshal(raw, &changed) != nil {
				t.Fatal("fixture decode")
			}
			change(changed.RecoveryEvidence)
			changed.Hash = ""
			changed.Hash = HostingDocumentHash(changed)
			if changed.Validate() == nil {
				t.Fatal("unproven recovery writer obligation accepted")
			}
		})
	}
	p.RecoveryEvidence = &HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: p.RecoveryEvidence.OriginalPlanHash}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("recovery without stop intent must have empty obligations")
	}
	p.Mode = "backup"
	p.RecoveryOfOperationID = ""
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() == nil {
		t.Fatal("non-recovery operation accepted recovery evidence")
	}
}

func TestStorageArchiveOperationHistoryOmitsLeaseAuthority(t *testing.T) {
	p := archiveDomainPlan(t)
	op := HostingOperation{StorageArchive: &HostingStorageArchive{Mode: "backup", BackupID: p.BackupID, Plan: p, Progress: []HostingStorageArchiveProgressEvidence{{Receipt: HostingReceipt{ID: "progress_receipt", BodyHash: p.Hash, CreatedAt: time.Now().UTC()}, PlanHash: p.Hash, Phase: "writer_snapshot"}}}}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"lease_id"`, `"fence"`, `"attempt"`, `"authority"`} {
		if strings.Contains(string(raw), field) {
			t.Fatal("public archive operation exposed lease authority")
		}
	}
}

func TestStorageArchiveOriginReferencesRemainCanonical(t *testing.T) {
	p := archiveDomainPlan(t)
	h := *p.SourceHealthy.Storage
	h.Plan.CreationOperationID = "another_origin"
	h.Plan.CreationNonce = HostingStorageCreationNonce(h.Plan.Reference, h.Plan.CreationOperationID, h.Plan.DaemonID)
	p.SourceHealthy.Storage = &h
	p.Writers = []HostingHealthy{p.SourceHealthy}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() == nil {
		t.Fatal("source origin descriptor disagreed with committed materialization")
	}
}

func TestStorageArchiveSourceMountMatchesImmutableStorage(t *testing.T) {
	p := archiveDomainPlan(t)
	h := *p.SourceHealthy.Storage
	h.Plan.MountPath = "/other_uploads"
	p.SourceHealthy.Storage = &h
	p.Writers = []HostingHealthy{p.SourceHealthy}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() == nil {
		t.Fatal("archive source mount differs from immutable storage")
	}
}

func TestStorageArchiveCodecContractParity(t *testing.T) {
	// Literal parity with storagearchive.Format and Limits.valid at helper 6bce.
	// The codec is a separately reviewed dependency, not present in this base.
	if HostingStorageArchiveFormat != "nerocd-storage-v1" {
		t.Fatal("archive format differs from maintained helper")
	}
	codecAccepts := func(l HostingStorageArchiveLimits) bool {
		return l.ArchiveBytes >= 1536 && l.ArchiveBytes <= 1<<40 && l.DataBytes > 0 && l.DataBytes <= l.ArchiveBytes && l.FileBytes > 0 && l.FileBytes <= l.DataBytes && l.Members > 0 && l.Members <= 1000000 && l.Depth > 0 && l.Depth <= 256 && l.PathBytes > 0 && l.PathBytes <= 255
	}
	for _, archive := range []int64{0, 1535, 1536, 1 << 30, 1<<30 + 1} {
		for _, data := range []int64{0, 1, 1536, 1 << 30, 1<<30 + 1} {
			for _, path := range []int{0, 1, 255, 256, 4096} {
				l := HostingStorageArchiveLimits{ArchiveBytes: archive, DataBytes: data, FileBytes: 1, Members: 1, Depth: 128, PathBytes: path}
				want := codecAccepts(l) && archive <= HostingStorageArchiveMaxBytes
				if (l.Validate() == nil) != want {
					t.Fatal("controller limit profile differs from bounded helper subset")
				}
			}
		}
	}
}

func TestStorageArchiveCombinedRuntimeFamily(t *testing.T) {
	p := archiveDomainPlan(t)
	p.SourceHealthy.Snapshot.PolicyVersion = HostingOwnedStorageRuntimePolicyVersion
	p.SourceHealthy.Snapshot.RuntimeConfiguration = HostingRuntimeConfigurationRef{Generation: 7, Hash: "sha256:" + strings.Repeat("a", 64)}
	p.Writers = []HostingHealthy{p.SourceHealthy}
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("combined owned storage/runtime source rejected")
	}
	for name, mutate := range map[string]func(*HostingStorageArchivePlan){
		"old policy with runtime": func(p *HostingStorageArchivePlan) {
			p.SourceHealthy.Snapshot.PolicyVersion = HostingOwnedStoragePolicyVersion
		},
		"combined without runtime": func(p *HostingStorageArchivePlan) {
			p.SourceHealthy.Snapshot.RuntimeConfiguration = HostingRuntimeConfigurationRef{}
		},
		"malformed runtime": func(p *HostingStorageArchivePlan) { p.SourceHealthy.Snapshot.RuntimeConfiguration.Generation = 0 },
		"database family":   func(p *HostingStorageArchivePlan) { p.SourceHealthy.Snapshot.ApplicationDatabase.BindingID = "binding" },
		"generic runtime family": func(p *HostingStorageArchivePlan) {
			p.SourceHealthy.Snapshot.PolicyVersion = HostingRuntimePolicyVersion
		},
		"retained mixed family": func(p *HostingStorageArchivePlan) {
			p.Writers[0].Snapshot.PolicyVersion = HostingOwnedStoragePolicyVersion
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			changed.Writers = append([]HostingHealthy(nil), p.Writers...)
			mutate(&changed)
			changed.Hash = ""
			changed.Hash = HostingDocumentHash(changed)
			if changed.Validate() == nil {
				t.Fatal("malformed mixed archive family accepted")
			}
		})
	}
}

func TestComposeStorageArchiveRequestRequiresCompleteSelection(t *testing.T) {
	valid := `{"request_id":"` + strings.Repeat("a", 32) + `","expected_resource_generation":1,"volume":"uploads","expected_deployed_operation_id":"hop_deployed","acknowledge_interruption":true}`
	var in HostingStorageBackupInput
	if json.Unmarshal([]byte(valid), &in) != nil {
		t.Fatal("valid Compose selection rejected")
	}
	for _, raw := range []string{
		strings.Replace(valid, `,"volume":"uploads"`, "", 1),
		strings.Replace(valid, `,"expected_deployed_operation_id":"hop_deployed"`, "", 1),
		strings.Replace(valid, `,"acknowledge_interruption":true`, "", 1),
		strings.Replace(valid, `"acknowledge_interruption":true`, `"acknowledge_interruption":false`, 1),
		strings.Replace(valid, `"volume":"uploads"`, `"volume":null`, 1),
		strings.Replace(valid, `"volume":"uploads"`, `"volume":"../uploads"`, 1),
		strings.Replace(valid, `"volume":"uploads"`, `"volume":"uploads","volume":"uploads"`, 1),
		strings.Replace(valid, `"volume":"uploads"`, `"Volume":"uploads"`, 1),
		`{"request_id":"` + strings.Repeat("a", 32) + `","expected_resource_generation":1,"acknowledge_interruption":false}`,
		strings.Replace(valid, `}`, `,"remote_target":{"target_id":"target","revision":1}}`, 1),
	} {
		if json.Unmarshal([]byte(raw), &in) == nil {
			t.Fatal("partial or ambiguous volume selection accepted")
		}
	}
}
