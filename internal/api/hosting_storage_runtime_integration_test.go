//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
)

const storageRuntimeLabel = "io.nerocd.storage-runtime.owner"
const storageRuntimeImage = "127.0.0.1:25000/nerocd-storage-archive-runtime-20261024-f0a618:"

// A fault drops a committed response while the real CLI is still awaiting it.
// The test joins that process before releasing the handler and reopening its journal.
type storageRuntimeFault struct {
	path     string
	entered  chan struct{}
	finished chan struct{}
	response []byte
}

type storageRuntimeFixture struct {
	t                                                   *testing.T
	control                                             *httptest.Server
	client                                              *http.Client
	root, token, serverID, destinationID, environmentID string
	process                                             *pushRunnerProcess
	logs                                                *runtimeFixtureLog
	roots                                               *x509.CertPool
	fault                                               atomic.Pointer[storageRuntimeFault]
	completions                                         atomic.Int64
	archiveCompletions                                  atomic.Int64
	capabilities                                        []string
	archiveConfig                                       string
	lastOperation                                       atomic.Pointer[domain.HostingOperation]
	diagnosticsRegistered                               bool
	preserveRoot                                        bool
	proxyCleanupID, proxyCleanupDaemon                  string
}

func storageRuntimeJSON[T any](f *storageRuntimeFixture, method, path string, body any, status int) T {
	f.t.Helper()
	return storageRuntimeAuthorizedJSON[T](f, f.token, method, path, body, status)
}

func storageRuntimeAuthorizedJSON[T any](f *storageRuntimeFixture, token, method, path string, body any, status int) T {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(f.t.Context(), method, f.control.URL+path, bodyReader)
	if err != nil {
		f.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.client.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		f.t.Fatal("API response read/close", readErr, closeErr)
	}
	if response.StatusCode != status {
		f.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.StatusCode, status, data)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		f.t.Fatal(err)
	}
	return result
}

func storageRuntimeDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := storageRuntimeDockerOutput(exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, storageRuntimeDockerArguments(args)...)...))
	if err != nil {
		t.Fatalf("owned Docker %s failed: %v", args[0], err)
	}
	return output
}

