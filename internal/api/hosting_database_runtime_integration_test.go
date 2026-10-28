package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

const databaseRunnerOwner = "nerocd-database-runner-20261024-dbr7319"
const databaseRunnerLabel = "io.nerocd.database-runner.owner"

type databaseRunnerLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *databaseRunnerLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data)+len(p) > 1<<20 {
		return 0, errors.New("fixture log bound exceeded")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *databaseRunnerLog) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data)
}

type databaseRunnerFault struct {
	entered, finished chan struct{}
	operationID       string
	request           domain.HostingCompleteRequest
}

type databaseRunnerExit struct {
	Exited bool
	Kind   string
	Code   int
}

type databaseRunnerProcess struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	exit    databaseRunnerExit // Written once before done closes.
}

func databaseRunnerExitFor(err error) databaseRunnerExit {
	if err == nil {
		return databaseRunnerExit{Exited: true, Kind: "success", Code: 0}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return databaseRunnerExit{Exited: true, Kind: "process_exit", Code: exit.ExitCode()}
	}
	return databaseRunnerExit{Exited: true, Kind: "wait_error", Code: -1}
}

func (p *databaseRunnerProcess) status() databaseRunnerExit {
	if p != nil {
		select {
		case <-p.done:
			return p.exit
		default:
		}
	}
	return databaseRunnerExit{Kind: "running", Code: -1}
}

// The caller has started cmd. This goroutine is its only Wait owner; closing
// done publishes a retained result to both wait diagnostics and cleanup.
func newDatabaseRunnerProcess(cmd *exec.Cmd, cancel context.CancelFunc) *databaseRunnerProcess {
	p := &databaseRunnerProcess{command: cmd, cancel: cancel, done: make(chan struct{})}
	go func() {
		p.exit = databaseRunnerExitFor(cmd.Wait())
		close(p.done)
	}()
	return p
}

var databaseRunnerDiagnosticRoutes = [...]struct{ method, path string }{
	{"GET", "/api/v1/runners/identity"},
	{"POST", "/api/v1/runners/heartbeat"},
	{"POST", "/api/v1/runners/claim"},
	{"POST", "/api/v1/runners/renew"},
	{"GET", "/api/v1/runners/lease"},
	{"POST", "/api/v1/runners/complete"},
	{"POST", "/api/v1/runners/events/batch"},
	{"POST", "/api/v1/runners/telemetry"},
	{"POST", "/api/v1/runners/hosting/plan"},
	{"POST", "/api/v1/runners/hosting/prepared"},
	{"POST", "/api/v1/runners/hosting/storage-create-begin"},
	{"POST", "/api/v1/runners/hosting/storage-materialized"},
	{"POST", "/api/v1/runners/hosting/database-credentials"},
	{"POST", "/api/v1/runners/hosting/apply-begin"},
	{"POST", "/api/v1/runners/hosting/status"},
	{"POST", "/api/v1/runners/hosting/complete"},
	{"POST", "/api/v1/runners/hosting/reconciliation"},
	{"POST", "/api/v1/runners/hosting/database-binding/state"},
	{"POST", "/api/v1/runners/hosting/database-binding/authorize"},
	{"POST", "/api/v1/runners/hosting/database-binding/stage"},
	{"POST", "/api/v1/runners/hosting/database-binding/storage/writer"},
	{"POST", "/api/v1/runners/hosting/database-binding/storage/authorize"},
	{"POST", "/api/v1/runners/hosting/database-backup/plan"},
	{"POST", "/api/v1/runners/hosting/database-backup/mutation"},
	{"POST", "/api/v1/runners/hosting/database-backup-transfer/plan"},
	{"POST", "/api/v1/runners/hosting/database-backup-transfer/credentials"},
	{"POST", "/api/v1/runners/hosting/database-backup-transfer/mutation"},
	{"POST", "/api/v1/runners/proxy/plan"},
	{"POST", "/api/v1/runners/proxy/prepared"},
	{"POST", "/api/v1/runners/proxy/apply-begin"},
	{"POST", "/api/v1/runners/proxy/status"},
	{"POST", "/api/v1/runners/proxy/complete"},
	{"GET", "/api/v1/hosting-operations/{id}"},
}

var databaseRunnerDiagnosticStatuses = [...]int{0, 200, 201, 202, 204, 400, 401, 403, 404, 409, 413, 429, 500, 503}

type databaseRunnerAPICounts struct {
	mu     sync.Mutex
	counts [len(databaseRunnerDiagnosticRoutes)][len(databaseRunnerDiagnosticStatuses)]uint32
}

func (c *databaseRunnerAPICounts) record(method, path string, status int) {
	if strings.HasPrefix(path, "/api/v1/hosting-operations/") {
		path = "/api/v1/hosting-operations/{id}"
	}
	for i, route := range databaseRunnerDiagnosticRoutes {
		if method != route.method || path != route.path {
			continue
		}
		column := 0 // Unknown statuses have one closed bucket.
		for j, allowed := range databaseRunnerDiagnosticStatuses {
			if status == allowed {
				column = j
			}
		}
		c.mu.Lock()
		if c.counts[i][column] < 9999 {
			c.counts[i][column]++
		}
		c.mu.Unlock()
		return
	}
}

func (c *databaseRunnerAPICounts) summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for i, route := range databaseRunnerDiagnosticRoutes {
		for j, status := range databaseRunnerDiagnosticStatuses {
			if n := c.counts[i][j]; n != 0 {
				out = append(out, route.method+" "+route.path+" "+strconv.Itoa(status)+"="+strconv.FormatUint(uint64(n), 10))
			}
		}
	}
	return strings.Join(out, ",")
}

// Fixed categories only: runner output can contain private authority and must
// never become a failure-report message, even when a canary scan passes.
func databaseRunnerFailureCategories(raw []byte) []string {
	var categories []string
	for _, item := range []struct{ text, category string }{
		{"application database execution requires exact retained evidence and current authority", "application_execution"},
		{"application database engine", "application_engine"},
		{"lease authority", "lease_authority"},
		{"journal", "journal"},
		{"context deadline exceeded", "deadline"},
		{"context canceled", "canceled"},
	} {
		if bytes.Contains(raw, []byte(item.text)) {
			categories = append(categories, item.category)
		}
	}
	return categories
}

func waitApplicationDatabaseComplete(ctx context.Context, process *databaseRunnerProcess, entered, failed <-chan struct{}) string {
	var exited <-chan struct{}
	if process != nil {
		exited = process.done
	}
	select {
	case <-entered:
		return ""
	case <-exited:
		select {
		case <-entered:
			return ""
		default:
		}
		return "runner_exit"
	case <-failed:
		return "browser_failure"
	case <-ctx.Done():
		return "deadline"
	}
}

type databaseRunnerStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *databaseRunnerStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *databaseRunnerStatusWriter) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(raw)
}

func databaseRunnerDiagnostic(op domain.HostingOperation, exit databaseRunnerExit, api *databaseRunnerAPICounts) string {
	state := "unknown"
	switch op.State {
	case "queued", "dispatched", "preparing", "prepared", "applying", "verifying", "succeeded", "failed", "canceled", "cancel_requested", "reconciliation_required":
		state = op.State
	}
	kind := "unknown"
	switch exit.Kind {
	case "running", "success", "process_exit", "wait_error":
		kind = exit.Kind
	}
	code := exit.Code
	if code < -1 || code > 255 {
		code = -1
	}
	return fmt.Sprintf("state=%s release=%t plan=%t prepared=%t result=%t complete=%t mutation_hold=%t restore_hold=%t runner_exited=%t exit_kind=%s exit_code=%d api=[%s]", state, op.Release != nil, op.Plan != nil, op.PreparedReceipt != nil, op.Result != nil, op.CompleteReceipt != nil, op.MutationHold, op.RestoreOwnershipHeld, exit.Exited, kind, code, api.summary())
}

type databaseRunnerFixture struct {
	service                               *app.Service
	owner                                 context.Context
	database                              *pgxpool.Pool
	capture                               atomic.Pointer[databaseCaptureRuntimeWire]
	backupWire                            atomic.Pointer[databaseBackupRuntimeWire]
	remoteBackupWire                      atomic.Pointer[databaseRemoteBackupWire]
	retentionWire                         atomic.Pointer[databaseRetentionWire]
	upgrade                               bool
	connected                             bool
	storage                               *applicationDatabaseStorageFixture
	routes                                *applicationDatabaseRouteFixture
	routeYield                            atomic.Pointer[applicationDatabaseRouteYieldWire]
	routeProxyComplete                    atomic.Pointer[applicationDatabaseRouteYieldWire]
	serverID                              string
	adminToken                            string
	upgradeComplete                       atomic.Pointer[databaseUpgradeCompleteWire]
	t                                     *testing.T
	server                                *httptest.Server
	client                                *http.Client
	root, token, destination, environment string
	process                               *databaseRunnerProcess
	processMu                             sync.Mutex
	lastExit                              databaseRunnerExit
	hostingStop                           func(context.Context) error
	hostingStore                          *store.PostgresStore
	apiCounts                             databaseRunnerAPICounts
	logs                                  *databaseRunnerLog
	failureOutput                         io.Writer // Private test sink; physical fixture defaults to stdout.
	fault                                 atomic.Pointer[databaseRunnerFault]
	claimRequests                         atomic.Int64
	completeMu                            sync.Mutex
	completeCounts                        map[string]int
	applyRequests                         atomic.Int64
	deliveryRequests                      atomic.Int64
	runtimeDeliveryRequests               atomic.Int64
	connectionAuthorizationRequests       atomic.Int64
	protected                             [][]byte
}

