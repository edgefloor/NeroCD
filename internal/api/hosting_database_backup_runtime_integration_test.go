package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/web"
)

// Only exact fixed terminal messages become classes. Never export a log line,
// even when it contains a recognized phrase or passes the fixture canary scan.
func databaseBackupTerminalClass(raw []byte) string {
	if len(raw) > 1<<20 {
		return "unknown"
	}
	lines := bytes.Split(raw, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSuffix(lines[i], []byte("\r"))
		if len(line) == 0 || json.Valid(line) || bytes.HasPrefix(line, []byte("time=")) {
			continue // Structured runner events and the fixture's server logger.
		}
		if raw, ok := bytes.CutPrefix(line, []byte("database backup execution failed at ")); ok {
			phase, stage, found := strings.Cut(string(raw), "/")
			phases := map[string]bool{"inspection": true, "preflight": true, "recheck": true, "execution": true, "post_import": true}
			stages := map[string]bool{"plan": true, "binding_lock": true, "history": true, "daemon": true, "origin": true, "container_read": true, "container_identity": true, "volume": true, "process": true, "stability": true, "daemon_recheck": true, "archive": true, "archive_list": true, "authority": true, "restore": true, "readiness": true}
			if found && phases[phase] && stages[stage] {
				return "backup_" + phase + "_" + stage
			}
			return "unknown"
		}
		switch string(line) {
		case "invalid or unconfigured database backup task":
			return "claim_configuration"
		case "database backup authority unavailable":
			return "plan_authority"
		case "invalid database backup plan response":
			return "plan_response"
		case "invalid hosted PostgreSQL backup request":
			return "request_validation"
		case "database backup or restore remains held; execution evidence requires reconciliation":
			return "backup_evidence_held"
		case "database backup receipt request failed":
			return "receipt_transport"
		case "invalid database backup receipt response":
			return "receipt_response"
		case "hosting execution cannot enter the generic runtime":
			return "generic_guard"
		case "context canceled":
			return "canceled"
		case "context deadline exceeded":
			return "deadline"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

type databaseBackupFailureEvidence struct {
	Mode, State, OwnershipHeld, IntentReceipt, TerminalReceipt, LeaseStatus string
	TerminalClass, Requests                                                 string
	RunnerExited                                                            bool
	RunnerCode                                                              int
}

func databaseBackupFailureProjection(observed bool, mode, state string, hold, intent, terminal bool, lease string, raw []byte, exit databaseRunnerExit, counts *databaseRunnerAPICounts) databaseBackupFailureEvidence {
	out := databaseBackupFailureEvidence{Mode: "unknown", State: "unknown", OwnershipHeld: "unknown", IntentReceipt: "unknown", TerminalReceipt: "unknown", LeaseStatus: "unknown", TerminalClass: databaseBackupTerminalClass(raw), Requests: counts.summary(), RunnerExited: exit.Exited, RunnerCode: -1}
	if exit.Exited && exit.Code >= 0 && exit.Code <= 255 {
		out.RunnerCode = exit.Code
	}
	if !observed {
		return out
	}
	switch mode {
	case "backup", "restore":
		out.Mode = mode
	}
	switch state {
	case "queued", "waiting_target", "dispatched", "applying", "succeeded", "failed", "uncertain":
		out.State = state
	}
	out.OwnershipHeld = fmt.Sprint(hold)
	out.IntentReceipt, out.TerminalReceipt = "absent", "absent"
	if intent {
		out.IntentReceipt = "present"
	}
	if terminal {
		out.TerminalReceipt = "present"
	}
	switch lease {
	case "none", "active", "succeeded", "failed", "canceled", "expired":
		out.LeaseStatus = lease
	}
	return out
}

func (f *databaseRunnerFixture) reportDatabaseBackupFailure(id string) {
	// Deferred by the physical test, before testing cleanup can remove the
	// runner log, controller schema or root. This observes; it never retries work.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mode, state, lease string
	var hold, intent, terminal bool
	observed := false
	if id != "" && f.database != nil {
		observed = f.database.QueryRow(ctx, `SELECT b.mode,b.state,b.ownership_held,
 EXISTS(SELECT 1 FROM hosting_database_backup_receipts r WHERE r.operation_id=b.id AND r.phase='intent'),
 EXISTS(SELECT 1 FROM hosting_database_backup_receipts r WHERE r.operation_id=b.id AND r.phase IN ('complete','failed','uncertain')),
 COALESCE((SELECT l.status FROM run_leases l WHERE l.run_id=b.run_id ORDER BY l.attempt DESC LIMIT 1),'none')
 FROM hosting_database_backups b WHERE b.id=$1`, id).Scan(&mode, &state, &hold, &intent, &terminal, &lease) == nil
	}
	data := databaseBackupFailureProjection(observed, mode, state, hold, intent, terminal, lease, f.logs.snapshot(), f.failureRunnerStatus(), &f.apiCounts)
	output := f.failureOutput
	if output == nil {
		output = os.Stdout
	}
	raw, err := json.Marshal(data)
	if err == nil {
		_, _ = fmt.Fprintf(output, "DATABASE_BACKUP_FAILURE_EVIDENCE %s\n", raw)
	}
}

func (f *databaseRunnerFixture) reportDatabaseBackupFailureIfFailed(id *string) {
	if f.t.Failed() {
		f.reportDatabaseBackupFailure(*id)
	}
}

func backupRuntimeAwait(f *databaseRunnerFixture, id string) domain.HostingDatabaseBackupOperation {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			f.t.Fatal("backup operation did not settle")
		case <-ticker.C:
			op := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+id, nil, 200)
			if op.State == "succeeded" {
				if op.OwnershipHeld || op.Blob == nil {
					f.t.Fatal("incomplete backup settlement")
				}
				return op
			}
			if op.State == "failed" || op.State == "uncertain" {
				f.t.Fatalf("backup execution held state=%s code=%s", op.State, op.FailureCode)
			}
		}
	}
}
func backupRuntimeApplication(f *databaseRunnerFixture, db domain.HostingDatabase, suffix string) domain.HostingOperation {
	return backupRuntimeApplicationObserved(f, db, suffix, nil)
}

