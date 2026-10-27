package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestStorageArchiveUnresolvedReplayAllowsUnrelatedClaimsAndGuardsSameVolume(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	e.failAt = "create"
	if err := x.execute(c.op); err == nil {
		t.Fatal("unknown helper accepted")
	}
	if !storageArchiveConflict(x.j.Snapshot(), x.p.SourceStorage.Reference(), "", "image_operation", "") {
		t.Fatal("same volume image mutation not guarded")
	}
	if !storageArchiveConflict(x.j.Snapshot(), domain.HostingStorageRef{}, x.p.SourceBindingID(), "image_operation", "") {
		t.Fatal("same binding mutation not guarded")
	}
	if storageArchiveConflict(x.j.Snapshot(), domain.HostingStorageRef{}, "unrelated_binding", "image_operation", "") {
		t.Fatal("unrelated image operation globally blocked")
	}
	// A generic startup replay cannot discard or probe this attempt. Dedicated
	// archive replay retains the uncertainty without preventing unrelated claims.
	authority := journalAttemptIdentity(x.a.RunID, x.lease, x.s)
	if _, err := x.j.AppendEvent(runner.JournalEvent{ID: "archive_event", Attempt: authority, Sequence: 1, Stream: "system", Message: "archive interrupted", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(500) }))
	defer server.Close()
	if err := reconcileRunnerJournalWithCounters(server.URL, "token", x.j, &runnerOperationalCounters{}, context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 || len(x.j.Snapshot().StorageArchive) == 0 {
		t.Fatal("generic replay touched or discarded archive")
	}
}

