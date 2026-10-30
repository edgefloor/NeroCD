package api

import (
	"bytes"
	"context"
	"io"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type databaseDownloadSettlementRepository struct {
	*databaseDownloadHTTPRepository
	mu      sync.Mutex
	plan    domain.HostingDatabaseBackupDownloadPlan
	created chan domain.HostingDatabaseBackupDownloadPlan
	closed  atomic.Bool
}

func (r *databaseDownloadSettlementRepository) DatabaseBackupDownloadOptions(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupDownloadOptions, error) {
	return domain.HostingDatabaseBackupDownloadOptions{BackupID: id, Available: true, Blob: &r.plan.Blob}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *databaseDownloadSettlementRepository) CreateDatabaseBackupDownload(ctx context.Context, id, actor, backup string, expires time.Time) (domain.HostingDatabaseBackupDownloadPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.plan
	p.SessionID = id
	p.ActorID = actor
	p.BackupID = backup
	p.ExpiresAt = expires
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	r.plan = p
	r.created <- p
	return p, nil
}
func (r *databaseDownloadSettlementRepository) DatabaseBackupDownloadPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plan, nil
}
func (r *databaseDownloadSettlementRepository) BeginDatabaseBackupDownload(ctx context.Context, runner string, in domain.HostingDatabaseBackupDownloadStream) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return r.DatabaseBackupDownloadPlan(ctx, runner, in.HostingAuthority)
}
func (r *databaseDownloadSettlementRepository) CloseDatabaseBackupDownload(context.Context, string, bool) error {
	r.closed.Store(true)
	return store.ErrConflict
}

func TestDatabaseDownloadDurableCloseFailureRefusesSuccessACK(t *testing.T) {
	server, base, owner, _, runnerToken := archiveHTTPFixture(t)
	repo := &databaseDownloadSettlementRepository{databaseDownloadHTTPRepository: &databaseDownloadHTTPRepository{archiveHTTPRepository: base}, created: make(chan domain.HostingDatabaseBackupDownloadPlan, 1)}
	repo.plan = domain.HostingDatabaseBackupDownloadPlan{Version: domain.HostingDatabaseBackupDownloadVersion, BackupVersion: domain.HostingDatabaseMySQLBackupVersion, ServerID: "server", ProjectID: base.resource.ProjectID, ResourceID: base.resource.ID, BindingID: "binding", CopyID: "copy", RunnerID: "runner_archive", DestinationID: "destination", DaemonID: "daemon", Blob: databaseDownloadBlob([]byte("archive")), LocalCopy: domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("b", 64), Device: 1, Inode: 2, Bytes: 7, Created: true}}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	server.databaseBackupDownloadEnabled = true
	server.trustedLoopbackRuntimeEnvironmentHTTP = true
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	browserDone := make(chan error, 1)
	go func() {
		req, e := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/database-backups/backup/archive", nil)
		if e != nil {
			browserDone <- e
			return
		}
		req.Header.Set("Authorization", "Bearer "+owner)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			browserDone <- e
			return
		}
		data, e := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if e == nil && (res.StatusCode != 200 || !bytes.Equal(data, []byte("archive"))) {
			e = store.ErrConflict
		}
		browserDone <- e
	}()
	var plan domain.HostingDatabaseBackupDownloadPlan
	select {
	case plan = <-repo.created:
	case <-ctx.Done():
		t.Fatal("browser did not admit download")
	}
	in := domain.HostingDatabaseBackupDownloadStream{HostingAuthority: domain.HostingAuthority{OperationID: plan.SessionID, RunID: "db_transfer_" + plan.SessionID, LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: plan.Hash}
	client := runnerclient.New(httpServer.URL, runnerToken)
	err = client.StreamDatabaseBackupDownload(runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true}), in, plan, bytes.NewReader([]byte("archive")))
	if e := <-browserDone; e != nil {
		t.Fatal("browser did not receive exact bytes before settlement failure", e)
	}
	if !repo.closed.Load() || err == nil {
		t.Fatal("failed durable settlement acknowledged a successful stream")
	}
}
