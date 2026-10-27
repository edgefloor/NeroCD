package runnerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func archiveClientPlan(t *testing.T) domain.HostingStorageArchivePlan {
	t.Helper()
	id, volume := domain.HostingStorageIdentity("binding")
	st := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner", CreatedAt: time.Now().UTC()}
	st.Hash = st.DocumentHash()
	r := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	r.Hash = domain.HostingDocumentHash(r)
	d := domain.HostingStoragePlan{Reference: st.Reference(), VolumeName: volume, MountPath: st.MountPath, CreationOperationID: "origin", DaemonID: r.DaemonID}
	d.CreationNonce = domain.HostingStorageCreationNonce(d.Reference, d.CreationOperationID, d.DaemonID)
	hash := "sha256:" + strings.Repeat("c", 64)
	h := domain.HostingHealthy{Snapshot: domain.HostingSnapshot{ResourceID: st.ResourceID, BindingID: st.BindingID, DestinationID: st.DestinationID, ServerID: "server", RunnerID: "runner", Storage: st.Reference(), PolicyVersion: domain.HostingOwnedStoragePolicyVersion}, Storage: &domain.HostingHealthyStorage{Plan: d, CreationPlanHash: hash}, OperationID: "origin", ContainerID: "container", Release: r, PlanHash: hash}
	m := domain.HostingStorageMaterialization{StorageRef: st.Reference(), State: "materialized", CreationOperationID: d.CreationOperationID, CreationPlanHash: hash, CreationNonce: d.CreationNonce, DaemonID: r.DaemonID, MaterializedReceipt: &domain.HostingReceipt{ID: "receipt", BodyHash: hash, CreatedAt: time.Now().UTC()}}
	m.Labels = domain.ExpectedHostingStorageLabels(st, m)
	helper := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ExecutableSHA256: "sha256:" + strings.Repeat("d", 64), SourceCommit: strings.Repeat("f", 40), ImageID: "sha256:" + strings.Repeat("e", 64), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	helper.Hash = domain.HostingDocumentHash(helper)
	p := domain.HostingStorageArchivePlan{OperationID: "archive", ProjectID: st.ProjectID, ServerID: "server", DestinationID: st.DestinationID, RunnerID: "runner", Mode: "backup", BackupID: "backup", SourceHealthy: h, SourceStorage: st, SourceMaterialization: m, Writers: []domain.HostingHealthy{h}, Helper: helper, Limits: domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 20, FileBytes: 1 << 20, Members: 1000, Depth: 16, PathBytes: 255}, CompilerVersion: domain.HostingStorageArchiveVersion, PolicyVersion: domain.HostingStorageArchiveVersion}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("valid archive fixture refused")
	}
	return p
}

func archiveClientOperation(p domain.HostingStorageArchivePlan, a domain.HostingAuthority) domain.HostingOperation {
	return domain.HostingOperation{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: p.SourceStorage.ResourceID, BindingID: p.SourceStorage.BindingID, RunID: a.RunID, TaskMode: p.Mode, State: "queued", StorageArchive: &domain.HostingStorageArchive{Mode: p.Mode, BackupID: p.BackupID, Plan: p}}
}