func storageRuntimeTrust(t *testing.T, root string) (*x509.CertPool, []byte) {
	t.Helper()
	automaticFixtureDNS(t)
	name := storageRuntimeOwner + "-pebble"
	if strings.TrimSpace(string(storageRuntimeDocker(t, "ps", "-aq", "--filter", "name=^/"+name+"$"))) != "" {
		t.Fatal("Pebble name already exists")
	}
	id := strings.TrimSpace(string(storageRuntimeDocker(t, "run", "-d", "--name", name, "--label", storageRuntimeLabel+"="+storageRuntimeOwner, "--network", "host", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/workspace/acceptance/runtime-proxy-control/pebble.json,dst=/pebble-config.json,readonly", automaticFixturePebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")))
	t.Logf("owned Pebble container=%s image=%s", id, automaticFixturePebbleImage)
	t.Cleanup(func() {
		identity := strings.TrimSpace(string(storageRuntimeDocker(t, "inspect", "--format", `{{.Id}} {{index .Config.Labels "io.nerocd.storage-runtime.owner"}}`, id)))
		if identity != id+" "+storageRuntimeOwner {
			t.Error("Pebble ownership changed")
			return
		}
		storageRuntimeDocker(t, "rm", "--force", id)
	})
	storageRuntimeDocker(t, "cp", name+":/test/certs/pebble.minica.pem", filepath.Join(root, "minica.pem"))
	public, err := os.ReadFile(filepath.Join(root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(public) {
		t.Fatal("invalid Pebble root")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get("https://localhost:15000/roots/0")
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			closeErr := response.Body.Close()
			if response.StatusCode == 200 && readErr == nil && closeErr == nil && roots.AppendCertsFromPEM(body) {
				return roots, append(append([]byte(nil), public...), body...)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Pebble roots unavailable")
	return nil, nil
}

func newStorageRuntimeFixture(t *testing.T, archive ...bool) *storageRuntimeFixture {
	t.Helper()
	if os.Getenv("NEROCD_STORAGE_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned storage runtime gate")
	}
	root, err := os.MkdirTemp("/var/lib", storageRuntimeOwner+"-")
	if err != nil {
		t.Fatal(err)
	}
	var f *storageRuntimeFixture
	t.Cleanup(func() {
		if f != nil && f.preserveRoot {
			t.Log("fixture root preserved after uncertain proxy cleanup")
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, proxyRoots := storageRuntimeTrust(t, root)
	server, pg, database := newHostingBuildAPIFixture(t)
	f = &storageRuntimeFixture{t: t, root: root, roots: roots, logs: &runtimeFixtureLog{}}
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			f.completions.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/storage-archive/complete" {
			f.archiveCompletions.Add(1)
		}
		fault := f.fault.Load()
		if fault == nil || r.URL.Path != fault.path || !f.fault.CompareAndSwap(fault, nil) {
			server.ServeHTTP(w, r)
			return
		}
		defer close(fault.finished)
		receipt := httptest.NewRecorder()
		server.ServeHTTP(receipt, r)
		fault.response = bytes.Clone(receipt.Body.Bytes())
		if receipt.Code != 200 {
			t.Errorf("fault request did not commit: path=%s status=%d body=%s", fault.path, receipt.Code, fault.response)
		}
		close(fault.entered)
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			t.Error("response-loss process did not cancel request")
		}
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:18190")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	control.Listener = listener
	control.StartTLS()
	f.control, f.client = control, control.Client()
	f.client.Timeout = 10 * time.Second
	t.Cleanup(control.Close)
	if err := os.WriteFile(filepath.Join(root, "api.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proxy-roots.pem"), proxyRoots, 0600); err != nil {
		t.Fatal(err)
	}
	login := storageRuntimeAuthorizedJSON[struct {
		Token string `json:"token"`
	}](f, "", http.MethodPost, "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
	f.token = login.Token
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingOwnedStorageCapability}
	if len(archive) > 0 && archive[0] {
		storageRuntimeConfigureArchive(t, f, pg)
		caps = append(caps, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability)
	}
	f.capabilities = append([]string(nil), caps...)
	enrollment := storageRuntimeJSON[app.CreatedRunnerEnrollment](f, http.MethodPost, "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_storage_runtime_fixture", RunnerName: "owned storage fixture", Capabilities: caps}, 201)
	credential := "ncr_storage_runtime_fixture_only"
	digest := sha256.Sum256([]byte(credential))
	enrolled := storageRuntimeAuthorizedJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, http.MethodPost, "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("e", 32), CredentialHash: hex.EncodeToString(digest[:])}, 200)
	if err := os.WriteFile(filepath.Join(root, "credential"), []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	target := storageRuntimeJSON[app.ServerDetail](f, http.MethodPost, "/api/v1/servers", app.ServerInput{Name: "storage runtime fixture", RunnerID: enrolled.Runner.ID}, 201)
	f.serverID, f.destinationID = target.Server.ID, target.Destination.ID
	storageRuntimeJSON[json.RawMessage](f, http.MethodPost, "/api/v1/destinations/"+f.destinationID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
	environment := storageRuntimeJSON[domain.ProjectEnvironment](f, http.MethodPost, "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage runtime fixture"}, 201)
	f.environmentID = environment.ID
	// Only queue infrastructure is initialized directly. Every hosting authority
	// and desired input below is obtained through an authenticated public API.
	for {
		tx, err := database.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := storageRuntimeRollback(tx); err != nil {
				t.Errorf("queue migration rollback: %v", err)
			}
		})
		changed, err := hostingjobs.MigrateNextTx(t.Context(), tx)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	stopJobs, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := stopJobs(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { f.stop(); t.Logf("compiled runner logs:\n%s", f.logs.String()) })
	f.start(caps)
	return f
}

func storageRuntimeRollback(tx interface{ Rollback(context.Context) error }) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return nil
}

type storageRuntimeRollbackFunc func(context.Context) error

func (rollback storageRuntimeRollbackFunc) Rollback(ctx context.Context) error { return rollback(ctx) }

func TestStorageRuntimeRollback(t *testing.T) {
	failure := errors.New("fixture rollback failure")
	for _, test := range []struct {
		name               string
		returned, expected error
	}{
		{name: "open transaction"},
		{name: "committed transaction", returned: pgx.ErrTxClosed},
		{name: "wrapped closed transaction", returned: fmt.Errorf("closed: %w", pgx.ErrTxClosed)},
		{name: "rollback error retained", returned: failure, expected: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := storageRuntimeRollback(storageRuntimeRollbackFunc(func(ctx context.Context) error {
				called = true
				deadline, ok := ctx.Deadline()
				remaining := time.Until(deadline)
				if ctx.Err() != nil || !ok || remaining <= 0 || remaining > 5*time.Second {
					t.Fatal("rollback requires a fresh live bounded cleanup context")
				}
				return test.returned
			}))
			if !called || !errors.Is(err, test.expected) {
				t.Fatalf("rollback called=%t error=%v want=%v", called, err, test.expected)
			}
		})
	}
}

func (f *storageRuntimeFixture) start(caps ...[]string) {
	f.t.Helper()
	if !f.diagnosticsRegistered {
		f.diagnosticsRegistered = true
		f.t.Cleanup(func() {
			if f.t.Failed() {
				f.failureDiagnostics()
			}
		})
	}
	if f.process != nil {
		f.t.Fatal("runner already started")
	}
	capabilities := append([]string(nil), f.capabilities...)
	if len(caps) > 0 {
		capabilities = caps[0]
	}
	ctx, cancel := context.WithCancel(f.t.Context())
	command := exec.CommandContext(ctx, "/fixture/nerocd-proxy-fixture", "runner", "--server", f.control.URL, "--credential-file", filepath.Join(f.root, "credential"), "--journal-dir", filepath.Join(f.root, "journal"), "--work-dir", filepath.Join(f.root, "runner"), "--poll-interval", "100ms", "--cancel-poll-interval", "100ms", "--capabilities", strings.Join(capabilities, ","), "--proxy-state-root", filepath.Join(f.root, "proxy"), "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch")
	if f.archiveConfig != "" {
		command.Args = append(command.Args, "--storage-archive-config-file", f.archiveConfig)
	}
	command.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+filepath.Join(f.root, "api.pem"), "NEROCD_PROXY_FIXTURE_ROOT="+filepath.Join(f.root, "proxy-roots.pem"), "NEROCD_PROXY_COMPILED_FIXTURE=disposable-dind")
	command.Stdout, command.Stderr = f.logs, f.logs
	if err := command.Start(); err != nil {
		cancel()
		f.t.Fatal(err)
	}
	process := &pushRunnerProcess{command: command, done: make(chan error, 1), cancel: cancel}
	f.process = process
	go func() { process.done <- command.Wait() }()
	f.t.Logf("compiled runner started pid=%d", command.Process.Pid)
}

