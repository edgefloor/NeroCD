//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"nerocd/internal/artifactfs"
	"nerocd/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The retention mode retains the accepted remote workflow and adds real local
// unlink, conditional S3 deletion, exact preview replay and remote restoration.
func TestHostingStorageBackupRetentionCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_STORAGE_RETENTION_RUNTIME") != "disposable-dind" {
		t.Skip("requires separately authorized owned retention runtime gate")
	}
	TestHostingStorageRemoteCompiledRunnerHTTPS(t)
}
func storageRetentionRuntimeAwait(f *hostingStorageRemoteRuntimeFixture, resource, id string) domain.HostingStorageBackupRetentionExecution {
	f.t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		v := storageRuntimeJSON[domain.HostingStorageBackupRetentionExecution](f.storageRuntimeFixture, "GET", "/api/v1/resources/"+resource+"/storage-backup-retention/executions/"+id, nil, 200)
		if v.State == "succeeded" {
			return v
		}
		if v.State == "failed" || v.State == "uncertain" {
			f.t.Fatalf("retention physical result not qualified: %+v", v)
		}
		select {
		case <-deadline.C:
			f.t.Fatal("retention physical completion timed out")
		case <-ticker.C:
		case <-f.t.Context().Done():
			f.t.Fatal("retention physical context canceled")
		}
	}
}
func hostingStorageRetentionRuntimeWorkflow(f *hostingStorageRemoteRuntimeFixture, _ *hostingStorageRemoteRuntimeSilo, observed *hostingStorageRemoteRuntimeObservation, resource domain.Resource, target string, revision int64, restoredBefore, originalSource []byte) {
	t := f.t
	t.Helper()
	base := "/api/v1/resources/" + resource.ID + "/storage-backup-retention"
	backups := []domain.HostingStorageBackup{}
	uploads := []hostingStorageRemoteRuntimeTransfer{}
	contents := [][]byte{}
	for i := range 3 {
		body := bytes.Repeat([]byte{byte(73 + i)}, 65537+i)
		contents = append(contents, body)
		f.https("storage-remote-restored.example.test", "PUT", "/upload", body, 201)
		f.https("storage-remote-restored.example.test", "POST", "/rename", nil, 204)
		backup := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", domain.HostingStorageBackupInput{RequestID: hostingStorageScheduleRuntimeID(t), ExpectedResourceGeneration: resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target, Revision: revision}}, 202)
		f.await(backup.OperationID, "succeeded")
		backup = storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup.ID, nil, 200)
		backups = append(backups, backup)
		uploads = append(uploads, hostingStorageRemoteRuntimeAwait(f, backup.ID, "upload", "succeeded"))
	}
	wire := &hostingStorageRetentionRuntimeWire{resource: resource.ID}
	f.retentionWire.Store(wire)
	f.browser.setRetention("retention", resource, target, backups, "")
	browserExecution := f.browser.await(t, "retention")
	f.retentionWire.Store(nil)
	wire.mu.Lock()
	request, preview, execute, admitted, requests, wireFailed := wire.previewRequest, wire.preview, wire.executionRequest, wire.execution, wire.executionRequests, wire.failed
	wire.mu.Unlock()
	if wireFailed || requests != 2 || admitted.ID != browserExecution || execute.PreviewID != preview.ID || execute.ExpectedPlanHash != preview.PlanHash {
		t.Fatal("real browser retention replay did not preserve exact request and preview")
	}
	policy := storageRuntimeJSON[domain.HostingStorageBackupRetentionPolicy](f.storageRuntimeFixture, "GET", base, nil, 200)
	if !policy.Enabled || policy.Automatic || policy.Local != (domain.BackupRetentionLimits{KeepCount: 1}) || policy.Remote != (domain.BackupRetentionLimits{KeepCount: 2}) {
		t.Fatal("browser did not save independent resource retention limits")
	}
	replay := storageRuntimeJSON[domain.HostingStorageBackupRetentionPreview](f.storageRuntimeFixture, "POST", base+"/previews", request, 200)
	if domain.HostingDocumentHash(preview) != domain.HostingDocumentHash(replay) {
		t.Fatal("physical preview replay selected another batch")
	}
	local, remote := 0, 0
	for _, copy := range preview.Items {
		if copy.Selected {
			if copy.Location == "local" {
				local++
			} else {
				remote++
			}
		}
	}
	if local != 2 || remote != 1 {
		t.Fatalf("independent physical retention selection local=%d remote=%d items=%+v", local, remote, preview.Items)
	}
	settled := storageRetentionRuntimeAwait(f, resource.ID, admitted.ID)
	repeated := storageRuntimeJSON[domain.HostingStorageBackupRetentionExecution](f.storageRuntimeFixture, "POST", base+"/executions", execute, 202)
	if repeated.ID != settled.ID {
		t.Fatal("physical execution replay chose another batch")
	}
	pool, err := artifactfs.OpenStore(filepath.Join(f.root, "archive-artifacts"), domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for i, backup := range backups {
		handle, _, err := pool.Open(t.Context(), strings.TrimPrefix(backup.Blob.SHA256, "sha256:"))
		if i < 2 {
			if err == nil {
				handle.Close()
				t.Fatal("selected local copy remains")
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("selected local copy absence is unproven: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal("retained local copy missing")
			}
			handle.Close()
		}
		logical := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup.ID, nil, 200)
		if logical.State != "succeeded" || logical.Blob == nil {
			t.Fatal("retention rewrote archive success")
		}
	}
	observed.mu.Lock()
	deleted := observed.retentionDeletes["/nerocd-storage-runtime/"+domain.StorageRemoteObjectKey(backups[0].ID, *backups[0].Blob)]
	unconditional := observed.unconditionedDelete
	observed.mu.Unlock()
	if deleted != 1 || unconditional {
		t.Fatal("exact conditional S3 deletion not observed", deleted, unconditional)
	}
	// The middle backup retained its remote copy; restoration must republish a new
	// owned local generation and deliver it to an independent application volume.
	restored := hostingStorageRemoteRuntimeRestore(f, backups[1].ID, uploads[1], revision, "retained remote storage restore")
	f.await(restored.Operation.ID, "succeeded")
	deployed := f.await(f.deploy(restored.Resource, "retention-restored-deploy", "/", 19086, nil).ID, "succeeded")
	hostingStorageRemoteRuntimeRoute(f, restored.Resource.ID, "storage-retention-restored.example.test", deployed.ID)
	if !bytes.Equal(f.https("storage-retention-restored.example.test", "GET", "/download", nil, 200), contents[1]) {
		t.Fatal("application did not read retained remote bytes")
	}
	fresh := []byte("retention restored application new writes")
	f.https("storage-retention-restored.example.test", "PUT", "/upload", fresh, 201)
	f.https("storage-retention-restored.example.test", "POST", "/rename", nil, 204)
	if !bytes.Equal(f.https("storage-retention-restored.example.test", "GET", "/download", nil, 200), fresh) || !bytes.Equal(f.https("storage-remote-restored.example.test", "GET", "/download", nil, 200), contents[2]) || !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), originalSource) {
		t.Fatal("retained restore writes changed source")
	}
	current := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backups[1].ID, nil, 200)
	republished := false
	for _, copy := range current.Copies {
		republished = republished || copy.Location == "local" && copy.Availability == "available" && strings.HasPrefix(copy.CopyID, "sbc_download_")
	}
	if !republished {
		t.Fatal("remote restore did not publish local generation")
	}
	// Restore this preexisting fixture application's upload content after the
	// additional scenario. The source application above was never changed.
	f.https("storage-remote-restored.example.test", "PUT", "/upload", restoredBefore, 201)
	f.https("storage-remote-restored.example.test", "POST", "/rename", nil, 204)
	f.browser.setRetention("retention_history", resource, target, backups, settled.ID)
	if f.browser.await(t, "retention_history") != settled.ID {
		t.Fatal("browser did not verify retention history and copy availability")
	}
	evidence := os.Getenv("NEROCD_STORAGE_REMOTE_EVIDENCE")
	raw, _ := json.Marshal(map[string]any{"status": "passed", "execution_id": settled.ID, "preview_hash": preview.PlanHash, "local_deleted": local, "remote_deleted": remote, "conditional_s3_delete": true, "remote_restore_republished_local": true, "application_reads_and_new_writes": true, "source_preserved": true, "browser_policy_preview_execute_replay": true, "browser_history_and_copy_availability": true})
	if evidence == "" || os.WriteFile(filepath.Join(evidence, "retention-markers.json"), append(raw, '\n'), 0600) != nil {
		t.Fatal("retention safe evidence unavailable")
	}
	t.Log(fmt.Sprintf("physical retention batch=%s local=%d remote=%d source_preserved=true", settled.ID, local, remote))
}

func (b *hostingStorageRemoteRuntimeBrowser) setRetention(phase string, resource domain.Resource, target string, backups []domain.HostingStorageBackup, execution string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.resource, b.target, b.backup = phase, resource.ID, target, backups[0].ID
	b.access, b.secret = "", ""
	b.environment, b.source, b.recoveryURL = "", "", ""
	ids := make([]string, len(backups))
	for i, backup := range backups {
		ids[i] = backup.ID
	}
	b.retentionName, b.retentionBackups, b.retentionExecution = resource.Name, strings.Join(ids, ","), execution
}