// Docker start acknowledges the process, not PostgreSQL readiness. Probe the
// original application, whose /ready opens a fresh authenticated DB connection,
// before retaining the existing single-attempt source data assertions.
func backupRuntimeWaitApplicationReady(parent context.Context, op domain.HostingOperation) error {
	if op.Result == nil || len(op.Result.ContainerID) != 64 || strings.Trim(op.Result.ContainerID, "0123456789abcdef") != "" || op.Snapshot.HostPort < 1024 || op.Snapshot.HostPort > 65535 || op.Snapshot.Readiness.Path != "/ready" || op.Snapshot.Readiness.TimeoutSeconds < 1 || op.Snapshot.Readiness.TimeoutSeconds > 30 {
		return errors.New("source application readiness target invalid")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(op.Snapshot.Readiness.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 4096, ResponseHeaderTimeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(op.Snapshot.HostPort)+"/ready", nil)
		if err != nil {
			return errors.New("source application readiness request invalid")
		}
		response, err := client.Do(request)
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 65))
			closeErr := response.Body.Close()
			ready := readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK && response.Header.Get("X-NeroCD-Fixture-Instance") == op.Result.ContainerID[:12] && string(raw) == "ready"
			clear(raw)
			if ready && ctx.Err() == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func backupRuntimeApplicationObserved(f *databaseRunnerFixture, db domain.HostingDatabase, suffix string, observe func(string, string)) domain.HostingOperation {
	f.t.Helper()
	progress := func(stage, connection string) {
		if observe != nil {
			observe(stage, connection)
		}
	}
	image := os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")
	if applicationDatabaseFixtureImage(image) != nil {
		f.t.Fatal("owned application image required")
	}
	resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: db.Resource.ProjectID, ProjectEnvironmentID: f.environment, Name: "backup app " + suffix, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
	progress("application_created", "")
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
	progress("application_bound", "")
	entries := []domain.HostingRuntimeEntry{}
	for _, v := range [][2]string{{"FIXTURE_DATABASE_ENGINE", "postgresql"}, {"FIXTURE_DATABASE_NAMESPACE", "backup_restored"}} {
		value := v[1]
		entries = append(entries, domain.HostingRuntimeEntry{Name: v[0], Kind: "literal", Value: &value})
	}
	config := databaseRunnerJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, RequestID: backupRuntimeRequestID(f.t), Entries: entries}, 200).Configuration
	progress("application_configured", "")
	in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: db.Resource.ID, ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: config.RuntimeGeneration, ExpectedDatabaseGeneration: db.Resource.Generation, RequestID: backupRuntimeRequestID(f.t)}, Deployment: domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "backup_app_" + suffix, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 30}}}
	connected := databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connections", in, 202)
	progress("connection_admitted", connected.ID)
	f.registerRouteOperationCleanup(connected.UpgradeOperationID)
	f.registerRouteOperationCleanup(connected.ApplicationOperationID)
	f.activeConnection(resource.ID)
	progress("connection_active", connected.ID)
	op := f.awaitConnectionOperation(connected.ApplicationOperationID)
	progress("application_settled", connected.ID)
	return op
}

func TestHostingDatabaseBackupCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact authorized backup runtime gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", backupRuntimeCreateInput(t, f.environment, f.destination), 201)
	f.registerRouteDatabaseCleanup(source.Resource.ID)
	requested := f.deploy(source, "backup_source")
	f.registerRouteOperationCleanup(requested.ID)
	f.await(requested.ID)
	a := backupRuntimeApplication(f, source, "source")
	f.appRecord(a, "PUT", "before", "application_origin")
	// The dump contains application-role sequence ownership, not source roles.
	db := f.connection(a.ResourceID)
	sourceOp := f.await(db.UpgradeOperationID)
	f.docker("exec", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; CREATE TABLE public.backup_sequence(id bigserial PRIMARY KEY, value text); INSERT INTO public.backup_sequence(value) VALUES ('before');")
	wire := &databaseBackupRuntimeWire{mode: "backup", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser := startDatabaseBackupBrowser(f, source)
	backupID := browser.await("backup")
	diagnosticID = backupID
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("backup receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	backup := backupRuntimeAwait(f, backupID)
	f.backupWire.Store(nil)
	f.appRecord(a, "PUT", "after_snapshot", "source_still_writable")
	// Restoration must not inspect or require the original source running.
	f.docker("container", "stop", sourceOp.Result.ContainerID)
	backupRuntimeRegisterSourceRestartCleanup(t, sourceOp.Result.ContainerID, func(ctx context.Context, id string) error {
		raw, err := databaseRunnerDocker(ctx, nil, "container", "start", id)
		clear(raw)
		return err
	})
	wire = &databaseBackupRuntimeWire{mode: "restore", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser.setPhase("restore", backup.ID)
	restoreID := browser.await("restore")
	diagnosticID = restoreID
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("restore receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	f.backupWire.Store(nil)
	browser.recordProgress("restore_replayed", "")
	restored := backupRuntimeAwait(f, restoreID)
	browser.recordProgress("restore_settled", "")
	if restored.TargetResourceID == source.Resource.ID || restored.BackupID != backup.ID || restored.Blob.Digest != backup.Blob.Digest {
		t.Fatal("restore changed immutable source or target identity")
	}
	target := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	browser.recordProgress("target_read", "")
	f.registerRouteDatabaseCleanup(target.Resource.ID)
	if target.Binding.ID == source.Binding.ID || target.StorageRef == source.StorageRef || target.Credentials[0].SecretID == source.Credentials[0].SecretID {
		t.Fatal("restore reused source binding, volume or credentials")
	}
	b := backupRuntimeApplicationObserved(f, target, "restored", browser.recordProgress)
	f.appRecord(b, "GET", "before", "application_origin")
	f.appRecord(b, "PUT", "new", "restored_new_write")
	f.appRecord(b, "GET", "new", "restored_new_write")
	targetConn := f.connection(b.ResourceID)
	targetOp := f.await(targetConn.UpgradeOperationID)
	sql := string(f.docker("exec", "--user", "postgres", targetOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--tuples-only", "--no-align", "--command", "SET ROLE ncd_app; INSERT INTO public.backup_sequence(value) VALUES ('restored') RETURNING id; SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.backup_sequence_id_seq'::regclass;"))
	if !strings.Contains(sql, "2") || !strings.Contains(sql, "ncd_app") {
		t.Fatal("restored application sequence ownership failed")
	}
	browser.recordProgress("target_data_verified", "")
	f.docker("container", "start", sourceOp.Result.ContainerID)
	if backupRuntimeWaitApplicationReady(f.t.Context(), a) != nil {
		t.Fatal("source application did not regain exact-instance database readiness")
	}
	f.appRecord(a, "GET", "before", "application_origin")
	f.appRecord(a, "GET", "after_snapshot", "source_still_writable")
	f.appRecord(a, "PUT", "source_new", "source_new_write")
	browser.recordProgress("source_data_verified", "")
	browser.setPhase("complete", restored.ID)
	browser.finished()
	backupRuntimeFailureCases(f, source, sourceOp, a, backup)
	fmt.Println("DATABASE_BACKUP_RESTORE_APP_PASS postgresql")
}

type databaseBackupBrowserFixture struct {
	f                                 *databaseRunnerFixture
	mu                                sync.Mutex
	phase, id                         string
	admitted                          chan domain.HostingDatabaseBackupOperation
	done                              chan struct{}
	once                              sync.Once
	stage, connectionID, lastProgress string
	progressOutput                    io.Writer // Protected by mu; separate from the failure reporter's writer.
}

type databaseBackupProgressEvidence struct {
	Stage, ConnectionState, Reason, CaptureRun, CaptureLease, CaptureReceipt string
	UpgradeState, ApplicationState, TerminalClass, Requests                  string
	RunnerExited                                                             bool
	RunnerCode                                                               int
}

const databaseBackupProgressQuery = `SELECT c.state,c.reason_code,COALESCE(r.status,'missing'),
 COALESCE((SELECT CASE WHEN l.status='active' AND l.expires_at<=clock_timestamp() THEN 'expired' ELSE l.status END FROM run_leases l WHERE l.run_id=n.run_id ORDER BY l.attempt DESC LIMIT 1),'missing'),
 CASE WHEN n.id IS NULL THEN 'missing' WHEN EXISTS(SELECT 1 FROM hosting_database_network_origin_receipts o WHERE o.capture_id=n.id) THEN 'present' ELSE 'absent' END,
 COALESCE(u.state,'missing'),COALESCE(a.state,'missing')
 FROM hosting_database_connections c
 LEFT JOIN hosting_database_network_captures n ON n.id=c.capture_id
 LEFT JOIN task_runs r ON r.id=n.run_id
 LEFT JOIN hosting_operations u ON u.id=c.upgrade_operation_id
 LEFT JOIN hosting_operations a ON a.id=c.application_operation_id WHERE c.id=$1`

func databaseBackupProgressProjection(stage, connection, reason, captureRun, captureLease, captureReceipt, upgrade, application string, raw []byte, exit databaseRunnerExit, counts *databaseRunnerAPICounts) databaseBackupProgressEvidence {
	closed := func(value string, allowed ...string) string {
		for _, item := range allowed {
			if value == item {
				return value
			}
		}
		return "unknown"
	}
	opState := func(value string) string {
		return closed(value, "not_admitted", "unavailable", "missing", "queued", "dispatched", "preparing", "prepared", "applying", "verifying", "succeeded", "failed", "canceled", "cancel_requested", "reconciliation_required")
	}
	out := databaseBackupProgressEvidence{
		Stage:           closed(stage, "restore_replayed", "restore_settled", "target_read", "application_created", "application_bound", "application_configured", "connection_admitted", "connection_active", "application_settled", "target_data_verified", "source_data_verified"),
		ConnectionState: closed(connection, "not_admitted", "unavailable", "missing", "binding_saved", "capture_requested", "capture_waiting", "upgrade_requested", "upgrade_waiting", "application_requested", "active", "held"),
		Reason:          closed(reason, "not_admitted", "unavailable", "missing", "accepted", "capture_required", "capture_waiting", "upgrade_waiting", "application_waiting", "active", "child_failed", "uncertainty_held"),
		CaptureRun:      closed(captureRun, "not_admitted", "unavailable", "missing", "queued", "waiting_approval", "running", "succeeded", "failed", "canceled", "rejected"),
		CaptureLease:    closed(captureLease, "not_admitted", "unavailable", "missing", "active", "expired", "succeeded", "failed", "canceled"),
		CaptureReceipt:  closed(captureReceipt, "not_admitted", "unavailable", "missing", "present", "absent"),
		UpgradeState:    opState(upgrade), ApplicationState: opState(application), TerminalClass: databaseBackupTerminalClass(raw), Requests: counts.summary(), RunnerExited: exit.Exited, RunnerCode: -1,
	}
	if exit.Exited && exit.Code >= 0 && exit.Code <= 255 {
		out.RunnerCode = exit.Code
	}
	if !exit.Exited {
		out.TerminalClass = "not_exited"
	}
	return out
}

func (b *databaseBackupBrowserFixture) recordProgress(stage, connection string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stage = stage
	if connection != "" {
		b.connectionID = connection
	}
	b.emitProgressLocked()
}

// Called both at milestones and by the live browser's existing state poll. It
// emits while the fixture is alive, before a browser failure can kill its Go
// process without running testing defers. Values are closed observations only.
func (b *databaseBackupBrowserFixture) emitProgressLocked() {
	if b.stage == "" {
		return
	}
	c, reason, capture, lease, receipt, upgrade, application := "not_admitted", "not_admitted", "not_admitted", "not_admitted", "not_admitted", "not_admitted", "not_admitted"
	if b.connectionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		err := b.f.database.QueryRow(ctx, databaseBackupProgressQuery, b.connectionID).Scan(&c, &reason, &capture, &lease, &receipt, &upgrade, &application)
		if err != nil {
			missing := "unavailable"
			if errors.Is(err, pgx.ErrNoRows) {
				missing = "missing"
			}
			c, reason, capture, lease, receipt, upgrade, application = missing, missing, missing, missing, missing, missing, missing
		}
	}
	out := databaseBackupProgressProjection(b.stage, c, reason, capture, lease, receipt, upgrade, application, b.f.logs.snapshot(), b.f.failureRunnerStatus(), &b.f.apiCounts)
	raw, err := json.Marshal(out)
	if err == nil && string(raw) != b.lastProgress {
		b.lastProgress = string(raw)
		output := b.progressOutput
		if output == nil {
			output = os.Stdout
		}
		_, _ = fmt.Fprintln(output, "DATABASE_BACKUP_PROGRESS "+string(raw))
	}
}

func (b *databaseBackupBrowserFixture) setPhase(phase, id string) {
	b.mu.Lock()
	b.phase, b.id = phase, id
	b.mu.Unlock()
}
func (b *databaseBackupBrowserFixture) await(mode string) string {
	b.f.t.Helper()
	select {
	case op := <-b.admitted:
		if op.Mode != mode {
			b.f.t.Fatal("unexpected browser admission")
		}
		return op.ID
	case <-time.After(120 * time.Second):
		b.f.t.Fatal("live backup UI did not admit operation")
	}
	return ""
}
func (b *databaseBackupBrowserFixture) finished() {
	b.f.t.Helper()
	select {
	case <-b.done:
	case <-time.After(90 * time.Second):
		b.f.t.Fatal("live backup UI completion missing")
	}
}
func startDatabaseBackupBrowser(f *databaseRunnerFixture, source domain.HostingDatabase) *databaseBackupBrowserFixture {
	f.t.Helper()
	origin := os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN")
	if !applicationDatabaseBrowserOrigin(origin) {
		f.t.Fatal("owned browser origin required")
	}
	_, err := f.service.BootstrapAdmin(f.t.Context(), app.BootstrapAdminInput{Email: "backup-bootstrap@example.local", Name: "Backup bootstrap", Password: "backup-bootstrap-private-only"})
	if err != nil {
		f.t.Fatal("browser bootstrap failed")
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: source.Resource.ProjectID, Email: "admin@example.local", Role: "owner"}, {ProjectID: source.Resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", member, 200)
	}
	changed, err := f.database.Exec(f.t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap' AND global_role='system_admin'`)
	if err != nil || changed.RowsAffected() != 1 {
		f.t.Fatal("browser owner demotion failed")
	}
	api := NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	b := &databaseBackupBrowserFixture{f: f, phase: "backup", admitted: make(chan domain.HostingDatabaseBackupOperation, 2), done: make(chan struct{})}
	control := "/api/v1/fixture/database-backup/" + strings.Repeat("b", 32)
	owner := applicationDatabaseBrowserOwner(api, source.Resource.ProjectID, origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "GET" && r.URL.Path == control+"/state" {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.emitProgressLocked()
			writeJSON(w, 200, map[string]string{"phase": b.phase, "operation_id": b.id})
			return
		}
		if r.Method == "POST" && r.URL.Path == control+"/done" {
			b.once.Do(func() { close(b.done) })
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(404)
	}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, control) {
			owner.ServeHTTP(w, r)
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/api/v1/resources/"+source.Resource.ID+"/database-backups" || strings.HasSuffix(r.URL.Path, "/restores")) {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, r)
			if rec.Code == 202 {
				var op domain.HostingDatabaseBackupOperation
				if json.Unmarshal(rec.Body.Bytes(), &op) != nil || op.Validate() != nil {
					f.t.Error("invalid browser admission")
				} else {
					select {
					case b.admitted <- op:
					default:
						f.t.Error("duplicate browser admission")
					}
				}
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		api.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		f.t.Fatal("owned browser bridge unavailable")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	joined := make(chan error, 1)
	go func() { joined <- server.Serve(listener) }()
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			f.t.Error("browser server did not shut down")
		}
		if err := <-joined; err != http.ErrServerClosed {
			f.t.Error("browser server did not join")
		}
	})
	meta := map[string]string{"origin": origin, "project_id": source.Resource.ProjectID, "environment": "database runner acceptance", "engine": "postgresql", "profile": "backup", "application_resource_id": source.Resource.ID, "application_name": source.Resource.Name, "database_resource_id": source.Resource.ID, "foreign_resource_id": "unused_fixture", "control_path": control}
	raw, _ := json.Marshal(meta)
	fmt.Println("DATABASE_BACKUP_BROWSER_READY " + string(raw))
	return b
}

// The repository stands in for persistence; requests still cross the real
// runner client, HTTP server, authentication, decoder and application service.
type databaseBackupWireRepository struct {
	*databaseBackupHTTPRepository
	received chan domain.HostingDatabaseBackupMutation
}

func (r *databaseBackupWireRepository) DatabaseBackupMutation(_ context.Context, runner string, in domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error) {
	if runner != "runner_archive" {
		return domain.HostingDatabaseBackupOperation{}, fmt.Errorf("unexpected runner identity")
	}
	r.received <- in
	out := r.value
	out.State = map[string]string{"intent": "applying", "failed": "failed", "uncertain": "uncertain", "complete": "succeeded"}[in.Phase]
	out.Blob, out.FailureCode, out.OwnershipHeld = in.Blob, in.FailureCode, in.Phase != "complete"
	return out, nil
}

func TestDatabaseBackupRuntimeWirePreservesHTTPReceipts(t *testing.T) {
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			server, original, _, _, token := archiveHTTPFixture(t)
			now := time.Unix(1700000000, 0).UTC()
			value := domain.HostingDatabaseBackupOperation{MaxBytes: 1 << 20, TimeoutSeconds: 30, ID: "operation", Version: domain.HostingDatabaseBackupVersion, ProjectID: original.resource.ProjectID, SourceResourceID: original.resource.ID, SourceGeneration: 1, BackupID: "operation", Mode: mode, State: "dispatched", Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
			if mode == "restore" {
				value.BackupID, value.TargetResourceID = "backup", "new_target"
			}
			repo := &databaseBackupWireRepository{databaseBackupHTTPRepository: &databaseBackupHTTPRepository{archiveHTTPRepository: original, value: value}, received: make(chan domain.HostingDatabaseBackupMutation, 8)}
			mem := repo.MemoryStore
			service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
			if err != nil {
				t.Fatal(err)
			}
			server.app = service
			wire := &databaseBackupRuntimeWire{mode: mode, entered: make(chan struct{}), release: make(chan struct{})}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !wire.serve(w, r, server) {
					server.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(httpServer.Close)
			t.Cleanup(func() { close(wire.release) })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			client := runnerclient.New(httpServer.URL, token)
			authority := domain.HostingAuthority{OperationID: value.ID, RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fixture_fence"}
			request := func(phase string) domain.HostingDatabaseBackupMutation {
				in := domain.HostingDatabaseBackupMutation{HostingAuthority: authority, ReceiptID: "receipt_" + phase, PlanHash: "sha256:" + strings.Repeat("a", 64), Phase: phase}
				if phase == "failed" {
					in.FailureCode = "execution_failed"
				}
				if phase == "complete" {
					in.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 123, Format: "postgresql_custom"}
				}
				return in
			}
			assertReceived := func(t *testing.T, in domain.HostingDatabaseBackupMutation) {
				t.Helper()
				select {
				case got := <-repo.received:
					if domain.HostingDocumentHash(got) != domain.HostingDocumentHash(in) {
						t.Fatal("handler changed receipt or full authority")
					}
				case <-ctx.Done():
					t.Fatal("receipt did not reach repository")
				}
			}
			for _, phase := range []string{"intent", "failed", "uncertain"} {
				t.Run(phase, func(t *testing.T) {
					in := request(phase)
					out, err := client.HostingDatabaseBackupMutation(ctx, in)
					if err != nil || out.ID != value.ID {
						t.Fatalf("forwarded %s receipt failed: %v", phase, err)
					}
					assertReceived(t, in)
					select {
					case <-wire.entered:
						t.Fatal("non-completion receipt triggered ACK fault")
					default:
					}
				})
			}
			complete := request("complete")
			lostCtx, cancelLost := context.WithCancel(ctx)
			defer cancelLost()
			joined := make(chan error, 1)
			go func() { _, err := client.HostingDatabaseBackupMutation(lostCtx, complete); joined <- err }()
			select {
			case <-wire.entered:
			case <-ctx.Done():
				t.Fatal("committed completion did not reach ACK fault")
			}
			assertReceived(t, complete)
			cancelLost()
			select {
			case err := <-joined:
				if err == nil {
					t.Fatal("lost completion ACK unexpectedly succeeded")
				}
			case <-ctx.Done():
				t.Fatal("cancelled completion request did not join")
			}
			out, err := client.HostingDatabaseBackupMutation(ctx, complete)
			if err != nil || out.State != "succeeded" || out.OwnershipHeld || wire.replays() != 1 {
				t.Fatalf("exact completion replay failed: %v", err)
			}
			assertReceived(t, complete)
			changed := complete
			changed.ReceiptID = "other_receipt"
			if _, err = client.HostingDatabaseBackupMutation(ctx, changed); err == nil || wire.replays() != 1 {
				t.Fatal("ACK interceptor accepted changed completion identity")
			}
			assertReceived(t, changed)
		})
	}
}

// This wire intercepts the real handler after its transaction commits. It never
// synthesizes an acknowledgement or grants authority to a second execution.
type databaseBackupRuntimeWire struct {
	mu               sync.Mutex
	mode             string
	pause            bool
	entered, release chan struct{}
	first            bool
	body, response   [32]byte
	replay           int
	plan             domain.HostingDatabaseBackupPlan
}

func (w *databaseBackupRuntimeWire) replays() int { w.mu.Lock(); defer w.mu.Unlock(); return w.replay }
func (w *databaseBackupRuntimeWire) serve(out http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	path := r.URL.Path
	if r.Method != "POST" || path != "/api/v1/runners/hosting/database-backup/mutation" && path != "/api/v1/runners/hosting/database-backup/plan" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		out.WriteHeader(400)
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	defer clear(raw)
	if path == "/api/v1/runners/hosting/database-backup/mutation" {
		var in domain.HostingDatabaseBackupMutation
		if json.Unmarshal(raw, &in) != nil || in.Phase != "complete" {
			// The reset body borrows raw. Forward before the deferred clear so
			// the real decoder reads the original bytes, including invalid JSON.
			handler.ServeHTTP(out, r)
			return true
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != 200 {
		for k, v := range rec.Header() {
			out.Header()[k] = v
		}
		out.WriteHeader(rec.Code)
		_, _ = out.Write(rec.Body.Bytes())
		return true
	}
	w.mu.Lock()
	if path == "/api/v1/runners/hosting/database-backup/plan" {
		if !w.pause || w.first {
			w.mu.Unlock()
			for k, v := range rec.Header() {
				out.Header()[k] = v
			}
			out.WriteHeader(rec.Code)
			_, _ = out.Write(rec.Body.Bytes())
			return true
		}
		if json.Unmarshal(rec.Body.Bytes(), &w.plan) != nil || w.plan.Operation.Mode != w.mode {
			w.mu.Unlock()
			for k, v := range rec.Header() {
				out.Header()[k] = v
			}
			out.WriteHeader(rec.Code)
			_, _ = out.Write(rec.Body.Bytes())
			return true
		}
		w.first = true
		close(w.entered)
		w.mu.Unlock()
		select {
		case <-w.release:
		case <-r.Context().Done():
			return true
		}
	} else {
		var op domain.HostingDatabaseBackupOperation
		if json.Unmarshal(rec.Body.Bytes(), &op) != nil || op.Mode != w.mode {
			w.mu.Unlock()
			for k, v := range rec.Header() {
				out.Header()[k] = v
			}
			out.WriteHeader(rec.Code)
			_, _ = out.Write(rec.Body.Bytes())
			return true
		}
		body, response := sha256.Sum256(raw), sha256.Sum256(rec.Body.Bytes())
		if !w.first {
			w.first = true
			w.body, w.response = body, response
			close(w.entered)
			w.mu.Unlock()
			select {
			case <-w.release:
			case <-r.Context().Done():
			}
			return true
		}
		if w.body != body || w.response != response {
			w.mu.Unlock()
			out.WriteHeader(409)
			return true
		}
		w.replay++
		w.mu.Unlock()
	}
	for k, v := range rec.Header() {
		out.Header()[k] = v
	}
	out.WriteHeader(rec.Code)
	_, _ = out.Write(rec.Body.Bytes())
	return true
}
func backupRuntimeFailureCases(f *databaseRunnerFixture, source domain.HostingDatabase, sourceOp, application domain.HostingOperation, backup domain.HostingDatabaseBackupOperation) {
	diagnosticID := backup.ID
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	f.t.Helper()
	// Pause the exact fresh restore plan before any effect, install a target-only
	// event trigger to force a genuine guest pg_restore deadline, then release it.
	wire := &databaseBackupRuntimeWire{mode: "restore", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	req := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore_guest_timeout", Name: "Timeout private target"}, 202)
	diagnosticID = req.ID
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatal("restore plan pause not reached")
	}
	wire.mu.Lock()
	plan := wire.plan
	wire.mu.Unlock()
	if plan.Target == nil || plan.TimeoutSeconds != 10 {
		f.t.Fatal("exact target/guest policy missing")
	}
	f.registerRouteDatabaseCleanup(req.TargetResourceID)
	f.docker("exec", "--user", "postgres", plan.Target.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "CREATE FUNCTION public.fixture_delay() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(30); END $$; CREATE EVENT TRIGGER fixture_delay ON ddl_command_start EXECUTE FUNCTION public.fixture_delay();")
	close(wire.release)
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		op := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+req.ID, nil, 200)
		if op.State == "uncertain" {
			if !op.OwnershipHeld {
				f.t.Fatal("timeout released target")
			}
			break
		}
		select {
		case <-ctx.Done():
			f.t.Fatal("guest timeout did not retain visible uncertainty")
		case <-ticker.C:
		}
	}
	f.stop()
	f.backupWire.Store(nil)
	// Local client exit alone is not evidence. Query the actual guest processes
	// until the bounded guest timeout has removed pg_restore, without resuming it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		top := string(f.docker("container", "top", plan.Target.ContainerID, "-eo", "pid,comm"))
		if !strings.Contains(top, "pg_restore") {
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatal("guest restore survived bounded deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.appRecord(application, "GET", "before", "application_origin")
	f.appRecord(application, "PUT", "after_timeout", "source_usable")
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+req.TargetResourceID+"/database/deployments", backupRuntimeHeldTargetDeploymentInput(), 409)
	// A new runner must refuse the unmatched/uncertain journal rather than execute
	// the import a second time. The exact target and its held evidence survive.
	f.start()
	select {
	case <-f.process.done:
	case <-time.After(15 * time.Second):
		f.t.Fatal("uncertain import unexpectedly resumed")
	}
	f.stop()
	before := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+req.ID, nil, 200)
	if before.State != "uncertain" || !before.OwnershipHeld {
		f.t.Fatal("restart altered uncertain target")
	}

	fmt.Println("DATABASE_BACKUP_GUEST_TIMEOUT_PASS postgresql")
	_ = source
	_ = sourceOp
}

func backupRuntimeHeldTargetDeploymentInput() domain.HostingDatabaseDeploymentInput {
	return domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "denied_held_target"}
}

func backupRuntimeRegisterSourceRestartCleanup(t *testing.T, containerID string, restart func(context.Context, string) error) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := restart(ctx, containerID); err != nil {
			t.Error("owned fixture Docker action failed")
		}
	})
}

