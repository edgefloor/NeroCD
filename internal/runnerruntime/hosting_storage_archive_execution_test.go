package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func archiveRuntimePlan(t *testing.T) domain.HostingStorageArchivePlan {
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

type archiveRuntimeController struct {
	t            *testing.T
	mu           sync.Mutex
	j            *runner.AttemptJournal
	op           domain.HostingOperation
	lease        domain.RunLease
	requests     []string
	lostPhase    string
	rejectPhase  string
	rejectStatus int
	revoked      bool
	lostComplete bool
	storageAck   *domain.HostingStorageCreateAck
}

func (c *archiveRuntimeController) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked {
		http.Error(w, "private_canary_server_detail", http.StatusForbidden)
		return
	}
	c.requests = append(c.requests, r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/lease") || strings.HasSuffix(r.URL.Path, "/renew") {
		_ = json.NewEncoder(w).Encode(c.lease)
		return
	}
	if c.storageAck != nil && (strings.HasSuffix(r.URL.Path, "/storage-create-begin") || strings.HasSuffix(r.URL.Path, "/storage-materialized")) {
		if strings.HasSuffix(r.URL.Path, "/storage-create-begin") {
			_ = json.NewEncoder(w).Encode(*c.storageAck)
			return
		}
		var req domain.HostingStorageMaterializedRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Matches(c.storageAck.Storage, c.storageAck.Materialization) != nil {
			c.t.Error("changed target materialization")
			w.WriteHeader(400)
			return
		}
		m := c.storageAck.Materialization
		m.State = "materialized"
		m.MaterializedReceipt = &domain.HostingReceipt{ID: req.ReceiptID, BodyHash: domain.HostingDocumentHash(req), CreatedAt: time.Now().UTC()}
		_ = json.NewEncoder(w).Encode(domain.HostingStorageMaterializedAck{Materialization: m, Receipt: *m.MaterializedReceipt})
		return
	}
	var raw json.RawMessage
	if json.NewDecoder(r.Body).Decode(&raw) != nil {
		c.t.Error("invalid fixture request")
		w.WriteHeader(400)
		return
	}
	var in struct {
		ReceiptID string `json:"receipt_id"`
	}
	if json.Unmarshal(raw, &in) != nil {
		c.t.Error("invalid fixture receipt")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/hosting/storage-archive/")
	if path != "plan" {
		found := false
		for _, e := range c.j.Snapshot().StorageArchive {
			if e.ID == in.ReceiptID {
				found = true
			}
		}
		if !found {
			c.t.Errorf("%s sent before durable journal", path)
		}
	}
	receipt := domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(raw), CreatedAt: time.Now().UTC()}
	switch path {
	case "plan":
	case "prepared":
		c.op.State = "prepared"
		c.op.StorageArchive.PreparedReceipt = &receipt
	case "apply-begin":
		c.op.State = "applying"
		c.op.MutationHold = true
	case "progress":
		var p domain.HostingStorageArchiveProgress
		if json.Unmarshal(raw, &p) != nil {
			c.t.Error("invalid progress")
			return
		}
		if c.rejectPhase == p.Phase {
			http.Error(w, "private_canary_server_detail", c.rejectStatus)
			return
		}
		proof := domain.HostingStorageArchiveProgressEvidence{Receipt: receipt, PlanHash: p.PlanHash, Phase: p.Phase, Writers: p.Writers, HelperContainerID: p.HelperContainerID, Blob: p.Blob, RestoreVerified: p.RestoreVerified}
		present := false
		for _, old := range c.op.StorageArchive.Progress {
			present = present || old.Receipt.ID == receipt.ID
		}
		if !present {
			c.op.StorageArchive.Progress = append(c.op.StorageArchive.Progress, proof)
		}
		if c.lostPhase == p.Phase {
			c.lostPhase = ""
			w.WriteHeader(503)
			return
		}
	case "complete":
		var p domain.HostingStorageArchiveCompleteRequest
		if json.Unmarshal(raw, &p) != nil {
			c.t.Error("invalid completion")
			return
		}
		c.op.StorageArchive.Result = &p.Result
		c.op.CompleteReceipt = &receipt
		c.op.State = "succeeded"
		c.op.MutationHold = p.Result.Uncertain
		if c.lostComplete {
			c.lostComplete = false
			w.WriteHeader(503)
			return
		}
	default:
		c.t.Errorf("unexpected archive call %s", r.URL.Path)
		w.WriteHeader(400)
		return
	}
	_ = json.NewEncoder(w).Encode(c.op)
}

