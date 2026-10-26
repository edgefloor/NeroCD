package api

import (
	"context"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

type storageDeletionHTTPRepository struct {
	*storageRetentionHTTPRepository
	backup string
}

func (r *storageDeletionHTTPRepository) PreviewStorageBackupDeletion(ctx context.Context, id, actor, resource, backup string, in domain.HostingStorageBackupDeletionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	r.calls++
	r.backup = backup
	return domain.HostingStorageBackupRetentionPreview{ID: id, BackupID: backup}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *storageDeletionHTTPRepository) ExecuteStorageBackupDeletion(ctx context.Context, id, actor, resource, backup string, in domain.HostingStorageBackupDeletionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	r.backup = backup
	return domain.HostingStorageBackupRetentionExecution{ID: id, BackupID: backup}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *storageDeletionHTTPRepository) ListStorageBackupDeletions(ctx context.Context, actor, resource, backup string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	r.backup = backup
	return []domain.HostingStorageBackupRetentionExecution{}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *storageDeletionHTTPRepository) GetStorageBackupDeletion(ctx context.Context, actor, resource, backup, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	r.backup = backup
	return domain.HostingStorageBackupRetentionExecution{ID: id, BackupID: backup}, r.authorize(ctx, actor, domain.RoleViewer)
}
func TestStorageBackupDeletionHTTPPermissionsAndClosedInputs(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "application"
	repo := &storageDeletionHTTPRepository{storageRetentionHTTPRepository: &storageRetentionHTTPRepository{archiveHTTPRepository: original}}
	mem := repo.MemoryStore
	service, e := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if e != nil {
		t.Fatal(e)
	}
	server.app = service
	path := "/api/v1/resources/" + repo.resource.ID + "/storage-backups/backup/deletion-previews"
	valid := `{"request_id":"preview","expected_resource_generation":1,"copy_ids":["copy"]}`
	archiveHTTPCall(t, server, "POST", path, "", valid, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, valid, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, valid, "application/json", 403)
	for _, body := range []string{`{"request_id":"preview","expected_resource_generation":1,"copy_ids":[]}`, `{"request_id":"preview","expected_resource_generation":1,"copy_ids":["copy","copy"]}`, `{"request_id":"preview","expected_resource_generation":1,"copy_ids":["copy"],"object_key":"foreign"}`, `{"request_id":"preview","request_id":"other","expected_resource_generation":1,"copy_ids":["copy"]}`} {
		before := repo.calls
		archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 400)
		if before != repo.calls {
			t.Fatal("invalid body reached persistence")
		}
	}
	response := archiveHTTPCall(t, server, "POST", path, owner, valid, "application/json", 200)
	if repo.backup != "backup" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing scope/cache")
	}
	archiveHTTPCall(t, server, "GET", "/api/v1/resources/"+repo.resource.ID+"/storage-backups/backup/deletions", viewer, "", "", 200)
}
