package api

import (
	"context"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type databaseDownloadHTTPRepository struct {
	*archiveHTTPRepository
	reads, creates int
}

func (r *databaseDownloadHTTPRepository) DatabaseBackupDownloadOptions(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupDownloadOptions, error) {
	r.reads++
	return domain.HostingDatabaseBackupDownloadOptions{BackupID: id, Available: true, Blob: &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 7, Format: "mysql_sql"}}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *databaseDownloadHTTPRepository) CreateDatabaseBackupDownload(context.Context, string, string, string, time.Time) (domain.HostingDatabaseBackupDownloadPlan, error) {
	r.creates++
	return domain.HostingDatabaseBackupDownloadPlan{}, store.ErrDatabaseDownloadUnavailable
}
func (*databaseDownloadHTTPRepository) DatabaseBackupDownloadPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return domain.HostingDatabaseBackupDownloadPlan{}, store.ErrConflict
}
func (*databaseDownloadHTTPRepository) CheckDatabaseBackupDownload(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return domain.HostingDatabaseBackupDownloadPlan{}, store.ErrConflict
}
func (*databaseDownloadHTTPRepository) BeginDatabaseBackupDownload(context.Context, string, domain.HostingDatabaseBackupDownloadStream) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return domain.HostingDatabaseBackupDownloadPlan{}, store.ErrConflict
}
func (*databaseDownloadHTTPRepository) CloseDatabaseBackupDownload(context.Context, string, bool) error {
	return nil
}

func TestDatabaseDownloadRegisteredCookieOptionsAndClosedInputs(t *testing.T) {
	server, base, owner, viewer, _ := archiveHTTPFixture(t)
	repo := &databaseDownloadHTTPRepository{archiveHTTPRepository: base}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	server.databaseBackupDownloadEnabled = true
	options := "/api/v1/database-backups/backup/archive-options"
	archiveHTTPCall(t, server, "GET", options, owner, "", "", 200)
	archiveHTTPCall(t, server, "GET", options, viewer, "", "", 403)
	cookie := createBrowserSession(t, server)
	request := httptest.NewRequest("GET", options, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cookie options rejected or cached", response.Code)
	}
	for _, path := range []string{options + "?key=unexpected", "/api/v1/database-backups/bad.id/archive-options", "/api/v1/database-backups/backup/archive"} {
		before := repo.reads
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		r.Header.Set("Range", "bytes=0-1")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != 400 || repo.creates != 0 || repo.reads != before {
			t.Fatal("invalid request reached copy/session admission", w.Code)
		}
	}
	for _, path := range []string{"/api/v1/runners/hosting/database-backup-download/plan", databaseDownloadStreamPath} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-NeroCD-CSRF", "1")
		r.Header.Set("Origin", server.publicOrigin)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("browser cookie admitted runner route", w.Code)
		}
	}
}