func (f *databaseRunnerFixture) completionCount(id string) int {
	f.completeMu.Lock()
	defer f.completeMu.Unlock()
	return f.completeCounts[id]
}

func databaseRunnerJSON[T any](f *databaseRunnerFixture, token, method, path string, body any, status int) T {
	f.t.Helper()
	var result T
	if strings.Contains(path, "/managed-secrets/") {
		f.t.Fatal("secret responses require protected helper")
	}
	raw, err := json.Marshal(body)
	defer clear(raw)
	if err != nil {
		f.t.Fatal("public fixture input failed")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatal("public fixture request failed")
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("public fixture transport failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	defer clear(data)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > 1<<20 || response.StatusCode != status || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		f.t.Fatalf("public fixture API method=%s path=%s status=%d want=%d", method, path, response.StatusCode, status)
	}
	if json.Unmarshal(data, &result) != nil {
		f.t.Fatal("public fixture response failed")
	}
	return result
}

func databaseRunnerEnrollmentConsumeInput(credential string) app.RunnerEnrollmentConsumeInput {
	sum := sha256.Sum256([]byte(credential))
	return app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(sum[:])}
}

func newDatabaseRunnerFixture(t *testing.T) *databaseRunnerFixture {
	t.Helper()
	return newDatabaseRunnerFixtureWithCapture(t, false)
}

func newDatabaseRunnerFixtureWithCapture(t *testing.T, capture bool) *databaseRunnerFixture {
	t.Helper()
	return newDatabaseRunnerFixtureWithProfile(t, capture, false)
}

type databaseRunnerConfigurator func(*store.PostgresStore, *secretcrypto.Keyring) error

func newDatabaseRunnerFixtureWithProfile(t *testing.T, capture, upgrade bool, connectedProfile ...bool) *databaseRunnerFixture {
	t.Helper()
	return newDatabaseRunnerFixtureConfigured(t, capture, upgrade, nil, connectedProfile...)
}

func newDatabaseRunnerFixtureConfigured(t *testing.T, capture, upgrade bool, configure databaseRunnerConfigurator, connectedProfile ...bool) *databaseRunnerFixture {
	t.Helper()
	connected := len(connectedProfile) > 0 && connectedProfile[0]
	storage := len(connectedProfile) == 2 && connectedProfile[1]
	if len(connectedProfile) > 2 || connected && (!capture || !upgrade) || storage && !connected {
		t.Fatal("connected fixture requires automatic capture and upgrade")
	}
	_, pg, database := newHostingBuildAPIFixture(t)
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") == "1" || os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		timeout := 10
		if os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
			timeout = 60
		}
		if pg.ConfigureDatabaseBackupLimits(domain.HostingDatabaseBackupLimits{MaxBytes: 1 << 30, TimeoutSeconds: timeout}) != nil {
			t.Fatal("protected backup fixture policy failed")
		}
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("fixture key generation failed")
	}
	ring, err := secretcrypto.New("database-runner-fixture", map[string][]byte{"database-runner-fixture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("fixture keyring failed")
	}
	if configure != nil {
		if err := configure(pg, ring); err != nil {
			t.Fatal("protected fixture configuration failed")
		}
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal("fixture service failed")
	}
	root, err := os.MkdirTemp("/var/lib", databaseRunnerOwner+"-")
	if err != nil {
		t.Fatal("fixture root failed")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error("fixture root cleanup failed")
		}
	})
	f := &databaseRunnerFixture{t: t, root: root, upgrade: upgrade, connected: connected, service: service, database: database, logs: &databaseRunnerLog{}, completeCounts: map[string]int{}}
	if storage {
		f.storage = &applicationDatabaseStorageFixture{}
	}
	handler := NewServer(service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static())
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := &databaseRunnerStatusWriter{ResponseWriter: w}
		defer func() { f.apiCounts.record(r.Method, r.URL.Path, observed.status) }()
		w = observed
		if wire := f.retentionWire.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if wire := f.remoteBackupWire.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if wire := f.backupWire.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if wire := f.capture.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if wire := f.routeYield.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if wire := f.routeProxyComplete.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if r.URL.Path == "/api/v1/runners/claim" {
			f.claimRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/apply-begin" {
			f.applyRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/database-credentials" {
			f.deliveryRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/runtime-environment" {
			f.runtimeDeliveryRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/database-binding/authorize" {
			f.connectionAuthorizationRequests.Add(1)
		}
		if wire := f.upgradeComplete.Load(); wire != nil && wire.serve(w, r, handler) {
			return
		}
		if r.URL.Path != "/api/v1/runners/hosting/complete" {
			handler.ServeHTTP(w, r)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
		var request domain.HostingCompleteRequest
		if e != nil || len(raw) > 8192 || json.Unmarshal(raw, &request) != nil {
			t.Error("completion request invalid")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		f.completeMu.Lock()
		f.completeCounts[request.OperationID]++
		f.completeMu.Unlock()
		fault := f.fault.Load()
		if fault == nil || request.OperationID != fault.operationID || !f.fault.CompareAndSwap(fault, nil) {
			handler.ServeHTTP(w, r)
			return
		}
		defer close(fault.finished)
		fault.request = request
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Error("completion fault did not follow real API commit")
			return
		}
		close(fault.entered)
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			t.Error("completion fault request did not join")
		}
	}))
	f.client = f.server.Client()
	f.client.Timeout = 10 * time.Second
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(f.server.Close)
	if err = os.WriteFile(filepath.Join(root, "api.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal("fixture CA file failed")
	}
	for _, name := range []string{"state", "secrets"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal("protected runtime root failed")
		}
	}
	login := databaseRunnerJSON[struct {
		Token string `json:"token"`
	}](f, "", "POST", "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
	f.token = login.Token
	f.adminToken = login.Token
	caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	if os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		caps = append(caps, domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability)
	}
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") == "1" || os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		caps = append(caps, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability)
		if os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") == "1" {
			caps = append(caps, "database_backup_transfer", "hosting_postgresql_remote_backup_v1", "hosting_mysql_remote_backup_v1", "hosting_mysql_remote_restore_v1")
			if os.Getenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME") == "1" {
				caps = append(caps, "database_backup_retention", "hosting_postgresql_backup_retention_v1")
			}
		}
	}
	if capture {
		principal, err := service.AuthenticateSessionToken(t.Context(), f.token)
		if err != nil {
			t.Fatal("fixture owner authentication failed")
		}
		f.owner = auth.WithCredentialSource(auth.WithPrincipal(t.Context(), principal), auth.CredentialSourceBearer)
		caps = append(caps, domain.RunTypeComposeDeploy, domain.HostingDatabaseNetworkCaptureCapability)
	}
	if upgrade {
		caps = append(caps, domain.HostingDatabaseUpgradeCapability)
	}
	if connected {
		caps = append(caps, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingApplicationDatabaseCapability, domain.HostingDatabaseMaintenanceCapability)
		if storage {
			caps = append(caps, "hosting_application_database_storage_v1")
		}
		if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
			caps = append(caps, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, "hosting_application_database_route_continuity_v1")
		}
	}
	enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, f.token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_database_acceptance", RunnerName: "private database acceptance", Capabilities: caps}, 201)
	credential := "ncr_database_runner_fixture_only"
	enrolled := databaseRunnerJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", databaseRunnerEnrollmentConsumeInput(credential), 200)
	if err = os.WriteFile(filepath.Join(root, "credential"), []byte(credential), 0600); err != nil {
		t.Fatal("runner credential file failed")
	}
	f.protected = append(f.protected, []byte(credential), []byte(f.token), []byte(enrollment.Token))
	target := databaseRunnerJSON[app.ServerDetail](f, f.token, "POST", "/api/v1/servers", app.ServerInput{Name: "database runner acceptance", RunnerID: enrolled.Runner.ID}, 201)
	f.destination = target.Destination.ID
	f.serverID = target.Server.ID
	databaseRunnerJSON[json.RawMessage](f, f.token, "POST", "/api/v1/destinations/"+f.destination+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
	env := databaseRunnerJSON[domain.ProjectEnvironment](f, f.token, "POST", "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "database runner acceptance"}, 201)
	f.environment = env.ID
	// Only queue infrastructure is initialized directly. Every hosting identity,
	// authority and desired input comes from authenticated public API requests.
	for {
		tx, e := database.Begin(t.Context())
		if e != nil {
			t.Fatal("queue migration begin failed")
		}
		changed, e := func() (changed bool, err error) {
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if rollbackErr := tx.Rollback(cleanup); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
					err = errors.Join(err, rollbackErr)
				}
			}()
			changed, err = hostingjobs.MigrateNextTx(t.Context(), tx)
			if err != nil {
				return false, err
			}
			return changed, tx.Commit(t.Context())
		}()
		if e != nil {
			t.Fatal("queue migration or cleanup failed")
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal("River startup failed")
	}
	f.hostingStop = stop
	f.hostingStore = pg
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stop(ctx) != nil {
			t.Error("River did not join")
		}
	})
	t.Cleanup(func() {
		f.stop()
		f.assertRedacted()
		for _, value := range f.protected {
			clear(value)
		}
	})
	f.start()
	return f
}