func backupRuntimeRegisterGrantCleanup(f *databaseRunnerFixture, project string) {
	f.t.Helper()
	f.t.Cleanup(func() {
		f.stop() // A denied attempt must join before its fixture grant is restored.
		if err := backupRuntimeRestoreGrant(f, project); err != nil {
			f.t.Error("owned fixture grant cleanup failed")
		}
	})
}

func backupRuntimeRestoreGrant(f *databaseRunnerFixture, project string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(app.DestinationProjectInput{ProjectID: project})
	defer clear(raw)
	if err != nil {
		return errors.New("owned fixture grant cleanup request invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/api/v1/destinations/"+f.destination+"/project-grants", bytes.NewReader(raw))
	if err != nil {
		return errors.New("owned fixture grant cleanup request invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, err := f.client.Do(request)
	if err != nil {
		return errors.New("owned fixture grant cleanup transport failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	defer clear(data)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusCreated || len(data) > 1<<20 || !json.Valid(data) || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		return errors.New("owned fixture grant cleanup response invalid")
	}
	return nil
}

func backupRuntimeWaitReplay(f *databaseRunnerFixture, wire *databaseBackupRuntimeWire) {
	f.t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	ctx, cancel := context.WithTimeout(f.t.Context(), 15*time.Second)
	defer cancel()
	for {
		if wire.replays() == 1 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			f.t.Fatal("exact receipt replay missing")
		}
	}
}

func TestHostingDatabaseBackupFailuresCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact authorized backup runtime gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	for _, kind := range []string{"corrupt", "truncated", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
			diagnosticID := ""
			defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
			source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", backupRuntimeCreateInput(t, f.environment, f.destination), 201)
			f.registerRouteDatabaseCleanup(source.Resource.ID)
			requested := f.deploy(source, "backup_negative_source")
			f.registerRouteOperationCleanup(requested.ID)
			op := f.await(requested.ID)
			f.docker("exec", "--user", "postgres", op.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; CREATE TABLE public.negative_record(id bigserial PRIMARY KEY); INSERT INTO public.negative_record DEFAULT VALUES;")
			path := "/api/v1/resources/" + source.Resource.ID + "/database-backups"
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", path, domain.HostingDatabaseBackupInput{RequestID: "stale_backup", ExpectedResourceGeneration: source.Resource.Generation + 1}, 409)
			b := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", path, domain.HostingDatabaseBackupInput{RequestID: "negative_backup", ExpectedResourceGeneration: source.Resource.Generation}, 202)
			diagnosticID = b.ID
			b = backupRuntimeAwait(f, b.ID)
			f.stop()
			if kind != "revoked" {
				file, err := os.OpenFile(filepath.Join(f.root, "state", "logical-backups", strings.TrimPrefix(b.Blob.Digest, "sha256:")), os.O_RDWR, 0)
				if err != nil {
					t.Fatal("owned artifact unavailable")
				}
				if kind == "truncated" {
					err = file.Truncate(5)
				} else {
					_, err = file.WriteAt([]byte("X"), 5)
				}
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatal("owned archive fault failed")
				}
			}
			var wire *databaseBackupRuntimeWire
			if kind == "revoked" {
				wire = &databaseBackupRuntimeWire{mode: "restore", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
				f.backupWire.Store(wire)
			}
			restore := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", "/api/v1/database-backups/"+b.ID+"/restores", domain.HostingDatabaseBackupRestoreInput{RequestID: "negative_restore", Name: "Rejected private restore"}, 202)
			diagnosticID = restore.ID
			f.registerRouteDatabaseCleanup(restore.TargetResourceID)
			f.start()
			if kind == "revoked" {
				select {
				case <-wire.entered:
				case <-time.After(90 * time.Second):
					t.Fatal("revocation boundary missing")
				}
				databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/destinations/"+f.destination+"/project-grants/revoke", app.DestinationProjectInput{ProjectID: source.Resource.ProjectID}, 200)
				backupRuntimeRegisterGrantCleanup(f, source.Resource.ProjectID)
				close(wire.release)
				select {
				case <-f.process.done:
				case <-time.After(20 * time.Second):
					t.Fatal("revoked attempt did not stop")
				}
				f.stop()
			} else {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				tick := time.NewTicker(100 * time.Millisecond)
				defer tick.Stop()
				for {
					restore = databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+restore.ID, nil, 200)
					if restore.State == "failed" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("corrupt import did not fail before execution")
					case <-tick.C:
					}
				}
			}
			if kind == "revoked" {
				databaseRunnerJSON[json.RawMessage](f, f.token, "GET", "/api/v1/database-backups/"+restore.ID, nil, 409)
				if f.database.QueryRow(t.Context(), `SELECT state,ownership_held FROM hosting_database_backups WHERE id=$1`, restore.ID).Scan(&restore.State, &restore.OwnershipHeld) != nil {
					t.Fatal("retained denied operation unavailable")
				}
			} else {
				restore = databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+restore.ID, nil, 200)
			}
			if !restore.OwnershipHeld || restore.State == "succeeded" {
				t.Fatal("unsafe target release")
			}
			var child string
			var intents int
			if f.database.QueryRow(t.Context(), `SELECT deployment_operation_id FROM hosting_database_backups WHERE id=$1`, restore.ID).Scan(&child) != nil || f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1 AND phase='intent'`, restore.ID).Scan(&intents) != nil || intents != 0 {
				t.Fatal("denied restore acquired execution intent")
			}
			var targetContainer string
			if f.database.QueryRow(t.Context(), `SELECT plan->'target'->>'container_id' FROM hosting_database_backups WHERE id=$1`, restore.ID).Scan(&targetContainer) != nil || len(targetContainer) != 64 {
				t.Fatal("saved exact target unavailable")
			}
			empty := string(f.docker("exec", "--user", "postgres", targetContainer, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--tuples-only", "--no-align", "--command", "SELECT to_regclass('public.negative_record') IS NULL;"))
			if strings.TrimSpace(empty) != "t" {
				t.Fatal("denied restore imported data")
			}
			f.docker("exec", "--user", "postgres", op.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; INSERT INTO public.negative_record DEFAULT VALUES;")
			fmt.Println("DATABASE_BACKUP_FAILURE_PASS postgresql " + kind)
		})
	}
}

func backupRuntimeRequestID(t *testing.T) string {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("fixture request identity unavailable")
	}
	return hex.EncodeToString(nonce[:])
}
func TestDatabaseBackupRuntimeFixtureRequestContracts(t *testing.T) {
	config := domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, RequestID: backupRuntimeRequestID(t), Entries: []domain.HostingRuntimeEntry{}}
	if _, _, err := config.Validate(); err != nil {
		t.Fatal("actual runtime-configuration request identity invalid")
	}
	binding := domain.HostingDatabaseBindingInput{DatabaseResourceID: "database", ExpectedResourceGeneration: 1, ExpectedDatabaseGeneration: 1, RequestID: backupRuntimeRequestID(t)}
	if binding.Validate() != nil || binding.RequestID == config.RequestID {
		t.Fatal("actual connection request identity invalid or reused")
	}
	raw, err := json.Marshal(binding)
	var decoded domain.HostingDatabaseBindingInput
	if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != binding {
		t.Fatal("closed binding request rejected generated identity")
	}
}

func TestDatabaseBackupHeldTargetDeploymentRequestContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		var input domain.HostingDatabaseDeploymentInput
		if err != nil || r.Method != http.MethodPost || json.Unmarshal(raw, &input) != nil || input.ExpectedGeneration != 1 || input.IdempotencyKey != "denied_held_target" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusConflict) // A valid request reaches held-target admission.
	}))
	defer server.Close()
	raw, err := json.Marshal(backupRuntimeHeldTargetDeploymentInput())
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Post(server.URL, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("held-target fixture request failed closed decoding: status=%d", response.StatusCode)
	}
}

func TestDatabaseBackupSourceRestartCleanupAfterCanceledTestContext(t *testing.T) {
	const containerID = "exact_owned_source_container"
	calls := 0
	var cleanupContext context.Context
	if !t.Run("owned_cleanup", func(child *testing.T) {
		backupRuntimeRegisterSourceRestartCleanup(child, containerID, func(ctx context.Context, id string) error {
			calls++
			cleanupContext = ctx
			if child.Context().Err() != context.Canceled {
				child.Error("restart seam did not run after test context cancellation")
			}
			deadline, bounded := ctx.Deadline()
			if id != containerID || ctx.Err() != nil || !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 45*time.Second {
				child.Error("restart lost its exact identity or independent bounded lifetime")
			}
			return ctx.Err()
		})
	}) {
		t.Fatal("source restart cleanup used the canceled test lifetime")
	}
	if calls != 1 || cleanupContext == nil || cleanupContext.Err() != context.Canceled {
		t.Fatal("source restart cleanup did not finish exactly once and cancel its own lifetime")
	}
}

func TestDatabaseBackupGrantCleanupAfterCanceledTestContext(t *testing.T) {
	childContexts := make(chan context.Context, 1)
	received := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		childContext := <-childContexts
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		var input app.DestinationProjectInput
		if err != nil || json.Unmarshal(raw, &input) != nil || input.ProjectID != "project_fixture" || r.Method != http.MethodPost || r.URL.Path != "/api/v1/destinations/dest_fixture/project-grants" || r.Header.Get("Authorization") != "Bearer fixture_token" || childContext.Err() != context.Canceled || r.Context().Err() != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- struct{}{}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"project_id":"project_fixture"}`)
	}))
	defer server.Close()
	if !t.Run("owned_cleanup", func(child *testing.T) {
		childContexts <- child.Context()
		f := &databaseRunnerFixture{t: child, server: server, client: server.Client(), destination: "dest_fixture", token: "fixture_token"}
		backupRuntimeRegisterGrantCleanup(f, "project_fixture")
	}) {
		t.Fatal("owned grant cleanup used the canceled test lifetime")
	}
	select {
	case <-received:
	default:
		t.Fatal("owned cleanup did not issue its bounded verified-TLS request")
	}
}