func archiveRecoveryRuntime(t *testing.T, x *archiveCoordinator, c *archiveRuntimeController, e *archiveRuntimeEngine, required []string) *archiveCoordinator {
	t.Helper()
	p := x.p
	p.OperationID = "fresh_recovery"
	p.Mode = "recover"
	p.RecoveryOfOperationID = x.p.OperationID
	p.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: x.p.Hash, Writers: slices.Clone(e.writers), ResumeRequiredContainerIDs: required}
	for _, r := range x.j.Snapshot().StorageArchive {
		if r.Progress != nil && r.Progress.Phase == "writer_snapshot" {
			p.RecoveryEvidence.SnapshotReceiptID = r.Progress.ReceiptID
		}
	}
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("invalid recovery fixture")
	}
	lease := x.lease
	lease.ID = "fresh_lease"
	lease.Fence = "fresh_fence"
	lease.RunID = "archive_recover_" + p.OperationID
	a := hostingAuthority(lease.RunID, p.OperationID, lease)
	ctx := runnerclient.WithStorageArchiveTransport(context.Background(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
	s := newAttemptSupervisorFrom(ctx, lease)
	t.Cleanup(s.Close)
	c.lease = lease
	c.op = domain.HostingOperation{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: p.SourceResourceID(), BindingID: p.SourceBindingID(), RunID: a.RunID, TaskMode: p.Mode, State: "preparing", StorageArchive: &domain.HostingStorageArchive{Mode: p.Mode, BackupID: p.BackupID, RecoveryOfOperationID: p.RecoveryOfOperationID, Plan: p}}
	e.effects = nil
	e.failAt = ""
	return &archiveCoordinator{s: s, j: x.j, c: x.c, e: e, a: a, lease: lease, p: p}
}

func TestStorageArchiveFreshRecoveryUsesOnlyExactPriorHelperAndNarrowedResumeSet(t *testing.T) {
	for _, required := range [][]string{{"container"}, nil} {
		t.Run(map[bool]string{true: "owed original writer", false: "already resumed subset"}[len(required) > 0], func(t *testing.T) {
			x, c, e, _ := archiveRuntimeFixture(t)
			e.failAt = "cleanup"
			if err := x.execute(c.op); err == nil {
				t.Fatal("failed helper cleanup accepted")
			}
			recovery := archiveRecoveryRuntime(t, x, c, e, required)
			if err := recovery.execute(c.op); err != nil {
				t.Fatal(err)
			}
			want := []string{"cleanup"}
			if len(required) > 0 {
				want = append(want, "resume:container")
			}
			if !slices.Equal(e.effects, want) {
				t.Fatalf("recovery effects %v", e.effects)
			}
			for _, effect := range e.effects {
				if effect == "create" || effect == "backup" || effect == "restore" || effect == "resume:stopped" || strings.HasPrefix(effect, "stop:") {
					t.Fatalf("recovery exceeded authority %v", e.effects)
				}
			}
			if len(x.j.Snapshot().StorageArchive) != 0 {
				t.Fatal("exact settled recovery retained root chain")
			}
		})
	}
}

func TestStorageArchiveRecoveryRefusesMissingIdentityAndMissingCurrentLease(t *testing.T) {
	for _, missingID := range []bool{true, false} {
		t.Run(map[bool]string{true: "unknown helper", false: "expired lease"}[missingID], func(t *testing.T) {
			x, c, e, _ := archiveRuntimeFixture(t)
			if missingID {
				e.failAt = "create"
			} else {
				e.failAt = "cleanup"
			}
			_ = x.execute(c.op)
			recovery := archiveRecoveryRuntime(t, x, c, e, []string{"container"})
			if !missingID {
				c.lease.ExpiresAt = time.Now().Add(-time.Second)
			}
			if err := recovery.execute(c.op); err == nil {
				t.Fatal("unproven recovery accepted")
			}
			if len(e.effects) != 0 {
				t.Fatalf("unproven recovery effects %v", e.effects)
			}
			if len(x.j.Snapshot().StorageArchive) == 0 {
				t.Fatal("uncertainty discarded")
			}
		})
	}
}

func TestStorageArchiveRecoveryRejectsReintroducedResumedObligation(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	c.lostPhase = "resumed"
	_ = x.execute(c.op)
	recovery := archiveRecoveryRuntime(t, x, c, e, []string{"container"})
	if _, _, err := storageArchiveRecoverySource(x.j.Snapshot(), recovery.p); err == nil {
		t.Fatal("durably resumed writer reintroduced")
	}
	recovery.p.RecoveryEvidence.ResumeRequiredContainerIDs = nil
	recovery.p.Hash = ""
	recovery.p.Hash = domain.HostingDocumentHash(recovery.p)
	if _, _, err := storageArchiveRecoverySource(x.j.Snapshot(), recovery.p); err != nil {
		t.Fatal("narrowed current obligations refused")
	}
}

func TestStorageArchivePreparedReplayHasNoDaemonOrGenericReady(t *testing.T) {
	x, c, _, _ := archiveRuntimeFixture(t)
	entry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "prepared", "prepared")
	entry.Prepared = &domain.HostingStorageArchivePreparedRequest{HostingAuthority: x.a, ReceiptID: entry.ID, PlanHash: x.p.Hash, Helper: x.p.Helper}
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		t.Fatal(err)
	}
	config := Config{Server: x.c.Server, Token: x.c.Token, RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}
	if err := reconcileStorageArchiveJournal(x.s.Context(), config, x.j); err != nil {
		t.Fatal(err)
	}
	if c.op.State != "prepared" || len(x.j.Snapshot().StorageArchive) != 1 {
		t.Fatal("prepared replay proceeded into effects")
	}
	if err := validateHostingPlanResponse(c.op, x.a, x.p.RunnerID, "deploy"); err == nil {
		t.Fatal("archive sidecar entered image coordinator")
	}
	raw, err := json.Marshal(c.op)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private_canary_bearer", "\"lease_id\"", "\"fence\"", "\"ready\""} {
		if strings.Contains(string(raw), private) {
			t.Fatal("public archive evidence leaked private authority or generic readiness")
		}
	}
}