type archiveRuntimeEngine struct {
	t                      *testing.T
	j                      *runner.AttemptJournal
	writers                []domain.HostingStorageArchiveWriterState
	effects                []string
	failAt                 string
	beforeEffect           func(string)
	restoreMaterialization *domain.HostingStorageMaterialization
	createPermissions      []bool
	uncertainCreateID      bool
	closeErr               error
	closeCalls             int
}

func (e *archiveRuntimeEngine) Close() error { e.closeCalls++; return e.closeErr }
func (e *archiveRuntimeEngine) Prepare(context.Context, domain.HostingStorageArchivePlan) error {
	return nil
}
func (e *archiveRuntimeEngine) InspectWriters(context.Context, domain.HostingStorageArchivePlan) ([]domain.HostingStorageArchiveWriterState, error) {
	if e.failAt == "inspect" {
		return nil, errors.New("private_canary_inspection")
	}
	return slices.Clone(e.writers), nil
}
func (e *archiveRuntimeEngine) EnsureRestoreVolume(ctx context.Context, req hostingOwnedVolumeRequest, before func(context.Context) error) (domain.HostingStorageMaterialization, error) {
	if e.restoreMaterialization == nil {
		e.t.Fatal("backup obtained restore authority")
	}
	e.createPermissions = append(e.createPermissions, req.CreatePermitted)
	if err := e.mutate(ctx, "volume", "", before); err != nil {
		return domain.HostingStorageMaterialization{}, err
	}
	return *e.restoreMaterialization, nil
}
func (e *archiveRuntimeEngine) mutate(ctx context.Context, name, phase string, before func(context.Context) error) error {
	found := phase == ""
	for _, r := range e.j.Snapshot().StorageArchive {
		if r.Progress != nil && r.Progress.Phase == phase {
			found = true
		}
		if name == "cleanup" && r.UncertainHelperContainerID != "" {
			found = true
		}
	}
	if !found {
		e.t.Errorf("%s mutation before durable %s", name, phase)
	}
	if e.beforeEffect != nil {
		e.beforeEffect(name)
	}
	if before == nil || before(ctx) != nil {
		return errLeaseAuthorityLost
	}
	e.effects = append(e.effects, name)
	if e.failAt == name {
		return errors.New("private_canary_engine_output")
	}
	return nil
}
func (e *archiveRuntimeEngine) StopWriter(ctx context.Context, _ domain.HostingStorageArchivePlan, w domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	if !w.Running {
		e.t.Error("stopped writer granted stop authority")
	}
	return e.mutate(ctx, "stop:"+w.ContainerID, "stop_intent", before)
}
func (e *archiveRuntimeEngine) ResumeWriter(ctx context.Context, _ domain.HostingStorageArchivePlan, w domain.HostingStorageArchiveWriterState, before func(context.Context) error) error {
	if !w.Running {
		e.t.Error("originally stopped writer granted start authority")
	}
	return e.mutate(ctx, "resume:"+w.ContainerID, "resume_intent", before)
}
func (e *archiveRuntimeEngine) CreateHelper(ctx context.Context, _ domain.HostingStorageArchivePlan, _ string, before func(context.Context) error) (string, error) {
	if err := e.mutate(ctx, "create", "helper_intent", before); err != nil {
		if e.uncertainCreateID {
			return strings.Repeat("e", 64), err
		}
		return "", err
	}
	return strings.Repeat("e", 64), nil
}
func (e *archiveRuntimeEngine) Backup(ctx context.Context, p domain.HostingStorageArchivePlan, _, _ string, before func(context.Context) error) (domain.HostingStorageArchiveBlob, error) {
	if err := e.mutate(ctx, "backup", "helper_identified", before); err != nil {
		return domain.HostingStorageArchiveBlob{}, err
	}
	return domain.HostingStorageArchiveBlob{BackupID: p.BackupID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 1024, Format: domain.HostingStorageArchiveFormat}, nil
}
func (e *archiveRuntimeEngine) Restore(ctx context.Context, p domain.HostingStorageArchivePlan, _, _ string, before func(context.Context) error) error {
	if p.Mode != "restore" || len(p.Writers) != 0 {
		e.t.Fatal("backup extracted archive or restore gained writers")
	}
	return e.mutate(ctx, "restore", "helper_identified", before)
}
func (e *archiveRuntimeEngine) CleanupHelper(ctx context.Context, _ domain.HostingStorageArchivePlan, _, _ string, before func(context.Context) error) error {
	return e.mutate(ctx, "cleanup", "helper_identified", before)
}

