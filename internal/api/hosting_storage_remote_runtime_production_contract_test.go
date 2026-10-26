//go:build nerocd_storage_remote_contract

package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

// The standalone acceptance commit builds without the product dependency. This
// integration-only tag makes missing production routes and wire changes fail.
func TestHostingStorageRemoteProductionContract(t *testing.T) {
	required := map[PublicRoute]bool{}
	for _, route := range []PublicRoute{
		{Method: "GET", Path: "/api/v1/storage-backups/{id}/transfers"},
		{Method: "POST", Path: "/api/v1/storage-backups/{id}/transfers/retry"},
		{Method: "GET", Path: "/api/v1/storage-backup-transfers/{id}"},
		{Method: "GET", Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations"},
		{Method: "POST", Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations"},
		{Method: "POST", Path: "/api/v1/runners/hosting/storage-backup-transfer/plan"},
		{Method: "POST", Path: "/api/v1/runners/hosting/storage-backup-transfer/mutation"},
		{Method: "POST", Path: "/api/v1/runners/hosting/storage-backup-transfer/credentials"},
	} {
		required[route] = false
	}
	for _, route := range publicRoutes {
		if _, ok := required[route]; ok {
			required[route] = true
		}
	}
	for route, found := range required {
		if !found {
			t.Errorf("required production route missing: %s %s", route.Method, route.Path)
		}
	}
	var backup domain.HostingStorageBackupInput
	if err := json.Unmarshal([]byte(`{"request_id":"`+strings.Repeat("a", 32)+`","expected_resource_generation":2,"remote_target":{"target_id":"target_storage","revision":3}}`), &backup); err != nil || backup.Validate() != nil || backup.RemoteTarget == nil || backup.RemoteTarget.TargetID != "target_storage" || backup.RemoteTarget.Revision != 3 {
		t.Fatal("fixture backup request incompatible with production", err)
	}
	t.Run("remote restore public request", testStorageRemoteRuntimeRestoreRequest)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	blob := domain.HostingStorageArchiveBlob{BackupID: "backup_storage", DestinationID: "destination_storage", DaemonID: "daemon_storage", SHA256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 4096, Format: domain.HostingStorageArchiveFormat}
	production := domain.HostingStorageBackupTransfer{ID: "transfer_storage", Version: domain.HostingStorageBackupTransferVersion, ProjectID: "proj_platform", TargetID: "target_storage", TargetRevision: 3, DestinationID: blob.DestinationID, Mode: "download", BackupID: blob.BackupID, RestoreOperationID: "restore_storage", State: "failed", FailureCode: "object_mismatch", Blob: &blob, CreatedAt: now, UpdatedAt: now}
	if production.Validate() != nil {
		t.Fatal("production fixture transfer invalid")
	}
	raw, err := json.Marshal(production)
	if err != nil {
		t.Fatal(err)
	}
	var fixture hostingStorageRemoteRuntimeTransfer
	if json.Unmarshal(raw, &fixture) != nil || fixture.ID != production.ID || fixture.BackupID != production.BackupID || fixture.RestoreOperationID != production.RestoreOperationID || fixture.TargetRevision != production.TargetRevision || fixture.FailureCode != production.FailureCode || !reflect.DeepEqual(fixture.Blob, production.Blob) {
		t.Fatal("runtime fixture cannot decode production transfer")
	}
	want := "nerocd/storage/" + blob.BackupID + "/" + strings.Repeat("a", 64) + ".tar"
	if domain.StorageRemoteObjectKey(blob.BackupID, blob) != want || domain.RunTypeStorageBackupTransfer != "storage_backup_transfer" || domain.HostingStorageBackupRemoteCapability != "hosting_storage_remote_backup_v1" {
		t.Fatal("fixture remote storage family differs from production")
	}
}