func (f *databaseRunnerFixture) start() {
	f.t.Helper()
	f.startRunner(databaseRunnerArguments(f))
}

func databaseRunnerArguments(f *databaseRunnerFixture) []string {
	args := []string{"runner", "--server", f.server.URL, "--credential-file", filepath.Join(f.root, "credential"), "--hosting-database", "--execute", "--database-state-root", filepath.Join(f.root, "state"), "--database-secret-root", filepath.Join(f.root, "secrets"), "--journal-dir", filepath.Join(f.root, "journal"), "--work-dir", filepath.Join(f.root, "work"), "--image-docker-endpoint", "unix:///var/run/docker.sock", "--poll-interval", "100ms", "--cancel-poll-interval", "100ms"}
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") == "1" || os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		args = append(args, "--hosting-database-backups")
		if os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") == "1" {
			args = append(args, "--hosting-database-remote-backups", "--hosting-backup-remote-trusted-loopback=http://127.0.0.1:9000")
			if os.Getenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME") == "1" {
				args = append(args, "--hosting-database-backup-retention")
			}
		}
	}
	if f.upgrade {
		args = append(args, "--hosting-database-network-upgrade")
	}
	if f.connected {
		args = append(args, "--hosting-database-network-capture", "--hosting-runtime-env", "--hosting-application-database", "--hosting-database-membership-maintenance")
		if os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") == "1" {
			args = append(args, "--proxy-state-root", filepath.Join(f.root, "proxy"), "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch", "--hosting-application-database-route-continuity")
		}
	}
	if f.storage != nil {
		args = append(args, "--hosting-application-database-storage")
	}
	return args
}

func (f *databaseRunnerFixture) startRunner(args []string) {
	f.t.Helper()
	if f.process != nil {
		f.t.Fatal("runner already active")
	}
	ctx, cancel := context.WithCancel(f.t.Context())
	cmd := exec.CommandContext(ctx, "/fixture/nerocd", args...)
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(f.root, "api.pem"), "NEROCD_MODE=development", "NEROCD_TOKEN=", "NEROCD_RUNNER_CREDENTIAL_FILE=")
	cmd.Stdout, cmd.Stderr = f.logs, f.logs
	cmd.Cancel = func() error {
		err := cmd.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 10 * time.Second
	if cmd.Start() != nil {
		cancel()
		f.t.Fatal("compiled runner start failed")
	}
	p := newDatabaseRunnerProcess(cmd, cancel)
	f.processMu.Lock()
	f.process = p
	f.processMu.Unlock()
	f.t.Logf("compiled database runner started pid=%d", cmd.Process.Pid)
}

func (f *databaseRunnerFixture) stop() {
	f.t.Helper()
	f.processMu.Lock()
	p := f.process
	f.processMu.Unlock()
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
		f.processMu.Lock()
		f.lastExit = p.status()
		f.process = nil
		f.processMu.Unlock()
		f.t.Logf("compiled database runner joined pid=%d exited=%t kind=%s code=%d", p.command.Process.Pid, f.lastExit.Exited, f.lastExit.Kind, f.lastExit.Code)
	case <-time.After(15 * time.Second):
		f.t.Fatal("compiled database runner did not join")
	}
}

func (f *databaseRunnerFixture) failureRunnerStatus() databaseRunnerExit {
	f.processMu.Lock()
	defer f.processMu.Unlock()
	if f.process == nil {
		if f.lastExit.Kind == "" {
			return databaseRunnerExit{Kind: "unknown", Code: -1}
		}
		return f.lastExit
	}
	return f.process.status()
}

func (f *databaseRunnerFixture) deploy(view domain.HostingDatabase, key string) domain.HostingOperation {
	f.t.Helper()
	return databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+view.Resource.ID+"/database/deployments", domain.HostingDatabaseDeploymentInput{ExpectedGeneration: view.Resource.Generation, IdempotencyKey: key}, 202)
}

func (f *databaseRunnerFixture) await(id string) domain.HostingOperation {
	f.t.Helper()
	op, reason := waitDatabaseRunnerOperation(f.t.Context(), f.process, func(ctx context.Context) (domain.HostingOperation, error) {
		return f.operation(ctx, id)
	})
	exit := f.lastExit
	if f.process != nil {
		exit = f.process.status()
	}
	op, finalSample := databaseRunnerFinalExitSample(f.t.Context(), op, reason, func(ctx context.Context) (domain.HostingOperation, error) { return f.operation(ctx, id) })
	if reason != "" {
		f.t.Fatalf("database wait failed reason=%s final_sample=%s %s", reason, finalSample, databaseRunnerDiagnostic(op, exit, &f.apiCounts))
	}
	if op.Result == nil || !op.Result.Ready || op.Result.Uncertain || op.MutationHold || op.RestoreOwnershipHeld || op.CompleteReceipt == nil || op.Plan == nil || databaseRunnerValidatePlan(op) != nil {
		f.t.Fatalf("database completion lacks exact healthy evidence %s", databaseRunnerDiagnostic(op, exit, &f.apiCounts))
	}
	return op
}

// A joined process exit cancels ordinary polling, not the parent test. One
// separately bounded read improves diagnostics without accepting a nonzero exit
// or inferring that an older sample proves the final hold/result state.
func databaseRunnerFinalExitSample(parent context.Context, previous domain.HostingOperation, reason string, fetch func(context.Context) (domain.HostingOperation, error)) (domain.HostingOperation, string) {
	if reason != "runner_exited" {
		return previous, "not_requested"
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return previous, "stale"
	}
	final, err := fetch(ctx)
	if err != nil || ctx.Err() != nil {
		return previous, "stale"
	}
	return final, "authoritative"
}

// Operation polling has a cancellable request context, so a runner exit also
// interrupts an in-flight HTTP poll. All error details remain private.
func (f *databaseRunnerFixture) operation(ctx context.Context, id string) (domain.HostingOperation, error) {
	var op domain.HostingOperation
	invalid := errors.New("operation poll unavailable")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+"/api/v1/hosting-operations/"+id, nil)
	if err != nil {
		return op, invalid
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	response, err := f.client.Do(req)
	if err != nil {
		return op, invalid
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	defer clear(raw)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > 1<<20 || response.StatusCode != http.StatusOK || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || json.Unmarshal(raw, &op) != nil {
		return domain.HostingOperation{}, invalid
	}
	return op, nil
}

func waitDatabaseRunnerOperation(parent context.Context, process *databaseRunnerProcess, fetch func(context.Context) (domain.HostingOperation, error)) (domain.HostingOperation, string) {
	ctx, cancel := context.WithTimeout(parent, 180*time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var done <-chan struct{}
	if process != nil {
		done = process.done
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-joined }()
	var op domain.HostingOperation
	for {
		if process.status().Exited {
			return op, "runner_exited"
		}
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return op, "canceled"
			}
			return op, "deadline"
		}
		next, err := fetch(ctx)
		if err != nil {
			if process.status().Exited || ctx.Err() != nil {
				continue
			}
			return op, "operation_poll"
		}
		op = next
		if op.State == "succeeded" {
			return op, ""
		}
		if op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
			return op, "terminal_operation"
		}
		select {
		case <-done:
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// readDatabaseRunnerReveal is deliberately separate from public error helpers.
// Parser errors and secret response bodies must never reach test output.
func readDatabaseRunnerReveal(r io.Reader, ref domain.HostingDatabaseCredentialRef) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 8193))
	defer clear(raw)
	if err != nil || len(raw) > 8192 {
		return nil, errors.New("protected reveal unavailable")
	}
	var out app.ManagedSecretReveal
	if domain.DecodeHostingStorageProtocolJSON(raw, &out) != nil || out.SecretID != ref.SecretID || out.Version != ref.Version || len(out.Value) != 43 {
		return nil, errors.New("protected reveal unavailable")
	}
	value := []byte(out.Value)
	out.Value = ""
	var entropy [32]byte
	n, e := base64.RawURLEncoding.Strict().Decode(entropy[:], value)
	clear(entropy[:])
	if e != nil || n != 32 {
		clear(value)
		return nil, errors.New("protected reveal unavailable")
	}
	return value, nil
}