func TestDatabaseBackupGrantCleanupResponseBoundaries(t *testing.T) {
	for _, kind := range []string{"status", "body", "size", "tls"} {
		t.Run(kind, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "status" {
					w.WriteHeader(http.StatusForbidden)
				} else {
					w.WriteHeader(http.StatusCreated)
				}
				body := `{"ok":true}`
				if kind == "body" {
					body = "ncr_cleanup_private_canary"
				} else if kind == "size" {
					body = strings.Repeat("x", (1<<20)+1)
				}
				_, _ = io.WriteString(w, body)
			})
			var server *httptest.Server
			if kind == "tls" {
				server = httptest.NewServer(handler)
			} else {
				server = httptest.NewTLSServer(handler)
			}
			defer server.Close()
			f := &databaseRunnerFixture{t: t, server: server, client: server.Client(), destination: "dest_fixture", token: "fixture_token"}
			if err := backupRuntimeRestoreGrant(f, "project_fixture"); err == nil || err.Error() != "owned fixture grant cleanup response invalid" {
				t.Fatal("cleanup accepted or exposed an invalid response")
			}
		})
	}
}

// Every controller schema shares the isolated daemon, so resource creation
// identities must be distinct even when desired metadata is otherwise equal.
func backupRuntimeCreateInput(t *testing.T, environment, destination string) domain.HostingDatabaseCreateInput {
	t.Helper()
	in := applicationDatabaseCreateInput("postgresql", environment, destination)
	in.RequestID = backupRuntimeRequestID(t)
	return in
}
func TestDatabaseBackupRuntimeFixtureCreationIdentities(t *testing.T) {
	seen := map[string]bool{}
	for _, phase := range []string{"main", "corrupt", "truncated", "revoked"} {
		in := backupRuntimeCreateInput(t, "penv_fixture", "dest_fixture")
		if in.Validate() != nil {
			t.Fatal("invalid typed creation input", phase)
		}
		id := domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID)
		if seen[id] {
			t.Fatal("controller schemas would reuse an existing daemon volume", phase)
		}
		seen[id] = true
		raw, err := json.Marshal(in)
		var decoded domain.HostingDatabaseCreateInput
		if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != in {
			t.Fatal("creation input violates closed wire contract", phase)
		}
	}
}