func TestStorageArchiveRecoveryRetirementSurvivesTwoInterruptionsAndLostACK(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	e.failAt = "cleanup"
	_ = x.execute(c.op)
	first := archiveRecoveryRuntime(t, x, c, e, []string{"container"})
	e.failAt = "resume:container"
	if err := first.execute(c.op); err == nil {
		t.Fatal("first interrupted recovery accepted")
	}
	second := archiveRecoveryRuntime(t, x, c, e, []string{"container"})
	second.p.OperationID = "second_recovery"
	second.p.Hash = ""
	second.p.Hash = domain.HostingDocumentHash(second.p)
	second.a.OperationID = second.p.OperationID
	second.a.RunID = "archive_recover_" + second.p.OperationID
	second.a.LeaseID = "second_lease"
	second.a.Fence = "second_fence"
	second.lease.ID = second.a.LeaseID
	second.lease.Fence = second.a.Fence
	second.lease.RunID = second.a.RunID
	c.lease = second.lease
	c.op.ID = second.p.OperationID
	c.op.RunID = second.a.RunID
	c.op.State = "preparing"
	c.op.MutationHold = false
	c.op.StorageArchive = &domain.HostingStorageArchive{Mode: "recover", BackupID: second.p.BackupID, RecoveryOfOperationID: x.p.OperationID, Plan: second.p}
	c.lostComplete = true
	if err := second.execute(c.op); err == nil {
		t.Fatal("lost recovery completion ACK accepted")
	}
	before := slices.Clone(e.effects)
	if c.op.CompleteReceipt == nil {
		t.Fatal("fixture lost committed completion")
	}
	deleted := 0
	err := retireStorageArchiveRecoveryWithAck(x.j, second.a, second.p, *c.op.CompleteReceipt, func(id string) error {
		deleted++
		if deleted == 3 {
			return errors.New("simulated retirement interruption")
		}
		return x.j.AckStorageArchive(id)
	})
	if err == nil {
		t.Fatal("retirement interruption ignored")
	}
	currentComplete := false
	settlement := false
	for _, r := range x.j.Snapshot().StorageArchive {
		if r.Authority() == second.a {
			currentComplete = currentComplete || r.Complete != nil
			settlement = settlement || r.RetirementReason == "fresh_recovery_settled"
		}
	}
	if !currentComplete || !settlement {
		t.Fatal("interrupted retirement lost current receipt/reason")
	}
	config := Config{Server: second.c.Server, Token: second.c.Token, RunnerID: second.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: second.p.Helper, TrustedLoopbackHTTP: true}}
	if err = reconcileStorageArchiveJournal(second.s.Context(), config, x.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, e.effects) || len(x.j.Snapshot().StorageArchive) != 0 {
		t.Fatal("metadata retirement replay reran effects or retained root chain")
	}
}

func archiveForMountedStorage(t *testing.T, f *mountedApplyFixture) domain.HostingStorageArchivePlan {
	t.Helper()
	f.plan.Snapshot.ServerID, f.plan.Snapshot.RunnerID = "server", "runner"
	compiled, err := domain.CompileHostingOwnedStoragePlan(f.plan.Snapshot, f.plan.Release, nil, nil, *f.plan.Storage)
	if err != nil {
		t.Fatal(err)
	}
	f.plan = compiled
	f.request.OriginPlanHash = compiled.Hash
	p := archiveRuntimePlan(t)
	p.ProjectID = f.request.Storage.ProjectID
	p.ServerID = f.plan.Snapshot.ServerID
	p.DestinationID = f.request.Storage.DestinationID
	p.RunnerID = f.plan.Snapshot.RunnerID
	p.SourceStorage = f.request.Storage
	p.SourceMaterialization = domain.HostingStorageMaterialization{StorageRef: f.request.Storage.Reference(), State: "materialized", CreationOperationID: f.request.OriginOperationID, CreationPlanHash: f.request.OriginPlanHash, CreationNonce: f.request.CreationNonce, DaemonID: f.request.DaemonID, MaterializedReceipt: &domain.HostingReceipt{ID: "prior_materialization", BodyHash: f.request.OriginPlanHash, CreatedAt: time.Now().UTC()}}
	p.SourceMaterialization.Labels = domain.ExpectedHostingStorageLabels(p.SourceStorage, p.SourceMaterialization)
	p.SourceHealthy = domain.HostingHealthy{Snapshot: f.plan.Snapshot, Release: f.plan.Release, OperationID: f.request.OriginOperationID, ContainerID: strings.Repeat("a", 64), PlanHash: f.request.OriginPlanHash, Storage: &domain.HostingHealthyStorage{Plan: *f.plan.Storage, CreationPlanHash: f.request.OriginPlanHash}}
	p.Writers = []domain.HostingHealthy{p.SourceHealthy}
	p.Helper.DaemonID = f.request.DaemonID
	p.Helper.Hash = ""
	p.Helper.Hash = domain.HostingDocumentHash(p.Helper)
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		t.Fatal("mounted archive fixture invalid")
	}
	return p
}

