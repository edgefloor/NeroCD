package store_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// This controller/typed-client reproduction retains the failed upload and bad
// downloads, then crosses the real restore receipt boundary without Docker.
func TestPostgresStorageRemoteRetriedDownloadRestoreHelperACK(t *testing.T) {
	f, target := storageRemoteControlFixture(t)
	b := storageRetentionBackupTarget(t, f, 120, true, &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision})
	uploads, err := f.service.ListStorageBackupTransfers(f.owner, b.ID, false)
	if err != nil || len(uploads) != 1 {
		t.Fatal(err)
	}
	failed := settleStorageRemote(t, f, uploads[0].ID, "access_denied")
	retry, err := f.service.RetryStorageBackupTransfer(f.owner, b.ID, domain.HostingStorageBackupTransferRetryInput{RequestID: "diagnose_upload_retry", ExpectedTransferID: failed.ID})
	if err != nil {
		t.Fatal(err)
	}
	a, transfer := claimStorageRemote(t, f, retry.ID)
	m := domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "diagnose_upload_intent", PlanHash: transfer.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
	if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); err != nil {
		t.Fatal(err)
	}
	m.Phase = "complete"
	m.ReceiptID = "diagnose_upload_complete"
	m.RemoteCopy = &domain.BackupRemoteCopyProof{ETag: `"owned-diagnosis-object"`, Created: true}
	uploaded, err := f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m)
	if err != nil {
		t.Fatal(err)
	}
	var restored domain.HostingStorageRestore
	var download domain.HostingStorageBackupTransfer
	for _, request := range []string{"8", "9"} {
		restored, err = f.service.RequestStorageRestore(f.owner, b.ID, domain.HostingStorageRestoreInput{RequestID: strings.Repeat(request, 32), Name: "diagnostic held restore " + request, RemoteSource: &domain.BackupRemoteSource{TransferID: uploaded.ID, ExpectedTargetRevision: target.Revision}})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err = f.db.QueryRow(f.ctx, `SELECT id FROM hosting_storage_backup_transfers WHERE restore_operation_id=$1`, restored.Operation.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		download = settleStorageRemote(t, f, id, "object_mismatch")
	}
	retry, err = f.service.RetryStorageBackupTransfer(f.owner, b.ID, domain.HostingStorageBackupTransferRetryInput{RequestID: "diagnose_download_retry", ExpectedTransferID: download.ID})
	if err != nil {
		t.Fatal(err)
	}
	a, transfer = claimStorageRemote(t, f, retry.ID)
	m = domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "diagnose_download_intent", PlanHash: transfer.Hash, Phase: "intent", DaemonID: f.helper.DaemonID}
	if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); err != nil {
		t.Fatal(err)
	}
	m.Phase = "complete"
	m.ReceiptID = "diagnose_download_complete"
	m.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 121, Bytes: b.Blob.SizeBytes, Created: false}
	if _, err = f.pg.StorageBackupTransferMutation(f.ctx, f.runner, m); err != nil {
		t.Fatal(err)
	}
	op, a := f.claim(t, restored.Operation.ID, "restore")
	p := op.StorageArchive.Plan
	ack, err := f.pg.HostingStorageCreateBegin(f.ctx, f.runner, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: p.Hash, StorageRef: restored.Storage.Reference(), CreationNonce: p.Target.Plan.CreationNonce})
	if err != nil {
		t.Fatal(err)
	}
	proof := archiveMaterializedProof(a, p.Hash, restored.Storage, ack.Materialization, "diagnose_materialized")
	if _, err = f.pg.HostingStorageMaterialized(f.ctx, f.runner, proof); err != nil {
		t.Fatal(err)
	}
	op = f.begin(t, op, a)
	op = f.progress(t, op, a, "helper_intent", nil, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/hosting/storage-archive/progress" {
			t.Error("unexpected diagnostic path")
			w.WriteHeader(404)
			return
		}
		var in domain.HostingStorageArchiveProgress
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		out, err := f.service.RunnerStorageArchiveProgress(f.runnerContext, in)
		if err != nil {
			t.Errorf("real helper ACK store boundary: %T %v", err, err)
			w.WriteHeader(409)
			return
		}
		if err = json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ctx := runnerclient.WithStorageArchiveTransport(f.ctx, runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
	request := domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "diagnose_helper_identified", PlanHash: p.Hash, Phase: "helper_identified", HelperContainerID: strings.Repeat("01", 32)}
	for range 2 {
		op, err = runnerclient.New(server.URL, "diagnostic_token").HostingStorageArchiveProgress(ctx, request)
		if err != nil {
			t.Fatalf("typed helper ACK boundary: %T %v", err, err)
		}
	}
	f.progress(t, op, a, "restore_verified", nil, p.Blob)
	final, err := f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "diagnose_restore_complete", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "not_needed", Blob: p.Blob, RestoreVerified: true}})
	if err != nil || final.State != "succeeded" || final.MutationHold || final.RestoreOwnershipHeld {
		t.Fatal("restore did not retain verified receipt chain", final.State, err)
	}
	if restored.Storage.VolumeName == f.storage.VolumeName {
		t.Fatal("restore reused source volume")
	}
}