func archiveRuntimeFixture(t *testing.T) (*archiveCoordinator, *archiveRuntimeController, *archiveRuntimeEngine, string) {
	t.Helper()
	p := archiveRuntimePlan(t)
	stopped := p.Writers[0]
	stopped.ContainerID = "stopped"
	p.Writers = append(p.Writers, stopped)
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid stopped-writer fixture")
	}
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	lease := domain.RunLease{ID: "lease", RunID: "backup_" + p.OperationID, RunnerID: p.RunnerID, Attempt: 1, Fence: "fence", Status: domain.LeaseActive, CreatedAt: time.Now().Add(-time.Second).UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
	a := hostingAuthority(lease.RunID, p.OperationID, lease)
	op := domain.HostingOperation{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: p.SourceStorage.ResourceID, BindingID: p.SourceStorage.BindingID, RunID: a.RunID, TaskMode: p.Mode, State: "preparing", StorageArchive: &domain.HostingStorageArchive{Mode: p.Mode, BackupID: p.BackupID, Plan: p}}
	c := &archiveRuntimeController{t: t, j: j, op: op, lease: lease}
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(server.Close)
	ctx := runnerclient.WithStorageArchiveTransport(context.Background(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, lease)
	t.Cleanup(s.Close)
	e := &archiveRuntimeEngine{t: t, j: j}
	for i, h := range p.Writers {
		e.writers = append(e.writers, domain.HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: i == 0})
	}
	x := &archiveCoordinator{s: s, j: j, c: runnerclient.New(server.URL, "private_canary_bearer"), e: e, a: a, lease: lease, p: p, drain: func(context.Context) error {
		for _, r := range j.Snapshot().StorageArchive {
			if r.Complete != nil {
				return nil
			}
		}
		t.Error("drain preceded result journal")
		return nil
	}}
	return x, c, e, dir
}

func TestStorageArchiveCoordinatorOrdersDurabilityAndPreservesStoppedWriters(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	if err := x.execute(c.op); err != nil {
		t.Fatal(err)
	}
	if want := []string{"stop:container", "create", "backup", "cleanup", "resume:container"}; !slices.Equal(e.effects, want) {
		t.Fatalf("effects %v", e.effects)
	}
	if len(x.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("settled exact completion retained evidence")
	}
	if c.op.StorageArchive.Result == nil || c.op.StorageArchive.Result.ArchiveOutcome != "succeeded" || c.op.StorageArchive.Result.ResumeOutcome != "resumed" || c.op.Result != nil {
		t.Fatal("archive result inferred generic readiness")
	}
}