func (f *databaseRunnerFixture) reveal(ref domain.HostingDatabaseCredentialRef) []byte {
	f.t.Helper()
	raw, err := json.Marshal(app.ManagedSecretRevealInput{Version: ref.Version})
	if err != nil {
		f.t.Fatal("reveal request failed")
	}
	r, err := http.NewRequestWithContext(f.t.Context(), "POST", f.server.URL+"/api/v1/managed-secrets/"+ref.SecretID+"/reveal", bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal("reveal request failed")
	}
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		f.t.Fatal("reveal audit identity failed")
	}
	requestID := "req_" + hex.EncodeToString(nonce[:])
	r.Header.Set("X-Request-ID", requestID)
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("protected reveal transport failed")
	}
	defer func() {
		if response.Body.Close() != nil {
			f.t.Error("protected reveal close failed")
		}
	}()
	if response.StatusCode != 200 || response.Header.Get("X-Request-ID") != requestID || response.Header.Get("Cache-Control") != "no-store" || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		f.t.Fatal("protected reveal authorization failed")
	}
	value, err := readDatabaseRunnerReveal(response.Body, ref)
	if err != nil {
		f.t.Fatal("protected reveal rejected")
	}
	returned := false
	defer func() {
		if !returned {
			clear(value)
		}
	}()
	f.protected = append(f.protected, bytes.Clone(value))
	found := false
	for offset := 0; offset < 2048; offset += 100 {
		page := databaseRunnerJSON[struct {
			Items []domain.AuditEvent `json:"items"`
			Total int                 `json:"total"`
		}](f, f.token, "GET", fmt.Sprintf("/api/v1/audit-events?limit=100&offset=%d", offset), nil, 200)
		for _, event := range page.Items {
			if event.Action == "managed_secret.revealed" && event.TargetID == ref.SecretID && event.ActorID == "usr_bootstrap" && event.Metadata["request_id"] == requestID && event.Metadata["version"] == float64(ref.Version) {
				found = true
			}
		}
		if found || offset+len(page.Items) >= page.Total {
			break
		}
	}
	if !found {
		clear(value)
		f.t.Fatal("owner reveal lacks committed version audit")
	}
	returned = true
	return value
}

type databaseRunnerBoundedOutput struct{ data []byte }

func (b *databaseRunnerBoundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1<<20 {
		return 0, errors.New("fixture output bound exceeded")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func databaseRunnerDocker(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var out databaseRunnerBoundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		clear(out.data)
		return nil, errors.New("owned Docker action failed")
	}
	return out.data, nil
}

func (f *databaseRunnerFixture) docker(args ...string) []byte {
	f.t.Helper()
	out, err := databaseRunnerDocker(f.t.Context(), nil, args...)
	if err != nil {
		f.t.Fatal("owned fixture Docker action failed")
	}
	return out
}

type databaseRunnerContainer struct {
	ID          string `json:"Id"`
	Name, Image string
	Config      struct {
		Labels     map[string]string
		Env        []string
		Cmd        []string
		Entrypoint []string
	}
	State      struct{ Running bool }
	HostConfig struct {
		NetworkMode  string
		PortBindings map[string]any
		Privileged   bool
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
	NetworkSettings struct {
		Ports    map[string]any
		Networks map[string]struct{ NetworkID, IPAddress string }
	}
}

func (f *databaseRunnerFixture) inspect(op domain.HostingOperation) databaseRunnerContainer {
	f.t.Helper()
	var container databaseRunnerContainer
	if json.Unmarshal(f.docker("container", "inspect", op.Result.ContainerID, "--format", "{{json .}}"), &container) != nil || container.ID != op.Result.ContainerID || !container.State.Running || container.Image != op.Release.ImageID || container.Config.Labels["io.nerocd.binding"] != op.BindingID || container.Config.Labels["io.nerocd.operation"] != op.ID || len(container.HostConfig.PortBindings) != 0 || container.HostConfig.Privileged || len(container.NetworkSettings.Networks) != 1 {
		f.t.Fatal("private database runtime inspection mismatch")
	}
	for _, bindings := range container.NetworkSettings.Ports {
		if bindings != nil {
			f.t.Fatal("private database published host port")
		}
	}
	network, ok := container.NetworkSettings.Networks[op.Plan.Database.NetworkName]
	if !ok || network.NetworkID == "" || network.IPAddress == "" {
		f.t.Fatal("private database network missing")
	}
	volume := false
	for _, mount := range container.Mounts {
		if mount.Type == "volume" && mount.Name == op.Plan.Storage.VolumeName && mount.Destination == op.Plan.Storage.MountPath && mount.RW {
			volume = true
		}
	}
	if !volume {
		f.t.Fatal("database did not mount pinned volume")
	}
	var bridge struct {
		ID, Driver string
		Internal   bool
		Labels     map[string]string
	}
	if json.Unmarshal(f.docker("network", "inspect", op.Plan.Database.NetworkName, "--format", "{{json .}}"), &bridge) != nil || bridge.ID != network.NetworkID || bridge.Driver != "bridge" || !bridge.Internal || bridge.Labels["io.nerocd.binding"] != op.BindingID {
		f.t.Fatal("database network is not the owned private bridge")
	}
	running := strings.Fields(string(f.docker("container", "ls", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+op.BindingID)))
	if len(running) != 1 || running[0] != container.ID {
		f.t.Fatal("database has duplicate or missing running writer")
	}
	return container
}

func (f *databaseRunnerFixture) volumeProof(op domain.HostingOperation) string {
	f.t.Helper()
	var proof struct {
		Name, Driver, Scope, CreatedAt string
		Labels                         map[string]string
		Options                        map[string]string
	}
	if json.Unmarshal(f.docker("volume", "inspect", op.Plan.Storage.VolumeName, "--format", "{{json .}}"), &proof) != nil || proof.Name != op.Plan.Storage.VolumeName || proof.Driver != "local" || proof.Scope != "local" || proof.CreatedAt == "" || len(proof.Labels) == 0 || len(proof.Options) != 0 {
		f.t.Fatal("retained database volume identity invalid")
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		f.t.Fatal("volume proof encoding failed")
	}
	return string(raw)
}

type databaseRunnerReplayInventory struct {
	BindingContainers []databaseRunnerContainer
	VolumeUsers       []string
}

type databaseRunnerReplayIdentities struct {
	BindingIDs  []string
	VolumeUsers []string
}

// Include stopped containers. These inventories prove retained identities and
// absence of extra residue, not a count of transient Docker create operations.
func databaseRunnerReplayIdentitiesFor(in databaseRunnerReplayInventory, known []domain.HostingOperation, current domain.HostingOperation) (databaseRunnerReplayIdentities, error) {
	var result databaseRunnerReplayIdentities
	if current.Plan == nil || current.Plan.Storage == nil || current.Result == nil {
		return result, errors.New("replay inventory lacks current scope")
	}
	allowed := make(map[string]domain.HostingOperation, len(known))
	for _, op := range known {
		if op.BindingID != current.BindingID || op.ResourceID != current.ResourceID || op.Plan == nil || op.Plan.Storage == nil || op.Release == nil || op.Result == nil || op.Plan.Storage.VolumeName != current.Plan.Storage.VolumeName || op.Result.ContainerID == "" {
			return result, errors.New("replay inventory known scope changed")
		}
		if _, duplicate := allowed[op.Result.ContainerID]; duplicate {
			return result, errors.New("replay inventory repeats known identity")
		}
		allowed[op.Result.ContainerID] = op
	}
	seen := map[string]bool{}
	for _, container := range in.BindingContainers {
		op, ok := allowed[container.ID]
		if !ok || seen[container.ID] || len(container.ID) != 64 || strings.ToLower(container.ID) != container.ID {
			return result, errors.New("replay inventory has unknown or duplicate binding residue")
		}
		if _, err := hex.DecodeString(container.ID); err != nil {
			return result, errors.New("replay inventory identity invalid")
		}
		if container.Config.Labels["io.nerocd.binding"] != op.BindingID || container.Config.Labels["io.nerocd.operation"] != op.ID || container.Config.Labels["io.nerocd.plan"] != op.Plan.Hash || container.Config.Labels["io.nerocd.release"] != op.Release.Hash || container.Image != op.Release.ImageID || container.State.Running != (container.ID == current.Result.ContainerID) {
			return result, errors.New("replay inventory container ownership or writer state changed")
		}
		mounted := false
		for _, mount := range container.Mounts {
			if mount.Type == "volume" && mount.Name == op.Plan.Storage.VolumeName && mount.Destination == op.Plan.Storage.MountPath && mount.RW {
				mounted = true
			}
		}
		if !mounted {
			return result, errors.New("replay inventory binding lacks exact retained volume")
		}
		seen[container.ID] = true
		result.BindingIDs = append(result.BindingIDs, container.ID)
	}
	if !seen[current.Result.ContainerID] {
		return result, errors.New("replay inventory current writer missing")
	}
	users := map[string]bool{}
	for _, id := range in.VolumeUsers {
		if !seen[id] || users[id] {
			return result, errors.New("replay inventory has foreign or duplicate retained volume user")
		}
		users[id] = true
		result.VolumeUsers = append(result.VolumeUsers, id)
	}
	if len(users) != len(seen) {
		return result, errors.New("replay inventory retained volume users do not match binding")
	}
	slices.Sort(result.BindingIDs)
	slices.Sort(result.VolumeUsers)
	return result, nil
}

func databaseRunnerReplayInventoryMatches(before, after databaseRunnerReplayInventory, known []domain.HostingOperation, current domain.HostingOperation) error {
	want, err := databaseRunnerReplayIdentitiesFor(before, known, current)
	if err != nil {
		return err
	}
	got, err := databaseRunnerReplayIdentitiesFor(after, known, current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return errors.New("replay changed scoped all-state container or retained volume identities")
	}
	return nil
}

func (f *databaseRunnerFixture) replayInventory(known []domain.HostingOperation, current domain.HostingOperation) databaseRunnerReplayInventory {
	f.t.Helper()
	if f.process != nil {
		f.t.Fatal("replay inventory requires joined runner")
	}
	if strings.TrimSpace(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+databaseRunnerOwner+"-client$"))) != "" {
		f.t.Fatal("replay inventory requires absent private client")
	}
	ids := strings.Fields(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+current.BindingID)))
	if len(ids) > len(known) {
		f.t.Fatal("replay inventory has extra all-state binding containers")
	}
	var snapshot databaseRunnerReplayInventory
	for _, id := range ids {
		var container databaseRunnerContainer
		if json.Unmarshal(f.docker("container", "inspect", id, "--format", "{{json .}}"), &container) != nil || container.ID != id {
			f.t.Fatal("replay all-state binding inspection failed")
		}
		snapshot.BindingContainers = append(snapshot.BindingContainers, container)
	}
	snapshot.VolumeUsers = strings.Fields(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+current.Plan.Storage.VolumeName)))
	identities, err := databaseRunnerReplayIdentitiesFor(snapshot, known, current)
	if err != nil {
		f.t.Fatal("scoped replay inventory contains unknown container or volume residue")
	}
	f.t.Logf("joined replay inventory operation=%s binding_ids=%v retained_volume_users=%v", current.ID, identities.BindingIDs, identities.VolumeUsers)
	return snapshot
}