func TestDatabaseBackupDiagnosticTerminalClassesAndRedaction(t *testing.T) {
	for message, want := range map[string]string{
		"invalid or unconfigured database backup task":                                        "claim_configuration",
		"database backup authority unavailable":                                               "plan_authority",
		"invalid database backup plan response":                                               "plan_response",
		"invalid hosted PostgreSQL backup request":                                            "request_validation",
		"database backup or restore remains held; execution evidence requires reconciliation": "backup_evidence_held",
		"database backup receipt request failed":                                              "receipt_transport",
		"invalid database backup receipt response":                                            "receipt_response",
		"hosting execution cannot enter the generic runtime":                                  "generic_guard",
		"context canceled":          "canceled",
		"context deadline exceeded": "deadline",
	} {
		if got := databaseBackupTerminalClass([]byte("{\"event\":\"claimed_run\"}\n" + message + "\ntime=fixture level=INFO msg=probe\n")); got != want {
			t.Fatalf("known terminal boundary was not retained: %s", want)
		}
	}
	for _, phase := range []string{"inspection", "preflight", "recheck", "execution", "post_import"} {
		for _, stage := range []string{"plan", "binding_lock", "history", "daemon", "origin", "container_read", "container_identity", "volume", "process", "stability", "daemon_recheck", "archive", "archive_list", "authority", "restore", "readiness"} {
			message := "database backup execution failed at " + phase + "/" + stage
			if got := databaseBackupTerminalClass([]byte(message)); got != "backup_"+phase+"_"+stage {
				t.Fatal("closed stage classification unavailable")
			}
		}
	}
	canary := "ncr_diagnostic_canary_private_value"
	for _, raw := range []string{canary, "database backup execution failed at execution/restore: " + canary, "database backup execution failed at execution/" + canary, "database backup execution failed at unknown/restore", "database backup execution failed at execution/unknown", "invalid database backup plan response: " + canary, "invalid database backup plan response\n" + canary, "{" + canary, strings.Repeat("x", (1<<20)+1)} {
		if got := databaseBackupTerminalClass([]byte(raw)); got != "unknown" {
			t.Fatal("unknown or canary-bearing terminal content became evidence")
		}
	}
	counts := &databaseRunnerAPICounts{}
	counts.record("POST", "/api/v1/runners/hosting/database-backup/plan", 200)
	counts.record("POST", "/api/v1/runners/hosting/database-backup/mutation", 409)
	counts.record("POST", "/api/v1/runners/hosting/database-backup/plan?token="+canary, 200)
	projection := databaseBackupFailureProjection(true, canary, canary, true, false, false, canary, []byte(canary), databaseRunnerExit{Exited: true, Kind: canary, Code: 1}, counts)
	raw, err := json.Marshal(projection)
	if err != nil || bytes.Contains(raw, []byte(canary)) || projection.Mode != "unknown" || projection.State != "unknown" || projection.LeaseStatus != "unknown" || projection.TerminalClass != "unknown" || projection.OwnershipHeld != "true" || projection.IntentReceipt != "absent" {
		t.Fatal("diagnostic projection emitted arbitrary state or log values")
	}
	if projection.Requests != "POST /api/v1/runners/hosting/database-backup/plan 200=1,POST /api/v1/runners/hosting/database-backup/mutation 409=1" {
		t.Fatal("backup endpoint counts lost their exact safe route identity")
	}
	projection = databaseBackupFailureProjection(true, "backup", "dispatched", true, false, false, "active", []byte("database backup authority unavailable\n"), databaseRunnerExit{Exited: true, Code: 1}, counts)
	if projection.Mode != "backup" || projection.State != "dispatched" || projection.OwnershipHeld != "true" || projection.IntentReceipt != "absent" || projection.TerminalReceipt != "absent" || projection.LeaseStatus != "active" || projection.TerminalClass != "plan_authority" || !projection.RunnerExited || projection.RunnerCode != 1 {
		t.Fatal("safe diagnostic lost the boundary between claim and execution intent")
	}
	projection = databaseBackupFailureProjection(false, "backup", "succeeded", false, true, true, "succeeded", nil, databaseRunnerExit{}, counts)
	if projection.State != "unknown" || projection.OwnershipHeld != "unknown" || projection.IntentReceipt != "unknown" || projection.LeaseStatus != "unknown" {
		t.Fatal("failed observation fabricated settled state")
	}
}