func TestStorageArchiveCoordinatorFailureAndRevocationRemainConservative(t *testing.T) {
	for _, failure := range []string{"inspect", "stop:container", "create", "backup", "cleanup", "resume:container"} {
		t.Run(failure, func(t *testing.T) {
			x, c, e, dir := archiveRuntimeFixture(t)
			e.failAt = failure
			_ = x.execute(c.op)
			if failure == "create" && slices.Contains(e.effects, "resume:container") {
				t.Fatal("unknown created helper allowed restart")
			}
			if failure == "backup" && !slices.Contains(e.effects, "resume:container") {
				t.Fatal("known failed helper did not permit bounded current-authority resume")
			}
			if failure == "cleanup" && slices.Contains(e.effects, "resume:container") {
				t.Fatal("uncertain helper permitted writer restart")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range []string{"private_canary", "CreatePermitted", "artifact_bytes", "/host/private"} {
				if strings.Contains(string(raw), canary) {
					t.Fatalf("private value persisted: %s", canary)
				}
			}
		})
	}
	t.Run("revoked after inspection", func(t *testing.T) {
		x, c, e, _ := archiveRuntimeFixture(t)
		e.beforeEffect = func(name string) {
			if name == "stop:container" {
				c.mu.Lock()
				c.revoked = true
				c.mu.Unlock()
			}
		}
		if err := x.execute(c.op); err == nil {
			t.Fatal("revoked authority completed")
		}
		if len(e.effects) != 0 {
			t.Fatalf("revoked mutation %v", e.effects)
		}
		if len(x.j.Snapshot().StorageArchive) == 0 {
			t.Fatal("revoked stop uncertainty discarded")
		}
	})
}

func TestStorageArchiveLostCompletionReplayNeverRunsHelperTwice(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	c.lostComplete = true
	if err := x.execute(c.op); err == nil {
		t.Fatal("lost completion ACK accepted")
	}
	effects := slices.Clone(e.effects)
	config := Config{Server: x.c.Server, Token: "private_canary_bearer", RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}
	if err := reconcileStorageArchiveJournal(x.s.Context(), config, x.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(effects, e.effects) || len(x.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("completion replay mutated engine or retained settled records")
	}
}

func archiveRestoreRuntimeFixture(t *testing.T) (*archiveCoordinator, *archiveRuntimeController, *archiveRuntimeEngine) {
	t.Helper()
	x, c, e, _ := archiveRuntimeFixture(t)
	p := x.p
	st := p.SourceStorage
	st.ResourceID = "restored_resource"
	st.BindingID = "restored_binding"
	st.ID, st.VolumeName = domain.HostingStorageIdentity(st.BindingID)
	st.Hash = st.DocumentHash()
	d := domain.HostingStoragePlan{Reference: st.Reference(), VolumeName: st.VolumeName, MountPath: st.MountPath, CreationOperationID: p.OperationID, DaemonID: p.Helper.DaemonID}
	d.CreationNonce = domain.HostingStorageCreationNonce(d.Reference, d.CreationOperationID, d.DaemonID)
	p.Mode = "restore"
	p.Writers = nil
	p.Target = &domain.HostingStorageArchiveTarget{Resource: domain.Resource{ID: st.ResourceID, ProjectID: p.ProjectID, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: p.SourceHealthy.Release.CanonicalImage}}}, Storage: st, Plan: d}
	p.Blob = &domain.HostingStorageArchiveBlob{BackupID: p.BackupID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("f", 64), SizeBytes: 1024, Format: domain.HostingStorageArchiveFormat}
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid restore fixture")
	}
	x.p = p
	x.a.RunID = "restore_" + p.OperationID
	x.lease.RunID = x.a.RunID
	c.lease = x.lease
	c.op.RunID = x.a.RunID
	c.op.TaskMode = p.Mode
	c.op.ResourceID = st.ResourceID
	c.op.BindingID = st.BindingID
	c.op.StorageArchive = &domain.HostingStorageArchive{Mode: p.Mode, BackupID: p.BackupID, RestoreID: "restore", Plan: p}
	m := domain.HostingStorageMaterialization{StorageRef: st.Reference(), State: "create_admitted", CreationOperationID: p.OperationID, CreationPlanHash: p.Hash, CreationNonce: d.CreationNonce, DaemonID: p.Helper.DaemonID}
	m.Labels = domain.ExpectedHostingStorageLabels(st, m)
	c.storageAck = &domain.HostingStorageCreateAck{Storage: st, Materialization: m, CreatePermitted: true}
	e.restoreMaterialization = &m
	return x, c, e
}