func databaseRunnerClientMatches(got databaseRunnerContainer, id, name, image, network string) bool {
	return databaseRunnerClientMismatch(got, id, name, image, network) == "none"
}

// Classification is closed: no inspected or expected value enters diagnostics.
func databaseRunnerClientMismatch(got databaseRunnerContainer, id, name, image, network string) string {
	if len(id) != 64 {
		return "expected_id_length"
	}
	if strings.ToLower(id) != id {
		return "expected_id_case"
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "expected_id_encoding"
	}
	_, attached := got.NetworkSettings.Networks[network]
	switch {
	case got.ID != id:
		return "container_id"
	case got.Name != "/"+name:
		return "container_name"
	case got.Image != image:
		return "image"
	case len(got.Config.Labels) != 1:
		return "label_count"
	case got.Config.Labels[databaseRunnerLabel] != databaseRunnerOwner:
		return "owner"
	case len(got.Config.Env) != 2 || !slices.Contains(got.Config.Env, "PATH=/nonexistent") || !slices.Contains(got.Config.Env, "HOME=/nonexistent"):
		return "environment"
	case len(got.Config.Cmd) != 0:
		return "command"
	case !reflect.DeepEqual(got.Config.Entrypoint, []string{"/client"}):
		return "entrypoint"
	case len(got.Mounts) != 0:
		return "mounts"
	case got.HostConfig.NetworkMode != network:
		return "network_mode"
	case got.HostConfig.Privileged:
		return "privileged"
	case len(got.HostConfig.PortBindings) != 0:
		return "port_bindings"
	case len(got.NetworkSettings.Networks) != 1:
		return "network_count"
	case !attached:
		return "network_attachment"
	default:
		return "none"
	}
}

func TestDatabaseRunnerClientIdentityBeforeSecretStdin(t *testing.T) {
	id := strings.Repeat("a", 64)
	name := databaseRunnerOwner + "-client"
	image := "sha256:" + strings.Repeat("b", 64)
	network := "ncdn_fixture"
	valid := databaseRunnerContainer{ID: id, Name: "/" + name, Image: image}
	valid.Config.Labels = map[string]string{databaseRunnerLabel: databaseRunnerOwner}
	valid.Config.Env = []string{"PATH=/nonexistent", "HOME=/nonexistent"}
	valid.Config.Entrypoint = []string{"/client"}
	valid.HostConfig.NetworkMode = network
	valid.NetworkSettings.Networks = map[string]struct{ NetworkID, IPAddress string }{network: {}}
	if !databaseRunnerClientMatches(valid, id, name, image, network) {
		t.Fatal("owned private client refused")
	}
	t.Run("environment_order", func(t *testing.T) {
		reordered := valid
		reordered.Config.Env = []string{"HOME=/nonexistent", "PATH=/nonexistent"}
		if !databaseRunnerClientMatches(reordered, id, name, image, network) {
			t.Fatal("owned private client refused reordered environment")
		}
	})
	poison := "synthetic-protected-value\nAuthorization: Bearer never-print-this\x00%+v"
	for change, want := range map[string]string{
		"expected_length": "expected_id_length", "expected_case": "expected_id_case", "expected_encoding": "expected_id_encoding",
		"identity": "container_id", "name": "container_name", "image": "image", "owner": "owner",
		"missing_labels": "label_count", "extra_label": "label_count", "network": "network_mode",
		"privilege": "privileged", "environment": "environment", "command": "command", "entrypoint": "entrypoint",
		"environment_empty": "environment", "environment_missing_path": "environment", "environment_missing_home": "environment",
		"environment_unexpected": "environment", "environment_duplicate_path": "environment", "environment_duplicate_home": "environment",
		"environment_extra_duplicate": "environment", "environment_changed_path": "environment", "environment_changed_home": "environment",
		"environment_malformed": "environment",
		"mount":                 "mounts", "port": "port_bindings", "network_count": "network_count", "network_attachment": "network_attachment",
		"expected_name": "container_name", "expected_image": "image", "expected_network": "network_mode",
	} {
		t.Run(change, func(t *testing.T) {
			raw, e := json.Marshal(valid)
			if e != nil {
				t.Fatal("client_identity_fixture_encoding")
			}
			defer clear(raw)
			var got databaseRunnerContainer
			if json.Unmarshal(raw, &got) != nil {
				t.Fatal("client identity fixture")
			}
			expectedID, expectedName, expectedImage, expectedNetwork := id, name, image, network
			switch change {
			case "expected_length":
				expectedID = poison
			case "expected_case":
				expectedID = strings.Repeat("A", 64)
			case "expected_encoding":
				expectedID = strings.Repeat("z", 64)
			case "expected_name":
				expectedName = poison
			case "expected_image":
				expectedImage = poison
			case "expected_network":
				expectedNetwork = poison
			case "identity":
				got.ID = poison
			case "name":
				got.Name = poison
			case "image":
				got.Image = poison
			case "owner":
				got.Config.Labels[databaseRunnerLabel] = poison
			case "missing_labels":
				got.Config.Labels = nil
			case "extra_label":
				got.Config.Labels[poison] = poison
			case "network":
				got.HostConfig.NetworkMode = poison
			case "network_count":
				got.NetworkSettings.Networks = nil
			case "network_attachment":
				delete(got.NetworkSettings.Networks, network)
				got.NetworkSettings.Networks[poison] = struct{ NetworkID, IPAddress string }{poison, poison}
			case "privilege":
				got.HostConfig.Privileged = true
			case "environment":
				got.Config.Env = []string{poison}
			case "environment_empty":
				got.Config.Env = nil
			case "environment_missing_path":
				got.Config.Env = []string{"HOME=/nonexistent"}
			case "environment_missing_home":
				got.Config.Env = []string{"PATH=/nonexistent"}
			case "environment_unexpected":
				got.Config.Env = append(got.Config.Env, "PASSWORD="+poison)
			case "environment_duplicate_path":
				got.Config.Env = []string{"PATH=/nonexistent", "PATH=/nonexistent"}
			case "environment_duplicate_home":
				got.Config.Env = []string{"HOME=/nonexistent", "HOME=/nonexistent"}
			case "environment_extra_duplicate":
				got.Config.Env = append(got.Config.Env, "PATH=/nonexistent")
			case "environment_changed_path":
				got.Config.Env = []string{"PATH=" + poison, "HOME=/nonexistent"}
			case "environment_changed_home":
				got.Config.Env = []string{"PATH=/nonexistent", "HOME=" + poison}
			case "environment_malformed":
				got.Config.Env = []string{"PATH", "HOME=/nonexistent"}
			case "command":
				got.Config.Cmd = []string{poison}
			case "entrypoint":
				got.Config.Entrypoint = []string{poison}
			case "mount":
				got.Mounts = append(got.Mounts, struct {
					Type, Name, Source, Destination string
					RW                              bool
				}{Type: "bind", Source: poison, Destination: poison})
			case "port":
				got.HostConfig.PortBindings = map[string]any{poison: poison}
			}
			mismatch := databaseRunnerClientMismatch(got, expectedID, expectedName, expectedImage, expectedNetwork)
			if mismatch != want || databaseRunnerClientMatches(got, expectedID, expectedName, expectedImage, expectedNetwork) {
				t.Fatal("changed client could receive credential stdin")
			}
			for _, diagnostic := range []string{
				fmt.Sprintf("client identity refused before credential stdin classification=%s", mismatch),
				fmt.Sprintf("verified client cleanup refused classification=%s", mismatch),
			} {
				if strings.Contains(diagnostic, poison) || strings.Contains(diagnostic, "never-print-this") || strings.ContainsAny(diagnostic, "\n\x00") {
					t.Fatal("client identity diagnostic exposed protected input")
				}
			}
		})
	}
}