func TestDatabaseBackupProgressClosedObservation(t *testing.T) {
	counts := &databaseRunnerAPICounts{}
	counts.record("POST", "/api/v1/runners/claim", 200)
	initial := databaseBackupProgressProjection("connection_admitted", "binding_saved", "accepted", "missing", "missing", "missing", "missing", "missing", nil, databaseRunnerExit{}, counts)
	if initial.ConnectionState != "binding_saved" || initial.Reason != "accepted" {
		t.Fatal("initial persisted connection reason became unknown")
	}
	known := databaseBackupProgressProjection("connection_admitted", "capture_waiting", "capture_waiting", "running", "expired", "absent", "missing", "missing", []byte("database backup authority unavailable\n"), databaseRunnerExit{Exited: true, Code: 1}, counts)
	if known.Stage != "connection_admitted" || known.ConnectionState != "capture_waiting" || known.CaptureRun != "running" || known.CaptureLease != "expired" || known.CaptureReceipt != "absent" || known.UpgradeState != "missing" || known.ApplicationState != "missing" || known.TerminalClass != "plan_authority" || !known.RunnerExited || known.RunnerCode != 1 {
		t.Fatal("progress lost the boundary between capture and child deployment")
	}
	canary := "ncr_progress_private_canary"
	unknown := databaseBackupProgressProjection(canary, canary, canary, canary, canary, canary, canary, canary, []byte(canary), databaseRunnerExit{Exited: true, Kind: canary, Code: 999}, counts)
	raw, err := json.Marshal(unknown)
	if err != nil || bytes.Contains(raw, []byte(canary)) {
		t.Fatal("progress leaked arbitrary observation content")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 12 {
		t.Fatal("progress schema changed")
	}
	for _, field := range []string{"Stage", "ConnectionState", "Reason", "CaptureRun", "CaptureLease", "CaptureReceipt", "UpgradeState", "ApplicationState", "TerminalClass"} {
		if string(fields[field]) != `"unknown"` {
			t.Fatal("progress emitted an unrecognized observation", field)
		}
	}
	if unknown.RunnerCode != -1 || unknown.Requests != "POST /api/v1/runners/claim 200=1" {
		t.Fatal("progress emitted an arbitrary exit code or lost closed counters")
	}
	for _, missing := range []string{"not_admitted", "unavailable", "missing"} {
		got := databaseBackupProgressProjection("target_read", missing, missing, missing, missing, missing, missing, missing, nil, databaseRunnerExit{}, counts)
		if got.ConnectionState != missing || got.CaptureRun != missing || got.CaptureLease != missing || got.CaptureReceipt != missing || got.UpgradeState != missing || got.ApplicationState != missing || got.RunnerCode != -1 {
			t.Fatal("progress fabricated state from unavailable evidence")
		}
	}
}

func TestDatabaseBackupProgressEmitsBeforeBrowserFailure(t *testing.T) {
	var output bytes.Buffer
	f := &databaseRunnerFixture{logs: &databaseRunnerLog{}}
	_, _ = f.logs.Write([]byte("database backup receipt request failed\n")) // An earlier intentionally interrupted process.
	b := &databaseBackupBrowserFixture{f: f, progressOutput: &output}
	b.recordProgress("restore_replayed", "")
	before := output.String()
	if !strings.Contains(before, `"Stage":"restore_replayed"`) || !strings.Contains(before, `"CaptureRun":"not_admitted"`) || !strings.Contains(before, `"TerminalClass":"not_exited"`) {
		t.Fatal("live milestone was not emitted independently of testing cleanup")
	}
	var joined sync.WaitGroup
	for range 8 {
		joined.Add(1)
		go func() {
			defer joined.Done()
			b.recordProgress("restore_replayed", "")
		}()
	}
	joined.Wait()
	if output.String() != before {
		t.Fatal("unchanged browser polls duplicated progress evidence")
	}
	b.recordProgress("ncr_progress_canary_private", "")
	if strings.Contains(output.String(), "ncr_progress_canary_private") || !strings.Contains(output.String(), `"Stage":"unknown"`) {
		t.Fatal("live progress bypassed the closed projection")
	}
}

func backupReadinessTestOperation(t *testing.T, server *httptest.Server) domain.HostingOperation {
	t.Helper()
	_, textPort, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(textPort)
	if err != nil {
		t.Fatal(err)
	}
	return domain.HostingOperation{Snapshot: domain.HostingSnapshot{HostPort: port, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 1}}, Result: &domain.HostingResult{ContainerID: strings.Repeat("a", 64)}}
}

