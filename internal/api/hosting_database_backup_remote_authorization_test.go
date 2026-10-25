package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/web"
)

// This uses the real HTTP middleware, service authorization, encrypted records,
// migrations and transactional runner admission. Runner receipts model effects;
// no Docker or S3 execution is claimed by this controller regression.
func TestPostgresBackupRemoteHTTPProjectIsolation(t *testing.T) {
	f := newCaptureHTTPFixture(t)
	ctx := f.admin
	if _, err := f.pool.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, []string{domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability}); err != nil {
		t.Fatal(err)
	}
	source := f.source(t, "postgresql")
	server := NewServer(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	tokens := map[string]string{}
	for _, role := range []string{domain.RoleOwner, domain.RoleViewer} {
		account, err := f.service.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "remote " + role, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = account.Token
	}
	foreign, err := f.service.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: "proj_security", Name: "foreign remote owner", Role: domain.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	tokens["foreign"] = foreign.Token
	call := func(method, path, role string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		defer clear(raw)
		out := archiveHTTPCall(t, server, method, path, tokens[role], string(raw), "application/json", want)
		for _, canary := range []string{"http-access-canary", "http-secret-canary", f.token} {
			if strings.Contains(out.Body.String(), canary) {
				t.Fatal("HTTP response disclosed credential")
			}
		}
		return out
	}
	base := "/api/v1/projects/proj_platform/database-backup-targets"
	input := map[string]any{"request_id": "http_target", "expected_revision": 0, "name": "HTTP target", "endpoint": "https://backup.example", "bucket": "http-fixture", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": "http-access-canary", "secret_key": "http-secret-canary"}}
	for _, role := range []string{"anonymous", "foreign", domain.RoleViewer} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("POST", base, role, input, want)
	}
	out := call("POST", base, domain.RoleOwner, input, 200)
	var target domain.HostingDatabaseBackupTarget
	if json.Unmarshal(out.Body.Bytes(), &target) != nil || target.ID == "" {
		t.Fatal("missing saved target")
	}
	edit := map[string]any{"request_id": "http_edit", "expected_revision": 1, "name": "HTTP target edited", "endpoint": target.Endpoint, "bucket": target.Bucket, "region": target.Region, "enabled": true}
	for _, role := range []string{"anonymous", "foreign", domain.RoleViewer} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("PUT", base+"/"+target.ID, role, edit, want)
	}
	for _, role := range []string{"anonymous", "foreign"} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("GET", base, role, nil, want)
	}
	call("GET", base, domain.RoleViewer, nil, 200)
	validationPath := base + "/" + target.ID + "/validations"
	validation := domain.HostingDatabaseBackupTargetValidationInput{RequestID: "http_validation", ExpectedRevision: 1, DestinationID: f.destination}
	for _, role := range []string{"anonymous", "foreign", domain.RoleViewer} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("POST", validationPath, role, validation, want)
	}
	out = call("POST", validationPath, domain.RoleOwner, validation, 202)
	var transfer domain.HostingDatabaseBackupTransfer
	if json.Unmarshal(out.Body.Bytes(), &transfer) != nil {
		t.Fatal("validation decode")
	}
	settle := func(id, failure string) domain.HostingDatabaseBackupTransfer {
		t.Helper()
		if err := f.pg.PrepareDatabaseBackupTransfer(ctx, id); err != nil {
			t.Fatal(err)
		}
		claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
		if err != nil || claim.Run.RunSpec.HostingOperationID != id {
			t.Fatal("transfer claim", err)
		}
		a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		p, err := f.pg.DatabaseBackupTransferPlan(ctx, f.runner, a)
		if err != nil {
			t.Fatal(err)
		}
		in := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "intent_" + id, PlanHash: p.Hash, Phase: "intent", DaemonID: source.Release.DaemonID}
		if _, err = f.pg.DatabaseBackupTransferMutation(ctx, f.runner, in); err != nil {
			t.Fatal(err)
		}
		in.ReceiptID = "result_" + id
		in.Phase = "complete"
		if failure != "" {
			in.Phase = "failed"
			in.FailureCode = failure
		}
		result, err := f.pg.DatabaseBackupTransferMutation(ctx, f.runner, in)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	settle(transfer.ID, "")
	for _, path := range []string{validationPath, "/api/v1/database-backup-transfers/" + transfer.ID} {
		for _, role := range []string{"anonymous", "foreign"} {
			want := 403
			if role == "anonymous" {
				want = 401
			}
			call("GET", path, role, nil, want)
		}
		call("GET", path, domain.RoleViewer, nil, 200)
	}
	// A valid foreign ID must not be usable under another project's URL.
	call("GET", "/api/v1/projects/proj_security/database-backup-targets/"+target.ID+"/validations", "foreign", nil, 404)
	backupInput := domain.HostingDatabaseBackupInput{RequestID: "http_backup", ExpectedResourceGeneration: source.Snapshot.ResourceGeneration, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}}
	out = call("POST", "/api/v1/resources/"+source.ResourceID+"/database-backups", domain.RoleOwner, backupInput, 202)
	var backup domain.HostingDatabaseBackupOperation
	if json.Unmarshal(out.Body.Bytes(), &backup) != nil || backup.ID == "" {
		t.Fatal("backup decode")
	}
	if err = f.pg.PrepareDatabaseBackup(ctx, backup.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != backup.ID {
		t.Fatal("backup claim", err)
	}
	a := domain.HostingAuthority{OperationID: backup.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	p, err := f.pg.DatabaseBackupPlan(ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	mutation := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "backup_intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "backup_complete"
	mutation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: "postgresql_custom"}
	if _, err = f.pg.DatabaseBackupMutation(ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	listPath := "/api/v1/database-backups/" + backup.ID + "/transfers"
	out = call("GET", listPath, domain.RoleViewer, nil, 200)
	var transfers []domain.HostingDatabaseBackupTransfer
	if json.Unmarshal(out.Body.Bytes(), &transfers) != nil || len(transfers) != 1 {
		t.Fatal("upload not admitted once")
	}
	failed := settle(transfers[0].ID, "access_denied")
	counts := func() string {
		t.Helper()
		var result string
		if err := f.pool.QueryRow(ctx, `SELECT concat((SELECT count(*) FROM hosting_database_backup_transfers),':',(SELECT count(*) FROM hosting_database_backups),':',(SELECT count(*) FROM resources))`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := counts()
	retry := domain.HostingDatabaseBackupTransferRetryInput{RequestID: "http_retry", ExpectedTransferID: failed.ID}
	restore := domain.HostingDatabaseBackupRestoreInput{RequestID: "http_restore", Name: "HTTP restored database", RemoteSource: &domain.BackupRemoteSource{TransferID: failed.ID, ExpectedTargetRevision: 1}}
	for _, role := range []string{"anonymous", "foreign", domain.RoleViewer} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("POST", listPath+"/retry", role, retry, want)
		call("POST", "/api/v1/database-backups/"+backup.ID+"/restores", role, restore, want)
	}
	for _, role := range []string{"anonymous", "foreign"} {
		want := 403
		if role == "anonymous" {
			want = 401
		}
		call("GET", listPath, role, nil, want)
		call("GET", "/api/v1/database-backup-transfers/"+failed.ID, role, nil, want)
	}
	if counts() != before {
		t.Fatal("denied HTTP request changed transfer/backup/resource rows")
	}
	out = call("POST", listPath+"/retry", domain.RoleOwner, retry, 202)
	if json.Unmarshal(out.Body.Bytes(), &transfer) != nil {
		t.Fatal("retry decode")
	}
	succeeded := settle(transfer.ID, "")
	restore.RemoteSource.TransferID = succeeded.ID
	// Denial must also hold with an otherwise valid successful remote source.
	for _, role := range []string{"foreign", domain.RoleViewer} {
		call("POST", "/api/v1/database-backups/"+backup.ID+"/restores", role, restore, 403)
	}
	out = call("POST", "/api/v1/database-backups/"+backup.ID+"/restores", domain.RoleOwner, restore, 202)
	var restored domain.HostingDatabaseBackupOperation
	if json.Unmarshal(out.Body.Bytes(), &restored) != nil || restored.TargetResourceID == "" || restored.TargetResourceID == source.ResourceID || !restored.OwnershipHeld {
		t.Fatal("restore did not create distinct held target")
	}
	call("PUT", base+"/"+target.ID, domain.RoleOwner, edit, 200)
}