func (f *databaseRunnerFixture) marker(view domain.HostingDatabase, op domain.HostingOperation, marker string, write bool) {
	f.t.Helper()
	f.markerAt(view, op, marker, write, false)
}

func (f *databaseRunnerFixture) markerAt(view domain.HostingDatabase, op domain.HostingOperation, marker string, write, alias bool) {
	f.t.Helper()
	container := f.inspect(op)
	endpoint := container.NetworkSettings.Networks[op.Plan.Database.NetworkName].IPAddress
	var ref domain.HostingDatabaseCredentialRef
	for _, candidate := range view.Credentials {
		if candidate.Role == "app" {
			ref = candidate
		}
	}
	if ref.SecretID == "" {
		f.t.Fatal("application credential reference missing")
	}
	password := f.reveal(ref)
	defer clear(password)
	input := struct {
		Engine, Host, Username string
		Password               []byte
		Marker                 string
		Write                  bool
	}{view.Engine, endpoint, ref.Username, password, marker, write}
	// Lower-case wire keys are explicit; this payload is sent only to stdin.
	payload := map[string]any{"engine": input.Engine, "host": input.Host, "username": input.Username, "password": input.Password, "marker": input.Marker, "write": input.Write}
	if alias {
		host, address, err := databaseUpgradeClientEndpoint(op, endpoint)
		if err != nil {
			f.t.Fatal("private alias client target invalid")
		}
		payload["host"], payload["address"] = host, address
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal("protected client payload failed")
	}
	defer clear(raw)
	name := databaseRunnerOwner + "-client"
	image := "nerocd-database-client:" + databaseRunnerOwner
	if strings.TrimSpace(string(f.docker("container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$"))) != "" {
		f.t.Fatal("client exact name already exists")
	}
	var imageProof struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(f.docker("image", "inspect", image, "--format", "{{json .}}"), &imageProof) != nil || imageProof.ID == "" || imageProof.Config.Labels[databaseRunnerLabel] != databaseRunnerOwner {
		f.t.Fatal("client image ownership mismatch")
	}
	id := ""
	verify := func(ctx context.Context, identity string) string {
		data, e := databaseRunnerDocker(ctx, nil, "container", "inspect", identity, "--format", "{{json .}}")
		if e != nil {
			return "inspect_unavailable"
		}
		var got databaseRunnerContainer
		if json.Unmarshal(data, &got) != nil {
			return "inspect_invalid"
		}
		return databaseRunnerClientMismatch(got, identity, name, imageProof.ID, op.Plan.Database.NetworkName)
	}
	cleanup := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		data, e := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
		if e != nil {
			return "cleanup_inventory_unavailable"
		}
		ids := strings.Fields(string(data))
		if len(ids) == 0 {
			return "none"
		}
		if len(ids) != 1 || id != "" && ids[0] != id {
			return "cleanup_identity"
		}
		if mismatch := verify(ctx, ids[0]); mismatch != "none" {
			return mismatch
		}
		if _, e = databaseRunnerDocker(ctx, nil, "container", "rm", "--force", ids[0]); e != nil {
			return "cleanup_remove_unavailable"
		}
		data, e = databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
		if e != nil || strings.TrimSpace(string(data)) != "" {
			return "cleanup_absence_unconfirmed"
		}
		return "none"
	}
	defer func() {
		if mismatch := cleanup(); mismatch != "none" {
			f.t.Fatalf("verified client cleanup refused classification=%s", mismatch)
		}
	}()
	created, e := databaseRunnerDocker(f.t.Context(), nil, "container", "create", "--interactive", "--name", name, "--label", databaseRunnerLabel+"="+databaseRunnerOwner, "--network", op.Plan.Database.NetworkName, "--env", "PATH=/nonexistent", "--env", "HOME=/nonexistent", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", imageProof.ID)
	if e != nil {
		f.t.Fatal("private client creation failed")
	}
	id = strings.TrimSpace(string(created))
	if mismatch := verify(f.t.Context(), id); mismatch != "none" {
		f.t.Fatalf("client identity refused before credential stdin classification=%s", mismatch)
	}
	inspected, err := json.Marshal(container)
	if err != nil {
		f.t.Fatal("private inspection encoding failed")
	}
	defer clear(inspected)
	for _, secret := range f.protected {
		if len(secret) > 0 && bytes.Contains(inspected, secret) {
			f.t.Fatal("protected credential entered Docker configuration")
		}
	}
	output, e := databaseRunnerDocker(f.t.Context(), raw, "container", "start", "--attach", "--interactive", id)
	defer clear(output)
	if e != nil || string(output) != "marker verified\n" {
		f.t.Fatal("authenticated private marker proof failed")
	}
	f.t.Logf("private authenticated marker engine=%s operation=%s write=%t", view.Engine, op.ID, write)
}

func (f *databaseRunnerFixture) assertRedacted() {
	f.t.Helper()
	out := f.logs.snapshot()
	defer clear(out)
	for _, secret := range f.protected {
		if len(secret) > 0 && (bytes.Contains(out, secret) || bytes.Contains(out, []byte(base64.StdEncoding.EncodeToString(secret)))) {
			f.t.Error("protected value entered API or runner output")
		}
	}
	for _, dir := range []string{"journal", "state"} {
		if err := filepath.WalkDir(filepath.Join(f.root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, e := entry.Info()
			if e != nil || info.Size() > 1<<20 {
				return errors.New("fixture durable metadata bound exceeded")
			}
			raw, e := os.ReadFile(path)
			defer clear(raw)
			if e != nil {
				return e
			}
			for _, secret := range f.protected {
				if len(secret) > 0 && (bytes.Contains(raw, secret) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(secret)))) {
					return errors.New("protected value entered durable public metadata")
				}
			}
			return nil
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			f.t.Error("durable public metadata redaction failed")
		}
	}
}

func TestHostingDatabaseCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-database-runner/gate.sh after explicit physical authorization")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	f := newDatabaseRunnerFixture(t)
	for i, engine := range []string{"postgresql", "redis"} {
		version := domain.HostingDatabasePostgresRecipe
		if engine == "redis" {
			version = domain.HostingDatabaseRedisRecipe
		}
		view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(string(rune('a'+i)), 32), ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "runner fixture " + engine, DestinationID: f.destination, Engine: engine, RecipeVersion: version}, 201)
		if view.RuntimeState != domain.HostingDatabaseUnqualified || len(view.Credentials) != 2 {
			t.Fatal("metadata asserted engine qualification or omitted references")
		}
		for _, ref := range view.Credentials {
			if ref.Role != "app" {
				value := f.reveal(ref)
				clear(value)
			}
		}
		first := f.await(f.deploy(view, "database_a").ID)
		a := f.inspect(first)
		volume := f.volumeProof(first)
		marker := "database_runner_" + engine + "_retained_marker"
		f.marker(view, first, marker, true)
		second := f.await(f.deploy(view, "database_b").ID)
		b := f.inspect(second)
		if f.volumeProof(second) != volume {
			t.Fatal("replacement recreated or relabeled volume")
		}
		if a.ID == b.ID || first.Plan.Storage.VolumeName != second.Plan.Storage.VolumeName || first.Plan.Database.NetworkName != second.Plan.Database.NetworkName || !reflect.DeepEqual(first.Plan.Database.Credentials, second.Plan.Database.Credentials) || a.NetworkSettings.Networks[first.Plan.Database.NetworkName].NetworkID != b.NetworkSettings.Networks[second.Plan.Database.NetworkName].NetworkID {
			t.Fatal("replacement changed retained storage/network/credentials or reused writer")
		}
		f.marker(view, second, marker, false)
		// Queue C with the runner stopped, then restart the compiled process using
		// the same protected roots. No database restart API is implied.
		f.stop()
		thirdID := f.deploy(view, "database_c").ID
		fault := &databaseRunnerFault{entered: make(chan struct{}), finished: make(chan struct{}), operationID: thirdID}
		if !f.fault.CompareAndSwap(nil, fault) {
			t.Fatal("completion fault already armed")
		}
		f.start()
		select {
		case <-fault.entered:
		case <-time.After(180 * time.Second):
			t.Fatal("real committed completion fault did not enter")
		}
		f.stop()
		select {
		case <-fault.finished:
		case <-time.After(10 * time.Second):
			t.Fatal("completion fault handler did not join")
		}
		third := f.await(thirdID)
		c := f.inspect(third)
		journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
		if err != nil {
			t.Fatal("stopped runner journal unavailable")
		}
		snapshot := journal.Snapshot()
		if journal.Close() != nil {
			t.Fatal("journal close failed")
		}
		pending := false
		for _, entry := range snapshot.Hosting {
			if entry.OperationID == thirdID && entry.Phase == "result" && entry.Database && entry.Result != nil && *entry.Result == fault.request {
				pending = true
			}
		}
		if !pending {
			t.Fatal("completion response loss did not retain exact durable result")
		}
		knownContainers := []domain.HostingOperation{first, second, third}
		beforeInventory := f.replayInventory(knownContainers, third)
		beforeComplete := f.completionCount(thirdID)
		beforeClaim := f.claimRequests.Load()
		beforeApply, beforeDelivery := f.applyRequests.Load(), f.deliveryRequests.Load()
		f.start()
		for deadline := time.Now().Add(30 * time.Second); ; {
			if f.completionCount(thirdID) > beforeComplete && f.claimRequests.Load() > beforeClaim {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("compiled restart did not replay completion")
			}
			time.Sleep(100 * time.Millisecond)
		}
		// Read-only public settled evidence and Docker identities must remain exact.
		replayed := f.await(thirdID)
		after := f.inspect(replayed)
		if f.volumeProof(replayed) != volume || after.NetworkSettings.Networks[third.Plan.Database.NetworkName].NetworkID != a.NetworkSettings.Networks[first.Plan.Database.NetworkName].NetworkID {
			t.Fatal("runner restart changed retained volume or private bridge")
		}
		if c.ID != after.ID || c.ID == b.ID || third.CompleteReceipt == nil || replayed.CompleteReceipt == nil || *third.CompleteReceipt != *replayed.CompleteReceipt || *third.Result != *replayed.Result || third.Plan.Storage.VolumeName != first.Plan.Storage.VolumeName || third.Plan.Database.NetworkName != first.Plan.Database.NetworkName || !reflect.DeepEqual(third.Plan.Database.Credentials, view.Credentials) {
			t.Fatal("completion replay duplicated writer or changed durable proof")
		}
		// Join the replay process before reopening its exclusive journal.
		f.stop()
		afterInventory := f.replayInventory(knownContainers, replayed)
		if databaseRunnerReplayInventoryMatches(beforeInventory, afterInventory, knownContainers, replayed) != nil {
			t.Fatal("Complete replay changed scoped all-state container or retained volume residue")
		}
		replayJournal, e := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
		if e != nil {
			t.Fatal("replayed journal unavailable")
		}
		remaining := replayJournal.Snapshot()
		if replayJournal.Close() != nil {
			t.Fatal("replayed journal close failed")
		}
		for _, entry := range remaining.Hosting {
			if entry.OperationID == thirdID {
				t.Fatal("acknowledged database result remains pending")
			}
		}
		if f.completionCount(thirdID) != beforeComplete+1 || f.applyRequests.Load() != beforeApply || f.deliveryRequests.Load() != beforeDelivery {
			t.Fatal("completion replay refreshed credentials or repeated writer admission")
		}
		f.marker(view, replayed, marker, false)
		f.start()
		metadata := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "GET", "/api/v1/resources/"+view.Resource.ID+"/database", nil, 200)
		if metadata.RuntimeState != domain.HostingDatabaseUnqualified || !reflect.DeepEqual(metadata.Credentials, view.Credentials) {
			t.Fatal("runtime fixture changed metadata contract")
		}
		t.Logf("engine=%s A=%s B=%s C=%s container=%s volume=%s network=%s recipe=%s child=%s", engine, first.ID, second.ID, third.ID, c.ID, third.Plan.Storage.VolumeName, third.Plan.Database.NetworkName, version, third.Release.CanonicalImage)
	}
}