func TestStorageArchiveIdentityRequiresIndependentExactOptIn(t *testing.T) {
	base := []string{domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.HostingOwnedStorageCapability}
	for _, tc := range []struct {
		name    string
		caps    []string
		options RuntimeExecutionOptions
		want    bool
	}{
		{"explicit", base, RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, true},
		{"disabled", base, RuntimeExecutionOptions{}, false},
		{"capability missing", []string{domain.RunTypeStorageArchive, domain.HostingOwnedStorageCapability}, RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, false},
		{"task missing", []string{domain.HostingStorageArchiveCapability, domain.HostingOwnedStorageCapability}, RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, false},
		{"storage missing", base[:2], RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, false},
		{"shell", append(append([]string(nil), base...), "shell"), RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, false},
		{"database separate", []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}, RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true}, false},
		{"legacy", []string{"shell"}, RuntimeExecutionOptions{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: tc.caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "canary_bearer").RuntimeExecutionIdentityWithOptions(context.Background(), tc.options)
			if (err == nil) != tc.want {
				t.Fatalf("identity acceptance %v", err)
			}
		})
	}
}
func TestStorageArchiveClientFullRequestReceiptsAndTypedPublicEvidence(t *testing.T) {
	p := archiveClientPlan(t)
	a := domain.HostingAuthority{OperationID: p.OperationID, RunID: "backup_" + p.OperationID, LeaseID: "private_lease", Attempt: 1, Fence: "private_fence"}
	op := archiveClientOperation(p, a)
	var requests [][]byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, append([]byte(nil), raw...))
		var document struct {
			ReceiptID string `json:"receipt_id"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Error(err)
		}
		receipt := &domain.HostingReceipt{ID: document.ReceiptID, BodyHash: domain.HostingDocumentHash(json.RawMessage(raw)), CreatedAt: time.Now().UTC()}
		switch r.URL.Path {
		case "/api/v1/runners/hosting/storage-archive/plan":
		case "/api/v1/runners/hosting/storage-archive/prepared":
			op.StorageArchive.PreparedReceipt = receipt
			op.State = "prepared"
		case "/api/v1/runners/hosting/storage-archive/apply-begin":
			op.State = "applying"
			op.MutationHold = true
		case "/api/v1/runners/hosting/storage-archive/progress":
			var in domain.HostingStorageArchiveProgress
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Error(err)
			}
			op.StorageArchive.Progress = []domain.HostingStorageArchiveProgressEvidence{{Receipt: *receipt, PlanHash: in.PlanHash, Phase: in.Phase, Writers: in.Writers}}
		case "/api/v1/runners/hosting/storage-archive/complete":
			var in domain.HostingStorageArchiveCompleteRequest
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Error(err)
			}
			op.StorageArchive.Result = &in.Result
			op.CompleteReceipt = receipt
			op.State = "succeeded"
			op.MutationHold = false
		default:
			t.Error("unexpected typed path")
		}
		if err := json.NewEncoder(w).Encode(op); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	c := New(server.URL, "bearer_canary")
	c.HTTP = server.Client()
	if _, err := c.HostingStorageArchivePlan(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	prepared := domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared", PlanHash: p.Hash, Helper: p.Helper}
	if _, err := c.HostingStorageArchivePrepared(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HostingStorageArchivePrepared(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if string(requests[1]) != string(requests[2]) {
		t.Fatal("identical prepared replay changed")
	}
	if _, err := c.HostingStorageArchiveApplyBegin(t.Context(), domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "apply", PlanHash: p.Hash}); err != nil {
		t.Fatal(err)
	}
	h := p.Writers[0]
	progress := domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "snapshot", PlanHash: p.Hash, Phase: "writer_snapshot", Writers: []domain.HostingStorageArchiveWriterState{{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: true}}}
	if _, err := c.HostingStorageArchiveProgress(t.Context(), progress); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "resumed", FailureCode: "helper_failed"}}
	if _, err := c.HostingStorageArchiveComplete(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{a.LeaseID, a.Fence, "bearer_canary", "artifact_bytes_canary"} {
		if strings.Contains(string(raw), canary) {
			t.Fatal("private request authority escaped public evidence")
		}
	}
}
func TestStorageArchiveClientRejectsWrongReceiptMixedFamiliesAndPlaintext(t *testing.T) {
	p := archiveClientPlan(t)
	a := domain.HostingAuthority{OperationID: p.OperationID, RunID: "backup_" + p.OperationID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	prepared := domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared", PlanHash: p.Hash, Helper: p.Helper}
	for _, change := range []func(*domain.HostingOperation){
		func(op *domain.HostingOperation) { op.ProjectID = "foreign" },
		func(op *domain.HostingOperation) { op.BindingID = "foreign" },
		func(op *domain.HostingOperation) { op.RunID = "foreign" },
		func(op *domain.HostingOperation) { op.Snapshot.ResourceID = "generic" },
		func(op *domain.HostingOperation) { op.Result = &domain.HostingResult{Ready: true} },
		func(op *domain.HostingOperation) {
			op.StorageArchive.PreparedReceipt.BodyHash = domain.HostingDocumentHash(p.Helper)
		},
	} {
		op := archiveClientOperation(p, a)
		op.StorageArchive.PreparedReceipt = &domain.HostingReceipt{ID: prepared.ReceiptID, BodyHash: domain.HostingDocumentHash(prepared), CreatedAt: time.Now().UTC()}
		change(&op)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewEncoder(w).Encode(op); err != nil {
				t.Error(err)
			}
		}))
		_, err := New(server.URL, "token").HostingStorageArchivePrepared(WithStorageArchiveTransport(t.Context(), StorageArchiveTransport{TrustedLoopbackHTTP: true}), prepared)
		server.Close()
		if err == nil {
			t.Fatal("changed or mixed archive response accepted")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("artifact_bytes_canary " + a.Fence))
	}))
	defer server.Close()
	_, err := New(server.URL, "token").HostingStorageArchivePlan(WithStorageArchiveTransport(t.Context(), StorageArchiveTransport{TrustedLoopbackHTTP: true}), a)
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "canary") || strings.Contains(fmt.Sprintf("%+v", err), a.Fence) {
		t.Fatal("failure diagnostic escaped")
	}
}
func TestStorageArchiveTransportRefusesRedirectAndUntaggedHTTP(t *testing.T) {
	p := archiveClientPlan(t)
	a := domain.HostingAuthority{OperationID: p.OperationID, RunID: "backup_" + p.OperationID, LeaseID: "lease_canary", Attempt: 1, Fence: "fence_canary"}
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/leak", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := New(source.URL, "bearer_canary")
	c.HTTP = source.Client()
	calls := []func(context.Context) error{
		func(ctx context.Context) error { _, err := c.HostingStorageArchivePlan(ctx, a); return err },
		func(ctx context.Context) error {
			_, err := c.HostingStorageArchivePrepared(ctx, domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared", PlanHash: p.Hash, Helper: p.Helper})
			return err
		},
		func(ctx context.Context) error {
			_, err := c.HostingStorageArchiveApplyBegin(ctx, domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "apply", PlanHash: p.Hash})
			return err
		},
		func(ctx context.Context) error {
			_, err := c.HostingStorageArchiveProgress(ctx, domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "helper_intent", PlanHash: p.Hash, Phase: "helper_intent"})
			return err
		},
		func(ctx context.Context) error {
			_, err := c.HostingStorageArchiveComplete(ctx, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "failed", ResumeOutcome: "uncertain", Uncertain: true}})
			return err
		},
		func(ctx context.Context) error {
			_, err := c.Renew(ctx, Authority{LeaseID: a.LeaseID, Attempt: 1, Fence: a.Fence})
			return err
		},
	}
	for _, call := range calls {
		if err := call(WithStorageArchiveTransport(t.Context(), StorageArchiveTransport{})); !HTTPStatus(err, 307) {
			t.Fatalf("redirect was not refused: %v", err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("archive bearer or authority leaked through redirect")
	}
	if _, err := New(target.URL, "token").HostingStorageArchivePlan(t.Context(), a); err == nil || leaked.Load() != 0 {
		t.Fatal("untagged archive HTTP was accepted")
	}
}

func TestComposeStorageRemoteIdentitySeparatesUnderstandingAndExecution(t *testing.T) {
	base := []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingStackCapability, domain.HostingOwnedStorageCapability, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.HostingComposeStorageArchiveCapability, domain.HostingComposeStorageRemoteCapability}
	for _, kind := range []string{"understanding_only", "remote_enabled", "old_reader", "unconfigured_transfer"} {
		t.Run(kind, func(t *testing.T) {
			caps := append([]string(nil), base...)
			options := RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true, RuntimeEnvironment: true}
			want := true
			switch kind {
			case "remote_enabled":
				caps = append(caps, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupRemoteCapability)
				options.RemoteStorageBackups = true
			case "old_reader":
				caps = caps[:len(caps)-1]
				want = false
			case "unconfigured_transfer":
				caps = append(caps, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupRemoteCapability)
				want = false
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(proxyIdentityResponse{RunnerID: "runner", Capabilities: caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), options)
			if (err == nil) != want {
				t.Fatal("capability/configuration mismatch", err)
			}
		})
	}
}