func TestDatabaseBackupSourceRestartReadiness(t *testing.T) {
	var mu sync.Mutex
	probes, ready := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("X-NeroCD-Fixture-Instance", strings.Repeat("a", 12))
		if r.Method == http.MethodGet && r.URL.Path == "/ready" {
			probes++
			ready = probes >= 2
			if ready {
				_, _ = io.WriteString(w, "ready")
				return
			}
		}
		if r.Method == http.MethodGet && r.URL.Path == "/records/before" && ready {
			_, _ = io.WriteString(w, "saved_application_record")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	read := func() (int, string) {
		t.Helper()
		client := &http.Client{Timeout: time.Second}
		response, err := client.Get(server.URL + "/records/before")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(raw)
	}
	if status, _ := read(); status != http.StatusServiceUnavailable {
		t.Fatal("immediate post-start read did not reproduce unavailable database")
	}
	if err := backupRuntimeWaitApplicationReady(t.Context(), backupReadinessTestOperation(t, server)); err != nil {
		t.Fatal("source did not become ready", err)
	}
	if status, body := read(); status != http.StatusOK || body != "saved_application_record" {
		t.Fatal("single source data assertion did not follow readiness")
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 2 {
		t.Fatal("readiness did not observe the controlled unavailable-to-ready transition")
	}
}

func TestDatabaseBackupSourceReadinessRejectsWrongEvidenceAndCancellation(t *testing.T) {
	for _, kind := range []string{"instance", "body"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-NeroCD-Fixture-Instance", strings.Repeat("a", 12))
				body := "ready"
				if kind == "instance" {
					w.Header().Set("X-NeroCD-Fixture-Instance", strings.Repeat("b", 12))
				} else {
					body = "ncr_source_readiness_canary"
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			err := backupRuntimeWaitApplicationReady(ctx, backupReadinessTestOperation(t, server))
			if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "ncr_source_readiness_canary") {
				t.Fatal("wrong readiness evidence was accepted or leaked")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled readiness started a request") }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := backupRuntimeWaitApplicationReady(ctx, backupReadinessTestOperation(t, server)); !errors.Is(err, context.Canceled) {
		t.Fatal("readiness ignored caller cancellation")
	}
	started, handlerDone := make(chan struct{}), make(chan struct{})
	inflight := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(handlerDone)
	}))
	defer inflight.Close()
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	op := backupReadinessTestOperation(t, inflight)
	go func() { done <- backupRuntimeWaitApplicationReady(ctx, op) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("readiness worker did not join during cleanup")
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("readiness request did not start")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal("in-flight readiness ignored caller cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("readiness request did not join after cancellation")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("canceled readiness transport retained its request")
	}
}

func TestDatabaseBackupDiagnosticDeferredChild(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	f := &databaseRunnerFixture{t: t, logs: &databaseRunnerLog{}, lastExit: databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}}
	_, _ = f.logs.Write([]byte("invalid database backup plan response\n"))
	t.Cleanup(func() { fmt.Println("DATABASE_BACKUP_DIAGNOSTIC_CLEANUP") })
	id := ""
	defer f.reportDatabaseBackupFailureIfFailed(&id)
	t.Fatal("intentional diagnostic child failure")
}

func TestDatabaseBackupDiagnosticSurvivesFatalBeforeCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseBackupDiagnosticDeferredChild$")
	cmd.Env = append(os.Environ(), "NEROCD_DATABASE_BACKUP_DIAGNOSTIC_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil {
		t.Fatal("diagnostic child did not terminate with its intended failure")
	}
	start, cleanup := bytes.Index(out, []byte("DATABASE_BACKUP_FAILURE_EVIDENCE ")), bytes.Index(out, []byte("DATABASE_BACKUP_DIAGNOSTIC_CLEANUP"))
	if start < 0 || cleanup <= start || !bytes.Contains(out, []byte(`"TerminalClass":"plan_response"`)) || !bytes.Contains(out, []byte(`"State":"unknown"`)) || !bytes.Contains(out, []byte(`"RunnerExited":true`)) {
		t.Fatal("fatal path lost safe evidence before fixture cleanup")
	}
}
