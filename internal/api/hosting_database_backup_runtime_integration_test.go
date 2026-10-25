package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"strings"
	"sync"
	"testing"
	"time"

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
	f.t.Helper()
	image := os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")
	if applicationDatabaseFixtureImage(image) != nil {
		f.t.Fatal("owned application image required")
	}
	resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: db.Resource.ProjectID, ProjectEnvironmentID: f.environment, Name: "backup app " + suffix, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
	entries := []domain.HostingRuntimeEntry{}
	for _, v := range [][2]string{{"FIXTURE_DATABASE_ENGINE", "postgresql"}, {"FIXTURE_DATABASE_NAMESPACE", "backup_restored"}} {
		value := v[1]
		entries = append(entries, domain.HostingRuntimeEntry{Name: v[0], Kind: "literal", Value: &value})
	}
	config := databaseRunnerJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, RequestID: backupRuntimeRequestID(f.t), Entries: entries}, 200).Configuration
	in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: db.Resource.ID, ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: config.RuntimeGeneration, ExpectedDatabaseGeneration: db.Resource.Generation, RequestID: backupRuntimeRequestID(f.t)}, Deployment: domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "backup_app_" + suffix, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 30}}}
	connected := databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connections", in, 202)
	f.registerRouteOperationCleanup(connected.UpgradeOperationID)
	f.registerRouteOperationCleanup(connected.ApplicationOperationID)
	f.activeConnection(resource.ID)
	return f.awaitConnectionOperation(connected.ApplicationOperationID)
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
	t.Cleanup(func() { f.docker("container", "start", sourceOp.Result.ContainerID) })
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
	restored := backupRuntimeAwait(f, restoreID)
	if restored.TargetResourceID == source.Resource.ID || restored.BackupID != backup.ID || restored.Blob.Digest != backup.Blob.Digest {
		t.Fatal("restore changed immutable source or target identity")
	}
	target := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	f.registerRouteDatabaseCleanup(target.Resource.ID)
	if target.Binding.ID == source.Binding.ID || target.StorageRef == source.StorageRef || target.Credentials[0].SecretID == source.Credentials[0].SecretID {
		t.Fatal("restore reused source binding, volume or credentials")
	}
	b := backupRuntimeApplication(f, target, "restored")
	f.appRecord(b, "GET", "before", "application_origin")
	f.appRecord(b, "PUT", "new", "restored_new_write")
	f.appRecord(b, "GET", "new", "restored_new_write")
	targetConn := f.connection(b.ResourceID)
	targetOp := f.await(targetConn.UpgradeOperationID)
	sql := string(f.docker("exec", "--user", "postgres", targetOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--tuples-only", "--no-align", "--command", "SET ROLE ncd_app; INSERT INTO public.backup_sequence(value) VALUES ('restored') RETURNING id; SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.backup_sequence_id_seq'::regclass;"))
	if !strings.Contains(sql, "2") || !strings.Contains(sql, "ncd_app") {
		t.Fatal("restored application sequence ownership failed")
	}
	f.docker("container", "start", sourceOp.Result.ContainerID)
	f.appRecord(a, "GET", "before", "application_origin")
	f.appRecord(a, "GET", "after_snapshot", "source_still_writable")
	f.appRecord(a, "PUT", "source_new", "source_new_write")
	browser.setPhase("complete", restored.ID)
	browser.finished()
	backupRuntimeFailureCases(f, source, sourceOp, a, backup)
	fmt.Println("DATABASE_BACKUP_RESTORE_APP_PASS postgresql")
}

type databaseBackupBrowserFixture struct {
	f         *databaseRunnerFixture
	mu        sync.Mutex
	phase, id string
	admitted  chan domain.HostingDatabaseBackupOperation
	done      chan struct{}
	once      sync.Once
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
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/resources/"+req.TargetResourceID+"/database/deployments", domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "denied_held_target"}, 409)
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
				t.Cleanup(func() { // Fixture teardown restores its own grant only after the denied attempt is joined.
					databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/destinations/"+f.destination+"/project-grants", app.DestinationProjectInput{ProjectID: source.Resource.ProjectID}, 201)
				})
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
	canary := "ncr_diagnostic_canary_private_value"
	for _, raw := range []string{canary, "invalid database backup plan response: " + canary, "invalid database backup plan response\n" + canary, "{" + canary, strings.Repeat("x", (1<<20)+1)} {
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