func TestStorageArchiveGuardCoversActualPreparedImageAndStartupWithoutGlobalBlock(t *testing.T) {
	for _, conflicting := range []bool{true, false} {
		t.Run(map[bool]string{true: "same volume", false: "unrelated volume"}[conflicting], func(t *testing.T) {
			f := freshStorageCoordinatorFixture(t)
			j, _ := openStorageCoordinatorJournal(t)
			p := archiveRuntimePlan(t)
			if conflicting {
				p = archiveForMountedStorage(t, f)
			}
			c := newStorageCoordinatorController(t, j, f)
			authority := runner.AttemptIdentity{RunID: "backup_" + p.OperationID, LeaseID: "archive_lease", Attempt: 1, Fence: "archive_fence", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
			request := domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: domain.HostingAuthority{OperationID: p.OperationID, RunID: authority.RunID, LeaseID: authority.LeaseID, Attempt: authority.Attempt, Fence: authority.Fence}, ReceiptID: "retained_archive", PlanHash: p.Hash}
			if _, err := j.AppendStorageArchive(runner.JournalStorageArchive{ID: "retained_archive", Attempt: authority, Plan: p, Phase: "apply_intent", ApplyBegin: &request, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			intent := runner.JournalHosting{ID: "image_intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: &f.plan, StorageRef: f.request.Storage.Reference(), CreatedAt: time.Now().UTC()}
			if _, err := j.AppendHosting(intent); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			opens := 0
			reached := errors.New("unrelated image engine reached")
			err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", context.Background(), func(string) (hostingStorageEngine, error) { opens++; return nil, reached })
			if conflicting {
				if err != nil || opens != 0 {
					t.Fatalf("conflicting startup opened engine: %v opens=%d", err, opens)
				}
				s := newAttemptSupervisorFrom(context.Background(), c.lease)
				defer s.Close()
				if err = executeHostingStoragePrepared(s, j, nil, runnerclient.New(server.URL, "token"), nil, c.a, c.lease, c.op); !errors.Is(err, errStorageArchiveHeld) {
					t.Fatalf("live prepared storage bypassed guard: %v", err)
				}
				if len(j.Snapshot().Hosting) != 1 {
					t.Fatal("conflicting image evidence discarded")
				}
			} else if !errors.Is(err, reached) || opens != 1 {
				t.Fatalf("unrelated startup blocked: %v opens=%d", err, opens)
			}
		})
	}
}

func TestStorageArchiveTerminalPreparedACKRetiresOnlyProvenPreEffectAttempt(t *testing.T) {
	for _, mode := range []string{"canceled", "failed", "changed plan", "changed authority", "changed receipt", "missing receipt", "mutation hold", "restore hold", "apply intent", "writer snapshot", "unverified helper", "storage create", "404", "409", "503", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			x, c, _, _ := archiveRuntimeFixture(t)
			if mode == "storage create" {
				x, c, _ = archiveRestoreRuntimeFixture(t)
			}
			planEntry := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "plan", "plan")
			if _, err := x.j.AppendStorageArchive(planEntry); err != nil {
				t.Fatal(err)
			}
			prepared := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "prepared", "prepared")
			req := domain.HostingStorageArchivePreparedRequest{HostingAuthority: x.a, ReceiptID: prepared.ID, PlanHash: x.p.Hash, Helper: x.p.Helper}
			prepared.Prepared = &req
			if _, err := x.j.AppendStorageArchive(prepared); err != nil {
				t.Fatal(err)
			}
			effect := archiveRecord(x.j, x.a, x.lease, x.s, x.p, "plan", "effect")
			switch mode {
			case "apply intent":
				effect.Phase = "apply_intent"
				effect.ApplyBegin = &domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: x.a, ReceiptID: effect.ID, PlanHash: x.p.Hash}
			case "writer snapshot":
				effect.Phase = "progress"
				effect.Progress = &domain.HostingStorageArchiveProgress{HostingAuthority: x.a, ReceiptID: effect.ID, PlanHash: x.p.Hash, Phase: "writer_snapshot", Writers: []domain.HostingStorageArchiveWriterState{}}
				for i, h := range x.p.Writers {
					effect.Progress.Writers = append(effect.Progress.Writers, domain.HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash, Running: i == 0})
				}
			case "unverified helper":
				effect.Phase = "helper_unverified"
				effect.UncertainHelperContainerID = strings.Repeat("e", 64)
			case "storage create":
				effect.Phase = "storage_create_intent"
				effect.StorageCreate = &domain.HostingStorageCreateRequest{HostingAuthority: x.a, PlanHash: x.p.Hash, StorageRef: x.p.Target.Storage.Reference(), CreationNonce: x.p.Target.Plan.CreationNonce}
			}
			if effect.Phase != "plan" {
				if _, err := x.j.AppendStorageArchive(effect); err != nil {
					t.Fatal(err)
				}
			}
			op := c.op
			op.State = "canceled"
			op.StorageArchive.PreparedReceipt = &domain.HostingReceipt{ID: req.ReceiptID, BodyHash: domain.HostingDocumentHash(req), CreatedAt: time.Now().UTC()}
			switch mode {
			case "failed":
				op.State = "failed"
			case "changed plan":
				op.StorageArchive.Plan.Limits.Members--
				op.StorageArchive.Plan.Hash = ""
				op.StorageArchive.Plan.Hash = domain.HostingDocumentHash(op.StorageArchive.Plan)
			case "changed authority":
				op.RunID = "backup_foreign"
			case "changed receipt":
				op.StorageArchive.PreparedReceipt.BodyHash = x.p.Hash
			case "missing receipt":
				op.StorageArchive.PreparedReceipt = nil
			case "mutation hold":
				op.MutationHold = true
			case "restore hold":
				op.RestoreOwnershipHeld = true
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/prepared") {
					w.WriteHeader(http.StatusConflict)
					return
				}
				switch mode {
				case "404":
					w.WriteHeader(http.StatusNotFound)
					return
				case "409":
					w.WriteHeader(http.StatusConflict)
					return
				case "503":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
					return
				}
				_ = json.NewEncoder(w).Encode(op)
			}))
			defer server.Close()
			ctx := x.s.Context()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			_ = reconcileStorageArchiveJournal(ctx, Config{Server: server.URL, Token: "token", RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}, x.j)
			retired := len(x.j.Snapshot().StorageArchive) == 0
			if want := mode == "canceled" || mode == "failed"; retired != want {
				t.Fatalf("retirement=%v want=%v", retired, want)
			}
		})
	}
}