func TestStorageArchiveRestoreIsNewTargetOnlyAndNeverReextracts(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost completion", true: "partial restore"}[failed], func(t *testing.T) {
			x, c, e := archiveRestoreRuntimeFixture(t)
			if failed {
				e.failAt = "restore"
			} else {
				c.lostComplete = true
			}
			if err := x.execute(c.op); err == nil {
				t.Fatal("lost ACK or partial target accepted")
			}
			if !slices.Equal(e.createPermissions, []bool{true}) {
				t.Fatalf("creation permissions %v", e.createPermissions)
			}
			for _, effect := range e.effects {
				if strings.HasPrefix(effect, "stop:") || strings.HasPrefix(effect, "resume:") || effect == "backup" {
					t.Fatalf("restore gained source effects %v", e.effects)
				}
			}
			before := slices.Clone(e.effects)
			config := Config{Server: x.c.Server, Token: x.c.Token, RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}
			if err := reconcileStorageArchiveJournal(x.s.Context(), config, x.j); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(before, e.effects) {
				t.Fatal("startup re-extracted or created another target")
			}
			if failed && len(x.j.Snapshot().StorageArchive) == 0 {
				t.Fatal("partial restore hold discarded")
			}
		})
	}
}

func TestStorageArchiveCancellationAndExpiredLeaseNeverInventAuthority(t *testing.T) {
	t.Run("cancel after stop", func(t *testing.T) {
		x, c, e, _ := archiveRuntimeFixture(t)
		e.beforeEffect = func(name string) {
			if name == "create" {
				x.s.cancel()
			}
		}
		if err := x.execute(c.op); err == nil {
			t.Fatal("canceled attempt completed")
		}
		if slices.Contains(e.effects, "create") || slices.Contains(e.effects, "resume:stopped") {
			t.Fatalf("canceled/stopped effects %v", e.effects)
		}
		// A create intent whose effect is unknown conservatively prevents restart.
		if slices.Contains(e.effects, "resume:container") {
			t.Fatal("unknown helper start resumed writer")
		}
	})
	t.Run("expired lease after inspection", func(t *testing.T) {
		x, c, e, _ := archiveRuntimeFixture(t)
		e.beforeEffect = func(name string) {
			if name == "stop:container" {
				c.mu.Lock()
				c.lease.ExpiresAt = time.Now().Add(-time.Second)
				c.mu.Unlock()
			}
		}
		if err := x.execute(c.op); err == nil {
			t.Fatal("expired lease accepted")
		}
		if len(e.effects) != 0 {
			t.Fatalf("expired lease mutated %v", e.effects)
		}
	})
	t.Run("known unverified create response", func(t *testing.T) {
		x, c, e, _ := archiveRuntimeFixture(t)
		e.failAt = "create"
		e.uncertainCreateID = true
		_ = x.execute(c.op)
		if !slices.Contains(e.effects, "cleanup") || !slices.Contains(e.effects, "resume:container") {
			t.Fatalf("exact unverified ID was not revalidated for cleanup %v", e.effects)
		}
		for _, p := range c.op.StorageArchive.Progress {
			if p.Phase == "helper_identified" {
				t.Fatal("unverified identity exposed as owned helper")
			}
		}
	})
}

