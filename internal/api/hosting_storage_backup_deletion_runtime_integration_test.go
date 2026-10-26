//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
)

// This profile extends the existing owned fixture; launching it needs exact
// candidate approval and separate disposable Docker/S3 authority.
func TestHostingStorageBackupDeletionCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_STORAGE_DELETION_RUNTIME") != "disposable-dind" {
		t.Skip("requires separately authorized manual deletion runtime gate")
	}
	TestHostingStorageRemoteCompiledRunnerHTTPS(t)
}
func hostingStorageDeletionRuntimeWorkflow(f *hostingStorageRemoteRuntimeFixture, resource domain.Resource, target string, revision int64, restoredBefore, originalSource []byte, observed *hostingStorageRemoteRuntimeObservation) {
	t := f.t
	t.Helper()
	contents := []byte("manual selected backup retained remote recovery bytes")
	f.https("storage-remote-restored.example.test", "PUT", "/upload", contents, 201)
	f.https("storage-remote-restored.example.test", "POST", "/rename", nil, 204)
	b := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", domain.HostingStorageBackupInput{RequestID: hostingStorageScheduleRuntimeID(t), ExpectedResourceGeneration: resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target, Revision: revision}}, 202)
	f.await(b.OperationID, "succeeded")
	uploaded := hostingStorageRemoteRuntimeAwait(f, b.ID, "upload", "succeeded")
	b = storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+b.ID, nil, 200)
	var local string
	for _, c := range b.Copies {
		if c.Location == "local" && c.Availability == "available" {
			local = c.CopyID
		}
	}
	if local == "" {
		t.Fatal("owned local copy unavailable")
	}
	base := "/api/v1/resources/" + resource.ID + "/storage-backups/" + b.ID
	wire := &hostingStorageDeletionRuntimeWire{resource: resource.ID, backup: b.ID}
	f.deletionWire.Store(wire)
	f.browser.setRetention("deletion", resource, target, []domain.HostingStorageBackup{b}, "")
	browserID := f.browser.await(t, "deletion")
	f.deletionWire.Store(nil)
	wire.mu.Lock()
	p, in, admitted, request, count, failed := wire.preview, wire.executionRequest, wire.execution, wire.previewRequest, wire.executionRequests, wire.failed
	wire.mu.Unlock()
	if failed || count != 2 || admitted.ID != browserID || p.FinalCopy || p.Origin != domain.StorageBackupManualSelection || len(p.Items) != 1 || p.Items[0].CopyID != local || len(request.CopyIDs) != 1 || request.CopyIDs[0] != local || in.PreviewID != p.ID || in.ExpectedPlanHash != p.PlanHash {
		t.Fatal("live browser manual selection/retry differs from exact reviewed plan")
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		v := storageRuntimeJSON[domain.HostingStorageBackupRetentionExecution](f.storageRuntimeFixture, "GET", base+"/deletions/"+admitted.ID, nil, 200)
		if v.State == "succeeded" {
			break
		}
		if v.State == "failed" || v.State == "uncertain" || time.Now().After(deadline) {
			t.Fatal("manual physical deletion not qualified", v)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatal("manual deletion canceled")
		}
	}
	replay := storageRuntimeJSON[domain.HostingStorageBackupRetentionExecution](f.storageRuntimeFixture, "POST", base+"/deletions", in, 202)
	if replay.ID != admitted.ID {
		t.Fatal("replay selected another backup")
	}
	pool, e := artifactfs.OpenStore(filepath.Join(f.root, "archive-artifacts"), domain.HostingStorageArchiveMaxBytes)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	h, _, e := pool.Open(t.Context(), strings.TrimPrefix(b.Blob.SHA256, "sha256:"))
	if e == nil {
		h.Close()
		t.Fatal("local archive remains")
	}
	if !errors.Is(e, os.ErrNotExist) {
		t.Fatal("local absence unproven", e)
	}
	logical := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+b.ID, nil, 200)
	if logical.State != "succeeded" || logical.Blob == nil {
		t.Fatal("backup history changed")
	}
	availableRemote := false
	for _, c := range logical.Copies {
		availableRemote = availableRemote || c.Location == "remote" && c.Availability == "available"
	}
	if !availableRemote {
		t.Fatal("remote copy removed")
	}
	f.browser.setRetention("deletion_history", resource, target, []domain.HostingStorageBackup{b}, admitted.ID)
	if f.browser.await(t, "deletion_history") != admitted.ID {
		t.Fatal("browser manual history/final-copy review missing")
	}
	restored := hostingStorageRemoteRuntimeRestore(f, b.ID, uploaded, revision, "manual deletion remote restored")
	f.await(restored.Operation.ID, "succeeded")
	deployed := f.await(f.deploy(restored.Resource, "manual-deletion-restored-deploy", "/", 19087, nil).ID, "succeeded")
	hostingStorageRemoteRuntimeRoute(f, restored.Resource.ID, "storage-deletion-restored.example.test", deployed.ID)
	if !bytes.Equal(f.https("storage-deletion-restored.example.test", "GET", "/download", nil, 200), contents) {
		t.Fatal("remaining remote archive recovery bytes differ")
	}
	fresh := []byte("manual recovery application new write")
	f.https("storage-deletion-restored.example.test", "PUT", "/upload", fresh, 201)
	f.https("storage-deletion-restored.example.test", "POST", "/rename", nil, 204)
	if !bytes.Equal(f.https("storage-deletion-restored.example.test", "GET", "/download", nil, 200), fresh) || !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), originalSource) {
		t.Fatal("restored volume read/write or source independence failed")
	}
	// After recovery is proven, the same owner selects both remaining physical
	// copies through the live UI and explicitly confirms final-copy deletion.
	finalWire := &hostingStorageDeletionRuntimeWire{resource: resource.ID, backup: b.ID}
	f.deletionWire.Store(finalWire)
	f.browser.setRetention("deletion_final", resource, target, []domain.HostingStorageBackup{b}, "")
	finalBrowserID := f.browser.await(t, "deletion_final")
	f.deletionWire.Store(nil)
	finalWire.mu.Lock()
	finalPreview, finalInput, finalAdmitted, finalCount, finalFailed := finalWire.preview, finalWire.executionRequest, finalWire.execution, finalWire.executionRequests, finalWire.failed
	finalWire.mu.Unlock()
	if finalFailed || finalCount != 2 || finalAdmitted.ID != finalBrowserID || !finalPreview.FinalCopy || !finalInput.ConfirmFinalCopy || len(finalPreview.Items) != 2 {
		t.Fatal("connected final copy confirmation or exact retry missing")
	}
	localSelected, remoteSelected := 0, 0
	for _, c := range finalPreview.Items {
		if c.BackupID != b.ID {
			t.Fatal("final selection escaped backup")
		}
		if c.Location == "local" {
			localSelected++
		} else {
			remoteSelected++
		}
	}
	if localSelected != 1 || remoteSelected != 1 {
		t.Fatal("final selection did not include the owned local generation and recorded remote object")
	}
	finalDeadline := time.Now().Add(90 * time.Second)
	for {
		v := storageRuntimeJSON[domain.HostingStorageBackupRetentionExecution](f.storageRuntimeFixture, "GET", base+"/deletions/"+finalAdmitted.ID, nil, 200)
		if v.State == "succeeded" {
			break
		}
		if v.State == "failed" || v.State == "uncertain" || time.Now().After(finalDeadline) {
			t.Fatal("final manual physical deletion not qualified", v)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatal("final manual deletion canceled")
		}
	}
	observed.mu.Lock()
	remoteDeletes := observed.retentionDeletes["/nerocd-storage-runtime/"+domain.StorageRemoteObjectKey(b.ID, *b.Blob)]
	unconditional := observed.unconditionedDelete
	observed.mu.Unlock()
	if remoteDeletes != 1 || unconditional {
		t.Fatal("manual selected recorded remote object lacks one conditional physical delete", remoteDeletes, unconditional)
	}
	h, _, e = pool.Open(t.Context(), strings.TrimPrefix(b.Blob.SHA256, "sha256:"))
	if e == nil {
		h.Close()
		t.Fatal("final local archive remains")
	}
	if !errors.Is(e, os.ErrNotExist) {
		t.Fatal("final local absence unproven", e)
	}
	finalLogical := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+b.ID, nil, 200)
	if finalLogical.State != "succeeded" || finalLogical.Blob == nil {
		t.Fatal("final deletion erased logical history")
	}
	for _, c := range finalLogical.Copies {
		if c.Availability == "available" {
			t.Fatal("selected final copy still available")
		}
	}
	if !bytes.Equal(f.https("storage-deletion-restored.example.test", "GET", "/download", nil, 200), fresh) || !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), originalSource) {
		t.Fatal("archive deletion changed deployed data volumes")
	}
	f.browser.setRetention("deletion_final_history", resource, target, []domain.HostingStorageBackup{b}, finalAdmitted.ID)
	if f.browser.await(t, "deletion_final_history") != finalAdmitted.ID {
		t.Fatal("final manual browser history missing")
	}
	f.https("storage-remote-restored.example.test", "PUT", "/upload", restoredBefore, 201)
	f.https("storage-remote-restored.example.test", "POST", "/rename", nil, 204)
	raw, _ := json.Marshal(map[string]any{"status": "passed", "case": "ManualDeleteSelectedBackupLocalKeepsRemoteAndHistory", "execution_id": admitted.ID, "final_execution_id": finalAdmitted.ID, "preview_hash": p.PlanHash, "local_absent": true, "remote_retained": true, "history_retained": true, "remaining_remote_restore": true, "new_volume_application_reads_and_writes": true, "source_preserved": true, "manual_remote_conditional_delete": true, "final_copy_confirmation": true, "final_history_retained": true, "browser_manual_selection_exact_retry": true, "browser_history_final_copy_review": true})
	evidence := os.Getenv("NEROCD_STORAGE_REMOTE_EVIDENCE")
	if evidence == "" || os.WriteFile(filepath.Join(evidence, "deletion-markers.json"), append(raw, '\n'), 0600) != nil {
		t.Fatal("manual deletion evidence unavailable")
	}
}