func (f *storageRuntimeFixture) stop() {
	f.t.Helper()
	if f.process == nil {
		return
	}
	pid := f.process.command.Process.Pid
	f.process.cancel()
	select {
	case <-f.process.done:
		f.process = nil
		f.t.Logf("compiled runner joined pid=%d", pid)
	case <-time.After(10 * time.Second):
		f.t.Fatal("runner did not join")
	}
}

func (f *storageRuntimeFixture) arm(path string) *storageRuntimeFault {
	f.t.Helper()
	fault := &storageRuntimeFault{path: path, entered: make(chan struct{}), finished: make(chan struct{})}
	if !f.fault.CompareAndSwap(nil, fault) {
		f.t.Fatal("fault already armed")
	}
	return fault
}

func (f *storageRuntimeFixture) lose(fault *storageRuntimeFault, phase string, operationID string) runner.JournalHosting {
	f.t.Helper()
	select {
	case <-fault.entered:
	case <-time.After(90 * time.Second):
		f.t.Fatalf("fault path did not enter %s logs=%s", fault.path, f.logs.String())
	}
	f.stop()
	select {
	case <-fault.finished:
	case <-time.After(10 * time.Second):
		f.t.Fatal("fault handler did not join")
	}
	journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot := journal.Snapshot()
	if err := journal.Close(); err != nil {
		f.t.Fatal(err)
	}
	for _, entry := range snapshot.Hosting {
		if entry.OperationID == operationID && entry.Phase == phase {
			f.t.Logf("durable response-loss entry operation=%s phase=%s id=%s attempt=%d", operationID, phase, entry.ID, entry.Attempt.Attempt)
			return entry
		}
	}
	f.t.Fatalf("missing durable %s entry for %s", phase, operationID)
	return runner.JournalHosting{}
}

func (f *storageRuntimeFixture) resource(name, version string) domain.Resource {
	f.t.Helper()
	resource := storageRuntimeJSON[domain.Resource](f, http.MethodPost, "/api/v1/resources", map[string]any{"project_id": "proj_platform", "project_environment_id": f.environmentID, "name": name, "kind": "application", "source": map[string]any{"kind": "image", "image": domain.ImageSource{Reference: storageRuntimeImage + version}}}, 201)
	storageRuntimeJSON[json.RawMessage](f, http.MethodPost, "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destinationID}, 201)
	return resource
}

func (f *storageRuntimeFixture) storage(resource domain.Resource, key string) domain.HostingStorage {
	f.t.Helper()
	storage := storageRuntimeJSON[domain.HostingStorage](f, http.MethodPost, "/api/v1/resources/"+resource.ID+"/storage", domain.HostingStorageInput{RequestID: key, ExpectedResourceGeneration: resource.Generation, MountPath: "/data/uploads"}, 201)
	read := storageRuntimeJSON[domain.HostingStorage](f, http.MethodGet, "/api/v1/resources/"+resource.ID+"/storage", nil, 200)
	if !reflect.DeepEqual(storage, read) || storage.Validate() != nil {
		f.t.Fatal("public storage metadata mismatch")
	}
	f.t.Logf("public storage resource=%s binding=%s id=%s volume=%s hash=%s", resource.ID, storage.BindingID, storage.ID, storage.VolumeName, storage.Hash)
	return storage
}

func (f *storageRuntimeFixture) deploy(resource domain.Resource, key, path string, port int, continuity *domain.HostingStorageRouteContinuityInput) domain.HostingOperation {
	f.t.Helper()
	op := storageRuntimeJSON[domain.HostingOperation](f, http.MethodPost, "/api/v1/resources/"+resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, IdempotencyKey: key, ContainerPort: 8080, HostPort: port, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: 3}, StorageRouteContinuity: continuity}, 202)
	f.t.Logf("public deployment operation=%s resource=%s binding=%s port=%d", op.ID, resource.ID, op.BindingID, port)
	return op
}