func TestDatabaseRunnerEnrollmentInputAcceptedByAPI(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	post := func(path, bearer string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal("fixture enrollment input encoding failed")
		}
		defer clear(raw)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, r)
		if rec.Code != status {
			t.Fatalf("fixture enrollment API path=%s status=%d want=%d", path, rec.Code, status)
		}
		return rec
	}
	caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	if os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		caps = append(caps, domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability)
	}
	if os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") == "1" || os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1" {
		caps = append(caps, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability)
		if os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") == "1" {
			caps = append(caps, "database_backup_transfer", "hosting_postgresql_remote_backup_v1", "hosting_mysql_remote_backup_v1", "hosting_mysql_remote_restore_v1")
			if os.Getenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME") == "1" {
				caps = append(caps, "database_backup_retention", "hosting_postgresql_backup_retention_v1")
			}
		}
	}
	created := post("/api/v1/runner-enrollments", token, app.RunnerEnrollmentInput{RunnerID: "runner_database_acceptance", RunnerName: "private database acceptance", Capabilities: caps}, http.StatusCreated)
	var enrollment app.CreatedRunnerEnrollment
	if json.Unmarshal(created.Body.Bytes(), &enrollment) != nil {
		t.Fatal("fixture enrollment response decoding failed")
	}
	credential := "ncr_database_runner_fixture_only"
	consumed := post("/api/v1/runner-enrollments/consume", enrollment.Token, databaseRunnerEnrollmentConsumeInput(credential), http.StatusOK)
	var enrolled app.ConsumedRunnerEnrollment
	if json.Unmarshal(consumed.Body.Bytes(), &enrolled) != nil || enrolled.Runner.ID != "runner_database_acceptance" {
		t.Fatal("fixture enrollment returned a different runner")
	}
	post("/api/v1/runners/heartbeat", credential, struct{}{}, http.StatusOK)
	tlsServer := httptest.NewTLSServer(server)
	t.Cleanup(tlsServer.Close)
	client := runnerclient.New(tlsServer.URL, credential)
	client.HTTP = tlsServer.Client()
	id, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") == "1" || os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1", RemoteDatabaseBackups: os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") == "1"})
	if err != nil || id != enrolled.Runner.ID {
		t.Fatal("real API/client rejected the enrolled database runtime identity")
	}
	if _, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{}); err == nil {
		t.Fatal("real API/client accepted database capabilities without configured database mode")
	}
}

func TestDatabaseRunnerExitChild(t *testing.T) {
	if os.Getenv("NEROCD_DATABASE_DIAGNOSTIC_EXIT_CHILD") == "1" {
		os.Exit(7)
	}
}

func TestDatabaseRunnerEarlyExitRetainsCleanupStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseRunnerExitChild$")
	cmd.Env = append(os.Environ(), "NEROCD_DATABASE_DIAGNOSTIC_EXIT_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal("diagnostic child start failed")
	}
	p := newDatabaseRunnerProcess(cmd, cancel)
	f := &databaseRunnerFixture{t: t, process: p}
	t.Cleanup(f.stop)
	started := time.Now()
	op, reason := waitDatabaseRunnerOperation(ctx, p, func(context.Context) (domain.HostingOperation, error) {
		return domain.HostingOperation{State: "dispatched"}, nil
	})
	if reason != "runner_exited" || (op.State != "" && op.State != "dispatched") || time.Since(started) > 2*time.Second {
		t.Fatal("database await did not promptly detect the exited runner")
	}
	want := databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 7}
	first, second := p.status(), p.status()
	if first != want || second != want {
		t.Fatal("exit inspection consumed or changed retained status")
	}
	f.stop()
	f.stop()
	if f.process != nil || f.lastExit != want || p.status() != want || cmd.ProcessState.ExitCode() != 7 {
		t.Fatal("cleanup lost or changed the runner exit result")
	}
}

func TestDatabaseRunnerExitCancelsInflightPoll(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := &databaseRunnerProcess{done: make(chan struct{})}
	blocked, published := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(published)
		select {
		case <-blocked:
			p.exit = databaseRunnerExit{Exited: true, Kind: "success", Code: 0}
			close(p.done)
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-published }()
	calls := 0
	started := time.Now()
	op, reason := waitDatabaseRunnerOperation(ctx, p, func(request context.Context) (domain.HostingOperation, error) {
		calls++
		if calls == 1 {
			return domain.HostingOperation{State: "prepared", Plan: &domain.HostingApplyPlan{}}, nil
		}
		close(blocked)
		<-request.Done()
		return domain.HostingOperation{}, errors.New("secret poll error must not be rendered")
	})
	if reason != "runner_exited" || op.State != "prepared" || op.Plan == nil || time.Since(started) > 2*time.Second {
		t.Fatal("runner exit did not cancel the in-flight poll and retain the last operation")
	}
	if !p.status().Exited {
		t.Fatal("in-flight poll consumed runner exit evidence")
	}
}

func TestDatabaseRunnerDiagnosticWhitelistRedaction(t *testing.T) {
	secret := "ncr_diagnostic_secret_\nhttps://private.invalid/?token=secret"
	var counts databaseRunnerAPICounts
	counts.record(secret, secret, 418)
	counts.record("POST", "/api/v1/runners/claim?token="+secret, 200)
	counts.record("GET", "/api/v1/hosting-operations/"+secret, 418)
	for range 10050 {
		counts.record("POST", "/api/v1/runners/claim", 401)
	}
	op := domain.HostingOperation{State: secret, ID: secret, RunID: secret, Snapshot: domain.HostingSnapshot{ImageReference: secret}, Plan: &domain.HostingApplyPlan{Hash: secret}, Result: &domain.HostingResult{Outcome: secret}, PreparedReceipt: &domain.HostingReceipt{ID: secret}}
	got := databaseRunnerDiagnostic(op, databaseRunnerExit{Exited: true, Kind: secret, Code: 1000}, &counts)
	want := "state=unknown release=false plan=true prepared=true result=true complete=false mutation_hold=false restore_hold=false runner_exited=true exit_kind=unknown exit_code=-1 api=[POST /api/v1/runners/claim 401=9999,GET /api/v1/hosting-operations/{id} 0=1]"
	if got != want || strings.Contains(got, secret) {
		t.Fatal("diagnostics escaped the fixed whitelist or count bound")
	}
	classified := databaseRunnerExitFor(errors.New(secret))
	if classified != (databaseRunnerExit{Exited: true, Kind: "wait_error", Code: -1}) {
		t.Fatal("runner error text escaped closed exit classification")
	}
	rec := httptest.NewRecorder()
	w := &databaseRunnerStatusWriter{ResponseWriter: rec}
	if _, err := w.Write([]byte(secret)); err != nil {
		t.Fatal("status observer write failed")
	}
	w.WriteHeader(500)
	if w.status != 200 || rec.Code != 200 || rec.Body.String() != secret {
		t.Fatal("status observer changed API response semantics")
	}
}

