package runner

import (
	"bytes"
	"encoding/json"
	"nerocd/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func archiveJournalPlan(t *testing.T) domain.HostingStorageArchivePlan {
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

func archiveJournalEntry(t *testing.T) JournalStorageArchive {
	t.Helper()
	plan := archiveJournalPlan(t)
	now := time.Now().UTC()
	return JournalStorageArchive{ID: "archive_plan", Phase: "plan", Plan: plan, CreatedAt: now, Attempt: AttemptIdentity{RunID: "backup_" + plan.OperationID, LeaseID: "lease", Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
}

func TestStorageArchiveJournalRejectsGenericCompletionAndPhaseSmuggling(t *testing.T) {
	e := archiveJournalEntry(t)
	j, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = j.AppendStorageArchive(e); err != nil {
		t.Fatal(err)
	}
	if _, err = j.AppendCompletion(JournalCompletion{ID: "generic_complete", Attempt: e.Attempt, Status: "succeeded", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("generic completion mixed into archive attempt")
	}
	if len(j.Snapshot().Completions) != 0 {
		t.Fatal("failed generic completion remained in memory")
	}
	state := domain.HostingStorageArchiveWriterState{ContainerID: e.Plan.Writers[0].ContainerID, OperationID: e.Plan.Writers[0].OperationID, ImageID: e.Plan.Writers[0].Release.ImageID, PlanHash: e.Plan.Writers[0].PlanHash, ReleaseHash: e.Plan.Writers[0].Release.Hash, Running: true}
	for _, phase := range []string{"writer_snapshot", "stop_intent", "helper_intent", "helper_identified", "archive_published", "restore_verified"} {
		bad := e
		bad.ID = "bad_" + phase
		bad.Phase = "progress"
		bad.Progress = &domain.HostingStorageArchiveProgress{HostingAuthority: e.Authority(), ReceiptID: bad.ID, PlanHash: e.Plan.Hash, Phase: phase, Writers: []domain.HostingStorageArchiveWriterState{state}, HelperContainerID: strings.Repeat("e", 64), RestoreVerified: true}
		if _, err = j.AppendStorageArchive(bad); err == nil {
			t.Fatalf("mixed phase payload accepted: %s", phase)
		}
	}
	unknown := e
	unknown.ID = "unverified"
	unknown.Phase = "helper_unverified"
	unknown.UncertainHelperContainerID = strings.Repeat("e", 64)
	if _, err = j.AppendStorageArchive(unknown); err != nil {
		t.Fatal("exact uncertain ID was not retained privately")
	}
	if _, err = j.AppendStorageArchive(JournalStorageArchive{ID: "false_settlement", Attempt: e.Attempt, Plan: e.Plan, Phase: "recovery_settlement", RecoverySettlement: &domain.HostingReceipt{ID: "invented", BodyHash: e.Plan.Hash, CreatedAt: time.Now().UTC()}, RetirementReason: "fresh_recovery_settled", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("unproven settlement accepted")
	}
}
func TestStorageArchiveJournalV12RestartStrictnessAndDowngrade(t *testing.T) {
	entry := archiveJournalEntry(t)
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, old := range databaseJournalFixture(t) {
		if _, err := j.AppendHosting(old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.AppendStorageArchive(entry); err != nil {
		t.Fatal(err)
	}
	snapshot := j.Snapshot()
	snapshot.StorageArchive[0].Plan.SourceMaterialization.Labels["private"] = "canary"
	if _, ok := j.Snapshot().StorageArchive[0].Plan.SourceMaterialization.Labels["private"]; ok {
		t.Fatal("snapshot aliases journal")
	}
	if err := j.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("generic discard lost archive uncertainty")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"version":12`)) || bytes.Contains(raw, []byte("canary")) {
		t.Fatal("archive format or copy boundary")
	}
	for index, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"version":12`), []byte(`"version":11`), 1),
		bytes.Replace(raw, []byte(`"storage_archive":`), []byte(`"Storage_Archive":`), 1),
		bytes.Replace(raw, []byte(`"storage_archive":`), []byte(`"storage_archive":[],"storage_archive":`), 1),
		bytes.Replace(raw, []byte(`"phase":"plan"`), []byte(`"phase":"plan","create_permitted":true`), 1),
	} {
		var state journalState
		if decodeStorageArchiveJournal(bad, &state) == nil && validateJournalState(state) == nil {
			t.Fatalf("ambiguous or downgraded archive accepted: %d", index)
		}
	}
	j, err = OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().StorageArchive) != 1 {
		t.Fatal("restart lost archive evidence")
	}
	if err := j.AckStorageArchive(entry.ID); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(root, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"version":11`)) {
		t.Fatal("retained database must stay v11")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().StorageArchive) != 0 || len(j.Snapshot().Hosting) == 0 {
		t.Fatal("downgrade lost legacy database evidence")
	}
}
func TestStorageArchiveJournalRejectsMixedChangedAndSecretDocuments(t *testing.T) {
	entry := archiveJournalEntry(t)
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	a := entry.Authority()
	prepared := entry
	prepared.ID = "prepared"
	prepared.Phase = "prepared"
	prepared.Prepared = &domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: prepared.ID, PlanHash: entry.Plan.Hash, Helper: entry.Plan.Helper}
	if _, err = j.AppendStorageArchive(prepared); err != nil {
		t.Fatal(err)
	}
	prepared.Prepared.Helper.DaemonID = "foreign"
	if _, err = j.AppendStorageArchive(prepared); err == nil {
		t.Fatal("changed identity accepted")
	}
	if _, err = j.AppendStorageArchive(entry); err != nil {
		t.Fatal(err)
	}
	changed := entry
	changed.ID = "new_plan"
	changed.Plan.Limits.Members++
	changed.Plan.Hash = ""
	changed.Plan.Hash = domain.HostingDocumentHash(changed.Plan)
	if _, err = j.AppendStorageArchive(changed); err == nil {
		t.Fatal("same attempt changed immutable plan")
	}
	bad := entry
	bad.ID = "mixed"
	bad.Prepared = &domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "mixed", PlanHash: entry.Plan.Hash, Helper: entry.Plan.Helper}
	if _, err = j.AppendStorageArchive(bad); err == nil {
		t.Fatal("payload in wrong phase")
	}
	raw, err := json.Marshal(j.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"bearer_canary", "artifact_bytes_canary", "/private/host/archive", "create_permitted"} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("private data serialized")
		}
	}
	for version := 1; version <= 11; version++ {
		if validateJournalState(journalState{Version: version, StorageArchive: []JournalStorageArchive{entry}}) == nil {
			t.Fatal("old format accepted archive")
		}
	}
}
func TestStorageArchiveRegistryNeverBuildsGenericPrimitives(t *testing.T) {
	for _, mode := range []struct{ mode, prefix string }{{"backup", "backup_"}, {"restore", "restore_"}, {"recover", "archive_recover_"}} {
		run := domain.TaskRun{ID: mode.prefix + "op", RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingMode: mode.mode, HostingOperationID: "op"}}
		p, err := NewRegistry().BuildPlan(run)
		if err != nil || p.RunID != run.ID || p.Process != nil || p.Checkout != nil || len(p.Secrets) != 0 || len(p.Artifacts) != 0 {
			t.Fatal("archive entered generic primitives")
		}
		run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"sh", "-c", "false"}}
		if _, err := NewRegistry().BuildPlan(run); err == nil {
			t.Fatal("generic process in archive")
		}
	}
}

func TestStorageArchiveJournalUsesCompletePhaseInventories(t *testing.T) {
	entry := archiveJournalEntry(t)
	other := entry.Plan.Writers[0]
	other.ContainerID = "originally_stopped"
	entry.Plan.Writers = append(entry.Plan.Writers, other)
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
	var writers []domain.HostingStorageArchiveWriterState
	for i, w := range entry.Plan.Writers {
		writers = append(writers, domain.HostingStorageArchiveWriterState{ContainerID: w.ContainerID, OperationID: w.OperationID, ImageID: w.Release.ImageID, ReleaseHash: w.Release.Hash, PlanHash: w.PlanHash, Running: i == 0})
	}
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "resume_intent", "resumed"} {
		t.Run(phase, func(t *testing.T) {
			e := entry
			e.ID = "batch_" + phase
			e.Phase = "progress"
			states := append([]domain.HostingStorageArchiveWriterState(nil), writers...)
			if phase == "stopped" {
				for i := range states {
					states[i].Running = false
				}
			}
			e.Progress = &domain.HostingStorageArchiveProgress{HostingAuthority: e.Authority(), ReceiptID: e.ID, PlanHash: e.Plan.Hash, Phase: phase, Writers: states}
			if err := validateJournalStorageArchive(e); err != nil {
				t.Fatal("complete controller phase inventory refused", err)
			}
			e.Progress.Writers = states[:1]
			if validateJournalStorageArchive(e) == nil {
				t.Fatal("old per-writer archive schema silently accepted")
			}
			if phase == "stopped" {
				e.Progress.Writers = writers
				if validateJournalStorageArchive(e) == nil {
					t.Fatal("stopped receipt retained Running=true")
				}
			}
		})
	}
	// Recovery uses the original full inventory while authority remains narrowed.
	entry.Plan.Mode = "recover"
	entry.Plan.RecoveryOfOperationID = entry.Plan.OperationID
	entry.Plan.OperationID = "recovery_batch"
	entry.Plan.RecoveryEvidence = &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: entry.Plan.Hash, SnapshotReceiptID: "snapshot", Writers: writers, ResumeRequiredContainerIDs: []string{writers[0].ContainerID}}
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
	entry.Attempt.RunID = "archive_recover_" + entry.Plan.OperationID
	entry.ID = "recovery_resumed"
	entry.Phase = "progress"
	entry.Progress = &domain.HostingStorageArchiveProgress{HostingAuthority: entry.Authority(), ReceiptID: entry.ID, PlanHash: entry.Plan.Hash, Phase: "resumed", Writers: writers}
	if err := validateJournalStorageArchive(entry); err != nil {
		t.Fatal("full recovery snapshot refused", err)
	}
	changed := append([]domain.HostingStorageArchiveWriterState(nil), writers...)
	changed[1].Running = true
	entry.Progress.Writers = changed
	if validateJournalStorageArchive(entry) == nil {
		t.Fatal("recovery changed originally-stopped observation")
	}
}

func TestStorageArchiveJournalRejectsUnacceptedPerWriterSchemaWithoutRewriting(t *testing.T) {
	entry := archiveJournalEntry(t)
	state := domain.HostingStorageArchiveWriterState{ContainerID: entry.Plan.Writers[0].ContainerID, OperationID: entry.Plan.Writers[0].OperationID, ImageID: entry.Plan.Writers[0].Release.ImageID, ReleaseHash: entry.Plan.Writers[0].Release.Hash, PlanHash: entry.Plan.Writers[0].PlanHash, Running: true}
	entry.Phase = "progress"
	entry.ID = "old_stop_first"
	entry.Progress = &domain.HostingStorageArchiveProgress{HostingAuthority: entry.Authority(), ReceiptID: entry.ID, PlanHash: entry.Plan.Hash, Phase: "stop_intent", Writers: []domain.HostingStorageArchiveWriterState{state}}
	duplicate := cloneStorageArchiveEntry(entry)
	duplicate.ID = "old_stop_second"
	duplicate.Progress.ReceiptID = duplicate.ID
	raw, err := json.Marshal(journalState{Version: 12, StorageArchive: []JournalStorageArchive{entry, duplicate}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenAttemptJournal(root); err == nil {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("ambiguous unaccepted v12 writer phases opened")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Fatal("refused archive evidence was rewritten or discarded")
	}
}