func (f *storageRuntimeFixture) await(id, state string) domain.HostingOperation {
	f.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		op := storageRuntimeJSON[domain.HostingOperation](f, http.MethodGet, "/api/v1/hosting-operations/"+id, nil, 200)
		f.lastOperation.Store(&op)
		if op.State == state {
			return op
		}
		if op.State == "failed" || op.State == "succeeded" || op.State == "canceled" {
			f.t.Fatalf("operation %s settled %s want %s result=%+v logs=%s", id, op.State, state, op.Result, f.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatalf("operation %s did not reach %s logs=%s", id, state, f.logs.String())
	return domain.HostingOperation{}
}

// Failure observation never stops the runner, clears a hold, or acquires the
// journal writer lock. Fixed categories replace transport errors and payloads.
func (f *storageRuntimeFixture) failureDiagnostics() {
	op := f.lastOperation.Load()
	if op == nil {
		f.t.Log("storage failure diagnostic unavailable: no polled operation")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	read := func(path, token string, out any) bool {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.control.URL+path, nil)
		if err != nil {
			return false
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := f.client.Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		return err == nil && len(raw) <= 1<<20 && response.StatusCode == 200 && json.Unmarshal(raw, out) == nil
	}
	current := *op
	fresh := read("/api/v1/hosting-operations/"+op.ID, f.token, &current)
	summary := map[string]any{"operation_id": op.ID, "run_id": op.RunID, "fresh": fresh, "state": storageDiagnosticWord(current.State, "queued", "dispatched", "preparing", "prepared", "applying", "verifying", "succeeded", "failed", "canceled", "uncertain"), "mutation_hold": current.MutationHold, "restore_ownership_held": current.RestoreOwnershipHeld}
	if current.Result != nil {
		summary["result"] = map[string]any{"outcome": storageDiagnosticWord(current.Result.Outcome, "succeeded", "failed"), "ready": current.Result.Ready, "restored": current.Result.Restored, "uncertain": current.Result.Uncertain}
	}
	if current.StorageArchive != nil {
		summary["archive_result"] = storageDiagnosticResult(current.StorageArchive.Result)
		phases := make([]string, 0, len(current.StorageArchive.Progress))
		for _, progress := range current.StorageArchive.Progress {
			if len(phases) == 32 {
				break
			}
			phases = append(phases, storageDiagnosticPhases([]runner.JournalStorageArchive{{Plan: domain.HostingStorageArchivePlan{OperationID: op.ID}, Progress: &domain.HostingStorageArchiveProgress{Phase: progress.Phase}}}, op.ID)...)
		}
		summary["archive_phases"] = phases
	}
	var run domain.TaskRun
	if op.RunID != "" && read("/api/v1/runs/"+op.RunID, f.token, &run) {
		summary["run_state"] = storageDiagnosticWord(run.Status, "queued", "pending", "running", "succeeded", "failed", "canceled", "active")
	} else {
		summary["run_state"] = "unavailable"
	}
	encoded, _ := json.Marshal(summary)
	f.t.Logf("storage failure operation summary=%s", encoded)
	if !strings.HasPrefix(filepath.Base(f.root), storageRuntimeOwner+"-") {
		f.t.Log("storage failure journal unavailable: fixture ownership missing")
		return
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		f.t.Log("storage failure journal unavailable: root")
		return
	}
	defer root.Close()
	file, err := root.Open("journal/journal.json")
	if err != nil {
		f.t.Log("storage failure journal unavailable: snapshot")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.t.Log("storage failure journal unavailable: file type")
		return
	}
	entries, err := storageDiagnosticJournal(file)
	if err != nil {
		f.t.Log("storage failure journal unavailable: invalid or bounded")
		return
	}
	f.t.Logf("storage failure durable archive phases=%v", storageDiagnosticPhases(entries, op.ID))
	for _, entry := range entries {
		if entry.Plan.OperationID != op.ID || entry.Complete == nil || entry.Complete.HostingAuthority != entry.Authority() {
			continue
		}
		result, _ := json.Marshal(storageDiagnosticResult(&entry.Complete.Result))
		f.t.Logf("storage failure durable archive result=%s", result)
		credential, credentialErr := root.ReadFile("credential")
		if credentialErr == nil && len(credential) <= 256 {
			a := entry.Authority()
			query := url.Values{"lease_id": {a.LeaseID}, "attempt": {fmt.Sprint(a.Attempt)}, "fence": {a.Fence}}
			var lease domain.RunLease
			if read("/api/v1/runners/lease?"+query.Encode(), string(credential), &lease) && lease.ID == a.LeaseID && lease.RunID == a.RunID && lease.Attempt == a.Attempt && lease.Fence == a.Fence {
				f.t.Logf("storage failure archive lease state=%s", storageDiagnosticWord(lease.Status, "active", "completed", "expired", "canceled", "failed", "succeeded"))
			} else {
				f.t.Log("storage failure archive lease unavailable")
			}
		}
		profileValid := entry.Plan.Validate() == nil
		for _, candidate := range entries {
			if candidate.Phase == "helper_unverified" && candidate.Authority() == entry.Authority() && reflect.DeepEqual(candidate.Plan, entry.Plan) {
				decoded, err := hex.DecodeString(candidate.UncertainHelperContainerID)
				profileValid = profileValid && err == nil && len(decoded) == 32 && candidate.UncertainHelperContainerID == strings.ToLower(candidate.UncertainHelperContainerID)
			}
		}
		if profileValid {
			storageArchiveHelperFailureProfile(f, entry, entries)
		} else {
			f.t.Log("storage failure helper profile unavailable: invalid durable identity")
		}
		break
	}
}

func (f *storageRuntimeFixture) proxyAwait(id string) domain.HostingProxyOperation {
	f.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		op := storageRuntimeJSON[domain.HostingProxyOperation](f, http.MethodGet, "/api/v1/proxy-operations/"+id, nil, 200)
		if op.State == "applied" {
			return op
		}
		if op.State == "failed" {
			f.t.Fatalf("proxy operation failed: %+v logs=%s", op, f.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("proxy did not apply", id, f.logs.String())
	return domain.HostingProxyOperation{}
}

func (f *storageRuntimeFixture) https(host, method, path string, body []byte, status int) []byte {
	f.t.Helper()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(f.t.Context(), method, "https://"+host+path, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		f.t.Fatal("trusted HTTPS", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != status || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		f.t.Fatalf("HTTPS %s %s status=%d read=%v close=%v body=%s", method, path, response.StatusCode, readErr, closeErr, data)
	}
	return data
}

func (f *storageRuntimeFixture) awaitLoadedProxy(id, daemon string) domain.HostingProxy {
	f.t.Helper()
	if f.proxyCleanupID != "" && (f.proxyCleanupID != id || f.proxyCleanupDaemon != daemon) {
		f.preserveRoot = true
		f.t.Fatal("fixture proxy cleanup identity changed")
	}
	if f.proxyCleanupID == "" {
		profile, err := storageRuntimeProxyProfileFor(f.root, id, f.serverID, f.destinationID, "runner_storage_runtime_fixture", daemon)
		if err != nil {
			f.preserveRoot = true
			f.t.Fatal("fixture proxy cleanup profile invalid")
		}
		f.proxyCleanupID, f.proxyCleanupDaemon = id, daemon
		f.t.Cleanup(func() {
			// A failed runner join must also keep bind roots intact. Preserve
			// any earlier uncertainty even if this exact proxy is absent.
			previouslyPreserved := f.preserveRoot
			f.preserveRoot = true
			f.stop()
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(cleanup, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, storageRuntimeDockerArguments(args)...)...)
				return storageRuntimeDockerOutput(cmd)
			}
			if err := storageRuntimeRemoveOwnedProxy(profile, command); err != nil {
				f.preserveRoot = true
				f.t.Errorf("fixture proxy cleanup failed closed: %v", err)
				return
			}
			f.preserveRoot = previouslyPreserved
			f.t.Logf("fixture proxy absent before root cleanup: proxy=%s daemon=%s", id, daemon)
		})
	}
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		proxy := storageRuntimeJSON[domain.HostingProxy](f, http.MethodGet, "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
		if proxy.ID != id {
			f.t.Fatal("public proxy identity changed")
		}
		if proxy.LoadedGeneration > 0 && proxy.LoadedGeneration == proxy.DesiredGeneration && proxy.ActiveOperationID == "" && proxy.HoldOperationID == "" && !proxy.RestoreOwnershipHeld {
			if proxy.DaemonID != daemon {
				f.t.Fatal("loaded proxy daemon differs from workload daemon")
			}
			return proxy
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("public proxy did not settle loaded", f.logs.String())
	return domain.HostingProxy{}
}

func (f *storageRuntimeFixture) continuity(resource domain.Resource, previous domain.HostingOperation) *domain.HostingStorageRouteContinuityInput {
	f.t.Helper()
	var options domain.HostingStorageDeploymentOptions
	for deadline := time.Now().Add(90 * time.Second); ; {
		options = storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, http.MethodGet, "/api/v1/resources/"+resource.ID+"/storage-deployment-options", nil, 200)
		if options.Available && options.StorageRouteContinuity != nil {
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatal("public storage deployment options unavailable", options.Reason, f.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if options.ResourceID != resource.ID || options.ResourceGeneration != resource.Generation || options.StorageRef == nil || *options.StorageRef != previous.Snapshot.Storage || options.PreviousHealthyOperationID == nil || *options.PreviousHealthyOperationID != previous.ID || options.HostPort == nil || *options.HostPort != previous.Snapshot.HostPort {
		f.t.Fatal("public storage options changed exact predecessor or storage")
	}
	selection := options.StorageRouteContinuity
	op := f.proxyAwait(selection.ExpectedLoadedProxyOperationID)
	proxy := storageRuntimeJSON[domain.HostingProxy](f, http.MethodGet, "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
	if proxy.LoadedGeneration != op.Generation || proxy.LoadedGeneration != selection.ExpectedLoadedGeneration || proxy.DaemonID != previous.Release.DaemonID || proxy.ActiveOperationID != "" || proxy.HoldOperationID != "" || proxy.RestoreOwnershipHeld {
		f.t.Fatal("public loaded generation differs from exact applied operation")
	}
	routes := storageRuntimeJSON[[]domain.HostingRoute](f, http.MethodGet, "/api/v1/resources/"+resource.ID+"/routes", nil, 200)
	var observed []domain.HostingRouteSwitchSelection
	for _, route := range routes {
		if route.Enabled {
			observed = append(observed, domain.HostingRouteSwitchSelection{RouteID: route.ID, ExpectedVersion: route.Version, ExpectedSelectedReleaseOperationID: route.SelectedReleaseOperationID})
		}
	}
	if len(selection.Routes) != 1 || !reflect.DeepEqual(observed, selection.Routes) || selection.Routes[0].ExpectedSelectedReleaseOperationID != previous.ID {
		f.t.Fatal("public options differ from exact current owned route")
	}
	f.t.Logf("public continuity predecessor=%s loaded_operation=%s generation=%d route=%s version=%d", previous.ID, op.ID, proxy.LoadedGeneration, selection.Routes[0].RouteID, selection.Routes[0].ExpectedVersion)
	return selection
}

func (f *storageRuntimeFixture) awaitContinuity(resourceID, operationID string) domain.HostingStorageRouteContinuityStatus {
	f.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		status := storageRuntimeJSON[domain.HostingStorageRouteContinuityStatus](f, http.MethodGet, "/api/v1/resources/"+resourceID+"/storage-route-continuity/"+operationID, nil, 200)
		if status.ResourceID != resourceID || status.OperationID != operationID {
			f.t.Fatal("public continuity status identity changed")
		}
		if status.State == "loaded" {
			if status.SuccessorProxyOperationID == "" || status.LoadedGeneration == nil {
				f.t.Fatal("loaded continuity omitted exact receipt")
			}
			op := f.proxyAwait(status.SuccessorProxyOperationID)
			if op.Generation != *status.LoadedGeneration {
				f.t.Fatal("continuity status generation differs from applied operation")
			}
			return status
		}
		if status.State != "reserved" && status.State != "proxy_pending" {
			f.t.Fatal("storage continuity did not settle", status.State, status.Reason)
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("public storage continuity did not load", f.logs.String())
	return domain.HostingStorageRouteContinuityStatus{}
}

func (f *storageRuntimeFixture) verifyVolume(storage domain.HostingStorage, origin domain.HostingStorageMaterializedRequest, containerID string) string {
	f.t.Helper()
	var volumes []struct {
		Name, Driver, Scope, CreatedAt string
		Labels, Options                map[string]string
	}
	if err := json.Unmarshal(storageRuntimeDocker(f.t, "volume", "inspect", storage.VolumeName), &volumes); err != nil || len(volumes) != 1 {
		f.t.Fatal("volume inspect", err)
	}
	v := volumes[0]
	if v.Name != storage.VolumeName || v.Driver != "local" || v.Scope != "local" || len(v.Options) != 0 || !reflect.DeepEqual(v.Labels, origin.Labels) || v.CreatedAt == "" {
		f.t.Fatal("volume exact evidence changed")
	}
	if strings.TrimSpace(string(storageRuntimeDocker(f.t, "info", "--format", "{{.ID}}"))) != origin.DaemonID {
		f.t.Fatal("volume daemon changed")
	}
	users := strings.Fields(string(storageRuntimeDocker(f.t, "ps", "-q", "--no-trunc", "--filter", "volume="+storage.VolumeName)))
	if len(users) != 1 || users[0] != containerID {
		f.t.Fatal("expected one exact running writer", users, containerID)
	}
	var containers []struct {
		ID     string `json:"Id"`
		Image  string
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if err := json.Unmarshal(storageRuntimeDocker(f.t, "inspect", containerID), &containers); err != nil || len(containers) != 1 || len(containers[0].Mounts) != 1 {
		f.t.Fatal("container mount inspection", err)
	}
	mount := containers[0].Mounts[0]
	if mount.Type != "volume" || mount.Name != storage.VolumeName || mount.Destination != storage.MountPath || !mount.RW {
		f.t.Fatal("writer mount differs")
	}
	f.t.Logf("exact writer=%s image=%s volume=%s created=%s daemon=%s origin=%s origin_plan=%s", containerID, containers[0].Image, v.Name, v.CreatedAt, origin.DaemonID, origin.CreationOperationID, origin.CreationPlanHash)
	return v.CreatedAt
}

func TestHostingStorageCompiledRunnerHTTPS(t *testing.T) {
	testHostingStorageCompiledRunnerHTTPS(t, false)
}

func testHostingStorageCompiledRunnerHTTPS(t *testing.T, archive bool) {
	if os.Getenv("NEROCD_STORAGE_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned storage runtime gate")
	}
	f := newStorageRuntimeFixture(t, archive)
	resource := f.resource("uploaded application A", "a")
	storage := f.storage(resource, strings.Repeat("1", 32))
	materializedLoss := f.arm("/api/v1/runners/hosting/storage-materialized")
	a := f.deploy(resource, "storage-a", "/", storageRuntimeApplicationPort, nil)
	entry := f.lose(materializedLoss, "storage_materialized", a.ID)
	if entry.StorageMaterialized == nil || entry.StorageMetadata == nil || entry.StorageMetadata.Validate() != nil || entry.StorageRef != storage.Reference() {
		t.Fatal("materialized journal lost actual metadata")
	}
	origin := *entry.StorageMaterialized
	var materializedACK domain.HostingStorageMaterializedAck
	if err := json.Unmarshal(materializedLoss.response, &materializedACK); err != nil || materializedACK.Materialization.State != "materialized" {
		t.Fatal("committed materialization ACK", err)
	}
	f.start()
	a = f.await(a.ID, "succeeded")
	if a.Result == nil || !a.Result.Ready {
		t.Fatal("A lacks readiness")
	}
	created := f.verifyVolume(storage, origin, a.Result.ContainerID)
	unrelated := f.resource("unrelated application", "a")
	u := f.await(f.deploy(unrelated, "unrelated-a", "/", storageRuntimeUnrelatedPort, nil).ID, "succeeded")
	f.stop()
	proxy := storageRuntimeJSON[domain.HostingProxy](f, http.MethodPost, "/api/v1/servers/"+f.serverID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "storage-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	certDir := filepath.Join(f.root, "proxy", proxy.ID, "certificates")
	if err := os.MkdirAll(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	publicRoot, err := os.ReadFile(filepath.Join(f.root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "root.pem"), publicRoot, 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.awaitLoadedProxy(proxy.ID, a.Release.DaemonID)
	for i, item := range []struct{ resource, host, operation string }{{resource.ID, "storage-runtime.example.test", a.ID}, {unrelated.ID, "storage-unrelated.example.test", u.ID}} {
		storageRuntimeJSON[domain.HostingHostnameGrant](f, http.MethodPost, "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: item.host}, 201)
		route := storageRuntimeJSON[domain.HostingRoute](f, http.MethodPost, "/api/v1/resources/"+item.resource+"/routes", domain.HostingRouteInput{IdempotencyKey: fmt.Sprintf("storage-route-%d", i), Hostname: item.host, PathPrefix: "/", ExpectedHealthyOperationID: item.operation}, 202)
		f.proxyAwait(route.OperationID)
	}
	if got := f.https("storage-runtime.example.test", "GET", "/", nil, 200); string(got) != "A\n" {
		t.Fatal("HTTPS A version", string(got))
	}
	upload := make([]byte, 65537)
	if _, err := rand.Read(upload); err != nil {
		t.Fatal(err)
	}
	f.https("storage-runtime.example.test", "PUT", "/upload", upload, 201)
	f.https("storage-runtime.example.test", "POST", "/rename", nil, 204)
	verifyBytes := func(version string) {
		t.Helper()
		if got := f.https("storage-runtime.example.test", "GET", "/download", nil, 200); !bytes.Equal(got, upload) {
			t.Fatal("HTTPS uploaded bytes changed")
		}
		if got := f.https("storage-runtime.example.test", "GET", "/", nil, 200); string(got) != version+"\n" {
			t.Fatal("HTTPS version changed", string(got))
		}
		if got := f.https("storage-unrelated.example.test", "GET", "/", nil, 200); string(got) != "A\n" {
			t.Fatal("unrelated HTTPS changed")
		}
	}
	verifyBytes("A")
	resource = storageRuntimeJSON[domain.Resource](f, http.MethodPatch, "/api/v1/resources/"+resource.ID, map[string]any{"expected_generation": resource.Generation, "name": "renamed uploaded application B", "source": map[string]any{"kind": "image", "image": domain.ImageSource{Reference: storageRuntimeImage + "b"}}}, 200)
	if read := storageRuntimeJSON[domain.HostingStorage](f, http.MethodGet, "/api/v1/resources/"+resource.ID+"/storage", nil, 200); !reflect.DeepEqual(read, storage) {
		t.Fatal("rename replaced immutable storage")
	}
	completeLoss := f.arm("/api/v1/runners/hosting/complete")
	b := f.deploy(resource, "storage-b", "/", storageRuntimeApplicationPort, f.continuity(resource, a))
	completed := f.lose(completeLoss, "result", b.ID)
	if completed.Result == nil || completed.Result.Result.ContainerID == a.Result.ContainerID {
		t.Fatal("B journal lacks distinct replacement")
	}
	b = f.await(b.ID, "succeeded")
	if b.Result == nil || b.Result.ContainerID != completed.Result.Result.ContainerID {
		t.Fatal("committed B differs from journal")
	}
	verifyBytes("B")
	completeRequests := f.completions.Load()
	f.start()
	for deadline := time.Now().Add(15 * time.Second); f.completions.Load() == completeRequests; {
		if time.Now().After(deadline) {
			t.Fatal("runner restart did not replay durable Complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A new API poll after restart must preserve the committed exact container.
	bAgain := f.await(b.ID, "succeeded")
	if !reflect.DeepEqual(b.Result, bAgain.Result) || f.verifyVolume(storage, origin, b.Result.ContainerID) != created {
		t.Fatal("restart changed B or volume origin")
	}
	loadedID := f.awaitContinuity(resource.ID, b.ID).SuccessorProxyOperationID
	verifyBytes("B")
	if archive {
		storageRuntimeArchiveRoundtrip(t, f, resource, storage, origin, a, b, upload)
	}
	failed := f.await(f.deploy(resource, "storage-unready", "/unready", storageRuntimeApplicationPort, f.continuity(resource, b)).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed readiness did not restore exact B", failed.Result)
	}
	f.stop()
	f.start()
	verifyBytes("B")
	if f.verifyVolume(storage, origin, b.Result.ContainerID) != created {
		t.Fatal("restoration replaced volume")
	}
	// Lose the first irreversible admission response before any Docker create.
	negative := f.resource("admitted missing volume", "a")
	missing := f.storage(negative, strings.Repeat("2", 32))
	admissionLoss := f.arm("/api/v1/runners/hosting/storage-create-begin")
	blocked := f.deploy(negative, "missing-volume", "/", storageRuntimeMissingPort, nil)
	intent := f.lose(admissionLoss, "storage_create_intent", blocked.ID)
	if intent.StorageCreate == nil {
		t.Fatal("missing durable creation intent")
	}
	var admitted domain.HostingStorageCreateAck
	if err := json.Unmarshal(admissionLoss.response, &admitted); err != nil || !admitted.CreatePermitted || admitted.Materialization.State != "create_admitted" {
		t.Fatal("fresh irreversible admission missing", err)
	}
	assertAbsent := func() {
		t.Helper()
		names := strings.Fields(string(storageRuntimeDocker(t, "volume", "ls", "--format", "{{.Name}}")))
		for _, name := range names {
			if name == missing.VolumeName {
				t.Fatal("admitted missing volume was recreated")
			}
		}
	}
	assertAbsent()
	f.start()
	// The compiled process exits fail closed after replay's read-only inspection.
	select {
	case err := <-f.process.done:
		f.process.cancel()
		f.process = nil
		if err == nil {
			t.Fatal("missing admitted volume unexpectedly succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("missing-volume replay did not fail closed")
	}
	assertAbsent()
	held := storageRuntimeJSON[domain.HostingOperation](f, http.MethodGet, "/api/v1/hosting-operations/"+blocked.ID, nil, 200)
	if !held.MutationHold || held.Result != nil {
		t.Fatal("missing-volume replay lost hold or claimed result")
	}
	journal, err := runner.OpenAttemptJournal(filepath.Join(f.root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Snapshot().Hosting) == 0 {
		t.Fatal("missing-volume journal retired")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	verifyBytes("B")
	if f.verifyVolume(storage, origin, b.Result.ContainerID) != created {
		t.Fatal("negative case changed serving volume")
	}
	t.Logf("HTTPS upload bytes=%d sha256=%x A=%s B=%s unrelated=%s loaded_operation=%s volume=%s origin_operation=%s origin_plan=%s daemon=%s missing_volume=%s absent=true", len(upload), sha256.Sum256(upload), a.Result.ContainerID, b.Result.ContainerID, u.Result.ContainerID, loadedID, storage.VolumeName, origin.CreationOperationID, origin.CreationPlanHash, origin.DaemonID, missing.VolumeName)
	// The outer gate records and removes all guest objects after joining this test.
}