func TestDatabaseRunnerProxyDiagnosticWhitelist(t *testing.T) {
	var counts databaseRunnerAPICounts
	var want []string
	for _, phase := range []string{"plan", "prepared", "apply-begin", "status", "complete"} {
		path := "/api/v1/runners/proxy/" + phase
		counts.record("POST", path, 200)
		counts.record("POST", path+"?token=ncr_private", 200)
		want = append(want, "POST "+path+" 200=1")
	}
	counts.record("POST", "/api/v1/runners/proxy/ncr_private", 200)
	if got := counts.summary(); got != strings.Join(want, ",") || strings.Contains(got, "ncr_private") {
		t.Fatal("proxy diagnostics omitted a fixed phase or included private request text")
	}
}

func TestDatabaseRunnerReplayInventoryRejectsStoppedResidue(t *testing.T) {
	binding, resource, volume := "binding_inventory", "resource_inventory", "ncdv_inventory"
	operation := func(id, containerID string) domain.HostingOperation {
		return domain.HostingOperation{ID: id, BindingID: binding, ResourceID: resource, Plan: &domain.HostingApplyPlan{Hash: "plan_" + id, Storage: &domain.HostingStoragePlan{VolumeName: volume, MountPath: "/data"}}, Release: &domain.HostingRelease{Hash: "release_" + id, ImageID: "image_" + id}, Result: &domain.HostingResult{ContainerID: containerID}}
	}
	previous := operation("previous", strings.Repeat("a", 64))
	current := operation("current", strings.Repeat("b", 64))
	container := func(op domain.HostingOperation, running bool) databaseRunnerContainer {
		var c databaseRunnerContainer
		c.ID = op.Result.ContainerID
		c.Image = op.Release.ImageID
		c.State.Running = running
		c.Config.Labels = map[string]string{"io.nerocd.binding": binding, "io.nerocd.operation": op.ID, "io.nerocd.plan": op.Plan.Hash, "io.nerocd.release": op.Release.Hash}
		c.Mounts = append(c.Mounts, struct {
			Type, Name, Source, Destination string
			RW                              bool
		}{Type: "volume", Name: volume, Destination: "/data", RW: true})
		return c
	}
	known := []domain.HostingOperation{previous, current}
	before := databaseRunnerReplayInventory{BindingContainers: []databaseRunnerContainer{container(current, true)}, VolumeUsers: []string{current.Result.ContainerID}}
	if err := databaseRunnerReplayInventoryMatches(before, before, known, current); err != nil {
		t.Fatal("unchanged scoped inventory rejected", err)
	}
	withStopped := databaseRunnerReplayInventory{BindingContainers: []databaseRunnerContainer{container(previous, false), container(current, true)}, VolumeUsers: []string{previous.Result.ContainerID, current.Result.ContainerID}}
	reordered := databaseRunnerReplayInventory{BindingContainers: []databaseRunnerContainer{container(current, true), container(previous, false)}, VolumeUsers: []string{current.Result.ContainerID, previous.Result.ContainerID}}
	if err := databaseRunnerReplayInventoryMatches(withStopped, reordered, known, current); err != nil {
		t.Fatal("unchanged known stopped identity rejected", err)
	}
	for _, change := range []string{"unknown_stopped_binding", "known_extra_stopped_binding", "foreign_stopped_volume_user", "lost_stopped_identity", "duplicate_binding", "foreign_label"} {
		t.Run(change, func(t *testing.T) {
			after := databaseRunnerReplayInventory{BindingContainers: slices.Clone(before.BindingContainers), VolumeUsers: slices.Clone(before.VolumeUsers)}
			start := before
			switch change {
			case "unknown_stopped_binding":
				extra := container(operation("unknown", strings.Repeat("c", 64)), false)
				after.BindingContainers = append(after.BindingContainers, extra)
				after.VolumeUsers = append(after.VolumeUsers, extra.ID)
			case "known_extra_stopped_binding":
				after.BindingContainers = append(after.BindingContainers, container(previous, false))
				after.VolumeUsers = append(after.VolumeUsers, previous.Result.ContainerID)
			case "foreign_stopped_volume_user":
				after.VolumeUsers = append(after.VolumeUsers, strings.Repeat("c", 64))
			case "lost_stopped_identity":
				start = databaseRunnerReplayInventory{BindingContainers: append(slices.Clone(before.BindingContainers), container(previous, false)), VolumeUsers: append(slices.Clone(before.VolumeUsers), previous.Result.ContainerID)}
			case "duplicate_binding":
				after.BindingContainers = append(after.BindingContainers, container(current, true))
			case "foreign_label":
				after.BindingContainers[0] = container(current, true)
				after.BindingContainers[0].Config.Labels["io.nerocd.binding"] = "foreign"
			}
			if databaseRunnerReplayInventoryMatches(start, after, known, current) == nil {
				t.Fatal("replay inventory accepted stopped or foreign residue")
			}
		})
	}
}

func TestDatabaseRunnerProtectedRevealDecoder(t *testing.T) {
	ref := domain.HostingDatabaseCredentialRef{Role: "app", Username: "ncd_app", SecretID: "fixture_secret", Version: 1}
	valid := fmt.Sprintf(`{"secret_id":"fixture_secret","version":1,"value":"%s"}`, strings.Repeat("A", 43))
	value, err := readDatabaseRunnerReveal(strings.NewReader(valid), ref)
	if err != nil || len(value) != 43 {
		t.Fatal("valid protected response rejected")
	}
	clear(value)
	for _, raw := range []string{valid + "{}", strings.Replace(valid, `"version":1`, `"version":2`, 1), strings.Replace(valid, `"version":1`, `"version":2,"version":1`, 1), strings.Replace(valid, `"value":`, `"Value":`, 1), strings.Replace(valid, "fixture_secret", "foreign", 1), strings.Replace(valid, `"value":`, `"extra":true,"value":`, 1), strings.Replace(valid, strings.Repeat("A", 43), "private diagnostic", 1), strings.Repeat("x", 8193)} {
		value, err := readDatabaseRunnerReveal(strings.NewReader(raw), ref)
		clear(value)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("protected decoder accepted or exposed invalid body")
		}
	}
}

func TestDatabaseRunnerFinalExitSamplePreservesFailureAndCurrentState(t *testing.T) {
	previous := domain.HostingOperation{State: "verifying", MutationHold: true}
	for _, state := range []string{"succeeded", "failed", "reconciliation_required", "verifying"} {
		t.Run(state, func(t *testing.T) {
			final := domain.HostingOperation{State: state, MutationHold: state == "reconciliation_required" || state == "verifying"}
			if state != "verifying" {
				final.Result = &domain.HostingResult{Outcome: "failed", Uncertain: state == "reconciliation_required"}
				final.CompleteReceipt = &domain.HostingReceipt{ID: "receipt"}
				if state == "succeeded" {
					final.Result.Outcome, final.Result.Ready = "succeeded", true
				}
			}
			reason := "runner_exited"
			calls := 0
			got, label := databaseRunnerFinalExitSample(t.Context(), previous, reason, func(ctx context.Context) (domain.HostingOperation, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 2*time.Second || ctx.Err() != nil {
					t.Error("final read did not get a fresh bounded context")
				}
				return final, nil
			})
			if calls != 1 || label != "authoritative" || !reflect.DeepEqual(got, final) {
				t.Fatal("final diagnostic changed failure or lost current predicates")
			}
		})
	}
	calls := 0
	got, label := databaseRunnerFinalExitSample(t.Context(), previous, "runner_exited", func(context.Context) (domain.HostingOperation, error) {
		calls++
		return domain.HostingOperation{}, errors.New("private_final_read_canary")
	})
	if calls != 1 || label != "stale" || !reflect.DeepEqual(got, previous) {
		t.Fatal("missing final sample asserted authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, label = databaseRunnerFinalExitSample(ctx, previous, "runner_exited", func(context.Context) (domain.HostingOperation, error) {
		t.Fatal("canceled parent performed final read")
		return domain.HostingOperation{}, nil
	})
	if label != "stale" || !reflect.DeepEqual(got, previous) {
		t.Fatal("canceled diagnostic discarded retained evidence")
	}
	_, label = databaseRunnerFinalExitSample(t.Context(), previous, "deadline", func(context.Context) (domain.HostingOperation, error) {
		t.Fatal("non-exit performed final read")
		return domain.HostingOperation{}, nil
	})
	if label != "not_requested" {
		t.Fatal("ordinary wait failure was reinterpreted")
	}
}
