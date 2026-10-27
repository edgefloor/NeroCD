package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/runnerclient"
	"nerocd/web"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func storageDownloadFixture(t *testing.T) (archiveControlFixture, domain.HostingStorageBackup) {
	t.Helper()
	f := storageRetentionFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupDownloadCapability); err != nil {
		t.Fatal(err)
	}
	b := storageRetentionBackup(t, f, 10, true)
	registered, err := f.service.RotateRunnerToken(f.owner, app.RunnerTokenInput{RunnerID: f.runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runner_enrollments SET credential_hash=(SELECT token_hash FROM runners WHERE id=$1) WHERE runner_id=$1 AND revoked_at IS NULL`, f.runner); err != nil {
		t.Fatal(err)
	}
	_ = registered
	f.runnerContext = storageDownloadRunnerContext(t, f)
	return f, b
}
func storageDownloadRunnerContext(t *testing.T, f archiveControlFixture) context.Context {
	t.Helper()
	var hash string
	if err := f.db.QueryRow(f.ctx, `SELECT token_hash FROM runners WHERE id=$1`, f.runner).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	ctx, err := store.BindHostingDatabaseConnectionRunnerContext(auth.WithPrincipal(f.ctx, auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner}), f.runner, hash)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}
func TestPostgresStorageDownloadAuthorityAndRetention(t *testing.T) {
	f, b := storageDownloadFixture(t)
	options, err := f.service.StorageBackupDownloadOptions(f.owner, b.ID)
	if err != nil || !options.Available || options.Blob == nil || *options.Blob != *b.Blob {
		t.Fatal("eligible archive", options, err)
	}
	deletionCapability(t, f)
	preview := deletionPreview(t, f, b, []string{b.Copies[0].CopyID})
	p, err := f.service.CreateStorageBackupDownload(f.owner, "download_one", b.ID, time.Now().Add(time.Minute))
	if err != nil || p.Validate() != nil {
		t.Fatal("admit", err)
	}
	if p.LocalCopy.Inode != 11 || p.CopyID == "" {
		t.Fatal("wrong original copy proof")
	}
	if _, err = f.service.ExecuteStorageBackupDeletion(f.owner, f.resource.ID, b.ID, domain.HostingStorageBackupDeletionInput{RequestID: "delete_during_download", PreviewID: preview.ID, ExpectedPlanHash: preview.PlanHash, ConfirmFinalCopy: preview.FinalCopy}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("active download did not fence previously reviewed deletion", err)
	}
	claim, err := f.service.ClaimRun(f.runnerContext)
	if err != nil || claim.Run.ID != "storage_transfer_"+p.SessionID || claim.PrimitivePlan.RunID != claim.Run.ID {
		t.Fatal("ordinary claim", err)
	}
	a := domain.HostingAuthority{OperationID: p.SessionID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	actual, err := f.service.RunnerStorageBackupDownloadPlan(f.runnerContext, a)
	if err != nil || actual != p {
		t.Fatal("current full lease", err)
	}
	stale := a
	stale.Fence = "wrong"
	if _, err = f.service.RunnerStorageBackupDownloadPlan(f.runnerContext, stale); err == nil {
		t.Fatal("stale fence admitted")
	}
	in := domain.HostingStorageBackupDownloadStream{HostingAuthority: a, PlanHash: p.Hash}
	if _, err = f.service.BeginStorageBackupDownload(f.runnerContext, in); err != nil {
		t.Fatal("begin", err)
	}
	if _, err = f.service.BeginStorageBackupDownload(f.runnerContext, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("duplicate byte stream admitted", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, p.DestinationID, p.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RunnerStorageBackupDownloadPlan(f.runnerContext, a); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked grant retained authority", err)
	}
	if err = f.service.CloseStorageBackupDownload(f.ctx, p.SessionID, false); err != nil {
		t.Fatal("close", err)
	}
	if _, err = f.service.RunnerStorageBackupDownloadPlan(f.runnerContext, a); err == nil {
		t.Fatal("closed session replay")
	}
}
func TestPostgresStorageDownloadRejectsUnprovenAndExpired(t *testing.T) {
	f, b := storageDownloadFixture(t)
	unproven := storageRetentionBackup(t, f, 11, false)
	options, err := f.service.StorageBackupDownloadOptions(f.owner, unproven.ID)
	if err != nil || options.Available || options.ReasonCode != "local_copy_unavailable" {
		t.Fatal("unproven local copy", options, err)
	}
	if _, err = f.service.CreateStorageBackupDownload(f.owner, "download_expired", b.ID, time.Now().Add(-time.Second)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("expired admission", err)
	}
	p, err := f.service.CreateStorageBackupDownload(f.owner, "download_expiring", b.ID, time.Now().Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, err = f.service.ClaimRun(f.runnerContext); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired session claimed", err)
	}
	var state string
	if err = f.db.QueryRow(f.ctx, `SELECT status FROM task_runs WHERE id=$1`, "storage_transfer_"+p.SessionID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("expired read was not retired", state, err)
	}
}

func TestPostgresStorageDownloadHTTPAuthorityAndDeadline(t *testing.T) {
	f, _ := storageDownloadFixture(t)
	data := bytes.Repeat([]byte("download archive bytes"), 4000)
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("8", 32), ExpectedResourceGeneration: f.resource.Generation})
	if err != nil {
		t.Fatal(err)
	}
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	digest := sha256.Sum256(data)
	blob := &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(data)), Format: domain.HostingStorageArchiveFormat}
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "resume_intent", "resumed"} {
		var states []domain.HostingStorageArchiveWriterState
		switch phase {
		case "writer_snapshot", "stop_intent", "resume_intent", "resumed":
			states = archiveStates(p, true)
		case "stopped":
			states = archiveStates(p, false)
		}
		if phase == "archive_published" {
			_, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "publish_http", PlanHash: p.Hash, Phase: phase, Blob: blob, LocalCopy: &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 88, Bytes: blob.SizeBytes, Created: true}})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			op = f.progress(t, op, a, phase, states, nil)
		}
	}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete_http", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed", Blob: blob}}); err != nil {
		t.Fatal(err)
	}
	_, ownerToken := proxyLegacyToken(t, f.pg, f.service, "tok_download_http", []string{domain.RoleSystemAdmin})
	registered, err := f.service.RotateRunnerToken(f.owner, app.RunnerTokenInput{RunnerID: f.runner})
	if err != nil {
		t.Fatal(err)
	}
	// The destination fixture stores an opaque synthetic enrollment credential;
	// bind its real HTTP token to the same enrolled runner for this transport test.
	if _, err = f.db.Exec(f.ctx, `UPDATE runner_enrollments SET credential_hash=(SELECT token_hash FROM runners WHERE id=$1) WHERE runner_id=$1 AND revoked_at IS NULL`, f.runner); err != nil {
		t.Fatal(err)
	}
	f.runnerContext = storageDownloadRunnerContext(t, f)
	server := httptest.NewUnstartedServer(api.NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), api.ServerConfig{StorageBackupDownload: true, TrustedLoopbackRuntimeEnvironmentHTTP: true}))
	server.Config.ReadTimeout = 80 * time.Millisecond
	server.Config.WriteTimeout = 80 * time.Millisecond
	server.Start()
	defer server.Close()
	type result struct {
		status  int
		body    []byte
		length  int64
		content string
		err     error
	}
	runBrowser := func(ctx context.Context) <-chan result {
		done := make(chan result, 1)
		go func() {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/storage-backups/"+b.ID+"/archive", nil)
			request.Header.Set("Authorization", "Bearer "+ownerToken)
			response, e := server.Client().Do(request)
			if e != nil {
				done <- result{err: e}
				return
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			body, e := io.ReadAll(response.Body)
			done <- result{status: response.StatusCode, body: body, length: response.ContentLength, content: response.Header.Get("Content-Disposition"), err: e}
		}()
		return done
	}
	var activeBrowser <-chan result
	waitSession := func(previous string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case got := <-activeBrowser:
				t.Fatalf("browser exited before dispatch: status=%d body=%s err=%v", got.status, got.body, got.err)
			default:
			}
			var id string
			e := f.db.QueryRow(f.ctx, `SELECT id FROM hosting_storage_backup_downloads WHERE backup_id=$1 AND id<>$2 AND state='waiting' ORDER BY created_at DESC LIMIT 1`, b.ID, previous).Scan(&id)
			if e == nil {
				return id
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("HTTP session not admitted")
		return ""
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/storage-backups/"+b.ID+"/archive-options", nil)
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	response, e := server.Client().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(response.Body)
	if e := response.Body.Close(); e != nil {
		t.Fatal(e)
	}
	if e != nil || response.StatusCode != 200 {
		t.Fatalf("HTTP eligibility status=%d body=%s err=%v", response.StatusCode, raw, e)
	}
	browser := runBrowser(t.Context())
	activeBrowser = browser
	id := waitSession("")
	claim, err := f.service.ClaimRun(f.runnerContext)
	if err != nil || claim.Run.RunSpec.HostingOperationID != id {
		t.Fatal("HTTP claim", err)
	}
	authority := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	client := runnerclient.New(server.URL, registered.Token)
	transport := runnerclient.WithStorageArchiveTransport(t.Context(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
	plan, err := client.HostingStorageBackupDownloadPlan(transport, authority)
	if err != nil {
		t.Fatal("HTTP plan", err)
	}
	stale := authority
	stale.Fence = "wrong"
	if err = client.StreamStorageBackupDownload(transport, domain.HostingStorageBackupDownloadStream{HostingAuthority: stale, PlanHash: plan.Hash}, plan, bytes.NewReader(data)); err == nil {
		t.Fatal("stale HTTP stream accepted")
	}
	// Both requests outlive the server's ordinary 80ms defaults only on these routes.
	time.Sleep(120 * time.Millisecond)
	if err = client.StreamStorageBackupDownload(transport, domain.HostingStorageBackupDownloadStream{HostingAuthority: authority, PlanHash: plan.Hash}, plan, bytes.NewReader(data)); err != nil {
		t.Fatal("denied request poisoned legitimate stream", err)
	}
	got := <-browser
	if got.err != nil || got.status != 200 || got.length != int64(len(data)) || !bytes.Equal(got.body, data) || !strings.Contains(got.content, b.ID+".tar") {
		t.Fatal("native attachment", got.status, got.err)
	}
	// Cancellation while awaiting a runner closes the one-use session immediately.
	cancelCtx, cancel := context.WithCancel(t.Context())
	cancelled := runBrowser(cancelCtx)
	activeBrowser = cancelled
	cancelID := waitSession(id)
	cancel()
	<-cancelled
	deadline := time.Now().Add(time.Second)
	closed := false
	for time.Now().Before(deadline) {
		var state string
		if e := f.db.QueryRow(f.ctx, `SELECT state FROM hosting_storage_backup_downloads WHERE id=$1`, cancelID).Scan(&state); e == nil && state == "failed" {
			closed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !closed {
		t.Fatal("browser disconnect retained live session")
	}
	// Block a periodic authorization query after initial bytes are flowing, then
	// disconnect the browser. Cancellation must interrupt the DB wait, not wait
	// for its five-second safety timeout.
	blockedCtx, stopBrowser := context.WithCancel(t.Context())
	defer stopBrowser()
	headers := make(chan *http.Response, 1)
	browserErrors := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(blockedCtx, http.MethodGet, server.URL+"/api/v1/storage-backups/"+b.ID+"/archive", nil)
		request.Header.Set("Authorization", "Bearer "+ownerToken)
		response, e := server.Client().Do(request)
		if e != nil {
			browserErrors <- e
			return
		}
		headers <- response
	}()
	activeBrowser = nil
	blockedID := waitSession(cancelID)
	blockedClaim, err := f.service.ClaimRun(f.runnerContext)
	if err != nil || blockedClaim.Run.RunSpec.HostingOperationID != blockedID {
		t.Fatal("blocked check claim", err)
	}
	blockedAuthority := domain.HostingAuthority{OperationID: blockedID, RunID: blockedClaim.Run.ID, LeaseID: blockedClaim.Lease.ID, Attempt: blockedClaim.Lease.Attempt, Fence: blockedClaim.Lease.Fence}
	blockedPlan, err := client.HostingStorageBackupDownloadPlan(transport, blockedAuthority)
	if err != nil {
		t.Fatal(err)
	}
	sourceReader, sourceWriter := io.Pipe()
	defer func() { _ = sourceReader.Close(); _ = sourceWriter.Close() }()
	sourceDone := make(chan error, 1)
	go func() { _, e := sourceWriter.Write(data); sourceDone <- e }()
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.StreamStorageBackupDownload(transport, domain.HostingStorageBackupDownloadStream{HostingAuthority: blockedAuthority, PlanHash: blockedPlan.Hash}, blockedPlan, sourceReader)
	}()
	var body *http.Response
	select {
	case body = <-headers:
	case e := <-browserErrors:
		t.Fatal(e)
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not expose initial bytes")
	}
	barrier, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(context.Background()) }()
	if _, err = barrier.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-storage-copy-admission',0))`); err != nil {
		t.Fatal(err)
	}
	waiting := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event='advisory' AND query LIKE '%nerocd-storage-copy-admission%'`).Scan(&count); e != nil {
			t.Fatal(e)
		} else if count > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("periodic authority check did not reach controlled DB wait")
	}
	stopBrowser()
	if err = body.Body.Close(); err != nil {
		t.Fatal(err)
	}
	_ = sourceReader.Close()
	_ = sourceWriter.Close()
	select {
	case e := <-streamDone:
		if e == nil {
			t.Fatal("disconnected stalled stream succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled authority check did not cancel promptly")
	}
	select {
	case <-sourceDone:
	case <-time.After(time.Second):
		t.Fatal("source writer survived cancellation")
	}

}

func TestPostgresStorageDownloadComposeUsesImmutablePublishedProof(t *testing.T) {
	f, source := composeStorageScheduleFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupDownloadCapability); err != nil {
		t.Fatal(err)
	}
	b, err := f.service.RequestStorageBackup(f.owner, f.resource.ID, domain.HostingStorageBackupInput{RequestID: strings.Repeat("6", 32), ExpectedResourceGeneration: f.resource.Generation, Volume: "db", ExpectedDeployedOperationID: source.ID, AcknowledgeInterruption: true})
	if err != nil {
		t.Fatal(err)
	}
	op, a := f.claim(t, b.OperationID, "backup")
	op = f.begin(t, op, a)
	p := op.StorageArchive.Plan
	blob := &domain.HostingStorageArchiveBlob{BackupID: b.ID, DestinationID: p.DestinationID, DaemonID: p.Helper.DaemonID, SHA256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 1536, Format: domain.HostingStorageArchiveFormat}
	proof := &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("7", 64), Device: 1, Inode: 91, Bytes: 1536, Created: true}
	for _, phase := range []string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "resume_intent", "resumed"} {
		var states []domain.HostingStorageArchiveWriterState
		switch phase {
		case "writer_snapshot", "stop_intent", "resume_intent", "resumed", "stopped":
			states = p.WriterStates()
			for i := range states {
				states[i].Running = phase != "stopped"
			}
		}
		if phase == "archive_published" {
			_, err = f.service.RunnerStorageArchiveProgress(f.runnerContext, domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "publish_compose_download", PlanHash: p.Hash, Phase: phase, Blob: blob, LocalCopy: proof})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			op = f.progress(t, op, a, phase, states, nil)
		}
	}
	if _, err = f.service.RunnerStorageArchiveComplete(f.runnerContext, domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete_compose_download", PlanHash: p.Hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "succeeded", ResumeOutcome: "resumed", Blob: blob}}); err != nil {
		t.Fatal(err)
	}
	options, err := f.service.StorageBackupDownloadOptions(f.owner, b.ID)
	if err != nil || !options.Available {
		t.Fatal("Compose archive hidden", options, err)
	}
	download, err := f.service.CreateStorageBackupDownload(f.owner, "download_compose", b.ID, time.Now().Add(time.Minute))
	if err != nil || download.LocalCopy != *proof || download.Blob != *blob {
		t.Fatal("immutable published proof not retained", err)
	}
	var copies int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_storage_backup_copies WHERE backup_id=$1`, b.ID).Scan(&copies); err != nil || copies != 0 {
		t.Fatal("Compose retention inventory was widened", copies, err)
	}
	if err = f.service.CloseStorageBackupDownload(f.ctx, download.SessionID, false); err != nil {
		t.Fatal(err)
	}
}