func TestStorageArchiveTransportPolicyReachesActualSharedLifecycle(t *testing.T) {
	for _, mode := range []string{"preflight", "watcher", "renewer", "reporter", "replay"} {
		t.Run(mode, func(t *testing.T) {
			x, _, _, _ := archiveRuntimeFixture(t)
			var leaks atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { leaks.Add(1); _, _ = w.Write([]byte(`{}`)) }))
			defer destination.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL+"/leak", http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			parent := runnerclient.WithStorageArchiveTransport(context.Background(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(parent, x.lease)
			defer s.Close()
			var err error
			switch mode {
			case "preflight":
				claim := domain.ClaimedRun{Run: domain.TaskRun{ID: x.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: x.a.OperationID, HostingMode: "backup"}}, Lease: x.lease}
				err = executeClaimWithOptionsContext(context.Background(), source.URL, "private_canary_bearer", claim, t.TempDir(), time.Millisecond, x.j, "", composeImagePolicyPreloaded, &runnerOperationalCounters{}, claimExecutionOptions{storageArchive: &StorageArchiveExecutionConfig{TrustedLoopbackHTTP: true}})
			case "watcher":
				worker := startLeaseWatcher(s.Context(), source.URL, "private_canary_bearer", x.lease, time.Millisecond, s.cancel)
				select {
				case <-s.Context().Done():
					err = s.Context().Err()
				case <-time.After(3 * time.Second):
					t.Error("watcher ignored redirect rejection")
				}
				worker.Stop()
			case "renewer":
				worker := startLeaseRenewer(s.Context(), s, source.URL, "private_canary_bearer", x.lease, time.Millisecond)
				select {
				case <-s.Context().Done():
					err = s.Context().Err()
				case <-time.After(3 * time.Second):
					t.Error("renewer ignored redirect rejection")
				}
				worker.Stop()
			case "reporter":
				reporter := startAttemptReporter(s.Context(), s, x.j, source.URL, "private_canary_bearer", x.a.RunID, x.lease)
				if emitErr := reporter.Emit("system", "archive started", 1); emitErr != nil {
					reporter.Stop()
					t.Fatal(emitErr)
				}
				err = reporter.WaitEmpty(s.Context())
				reporter.Stop()
			case "replay":
				entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "prepared", "prepared")
				entry.Prepared = &domain.HostingStorageArchivePreparedRequest{HostingAuthority: x.a, ReceiptID: entry.ID, PlanHash: x.p.Hash, Helper: x.p.Helper}
				if _, err = x.j.AppendStorageArchive(entry); err != nil {
					t.Fatal(err)
				}
				err = reconcileStorageArchiveJournal(parent, Config{Server: source.URL, Token: "private_canary_bearer", RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}, x.j)
			}
			if err == nil || leaks.Load() != 0 {
				t.Fatalf("shared lifecycle followed redirect: err=%v leaks=%d", err, leaks.Load())
			}
			if strings.Contains(err.Error(), "private_canary_bearer") {
				t.Fatal("transport error leaked bearer")
			}
		})
	}
}

func TestStorageArchiveCannotReachGenericCompletion(t *testing.T) {
	completions, claims := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner"})
		case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v1/runners/claim":
			claims++
			_ = json.NewEncoder(w).Encode(domain.ClaimedRun{Run: domain.TaskRun{ID: "backup_operation", RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: "operation", HostingMode: "backup"}}})
		case "/api/v1/runners/complete":
			completions++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	err := Run(Config{Server: server.URL, Token: "token", RunnerID: "runner", WorkDir: t.TempDir(), JournalDir: filepath.Join(t.TempDir(), "journal"), CompleteStatus: "succeeded", Once: true, PollInterval: time.Second, CancelPollInterval: time.Second})
	if err == nil || claims != 1 || completions != 0 {
		t.Fatalf("generic archive completion: %v claims=%d complete=%d", err, claims, completions)
	}
}

func TestStorageArchiveClaimPreservesCloseFailuresWithoutRepeatingSettlement(t *testing.T) {
	for _, mode := range []string{"settled success", "lost completion ACK", "canceled before plan"} {
		t.Run(mode, func(t *testing.T) {
			x, c, e, _ := archiveRuntimeFixture(t)
			closeFailure := errors.New("protected archive engine cleanup failed")
			e.closeErr = closeFailure
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: x.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: x.a.OperationID, HostingMode: x.p.Mode}}, Lease: x.lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: x.a.RunID}}
			reporter := &attemptReporter{journal: x.j, supervisor: x.s, authority: journalAttemptIdentity(x.a.RunID, x.lease, x.s)}
			config := &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}
			switch mode {
			case "lost completion ACK":
				c.lostComplete = true
			case "canceled before plan":
				x.s.cancel()
			}
			err := executeStorageArchiveClaimWithEngine(x.s, x.j, reporter, x.c, claim, config, e)
			if !errors.Is(err, closeFailure) || e.closeCalls != 1 {
				t.Fatalf("engine cleanup failure discarded or close repeated: %v closes=%d", err, e.closeCalls)
			}
			if mode == "lost completion ACK" && !errors.Is(err, errStorageArchiveHeld) {
				t.Fatal("primary uncertainty was replaced by cleanup failure")
			}
			if mode == "canceled before plan" && !errors.Is(err, context.Canceled) {
				t.Fatal("primary cancellation was replaced by cleanup failure")
			}
			if mode == "settled success" && len(x.j.Snapshot().StorageArchive) != 0 {
				t.Fatal("cleanup failure recreated retired journal")
			}
			if mode != "canceled before plan" && (c.op.StorageArchive.Result == nil || c.op.StorageArchive.Result.ArchiveOutcome != "succeeded" || c.op.MutationHold) {
				t.Fatal("cleanup failure relabeled acknowledged server result")
			}
			if mode == "lost completion ACK" && len(x.j.Snapshot().StorageArchive) == 0 {
				t.Fatal("lost completion receipt was discarded")
			}
			effects := slices.Clone(e.effects)
			if mode != "canceled before plan" {
				if replayErr := reconcileStorageArchiveJournal(x.s.Context(), Config{Server: x.c.Server, Token: x.c.Token, RunnerID: x.p.RunnerID, StorageArchive: config}, x.j); replayErr != nil {
					t.Fatal(replayErr)
				}
				if len(x.j.Snapshot().StorageArchive) != 0 {
					t.Fatal("exact completion receipt did not settle")
				}
			}
			if !slices.Equal(effects, e.effects) || e.closeCalls != 1 {
				t.Fatal("cleanup error replay repeated effects or engine cleanup")
			}
		})
	}
}

