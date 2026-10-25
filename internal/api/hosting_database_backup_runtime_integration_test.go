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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/web"
)

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
	source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseCreateInput("postgresql", f.environment, f.destination), 201)
	f.registerRouteDatabaseCleanup(source.Resource.ID)
	requested := f.deploy(source, "backup_source")
	f.registerRouteOperationCleanup(requested.ID)
	f.await(requested.ID)
	a := backupRuntimeApplication(f, source, "source")
	f.appRecord(a, "PUT", "before", "application_origin")
	// The dump contains application-role sequence ownership, not source roles.
	db := f.connection(a.ResourceID)
	sourceOp := f.awaitConnectionOperation(db.UpgradeOperationID)
	f.docker("exec", "--user", "postgres", sourceOp.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; CREATE TABLE public.backup_sequence(id bigserial PRIMARY KEY, value text); INSERT INTO public.backup_sequence(value) VALUES ('before');")
	wire := &databaseBackupRuntimeWire{mode: "backup", entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	browser := startDatabaseBackupBrowser(f, source)
	backupID := browser.await("backup")
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
	targetOp := f.awaitConnectionOperation(targetConn.UpgradeOperationID)
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
			return false
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
	f.t.Helper()
	// Pause the exact fresh restore plan before any effect, install a target-only
	// event trigger to force a genuine guest pg_restore deadline, then release it.
	wire := &databaseBackupRuntimeWire{mode: "restore", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
	f.backupWire.Store(wire)
	req := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore_guest_timeout", Name: "Timeout private target"}, 202)
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
			source := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseCreateInput("postgresql", f.environment, f.destination), 201)
			f.registerRouteDatabaseCleanup(source.Resource.ID)
			requested := f.deploy(source, "backup_negative_source")
			f.registerRouteOperationCleanup(requested.ID)
			op := f.await(requested.ID)
			f.docker("exec", "--user", "postgres", op.Result.ContainerID, "psql", "--username=postgres", "--dbname=app", "--no-psqlrc", "--command", "SET ROLE ncd_app; CREATE TABLE public.negative_record(id bigserial PRIMARY KEY); INSERT INTO public.negative_record DEFAULT VALUES;")
			path := "/api/v1/resources/" + source.Resource.ID + "/database-backups"
			databaseRunnerJSON[json.RawMessage](f, f.token, "POST", path, domain.HostingDatabaseBackupInput{RequestID: "stale_backup", ExpectedResourceGeneration: source.Resource.Generation + 1}, 409)
			b := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](f, f.token, "POST", path, domain.HostingDatabaseBackupInput{RequestID: "negative_backup", ExpectedResourceGeneration: source.Resource.Generation}, 202)
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
				databaseRunnerJSON[json.RawMessage](f, f.token, "GET", "/api/v1/database-backups/"+restore.ID, nil, 403)
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