func TestStorageArchivePlanOnlyRetainsEvidenceWithoutInventingUncertainEffects(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	old := x.p
	old.OperationID = "read_only_old"
	old.Hash = ""
	old.Hash = domain.HostingDocumentHash(old)
	a := runner.AttemptIdentity{RunID: "backup_" + old.OperationID, LeaseID: "old_lease", Attempt: 1, Fence: "old_fence", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
	entry := runner.JournalStorageArchive{ID: "old_plan", Attempt: a, Plan: old, Phase: "plan", CreatedAt: time.Now().UTC()}
	if _, err := x.j.AppendStorageArchive(entry); err != nil {
		t.Fatal(err)
	}
	config := Config{Server: x.c.Server, Token: x.c.Token, RunnerID: x.p.RunnerID, StorageArchive: &StorageArchiveExecutionConfig{Helper: x.p.Helper, TrustedLoopbackHTTP: true}}
	if err := reconcileStorageArchiveJournal(x.s.Context(), config, x.j); err != nil {
		t.Fatal(err)
	}
	if len(e.effects) != 0 || len(c.requests) != 0 || len(x.j.Snapshot().StorageArchive) != 1 {
		t.Fatal("plan-only startup resumed work, inferred ACK, or discarded evidence")
	}
	if storageArchiveConflict(x.j.Snapshot(), old.SourceStorage.Reference(), old.SourceStorage.BindingID, x.p.OperationID, "") {
		t.Fatal("read-only plan was classified as uncertain mutation")
	}
	if err := x.execute(c.op); err != nil {
		t.Fatal("fresh controller-authorized operation blocked", err)
	}
	if len(x.j.Snapshot().StorageArchive) != 1 || x.j.Snapshot().StorageArchive[0].ID != entry.ID {
		t.Fatal("fresh settlement erased old plan-only evidence")
	}
	for _, phase := range []string{"prepared", "apply_intent", "storage_create_intent", "storage_materialized", "progress", "helper_unverified", "complete", "recovery_settlement", "unknown"} {
		changed := entry
		changed.Phase = phase
		snapshot := runner.JournalSnapshot{StorageArchive: []runner.JournalStorageArchive{entry, changed}}
		if !storageArchiveConflict(snapshot, old.SourceStorage.Reference(), old.SourceStorage.BindingID, "new_operation", "") {
			t.Fatalf("possible-effect/unknown phase excluded: %s", phase)
		}
	}
}