func TestStorageArchiveRestoreHelperACKFailureRetainsSafeBoundaryDiagnostic(t *testing.T) {
	for _, status := range []int{409, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			x, c, e := archiveRestoreRuntimeFixture(t)
			if status == 409 {
				c.rejectPhase = "helper_identified"
				c.rejectStatus = status
			} else {
				c.lostPhase = "helper_identified"
			}
			err := x.execute(c.op)
			if !errors.Is(err, errStorageArchiveHeld) {
				t.Fatalf("lost restore hold: %v", err)
			}
			var boundary *storageArchiveProgressError
			if !errors.As(err, &boundary) || boundary.stage != "controller" || boundary.class != "http" || boundary.status != status {
				t.Fatalf("missing safe boundary: %v", err)
			}
			if slices.Contains(e.effects, "restore") {
				t.Fatal("extraction ran without helper ACK")
			}
			if c.op.StorageArchive.Result == nil || !c.op.StorageArchive.Result.Uncertain || c.op.StorageArchive.Result.RestoreVerified {
				t.Fatal("ACK failure fabricated verified restore")
			}
			for _, secret := range []string{"private_canary", "127.0.0.1", "http://", "/host/private", "lease", "fence"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic leaked %s", secret)
				}
			}
			found := false
			for _, entry := range x.j.Snapshot().StorageArchive {
				found = found || entry.Progress != nil && entry.Progress.Phase == "helper_identified"
			}
			if !found {
				t.Fatal("failed ACK discarded durable helper evidence")
			}
		})
	}
}
func TestStorageArchiveProgressDiagnosticDoesNotRenderPrivateCauses(t *testing.T) {
	for _, tc := range []struct {
		name, stage, want string
		err               error
		status            int
	}{
		{"journal", "journal", "protocol_or_local", errors.New("/host/private journal failure"), 0},
		{"HTTP", "controller", "http", &runnerclient.APIError{StatusCode: 409, URL: "https://private/fence", Detail: "private_canary", Status: "private_canary"}, 409},
		{"transport_or_response", "controller", "transport_or_response", &runnerclient.APIError{URL: "https://private/fence", Err: errors.New("private_canary")}, 0},
		{"deadline", "controller", "deadline", context.DeadlineExceeded, 0},
		{"cancel", "controller", "canceled", context.Canceled, 0},
		{"invalid", "controller", "invalid_request", domain.ErrInvalidStorageArchive, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := archiveProgressDiagnostic(tc.stage, tc.err)
			var boundary *storageArchiveProgressError
			if !errors.As(err, &boundary) || boundary.class != tc.want || boundary.status != tc.status || !errors.Is(err, tc.err) {
				t.Fatal("diagnostic changed classification", err)
			}
			for _, private := range []string{"private", "https://", "/host/", "fence"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("private cause rendered", err)
				}
			}
		})
	}
}
