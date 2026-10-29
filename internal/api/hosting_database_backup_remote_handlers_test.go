package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type remoteBackupHTTPRepository struct {
	*archiveHTTPRepository
	store.HostingDatabaseBackupRemoteRepository
	target   domain.HostingDatabaseBackupTarget
	transfer domain.HostingDatabaseBackupTransfer
	retry    domain.HostingDatabaseBackupTransferRetryInput
}

func (r *remoteBackupHTTPRepository) SaveDatabaseBackupTarget(ctx context.Context, id, project, actor, existing string, in domain.HostingDatabaseBackupTargetInput, records *store.BackupRemoteCredentialRecords, _ store.BackupRemoteCredentialCompare) (domain.HostingDatabaseBackupTarget, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return r.target, err
	}
	if records == nil || bytes.Contains(records.Secret.Envelope.Data, []byte("fixture-secret-canary")) {
		return r.target, store.ErrConflict
	}
	now := time.Now().UTC()
	r.target = domain.HostingDatabaseBackupTarget{ID: id, ProjectID: project, Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: in.Name, Endpoint: in.Endpoint, Bucket: in.Bucket, Region: in.Region, Enabled: in.Enabled, CreatedAt: now, UpdatedAt: now}
	return r.target, nil
}
func (r *remoteBackupHTTPRepository) ListDatabaseBackupTargets(ctx context.Context, actor, project string) ([]domain.HostingDatabaseBackupTarget, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleViewer); err != nil {
		return nil, err
	}
	if project != r.target.ProjectID {
		return nil, store.ErrNotFound
	}
	return []domain.HostingDatabaseBackupTarget{r.target}, nil
}
func (r *remoteBackupHTTPRepository) ValidateDatabaseBackupTarget(ctx context.Context, id, project, actor, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingDatabaseBackupTransfer, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return r.transfer, err
	}
	if project != r.target.ProjectID || target != r.target.ID || in.ExpectedRevision != r.target.Revision {
		return r.transfer, store.ErrConflict
	}
	now := time.Now().UTC()
	r.transfer = domain.HostingDatabaseBackupTransfer{ID: id, Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: project, TargetID: target, TargetRevision: in.ExpectedRevision, DestinationID: in.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
	return r.transfer, nil
}
func (r *remoteBackupHTTPRepository) ListDatabaseBackupTransfers(ctx context.Context, actor, id string, _ bool) ([]domain.HostingDatabaseBackupTransfer, error) {
	r.calls++
	return []domain.HostingDatabaseBackupTransfer{r.transfer}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *remoteBackupHTTPRepository) GetDatabaseBackupTransfer(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupTransfer, error) {
	r.calls++
	return r.transfer, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *remoteBackupHTTPRepository) RetryDatabaseBackupTransfer(ctx context.Context, id, actor, backup string, input domain.HostingDatabaseBackupTransferRetryInput) (domain.HostingDatabaseBackupTransfer, error) {
	r.calls++
	r.retry = input
	return r.transfer, r.authorize(ctx, actor, domain.RoleOwner)
}

func TestBackupRemoteHTTPPermissionsClosedInputsAndWriteOnlyCredentials(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	repo := &remoteBackupHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	ring, err := secretcrypto.New("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{8}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, SecretKeyring: ring, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/projects/" + repo.resource.ProjectID + "/database-backup-targets"
	body := `{"request_id":"target","expected_revision":0,"name":"Fixture","endpoint":"https://backup.example/","bucket":"backup-fixture","region":"us-east-1","enabled":true,"credentials":{"access_key":"fixture-access-canary","secret_key":"fixture-secret-canary"}}`
	archiveHTTPCall(t, server, "POST", path, "", body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, body, "application/json", 403)
	before := repo.calls
	for _, bad := range []string{`null`, body + `{}`, strings.Replace(body, `"secret_key":`, `"secret_key":"first","secret_key":`, 1), strings.Replace(body, `"enabled":true`, `"enabled":true,"private_network":true`, 1), strings.Replace(body, `"region":"us-east-1"`, `"region":""`, 1), strings.Replace(body, `https://backup.example/`, `https://user:password@backup.example/`, 1)} {
		archiveHTTPCall(t, server, "POST", path, owner, bad, "application/json", 400)
	}
	if repo.calls != before {
		t.Fatal("closed input failed before repository")
	}
	response := archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 200)
	if strings.Contains(response.Body.String(), "canary") || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "key_id") {
		t.Fatal("target response disclosed credentials")
	}
	var target domain.HostingDatabaseBackupTarget
	if json.Unmarshal(response.Body.Bytes(), &target) != nil || target.Endpoint != "https://backup.example/" {
		t.Fatal("saved target changed validated endpoint")
	}
	archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	validationPath := path + "/" + target.ID + "/validations"
	validation := `{"request_id":"validate","expected_revision":1,"destination_id":"destination"}`
	archiveHTTPCall(t, server, "POST", validationPath, viewer, validation, "application/json", 403)
	archiveHTTPCall(t, server, "POST", validationPath, owner, validation, "application/json", 202)
	archiveHTTPCall(t, server, "GET", validationPath, viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", path+"/foreign/validations", viewer, "", "", 404)
	archiveHTTPCall(t, server, "POST", "/api/v1/database-backups/backup/transfers/retry", viewer, `{"request_id":"retry","expected_transfer_id":"transfer"}`, "application/json", 403)
	repo.transfer.Version = domain.HostingDatabaseMySQLBackupTransferVersion
	repo.transfer.Mode = "upload"
	repo.transfer.BackupID = "backup"
	repo.transfer.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 42, Format: "mysql_sql"}
	reply := archiveHTTPCall(t, server, "POST", "/api/v1/database-backups/backup/transfers/retry", owner, `{"request_id":"retry","expected_transfer_id":"transfer"}`, "application/json", 202)
	var retry domain.HostingDatabaseBackupTransfer
	if json.Unmarshal(reply.Body.Bytes(), &retry) != nil || retry.Version != domain.HostingDatabaseMySQLBackupTransferVersion || retry.Blob == nil || retry.Blob.Format != "mysql_sql" || repo.retry.RequestID != "retry" || repo.retry.ExpectedTransferID != "transfer" {
		t.Fatal("MySQL retry wire changed")
	}

}
