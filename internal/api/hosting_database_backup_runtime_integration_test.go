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
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
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
		if code, ok := bytes.CutPrefix(line, []byte("remote backup transfer: ")); ok {
			switch string(code) {
			case "access_denied", "network_denied", "remote_unavailable", "object_mismatch", "archive_unavailable", "interrupted", "authority_denied":
				return "remote_" + string(code)
			default:
				return "unknown"
			}
		}
		switch string(line) {
		case "remote backup authority unavailable":
			return "remote_plan_authority"
		case "invalid PostgreSQL remote backup request":
			return "remote_response"
		case "remote backup credentials unavailable":
			return "remote_credentials"
		case "remote backup receipt request failed":
			return "remote_receipt_transport"
		case "remote backup evidence requires reconciliation":
			return "remote_evidence_held"
		case "invalid protected remote backup network policy":
			return "remote_network_policy"
		case "remote backup network denied":
			return "remote_network_denied"
		case "remote database backups require native Linux arm64":
			return "remote_platform"
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

func backupRuntimeApplicationObserved(f *databaseRunnerFixture, db domain.HostingDatabase, suffix string, observe func(string, string), namespaces ...string) domain.HostingOperation {
	f.t.Helper()
	progress := func(stage, connection string) {
		if observe != nil {
			observe(stage, connection)
		}
	}
	image := os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")
	storageSchedule := db.Engine == "mysql" && os.Getenv("NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME") == "1"
	if storageSchedule {
		image = os.Getenv("NEROCD_DATABASE_STORAGE_HTTP_APP_IMAGE")
	}
	if applicationDatabaseFixtureImage(image) != nil {
		f.t.Fatal("owned application image required")
	}
	resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: db.Resource.ProjectID, ProjectEnvironmentID: f.environment, Name: "backup app " + suffix, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
	progress("application_created", "")
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
	progress("application_bound", "")
	namespace := "backup_restored"
	if len(namespaces) > 1 {
		f.t.Fatal("one application namespace required")
	}
	if len(namespaces) == 1 {
		namespace = namespaces[0]
	}
	entries := []domain.HostingRuntimeEntry{}
	if storageSchedule {
		value := "1"
		entries = append(entries, domain.HostingRuntimeEntry{Name: "FIXTURE_UPLOADS", Kind: "literal", Value: &value})
	}
	for _, v := range [][2]string{{"FIXTURE_DATABASE_ENGINE", db.Engine}, {"FIXTURE_DATABASE_NAMESPACE", namespace}} {
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

// qualifyMySQLConnectedBackup extends the connected storage workflow in place.
// The client reads both snapshots through independently admitted bindings.
func (f *databaseRunnerFixture) qualifyMySQLConnectedBackup(source domain.HostingDatabase, sourceOp, application domain.HostingOperation) {
	f.t.Helper()
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	f.start()
	wire := &databaseBackupRuntimeWire{mode: "backup", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser := startDatabaseBackupBrowser(f, source)
	diagnosticID = browser.await("backup")
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatal("MySQL backup receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	backup := backupRuntimeAwait(f, diagnosticID)
	f.backupWire.Store(nil)
	if backup.Version != domain.HostingDatabaseMySQLBackupVersion || backup.Blob.Format != "mysql_sql" || backup.Consistency != "logical_snapshot_source_writable" {
		f.t.Fatal("MySQL backup public contract differs")
	}
	f.appRecord(application, "PUT", "source_only", "after_snapshot")
	f.docker("container", "stop", sourceOp.Result.ContainerID)
	backupRuntimeRegisterSourceRestartCleanup(f.t, sourceOp.Result.ContainerID, func(ctx context.Context, id string) error {
		raw, err := databaseRunnerDocker(ctx, nil, "container", "start", id)
		clear(raw)
		return err
	})
	wire = &databaseBackupRuntimeWire{mode: "restore", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser.setPhase("restore", backup.ID)
	diagnosticID = browser.await("restore")
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatal("MySQL restore receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	f.backupWire.Store(nil)
	restored := backupRuntimeAwait(f, diagnosticID)
	if restored.TargetResourceID == source.Resource.ID || restored.BackupID != backup.ID || restored.Blob.Digest != backup.Blob.Digest {
		f.t.Fatal("MySQL restore changed source/target archive identity")
	}
	target := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	f.registerRouteDatabaseCleanup(target.Resource.ID)
	if target.Engine != "mysql" || target.RecipeVersion != source.RecipeVersion || target.Binding.ID == source.Binding.ID || target.StorageRef == source.StorageRef || target.Credentials[0].SecretID == source.Credentials[0].SecretID {
		f.t.Fatal("MySQL restore reused source identity")
	}
	b := backupRuntimeApplicationObserved(f, target, "mysql_restored", browser.recordProgress, "connected_mysql")
	if b.ApplicationDatabase == nil {
		f.t.Fatal("restored app lacks admitted database snapshot")
	}
	binding := b.ApplicationDatabase.Binding
	credential := target.Credentials[0]
	for _, candidate := range target.Credentials {
		if candidate.Role == "app" {
			credential = candidate
		}
	}
	if binding.DatabaseResourceID != target.Resource.ID || binding.DatabaseBindingID != target.Binding.ID || binding.Alias != domain.HostingDatabaseBindingAlias(target.Binding.ID) || binding.Credential.SecretID != credential.SecretID || binding.Credential.Version != credential.Version || binding.Database.Engine != "mysql" {
		f.t.Fatal("restored app did not pin exact target alias and credential version")
	}
	f.appRecord(b, "GET", "a", "retained_a")
	// The original server remains stopped. Reading a source record proves this
	// application uses the restored archive rather than the original endpoint.
	if applicationDatabaseHTTPRecord(f.t.Context(), b, "GET", "source_only", "after_snapshot") == nil {
		f.t.Fatal("restore included a post-backup source-only record")
	}
	f.appRecord(b, "PUT", "restored_only", "restored_new_write")
	f.appRecord(b, "GET", "restored_only", "restored_new_write")
	f.docker("container", "start", sourceOp.Result.ContainerID)
	if backupRuntimeWaitApplicationReady(f.t.Context(), application) != nil {
		f.t.Fatal("MySQL source did not regain exact-instance readiness")
	}
	f.appRecord(application, "GET", "a", "retained_a")
	f.appRecord(application, "GET", "source_only", "after_snapshot")
	if applicationDatabaseHTTPRecord(f.t.Context(), application, "GET", "restored_only", "restored_new_write") == nil {
		f.t.Fatal("restore writes reached original source")
	}
	f.storage.assertFiles(f, application)
	f.routes.assertSelected(application)
	f.routes.record(f.t.Context(), application, "GET", "source_only", "after_snapshot")
	browser.setPhase("complete", restored.ID)
	browser.finished()
	f.stop()
	fmt.Println("DATABASE_BACKUP_RESTORE_APP_PASS mysql")
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

func (b *databaseBackupBrowserFixture) awaitSchedule() domain.HostingDatabaseBackupSchedule {
	b.f.t.Helper()
	select {
	case schedule := <-b.schedules:
		return schedule
	case <-time.After(120 * time.Second):
		b.f.t.Fatal("live schedule UI did not admit exact revision")
	}
	return domain.HostingDatabaseBackupSchedule{}
}
func backupRuntimeRestartController(f *databaseRunnerFixture) {
	f.t.Helper()
	if f.hostingStop == nil {
		f.t.Fatal("owned controller stop authority missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := f.hostingStop(ctx)
	cancel()
	if err != nil {
		f.t.Fatal("owned controller did not join")
	}
	stop, err := f.hostingStore.StartHostingExecution(f.t.Context())
	if err != nil {
		f.t.Fatal("owned controller restart failed")
	}
	f.hostingStop = stop
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stop(ctx) != nil {
			f.t.Error("restarted controller did not join")
		}
	})
}

func TestHostingDatabaseBackupScheduleCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_BACKUP_SCHEDULE_RUNTIME") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact authorized schedule runtime gate")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	mysql := os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1"
	f := newDatabaseRunnerFixtureWithProfile(t, true, true, true, mysql)
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	create := backupRuntimeCreateInput(t, f.environment, f.destination)
	if mysql {
		create = applicationDatabaseCreateInput("mysql", f.environment, f.destination)
		create.RequestID = backupRuntimeRequestID(t)
	}
	source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", create, 201)
	f.registerRouteDatabaseCleanup(source.Resource.ID)
	requested := f.deploy(source, "schedule_source")
	f.registerRouteOperationCleanup(requested.ID)
	f.await(requested.ID)
	application := backupRuntimeApplication(f, source, "scheduled_source")
	if mysql {
		resource := databaseRunnerJSON[domain.Resource](f, f.token, "GET", "/api/v1/resources/"+application.ResourceID, nil, 200)
		application = f.storage.bootstrap(f, resource, application, domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 30}})
		f.appRecord(application, "PUT", "a", "retained_a")
		roots, public := newApplicationDatabaseRouteTrust(t)
		f.initialApplicationDatabaseRoute(application, "mysql", roots, public)
		f.storage.write(f, application, "retained_a", 0x41)
	}
	f.appRecord(application, "PUT", "before", "application_origin")
	connection := f.connection(application.ResourceID)
	sourceOp := f.await(connection.UpgradeOperationID)
	if !mysql {
		f.docker("exec", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; CREATE TABLE public.backup_sequence(id bigserial PRIMARY KEY,value text); INSERT INTO public.backup_sequence(value) VALUES ('before');")
	}
	wire := &databaseBackupRuntimeWire{mode: "backup", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser := startDatabaseBackupBrowserProfile(f, source, "backup_schedule")
	created := browser.awaitSchedule()
	if created.Revision != 1 || !created.Enabled || created.Frequency != "every_minute" || created.Timezone != "UTC" || mysql && created.Version != "mysql.logical-local.schedule.v1" {
		t.Fatal("live schedule creation lost reviewed configuration")
	}
	// Discovery must come from the running hosting controller, not a manual
	// test tick that could conceal a missing StartHostingExecution hookup.
	var backupID string
	discoveryDeadline := time.Now().Add(90 * time.Second)
	for {
		err := f.database.QueryRow(t.Context(), `SELECT operation_id FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1 AND operation_id IS NOT NULL`, created.ID).Scan(&backupID)
		if err == nil && backupID != "" {
			break
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) || time.Now().After(discoveryDeadline) {
			t.Fatal("running schedule controller did not admit exact operation")
		}
		time.Sleep(100 * time.Millisecond)
	}
	diagnosticID = backupID
	browser.setPhase("backup", backupID)
	edited := browser.awaitSchedule()
	disabled := browser.awaitSchedule()
	if edited.ID != created.ID || edited.Revision != 2 || edited.Frequency != "* * * * *" || edited.Timezone != "Etc/UTC" || !edited.Enabled || disabled.ID != created.ID || disabled.Revision != 3 || disabled.Enabled {
		t.Fatal("live schedule edit/disable changed identity or revisions")
	}
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("scheduled backup receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	backupRuntimeRestartController(f)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	backup := backupRuntimeAwait(f, backupID)
	f.backupWire.Store(nil)
	if mysql && (backup.Version != domain.HostingDatabaseMySQLBackupVersion || backup.Blob.Format != "mysql_sql") {
		t.Fatal("scheduled MySQL artifact public contract changed")
	}
	if f.hostingStore.ReconcileDatabaseBackupSchedules(t.Context()) != nil {
		t.Fatal("restarted schedule controller failed")
	}
	var occurrences, minutes, operations int
	if f.database.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT scheduled_at),count(operation_id) FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, created.ID).Scan(&occurrences, &minutes, &operations) != nil || occurrences < 1 || minutes != occurrences || operations != 1 {
		t.Fatal("controller restart duplicated schedule occurrence")
	}
	var configs, requests int
	if f.database.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_database_backup_schedule_versions WHERE schedule_id=$1),(SELECT count(*) FROM hosting_database_backup_schedule_requests WHERE schedule_id=$1)`, created.ID).Scan(&configs, &requests) != nil || configs != 3 || requests != 3 {
		t.Fatal("lost schedule ACK created duplicate versions or requests")
	}
	f.appRecord(application, "PUT", "after_snapshot", "source_still_writable")
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
		t.Fatal("scheduled restore receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	backupRuntimeWaitReplay(f, wire)
	f.backupWire.Store(nil)
	restored := backupRuntimeAwait(f, restoreID)
	target := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	f.registerRouteDatabaseCleanup(target.Resource.ID)
	if target.Resource.ID == source.Resource.ID || target.Binding.ID == source.Binding.ID || target.StorageRef == source.StorageRef || target.Credentials[0].SecretID == source.Credentials[0].SecretID || restored.BackupID != backup.ID || restored.Blob.Digest != backup.Blob.Digest {
		t.Fatal("scheduled restore reused source identity or changed archive")
	}
	consumer := backupRuntimeApplicationObserved(f, target, "scheduled_target", browser.recordProgress)
	f.appRecord(consumer, "GET", "before", "application_origin")
	f.appRecord(consumer, "PUT", "restored_new", "restored_new_write")
	if !mysql {
		upgraded := f.connection(consumer.ResourceID)
		targetOp := f.await(upgraded.UpgradeOperationID)
		sequence := string(f.docker("exec", "--user", "postgres", targetOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; INSERT INTO public.backup_sequence(value) VALUES ('new'); SELECT max(id),pg_get_userbyid(c.relowner) FROM public.backup_sequence,pg_class c WHERE c.oid='public.backup_sequence'::regclass GROUP BY c.relowner;"))
		if !strings.Contains(sequence, "2") || !strings.Contains(sequence, "ncd_app") {
			t.Fatal("scheduled restore lost sequence ownership")
		}
	} else {
		credential := target.Credentials[0]
		for _, candidate := range target.Credentials {
			if candidate.Role == "app" {
				credential = candidate
			}
		}
		if consumer.ApplicationDatabase == nil || consumer.ApplicationDatabase.Binding.DatabaseResourceID != target.Resource.ID || consumer.ApplicationDatabase.Binding.DatabaseBindingID != target.Binding.ID || consumer.ApplicationDatabase.Binding.Alias != domain.HostingDatabaseBindingAlias(target.Binding.ID) || credential.SecretID == "" || consumer.ApplicationDatabase.Binding.Credential.SecretID != credential.SecretID || consumer.ApplicationDatabase.Binding.Credential.Version != credential.Version || consumer.ApplicationDatabase.Binding.Database.Engine != "mysql" {
			t.Fatal("scheduled MySQL consumer lost exact target binding")
		}
		if applicationDatabaseHTTPRecord(t.Context(), consumer, "GET", "after_snapshot", "source_still_writable") == nil {
			t.Fatal("scheduled restore read original source instead of snapshot")
		}
		route := &applicationDatabaseRouteFixture{f: f, roots: f.routes.roots, publicRoot: f.routes.publicRoot, proxy: f.routes.proxy, hostname: "restored-mysql.example.test", daemonID: consumer.Release.DaemonID}
		databaseRunnerJSON[domain.HostingHostnameGrant](f, f.adminToken, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: consumer.ProjectID, Hostname: route.hostname}, 201)
		route.route = databaseRunnerJSON[domain.HostingRoute](f, f.token, "POST", "/api/v1/resources/"+consumer.ResourceID+"/routes", domain.HostingRouteInput{IdempotencyKey: "scheduled_restored_route", Hostname: route.hostname, PathPrefix: "/", ExpectedHealthyOperationID: consumer.ID}, 202)
		route.awaitProxy()
		route.assertSelected(consumer)
		route.record(t.Context(), consumer, "GET", "before", "application_origin")
		route.record(t.Context(), consumer, "PUT", "restored_https", "independent_target_write")
		route.record(t.Context(), consumer, "GET", "restored_https", "independent_target_write")
	}
	f.docker("container", "start", sourceOp.Result.ContainerID)
	if backupRuntimeWaitApplicationReady(t.Context(), application) != nil {
		t.Fatal("scheduled source did not regain exact readiness")
	}
	f.appRecord(application, "GET", "before", "application_origin")
	f.appRecord(application, "GET", "after_snapshot", "source_still_writable")
	f.appRecord(application, "PUT", "source_new", "source_new_write")
	if mysql {
		if applicationDatabaseHTTPRecord(t.Context(), application, "GET", "restored_new", "restored_new_write") == nil || applicationDatabaseHTTPRecord(t.Context(), application, "GET", "restored_https", "independent_target_write") == nil {
			t.Fatal("scheduled target writes reached original source")
		}
		f.storage.assertFiles(f, application)
		f.routes.assertSelected(application)
		f.routes.record(t.Context(), application, "GET", "after_snapshot", "source_still_writable")
		var due time.Time
		if f.database.QueryRow(t.Context(), `SELECT max(scheduled_at) FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1`, created.ID).Scan(&due) != nil {
			t.Fatal("scheduled occurrence clock unavailable")
		}
		// Cross a real subsequent due minute while disabled. The running
		// controller remains responsible for discovery; no fake tick time.
		for time.Now().Before(due.Add(time.Minute + time.Second)) {
			time.Sleep(100 * time.Millisecond)
		}
		if f.hostingStore.ReconcileDatabaseBackupSchedules(t.Context()) != nil {
			t.Fatal("disabled schedule reconciliation failed")
		}
		var admitted int
		if f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1 AND operation_id IS NOT NULL`, created.ID).Scan(&admitted) != nil || admitted != 1 {
			t.Fatal("disabled MySQL schedule admitted a later backup")
		}
	}
	browser.setPhase("complete", restored.ID)
	browser.finished()
	if mysql {
		backupRuntimeMySQLScheduleDenied(f, source, application, disabled)
		fmt.Println("DATABASE_BACKUP_SCHEDULE_PASS mysql")
	} else {
		backupRuntimeGuestDumpTimeout(f, source, sourceOp, application)
		fmt.Println("DATABASE_BACKUP_SCHEDULE_PASS postgresql")
	}
}

// Reuse the owned controller lifecycle and ordinary schedule/grant APIs. No
// backup run is admitted after current destination authority is revoked.
func backupRuntimeMySQLScheduleDenied(f *databaseRunnerFixture, source domain.HostingDatabase, application domain.HostingOperation, disabled domain.HostingDatabaseBackupSchedule) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := f.hostingStop(ctx)
	cancel()
	if err != nil {
		f.t.Fatal("schedule controller did not join before denied admission")
	}
	path := "/api/v1/resources/" + source.Resource.ID + "/database-backup-schedules/" + disabled.ID
	input := domain.HostingDatabaseBackupScheduleInput{RequestID: backupRuntimeRequestID(f.t), ExpectedResourceGeneration: source.Resource.Generation, ExpectedVersion: disabled.Revision, Frequency: "every_minute", Timezone: "UTC", Enabled: true}
	enabled := databaseRunnerJSON[domain.HostingDatabaseBackupSchedule](f, f.token, "PUT", path, input, 200)
	restore := backupRuntimeRegisterGrantCleanup(f, source.Resource.ProjectID, f.adminToken)
	databaseRunnerJSON[json.RawMessage](f, f.adminToken, "POST", "/api/v1/destinations/"+f.destination+"/project-grants/revoke", app.DestinationProjectInput{ProjectID: source.Resource.ProjectID}, 200)
	backupRuntimeRestartController(f)
	deadline := time.Now().Add(90 * time.Second)
	for {
		var state, reason string
		var admitted bool
		err = f.database.QueryRow(f.t.Context(), `SELECT state,reason,operation_id IS NOT NULL FROM hosting_database_backup_schedule_occurrences WHERE schedule_id=$1 AND schedule_revision=$2`, enabled.ID, enabled.Revision).Scan(&state, &reason, &admitted)
		if err == nil {
			if state != "skipped" || reason != "source_unavailable" || admitted {
				f.t.Fatal("denied schedule claimed an admitted operation")
			}
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) || time.Now().After(deadline) {
			f.t.Fatal("running controller omitted denied schedule history")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if restore() != nil {
		f.t.Fatal("owned schedule grant restoration failed")
	}
	input.RequestID = backupRuntimeRequestID(f.t)
	input.ExpectedVersion = enabled.Revision
	input.Enabled = false
	settled := databaseRunnerJSON[domain.HostingDatabaseBackupSchedule](f, f.token, "PUT", path, input, 200)
	if settled.Enabled || settled.Revision != enabled.Revision+1 {
		f.t.Fatal("denied schedule did not disable exactly")
	}
	occurrences := databaseRunnerJSON[[]domain.HostingDatabaseBackupOccurrence](f, f.token, "GET", path+"/occurrences", nil, 200)
	found := false
	for _, occurrence := range occurrences {
		if occurrence.ScheduleRevision == enabled.Revision {
			found = occurrence.State == "skipped" && occurrence.Reason == "source_unavailable" && occurrence.OperationID == "" && !occurrence.OwnershipHeld
		}
	}
	if !found {
		f.t.Fatal("ordinary denied occurrence projection changed")
	}
	f.appRecord(application, "GET", "before", "application_origin")
	f.appRecord(application, "PUT", "after_denial", "source_remains_usable")
	f.routes.assertSelected(application)
	f.routes.record(f.t.Context(), application, "GET", "after_denial", "source_remains_usable")
	f.storage.assertFiles(f, application)
	fmt.Println("DATABASE_BACKUP_SCHEDULE_ADMISSION_FAILURE_PASS mysql source_unavailable")
}

// This is a pg_dump failure, separate from the existing pg_restore deadline.
// A positively observed source-table DDL lock stalls the real pinned dump tool.
func backupRuntimeGuestDumpTimeout(f *databaseRunnerFixture, source domain.HostingDatabase, sourceOp, application domain.HostingOperation) {
	f.t.Helper()
	diagnosticID := ""
	defer f.reportDatabaseBackupFailureIfFailed(&diagnosticID)
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 40*time.Second)
	lockDone := make(chan struct{}, 1)
	go func() {
		raw, _ := databaseRunnerDocker(lockCtx, nil, "exec", "--env", "PGAPPNAME=fixture_backup_dump_lock", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--command", "SET statement_timeout='25s'; BEGIN; LOCK TABLE public.backup_sequence IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(30);")
		clear(raw)
		lockDone <- struct{}{}
	}()
	release := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := databaseRunnerDocker(ctx, nil, "exec", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='fixture_backup_dump_lock' AND usename='postgres';")
		clear(raw)
		if err != nil {
			f.t.Error("owned dump-lock cleanup failed")
		}
		cancelLock()
		select {
		case <-lockDone:
		case <-ctx.Done():
			f.t.Error("owned dump-lock process did not join")
		}
	}
	defer release()
	deadline := time.Now().Add(5 * time.Second)
	var lockPID int
	for {
		ctx, cancel := context.WithTimeout(f.t.Context(), 2*time.Second)
		raw, queryErr := databaseRunnerDocker(ctx, nil, "exec", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--tuples-only", "--no-align", "--command", "SELECT a.pid FROM pg_stat_activity a WHERE a.application_name='fixture_backup_dump_lock' AND a.usename='postgres' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.mode='AccessExclusiveLock' AND l.granted AND l.relation='public.backup_sequence'::regclass);")
		cancel()
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		clear(raw)
		if queryErr != nil {
			f.t.Fatal("owned source lock observation failed")
		}
		if err == nil && pid > 1 {
			lockPID = pid
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatal("owned source lock was not observed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	requested := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", "/api/v1/resources/"+source.Resource.ID+"/database-backups", domain.HostingDatabaseBackupInput{RequestID: "genuine_guest_dump_timeout", ExpectedResourceGeneration: source.Resource.Generation}, 202)
	diagnosticID = requested.ID
	if requested.TimeoutSeconds != 10 {
		f.t.Fatal("saved dump timeout policy mismatch")
	}
	proofCtx, cancelProof := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancelProof()
	var proof backupRuntimeDumpDeadlineProof
	for {
		ctx, cancel := context.WithTimeout(proofCtx, 3*time.Second)
		sample, err := backupRuntimeObserveGuestDump(ctx, f, sourceOp.Result.ContainerID, requested.ID, lockPID, requested.TimeoutSeconds, proof.execID)
		cancel()
		if err != nil {
			f.t.Fatal("guest dump deadline observation failed")
		}
		done, err := proof.advance(sample)
		if err != nil {
			f.t.Fatal("guest dump deadline proof rejected")
		}
		if done {
			break
		}
		select {
		case <-proofCtx.Done():
			f.t.Fatal("guest dump deadline proof did not complete")
		case <-time.After(100 * time.Millisecond):
		}
	}
	// The guest process and PostgreSQL session must already be absent while
	// the exact blocking lock remains held. Stopping the runner cannot satisfy
	// either proof. Only now may the fixture join it and exercise replay.
	f.stop()
	f.appRecord(application, "GET", "before", "application_origin")
	f.appRecord(application, "PUT", "after_dump_timeout", "source_usable")
	f.start()
	select {
	case <-f.process.done:
	case <-time.After(15 * time.Second):
		f.t.Fatal("uncertain dump unexpectedly resumed")
	}
	f.stop()
	current := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "GET", "/api/v1/database-backups/"+requested.ID, nil, 200)
	if current.State != "uncertain" || !current.OwnershipHeld {
		f.t.Fatal("dump restart changed uncertain ownership")
	}
	fmt.Println("DATABASE_BACKUP_GUEST_DUMP_TIMEOUT_PASS postgresql")
}

// These observations are fixture-private, never public API evidence or authority.
type backupRuntimeDumpSample struct {
	LockHeld              bool `json:"lock_held"`
	BlockedPID            int  `json:"blocked_pid"`
	Sessions              int  `json:"sessions"`
	Processes             int
	ExecID                string
	ExecRunning           bool
	ExecExitCode, ExecPID int
	Uncertain, Held, Blob bool
}
type backupRuntimeDumpDeadlineProof struct {
	blockedPID int
	execID     string
}

func (p *backupRuntimeDumpDeadlineProof) advance(s backupRuntimeDumpSample) (bool, error) {
	invalid := errors.New("guest dump deadline proof rejected")
	if !s.LockHeld || s.Sessions < 0 || s.Sessions > 1 || s.Processes < 0 || s.Processes > 1 || s.BlockedPID < 0 || s.Uncertain && (!s.Held || s.Blob) {
		return false, invalid
	}
	if s.BlockedPID > 1 {
		if p.blockedPID != 0 && p.blockedPID != s.BlockedPID {
			return false, invalid
		}
		if p.blockedPID == 0 {
			if s.Sessions != 1 || s.Processes != 1 {
				return false, invalid
			}
			if s.ExecID != "" && s.ExecRunning && s.ExecPID > 1 {
				p.blockedPID = s.BlockedPID
				p.execID = s.ExecID
			}
		}
		// SQL and docker top are separate reads. Once the exact blocked
		// guest was proved, expiry may occur between them. A late mixed
		// sample stays pending; only a later all-absent sample may complete.
	}
	if s.Uncertain && p.blockedPID == 0 {
		return false, invalid
	}
	if s.ExecID != "" && !backupRuntimeValidExecID(s.ExecID) || p.execID != "" && s.ExecID != p.execID {
		return false, invalid
	}
	if p.execID != "" && !s.ExecRunning && s.ExecExitCode != 124 {
		return false, invalid
	}
	// The exact GNU timeout exec must itself report timeout termination. Request
	// age, queue delay and when polling first noticed the guest are irrelevant.
	return p.blockedPID != 0 && p.execID != "" && !s.ExecRunning && s.ExecExitCode == 124 && s.Uncertain && s.Processes == 0 && s.Sessions == 0 && s.BlockedPID == 0, nil
}

func backupRuntimeObserveGuestDump(ctx context.Context, f *databaseRunnerFixture, container, operation string, lockPID, seconds int, execID string) (backupRuntimeDumpSample, error) {
	var sample backupRuntimeDumpSample
	invalid := errors.New("guest dump observation unavailable")
	if lockPID <= 1 {
		return sample, invalid
	}
	// lockPID is an observed integer, not caller-provided SQL. Both the owner
	// session and the waiting pg_dump must target this exact table and database.
	query := fmt.Sprintf(`SELECT json_build_object(
 'lock_held',EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.pid=%d AND a.application_name='fixture_backup_dump_lock' AND a.usename='postgres' AND a.datname='app' AND l.mode='AccessExclusiveLock' AND l.granted AND l.relation='public.backup_sequence'::regclass),
 'blocked_pid',COALESCE((SELECT min(a.pid) FROM pg_stat_activity a WHERE a.application_name='pg_dump' AND a.usename='postgres' AND a.datname='app' AND a.wait_event_type='Lock' AND %d=ANY(pg_blocking_pids(a.pid)) AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND NOT l.granted AND l.relation='public.backup_sequence'::regclass)),0),
 'sessions',(SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump' AND usename='postgres' AND datname='app'))`, lockPID, lockPID)
	raw, err := databaseRunnerDocker(ctx, nil, "exec", "--user", "postgres", container, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command", query)
	if err != nil {
		return sample, invalid
	}
	defer clear(raw)
	if len(raw) > 1024 || json.Unmarshal(bytes.TrimSpace(raw), &sample) != nil {
		return sample, invalid
	}
	top, err := databaseRunnerDocker(ctx, nil, "container", "top", container, "-eo", "pid,comm")
	if err != nil {
		return sample, invalid
	}
	defer clear(top)
	processes, err := backupRuntimeDumpProcesses(top)
	if err != nil {
		return sample, invalid
	}
	sample.Processes = processes
	guest, err := backupRuntimeGuestDumpExec(ctx, container, seconds, execID)
	if err != nil {
		return sample, invalid
	}
	if guest != nil {
		sample.ExecID = guest.ID
		sample.ExecRunning = guest.Running
		sample.ExecPID = guest.Pid
		if guest.ExitCode != nil {
			sample.ExecExitCode = *guest.ExitCode
		}
	}
	err = f.database.QueryRow(ctx, `SELECT state='uncertain',ownership_held,blob IS NOT NULL FROM hosting_database_backups WHERE id=$1 AND mode='backup'`, operation).Scan(&sample.Uncertain, &sample.Held, &sample.Blob)
	if err != nil {
		return sample, invalid
	}
	return sample, nil
}

// Exec inspection reads only the fixed owned Docker socket. It neither starts
// an exec nor changes its lifetime. The completed ID remains the proof even
// after it disappears from the container's active ExecIDs collection.
type backupRuntimeGuestExec struct {
	ID, ContainerID string
	Running         bool
	ExitCode        *int
	Pid             int
	ProcessConfig   struct {
		Entrypoint      string   `json:"entrypoint"`
		Arguments       []string `json:"arguments"`
		User            string   `json:"user"`
		Privileged, Tty bool
	}
}

func backupRuntimeValidExecID(id string) bool {
	return len(id) == 64 && strings.Trim(id, "0123456789abcdef") == ""
}
func backupRuntimeGuestDumpExecMatches(v backupRuntimeGuestExec, container string, seconds int) bool {
	expected := []string{"--signal=TERM", "--kill-after=5s", strconv.Itoa(seconds) + "s", "pg_dump", "--username=postgres", "--dbname=app", "--format=custom", "--no-owner", "--no-acl"}
	return backupRuntimeValidExecID(v.ID) && v.ContainerID == container && v.ProcessConfig.Entrypoint == "timeout" && v.ProcessConfig.User == "postgres" && !v.ProcessConfig.Privileged && !v.ProcessConfig.Tty && slices.Equal(v.ProcessConfig.Arguments, expected) && (v.Running && v.Pid > 1 || !v.Running && v.ExitCode != nil)
}
func backupRuntimeReadGuestExec(ctx context.Context, client *http.Client, id string) (backupRuntimeGuestExec, error) {
	var value backupRuntimeGuestExec
	invalid := errors.New("guest exec inspection unavailable")
	if !backupRuntimeValidExecID(id) {
		return value, invalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.54/exec/"+id+"/json", nil)
	if err != nil {
		return value, invalid
	}
	response, err := client.Do(request)
	if err != nil {
		return value, invalid
	}
	// The bounded read and exact JSON identity determine the observation result.
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	defer clear(raw)
	if err != nil || response.StatusCode != http.StatusOK || len(raw) > 16<<10 || json.Unmarshal(raw, &value) != nil || value.ID != id {
		return value, invalid
	}
	return value, nil
}
func backupRuntimeGuestDumpExec(ctx context.Context, container string, seconds int, known string) (*backupRuntimeGuestExec, error) {
	invalid := errors.New("guest exec observation unavailable")
	if !backupRuntimeValidExecID(container) || seconds < 1 || seconds > 3600 {
		return nil, invalid
	}
	ids := []string{known}
	if known == "" {
		raw, err := databaseRunnerDocker(ctx, nil, "container", "inspect", container, "--format", "{{json .ExecIDs}}")
		if err != nil {
			return nil, invalid
		}
		defer clear(raw)
		if len(raw) > 2048 || json.Unmarshal(raw, &ids) != nil || len(ids) > 16 {
			return nil, invalid
		}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var matched *backupRuntimeGuestExec
	for _, id := range ids {
		value, err := backupRuntimeReadGuestExec(ctx, client, id)
		if err != nil {
			return nil, invalid
		}
		if backupRuntimeGuestDumpExecMatches(value, container, seconds) {
			if matched != nil {
				return nil, invalid
			}
			matched = &value
		} else if known != "" {
			return nil, invalid
		}
	}
	return matched, nil
}

func backupRuntimeDumpProcesses(raw []byte) (int, error) {
	invalid := errors.New("guest process observation unavailable")
	if len(raw) == 0 || len(raw) > 64<<10 {
		return 0, invalid
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if strings.Join(strings.Fields(lines[0]), " ") != "PID COMMAND" {
		return 0, invalid
	}
	count := 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return 0, invalid
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return 0, invalid
		}
		if fields[1] == "pg_dump" {
			count++
		}
	}
	return count, nil
}

func TestDatabaseBackupGuestDumpDeadlineProofRejectsFalsePositives(t *testing.T) {
	id := strings.Repeat("e", 64)
	blocked := backupRuntimeDumpSample{LockHeld: true, BlockedPID: 42, Sessions: 1, Processes: 1, Held: true, ExecID: id, ExecRunning: true, ExecPID: 123}
	absent := backupRuntimeDumpSample{LockHeld: true, Uncertain: true, Held: true, ExecID: id, ExecExitCode: 124}
	var proof backupRuntimeDumpDeadlineProof
	if done, err := proof.advance(absent); err == nil || done {
		t.Fatal("uncertainty without observed blocked guest accepted")
	}
	proof = backupRuntimeDumpDeadlineProof{}
	unproved := blocked
	unproved.Processes = 0
	if done, err := proof.advance(unproved); err == nil || done {
		t.Fatal("initial process proof omitted")
	}
	proof = backupRuntimeDumpDeadlineProof{}
	if done, err := proof.advance(blocked); err != nil || done {
		t.Fatal("valid blocked guest rejected")
	}
	transition := blocked
	transition.Processes = 0
	transition.ExecRunning = false
	transition.ExecExitCode = 124
	transition.Uncertain = true
	if done, err := proof.advance(transition); err != nil || done {
		t.Fatal("expiry between SQL and process observations was not retried")
	}
	if done, err := proof.advance(absent); err != nil || !done {
		t.Fatal("exact timeout termination rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*backupRuntimeDumpSample)
	}{
		{"delayed_admission_early_guest_failure", func(s *backupRuntimeDumpSample) { s.ExecExitCode = 1 }},
		{"normal_exit_without_timeout", func(s *backupRuntimeDumpSample) { s.ExecExitCode = 0 }},
		{"unexplained_kill", func(s *backupRuntimeDumpSample) { s.ExecExitCode = 137 }},
		{"foreign_exec", func(s *backupRuntimeDumpSample) { s.ExecID = strings.Repeat("f", 64) }},
		{"missing_exec", func(s *backupRuntimeDumpSample) { s.ExecID = "" }},
		{"lock_released", func(s *backupRuntimeDumpSample) { s.LockHeld = false }},
		{"released_hold", func(s *backupRuntimeDumpSample) { s.Held = false }},
		{"published_blob", func(s *backupRuntimeDumpSample) { s.Blob = true }},
		{"extra_guest", func(s *backupRuntimeDumpSample) { s.Sessions = 2; s.Processes = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Even if admission took eleven seconds and the guest then failed after one
			// second, request age cannot turn its non-timeout exit into timeout proof.
			p := backupRuntimeDumpDeadlineProof{blockedPID: 42, execID: id}
			sample := absent
			tc.change(&sample)
			if done, err := p.advance(sample); err == nil || done {
				t.Fatal("false deadline proof accepted")
			}
		})
	}
	p := backupRuntimeDumpDeadlineProof{blockedPID: 42, execID: id}
	changed := blocked
	changed.BlockedPID = 43
	if done, err := p.advance(changed); err == nil || done {
		t.Fatal("changed backend accepted")
	}
	pending := absent
	pending.Processes = 1
	if done, err := p.advance(pending); err != nil || done {
		t.Fatal("live guest treated as absent")
	}

	for _, raw := range []string{"", "CANARY_PRIVATE", "PID COMMAND\nnot-a-pid pg_dump", "PID COMMAND\n1 pg_dump extra"} {
		if _, err := backupRuntimeDumpProcesses([]byte(raw)); err == nil {
			t.Fatal("malformed process output accepted")
		}
	}
	for _, tc := range []struct {
		raw  string
		want int
	}{{"PID COMMAND\n1 postgres\n2 pg_dump", 1}, {"PID COMMAND\n1 postgres\n2 pg_dump_other", 0}} {
		if n, err := backupRuntimeDumpProcesses([]byte(tc.raw)); err != nil || n != tc.want {
			t.Fatal("guest process identity weakened")
		}
	}
}

func TestDatabaseBackupGuestExecInspection(t *testing.T) {
	id, container := strings.Repeat("e", 64), strings.Repeat("c", 64)
	valid := func() backupRuntimeGuestExec {
		v := backupRuntimeGuestExec{ID: id, ContainerID: container, Running: true, Pid: 123}
		v.ProcessConfig.Entrypoint = "timeout"
		v.ProcessConfig.User = "postgres"
		v.ProcessConfig.Arguments = []string{"--signal=TERM", "--kill-after=5s", "10s", "pg_dump", "--username=postgres", "--dbname=app", "--format=custom", "--no-owner", "--no-acl"}
		return v
	}
	for _, tc := range []struct {
		name   string
		change func(*backupRuntimeGuestExec)
		want   bool
	}{
		{"running", func(*backupRuntimeGuestExec) {}, true},
		{"completed_timeout", func(v *backupRuntimeGuestExec) { v.Running = false; code := 124; v.ExitCode = &code }, true},
		{"foreign_container", func(v *backupRuntimeGuestExec) { v.ContainerID = strings.Repeat("a", 64) }, false},
		{"foreign_user", func(v *backupRuntimeGuestExec) { v.ProcessConfig.User = "CANARY_PRIVATE" }, false},
		{"altered_deadline", func(v *backupRuntimeGuestExec) { v.ProcessConfig.Arguments[2] = "1s" }, false},
		{"altered_tool", func(v *backupRuntimeGuestExec) { v.ProcessConfig.Arguments[3] = "pg_restore" }, false},
		{"extra_argument", func(v *backupRuntimeGuestExec) {
			v.ProcessConfig.Arguments = append(v.ProcessConfig.Arguments, "CANARY_PRIVATE")
		}, false},
		{"shell_wrapper", func(v *backupRuntimeGuestExec) { v.ProcessConfig.Entrypoint = "sh" }, false},
		{"privileged", func(v *backupRuntimeGuestExec) { v.ProcessConfig.Privileged = true }, false},
		{"tty", func(v *backupRuntimeGuestExec) { v.ProcessConfig.Tty = true }, false},
		{"missing_exit", func(v *backupRuntimeGuestExec) { v.Running = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid()
			tc.change(&v)
			if backupRuntimeGuestDumpExecMatches(v, container, 10) != tc.want {
				t.Fatal("guest exec qualification mismatch")
			}
		})
	}
	v := valid()
	raw, _ := json.Marshal(v)
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		{"exact", 200, raw, true},
		{"missing", 404, []byte("CANARY_PRIVATE"), false},
		{"malformed", 200, []byte("CANARY_PRIVATE"), false},
		{"foreign_id", 200, bytes.Replace(raw, []byte(id), []byte(strings.Repeat("f", 64)), 1), false},
		{"oversized", 200, bytes.Repeat([]byte("x"), (16<<10)+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1.54/exec/"+id+"/json" || r.URL.RawQuery != "" {
					t.Error("unexpected guest observation request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			value, err := backupRuntimeReadGuestExec(ctx, client, id)
			if (err == nil) != tc.want || err != nil && strings.Contains(err.Error(), "CANARY_PRIVATE") {
				t.Fatal("unsafe or incorrect exec response handling")
			}
			if tc.want && !backupRuntimeGuestDumpExecMatches(value, container, 10) {
				t.Fatal("exact HTTP exec proof lost")
			}
			canceled, stop := context.WithCancel(t.Context())
			stop()
			if _, err := backupRuntimeReadGuestExec(canceled, client, id); err == nil {
				t.Fatal("canceled guest observation succeeded")
			}
		})
	}
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
	schedules                         chan domain.HostingDatabaseBackupSchedule
	scheduleSeen                      map[string]bool
	retentionPreviews                 chan databaseRetentionPreview
	retentions                        chan databaseRetentionExecution
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
	return startDatabaseBackupBrowserProfile(f, source, "backup")
}

func startDatabaseBackupBrowserProfile(f *databaseRunnerFixture, source domain.HostingDatabase, profile string) *databaseBackupBrowserFixture {
	f.t.Helper()
	if profile != "backup" && profile != "backup_schedule" && profile != "backup_remote" && profile != "backup_retention" && profile != "backup_retention_recovery" {
		f.t.Fatal("closed backup browser profile required")
	}
	origin := os.Getenv("NEROCD_DATABASE_CONNECTION_BROWSER_ORIGIN")
	if !applicationDatabaseBrowserOrigin(origin) {
		f.t.Fatal("owned browser origin required")
	}
	if source.Engine == "mysql" && profile != "backup_schedule" {
		// The immediately preceding storage browser prepared this same owner.
		var owner, viewer, admins int
		if f.database.QueryRow(f.t.Context(), `SELECT
   (SELECT count(*) FROM users u JOIN project_members m ON m.user_id=u.id WHERE u.id='usr_bootstrap' AND u.global_role='user' AND u.status='active' AND m.project_id=$1 AND m.role='owner'),
   (SELECT count(*) FROM users u JOIN project_members m ON m.user_id=u.id WHERE u.id='usr_viewer' AND u.global_role='user' AND u.status='active' AND m.project_id=$1 AND m.role='viewer'),
   (SELECT count(*) FROM users WHERE global_role='system_admin' AND status='active')`, source.Resource.ProjectID).Scan(&owner, &viewer, &admins) != nil || owner != 1 || viewer != 1 || admins < 1 {
			f.t.Fatal("prepared backup browser lacks ordinary owner/viewer and retained administrator")
		}
	} else {
		_, err := f.service.BootstrapAdmin(f.t.Context(), app.BootstrapAdminInput{Email: "backup-bootstrap@example.local", Name: "Backup bootstrap", Password: "backup-bootstrap-private-only"})
		if err != nil {
			f.t.Fatal("browser bootstrap failed")
		}
		if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
			backupRuntimeRetainRouteAdministrator(f)
		}
		for _, member := range []app.ProjectMemberInput{{ProjectID: source.Resource.ProjectID, Email: "admin@example.local", Role: "owner"}, {ProjectID: source.Resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
			databaseRunnerJSON[domain.ProjectMember](f, f.token, "POST", "/api/v1/project-members", member, 200)
		}
		changed, err := f.database.Exec(f.t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap' AND global_role='system_admin'`)
		if err != nil || changed.RowsAffected() != 1 {
			f.t.Fatal("browser owner demotion failed")
		}
	}
	api := NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	b := &databaseBackupBrowserFixture{f: f, phase: "backup", admitted: make(chan domain.HostingDatabaseBackupOperation, 2), done: make(chan struct{}), schedules: make(chan domain.HostingDatabaseBackupSchedule, 4), scheduleSeen: make(map[string]bool), retentionPreviews: make(chan databaseRetentionPreview, 1), retentions: make(chan databaseRetentionExecution, 1)}
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
		if profile == "backup_remote" && r.Method == "POST" && r.URL.Path == control+"/warning-seen" {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.phase != "denied" {
				w.WriteHeader(409)
				return
			}
			b.phase = "warning_seen"
			w.WriteHeader(204)
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
		if (profile == "backup_retention" || profile == "backup_retention_recovery") && r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/resources/"+source.Resource.ID+"/database-backup-retention/") {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, r)
			if strings.HasSuffix(r.URL.Path, "/previews") && rec.Code == 200 {
				var value databaseRetentionPreview
				if json.Unmarshal(rec.Body.Bytes(), &value) != nil || value.ID == "" {
					f.t.Error("invalid browser retention preview")
				} else {
					select {
					case b.retentionPreviews <- value:
					default:
						f.t.Error("duplicate browser retention preview")
					}
				}
			}
			if strings.HasSuffix(r.URL.Path, "/executions") && rec.Code == 202 {
				var value databaseRetentionExecution
				if json.Unmarshal(rec.Body.Bytes(), &value) != nil || value.ID == "" {
					f.t.Error("invalid browser retention execution")
				} else {
					select {
					case b.retentions <- value:
					default:
						f.t.Error("duplicate browser retention execution")
					}
				}
			}
			databaseRemoteCopyResponse(w, rec)
			return
		}
		if profile == "backup_schedule" && (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.Contains(r.URL.Path, "/database-backup-schedules") {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, r)
			if rec.Code == 200 {
				var schedule domain.HostingDatabaseBackupSchedule
				if json.Unmarshal(rec.Body.Bytes(), &schedule) != nil || schedule.ID == "" || schedule.Revision < 1 || schedule.ResourceID != source.Resource.ID || schedule.ProjectID != source.Resource.ProjectID {
					f.t.Error("invalid browser schedule admission")
				} else {
					key := schedule.ID + ":" + strconv.FormatInt(schedule.Revision, 10)
					b.mu.Lock()
					seen := b.scheduleSeen[key]
					b.scheduleSeen[key] = true
					b.mu.Unlock()
					if !seen {
						select {
						case b.schedules <- schedule:
						default:
							f.t.Error("duplicate browser schedule admission")
						}
					}
					// Lose only the first committed create response. No request body
					// is consumed or rewritten; explicit UI replay retains its ID.
					if !seen && schedule.Revision == 1 && r.Method == http.MethodPost {
						if backupRuntimeLoseScheduleACK(w, rec.Body.Bytes()) != nil {
							f.t.Error("schedule ACK fault unavailable")
						}
						return
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
	meta := map[string]string{"origin": origin, "project_id": source.Resource.ProjectID, "environment": "database runner acceptance", "engine": source.Engine, "profile": profile, "application_resource_id": source.Resource.ID, "application_name": source.Resource.Name, "database_resource_id": source.Resource.ID, "foreign_resource_id": "unused_fixture", "control_path": control}
	raw, _ := json.Marshal(meta)
	fmt.Println("DATABASE_BACKUP_BROWSER_READY " + string(raw))
	return b
}

func backupRuntimeLoseScheduleACK(w http.ResponseWriter, body []byte) error {
	hijacker, ok := w.(http.Hijacker)
	flusher, flushOK := w.(http.Flusher)
	if !ok || !flushOK || len(body) < 2 {
		return errors.New("schedule ACK fault unavailable")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body[:len(body)/2])
	if err != nil {
		return errors.New("schedule ACK fault unavailable")
	}
	flusher.Flush()
	connection, _, err := hijacker.Hijack()
	if err != nil {
		return errors.New("schedule ACK fault unavailable")
	}
	if connection.Close() != nil {
		return errors.New("schedule ACK fault unavailable")
	}
	return nil
}

func TestDatabaseBackupScheduleCreateACKFault(t *testing.T) {
	body := []byte(`{"id":"schedule_owned","revision":1}`)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if backupRuntimeLoseScheduleACK(w, body) != nil {
				t.Error("owned ACK fault failed")
			}
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	response, err := client.Post(server.URL, "application/json", strings.NewReader(`{"request_id":"same_request"}`))
	if err != nil {
		t.Fatal("owned first response headers missing")
	}
	partial, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(partial) >= len(body) {
		t.Fatal("fixture did not lose a committed response body")
	}
	response, err = client.Post(server.URL, "application/json", strings.NewReader(`{"request_id":"same_request"}`))
	if err != nil {
		t.Fatal("owned replay transport failed")
	}
	complete, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || !bytes.Equal(complete, body) || calls != 2 {
		t.Fatal("owned replay did not preserve response")
	}
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
		if w.pause || json.Unmarshal(raw, &in) != nil || in.Phase != "complete" {
			// Plan-pause mode never changes mutation acknowledgements. The reset
			// body borrows raw, so forward before its deferred clear.
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

func backupRuntimeRegisterGrantCleanup(f *databaseRunnerFixture, project, token string) func() error {
	f.t.Helper()
	var mu sync.Mutex
	restored := false
	restore := func() error {
		mu.Lock()
		defer mu.Unlock()
		if restored {
			return nil
		}
		if err := backupRuntimeRestoreGrant(f, project, token); err != nil {
			return err
		}
		restored = true
		return nil
	}
	f.t.Cleanup(func() {
		f.stop() // A denied attempt must join before its fixture grant is restored.
		if err := restore(); err != nil {
			f.t.Error("owned fixture grant cleanup failed")
		}
	})
	return restore
}

func backupRuntimeRestoreGrant(f *databaseRunnerFixture, project, token string) error {
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
	request.Header.Set("Authorization", "Bearer "+token)
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
				backupRuntimeRegisterGrantCleanup(f, source.Resource.ProjectID, f.token)
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
		backupRuntimeRegisterGrantCleanup(f, "project_fixture", f.token)
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
				switch kind {
				case "body":
					body = "ncr_cleanup_private_canary"
				case "size":
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
			if err := backupRuntimeRestoreGrant(f, "project_fixture", f.token); err == nil || err.Error() != "owned fixture grant cleanup response invalid" {
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

func TestDatabaseBackupGrantCleanupAfterExplicitRestore(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"conflict"}}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"project_id":"project_fixture"}`)
	}))
	defer server.Close()
	if !t.Run("restored_before_cleanup", func(child *testing.T) {
		f := &databaseRunnerFixture{t: child, server: server, client: server.Client(), destination: "dest_fixture", token: "fixture_token"}
		restore := backupRuntimeRegisterGrantCleanup(f, "project_fixture", f.token)
		if err := restore(); err != nil {
			child.Fatal("explicit grant restore failed")
		}
	}) || calls != 1 {
		t.Fatal("cleanup repeated an already successful grant mutation")
	}
}

func TestDatabaseBackupPlanPauseForwardsCompletion(t *testing.T) {
	wire := &databaseBackupRuntimeWire{mode: "backup", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
	close(wire.release)
	var received []domain.HostingDatabaseBackupMutation
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil {
			t.Error("request unavailable")
			w.WriteHeader(400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/plan") {
			var in domain.HostingAuthority
			if domain.DecodeHostingStorageProtocolJSON(raw, &in) != nil {
				t.Error("plan authority corrupted")
			}
			writeJSON(w, 200, domain.HostingDatabaseBackupPlan{Operation: domain.HostingDatabaseBackupOperation{Mode: "backup"}})
			return
		}
		var in domain.HostingDatabaseBackupMutation
		if domain.DecodeHostingStorageProtocolJSON(raw, &in) != nil {
			t.Error("mutation corrupted")
			w.WriteHeader(400)
			return
		}
		received = append(received, in)
		writeJSON(w, 200, domain.HostingDatabaseBackupOperation{Mode: "backup", State: "succeeded"})
	})
	a := domain.HostingAuthority{OperationID: "operation", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	call := func(path string, in any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup/"+path, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		if !wire.serve(w, r, handler) {
			handler.ServeHTTP(w, r)
		}
		return w
	}
	if call("plan", a).Code != 200 {
		t.Fatal("plan pause failed")
	}
	for _, receipt := range []string{"first_completion", "next_backup_completion"} {
		in := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: receipt, PlanHash: "sha256:" + strings.Repeat("a", 64), Phase: "complete", Blob: &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 123, Format: "postgresql_custom"}}
		if call("mutation", in).Code != 200 {
			t.Fatal("plan-only pause injected completion failure")
		}
		if len(received) == 0 || !reflect.DeepEqual(received[len(received)-1], in) {
			t.Fatal("plan-only pause changed completion body")
		}
	}
	if len(received) != 2 || wire.replays() != 0 {
		t.Fatal("plan pause created a receipt replay fault")
	}
}

// Use the real session middleware and proxy handler without starting a runner.
func TestDatabaseBackupRouteAdministratorSession(t *testing.T) {
	api, pg, database := newHostingBuildAPIFixture(t)
	for {
		tx, err := database.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		changed, err := hostingjobs.MigrateNextTx(t.Context(), tx)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stop(ctx) != nil {
			t.Error("focused controller did not join")
		}
	})
	server := httptest.NewTLSServer(api)
	t.Cleanup(server.Close)
	f := &databaseRunnerFixture{t: t, server: server, client: server.Client(), database: database}
	owner := databaseRunnerJSON[struct {
		Token string `json:"token"`
	}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
	f.token = owner.Token
	f.adminToken = owner.Token
	if _, err := api.app.BootstrapAdmin(t.Context(), app.BootstrapAdminInput{Email: "backup-bootstrap@example.local", Name: "Backup bootstrap", Password: "backup-bootstrap-private-only"}); err != nil {
		t.Fatal("retained fixture administrator bootstrap failed")
	}
	enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, f.token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_backup_admin", RunnerName: "backup admin", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}}, 201)
	databaseRunnerJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", databaseRunnerEnrollmentConsumeInput("ncr_backup_admin_fixture"), 200)
	target := databaseRunnerJSON[app.ServerDetail](f, f.token, "POST", "/api/v1/servers", app.ServerInput{Name: "backup admin fixture", RunnerID: "runner_backup_admin"}, 201)
	f.destination = target.Destination.ID
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/destinations/"+target.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
	databaseRunnerJSON[domain.HostingProxy](f, f.token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "backup_admin_proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	if changed, err := database.Exec(t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap' AND global_role='system_admin'`); err != nil || changed.RowsAffected() != 1 {
		t.Fatal("fixture owner demotion failed")
	}
	path := "/api/v1/servers/" + target.Server.ID + "/proxy/hostname-grants"
	input := domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "restored-mysql.example.test"}
	databaseRunnerJSON[json.RawMessage](f, "", "POST", path, input, 401)
	databaseRunnerJSON[json.RawMessage](f, f.adminToken, "POST", path, input, 403)
	backupRuntimeRetainRouteAdministrator(f)
	principal := databaseRunnerJSON[struct {
		Roles []string `json:"roles"`
	}](f, f.adminToken, "GET", "/api/v1/me", nil, 200)
	if f.adminToken == f.token || !slices.Contains(principal.Roles, domain.RoleSystemAdmin) {
		t.Fatal("route fixture retained demoted owner token")
	}
	databaseRunnerJSON[domain.HostingHostnameGrant](f, f.adminToken, "POST", path, input, 201)
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", path, input, 403)
	grantPath := "/api/v1/destinations/" + target.Destination.ID + "/project-grants/revoke"
	grant := app.DestinationProjectInput{ProjectID: "proj_platform"}
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", grantPath, grant, 403)
	restore := backupRuntimeRegisterGrantCleanup(f, "proj_platform", f.adminToken)
	databaseRunnerJSON[json.RawMessage](f, f.adminToken, "POST", grantPath, grant, 200)
	if restore() != nil {
		t.Fatal("retained administrator grant restoration failed")
	}
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", grantPath, grant, 403)
}

func backupRuntimeRetainRouteAdministrator(f *databaseRunnerFixture) {
	f.t.Helper()
	const password = "backup-bootstrap-private-only"
	f.protected = append(f.protected, []byte(password))
	session := databaseRunnerJSON[struct {
		Token string `json:"token"`
	}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "backup-bootstrap@example.local", "password": password}, 201)
	if session.Token == "" || session.Token == f.token {
		f.t.Fatal("retained administrator session missing")
	}
	f.adminToken = session.Token
	f.protected = append(f.protected, []byte(session.Token))
}
