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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
	"nerocd/internal/secretcrypto"
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
type databaseRunnerFixture struct {
	t                                     *testing.T
	server                                *httptest.Server
	client                                *http.Client
	root, token, destination, environment string
	process                               *pushRunnerProcess
	logs                                  *databaseRunnerLog
	fault                                 atomic.Pointer[databaseRunnerFault]
	claimRequests                         atomic.Int64
	completeMu                            sync.Mutex
	completeCounts                        map[string]int
	applyRequests                         atomic.Int64
	deliveryRequests                      atomic.Int64
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
	_, pg, database := newHostingBuildAPIFixture(t)
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("fixture key generation failed")
	}
	ring, err := secretcrypto.New("database-runner-fixture", map[string][]byte{"database-runner-fixture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("fixture keyring failed")
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
	f := &databaseRunnerFixture{t: t, root: root, logs: &databaseRunnerLog{}, completeCounts: map[string]int{}}
	handler := NewServer(service, slog.New(slog.NewTextHandler(f.logs, nil)), web.Static())
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/claim" {
			f.claimRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/apply-begin" {
			f.applyRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/database-credentials" {
			f.deliveryRequests.Add(1)
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
	caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	enrollment := databaseRunnerJSON[app.CreatedRunnerEnrollment](f, f.token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_database_acceptance", RunnerName: "private database acceptance", Capabilities: caps}, 201)
	credential := "ncr_database_runner_fixture_only"
	enrolled := databaseRunnerJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", databaseRunnerEnrollmentConsumeInput(credential), 200)
	if err = os.WriteFile(filepath.Join(root, "credential"), []byte(credential), 0600); err != nil {
		t.Fatal("runner credential file failed")
	}
	f.protected = append(f.protected, []byte(credential), []byte(f.token), []byte(enrollment.Token))
	target := databaseRunnerJSON[app.ServerDetail](f, f.token, "POST", "/api/v1/servers", app.ServerInput{Name: "database runner acceptance", RunnerID: enrolled.Runner.ID}, 201)
	f.destination = target.Destination.ID
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
	if f.process != nil {
		f.t.Fatal("runner already active")
	}
	ctx, cancel := context.WithCancel(f.t.Context())
	cmd := exec.CommandContext(ctx, "/fixture/nerocd", "runner", "--server", f.server.URL, "--credential-file", filepath.Join(f.root, "credential"), "--hosting-database", "--execute", "--database-state-root", filepath.Join(f.root, "state"), "--database-secret-root", filepath.Join(f.root, "secrets"), "--journal-dir", filepath.Join(f.root, "journal"), "--work-dir", filepath.Join(f.root, "work"), "--image-docker-endpoint", "unix:///var/run/docker.sock", "--poll-interval", "100ms", "--cancel-poll-interval", "100ms")
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
	p := &pushRunnerProcess{command: cmd, cancel: cancel, done: make(chan error, 1)}
	f.process = p
	go func() { p.done <- cmd.Wait() }()
	f.t.Logf("compiled database runner started pid=%d", cmd.Process.Pid)
}

func (f *databaseRunnerFixture) stop() {
	f.t.Helper()
	p := f.process
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
		f.process = nil
		f.t.Logf("compiled database runner joined pid=%d", p.command.Process.Pid)
	case <-time.After(15 * time.Second):
		f.t.Fatal("compiled database runner did not join")
	}
}

func (f *databaseRunnerFixture) deploy(view domain.HostingDatabase, key string) domain.HostingOperation {
	f.t.Helper()
	return databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+view.Resource.ID+"/database/deployments", domain.HostingDatabaseDeploymentInput{ExpectedGeneration: view.Resource.Generation, IdempotencyKey: key}, 202)
}

func (f *databaseRunnerFixture) await(id string) domain.HostingOperation {
	f.t.Helper()
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); {
		op := databaseRunnerJSON[domain.HostingOperation](f, f.token, "GET", "/api/v1/hosting-operations/"+id, nil, 200)
		if op.State == "succeeded" {
			if op.Result == nil || !op.Result.Ready || op.Result.Uncertain || op.MutationHold || op.RestoreOwnershipHeld || op.CompleteReceipt == nil || op.Plan == nil || domain.ValidateHostingDatabasePlan(*op.Plan) != nil {
				f.t.Fatal("database completion lacks exact healthy evidence")
			}
			return op
		}
		if op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
			f.t.Fatalf("database operation settled unsupported state=%s", op.State)
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("database wait canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
	f.t.Fatal("database operation did not settle")
	return domain.HostingOperation{}
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
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return false
	}
	_, attached := got.NetworkSettings.Networks[network]
	return got.ID == id && got.Name == "/"+name && got.Image == image && len(got.Config.Labels) == 1 && got.Config.Labels[databaseRunnerLabel] == databaseRunnerOwner && len(got.Config.Env) == 0 && len(got.Config.Cmd) == 0 && reflect.DeepEqual(got.Config.Entrypoint, []string{"/client"}) && len(got.Mounts) == 0 && got.HostConfig.NetworkMode == network && !got.HostConfig.Privileged && len(got.HostConfig.PortBindings) == 0 && len(got.NetworkSettings.Networks) == 1 && attached
}

func TestDatabaseRunnerClientIdentityBeforeSecretStdin(t *testing.T) {
	id := strings.Repeat("a", 64)
	name := databaseRunnerOwner + "-client"
	image := "sha256:" + strings.Repeat("b", 64)
	network := "ncdn_fixture"
	valid := databaseRunnerContainer{ID: id, Name: "/" + name, Image: image}
	valid.Config.Labels = map[string]string{databaseRunnerLabel: databaseRunnerOwner}
	valid.Config.Entrypoint = []string{"/client"}
	valid.HostConfig.NetworkMode = network
	valid.NetworkSettings.Networks = map[string]struct{ NetworkID, IPAddress string }{network: {}}
	if !databaseRunnerClientMatches(valid, id, name, image, network) {
		t.Fatal("owned private client refused")
	}
	for _, change := range []string{"identity", "name", "image", "owner", "network", "privilege", "environment", "command", "entrypoint", "mount", "port"} {
		t.Run(change, func(t *testing.T) {
			raw, e := json.Marshal(valid)
			if e != nil {
				t.Fatal(e)
			}
			var got databaseRunnerContainer
			if json.Unmarshal(raw, &got) != nil {
				t.Fatal("client identity fixture")
			}
			switch change {
			case "identity":
				got.ID = strings.Repeat("c", 64)
			case "name":
				got.Name = "/foreign"
			case "image":
				got.Image = "foreign"
			case "owner":
				got.Config.Labels[databaseRunnerLabel] = "foreign"
			case "network":
				got.HostConfig.NetworkMode = "host"
			case "privilege":
				got.HostConfig.Privileged = true
			case "environment":
				got.Config.Env = []string{"UNEXPECTED=1"}
			case "command":
				got.Config.Cmd = []string{"unexpected"}
			case "entrypoint":
				got.Config.Entrypoint = []string{"/foreign"}
			case "mount":
				got.Mounts = append(got.Mounts, struct {
					Type, Name, Source, Destination string
					RW                              bool
				}{Type: "bind"})
			case "port":
				got.HostConfig.PortBindings = map[string]any{"80/tcp": true}
			}
			if databaseRunnerClientMatches(got, id, name, image, network) {
				t.Fatal("changed client could receive credential stdin")
			}
		})
	}
}

func (f *databaseRunnerFixture) marker(view domain.HostingDatabase, op domain.HostingOperation, marker string, write bool) {
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
	raw, err := json.Marshal(map[string]any{"engine": input.Engine, "host": input.Host, "username": input.Username, "password": input.Password, "marker": input.Marker, "write": input.Write})
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
	verify := func(ctx context.Context, identity string) error {
		data, e := databaseRunnerDocker(ctx, nil, "container", "inspect", identity, "--format", "{{json .}}")
		if e != nil {
			return e
		}
		var got databaseRunnerContainer
		if json.Unmarshal(data, &got) != nil || !databaseRunnerClientMatches(got, identity, name, imageProof.ID, op.Plan.Database.NetworkName) {
			return errors.New("client ownership changed")
		}
		return nil
	}
	cleanup := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		data, e := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
		if e != nil {
			return e
		}
		ids := strings.Fields(string(data))
		if len(ids) == 0 {
			return nil
		}
		if len(ids) != 1 || id != "" && ids[0] != id {
			return errors.New("client cleanup identity changed")
		}
		if e = verify(ctx, ids[0]); e != nil {
			return e
		}
		if _, e = databaseRunnerDocker(ctx, nil, "container", "rm", "--force", ids[0]); e != nil {
			return e
		}
		data, e = databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
		if e != nil || strings.TrimSpace(string(data)) != "" {
			return errors.New("client remains attached")
		}
		return nil
	}
	defer func() {
		if cleanup() != nil {
			f.t.Fatal("verified client cleanup failed; no further database operation permitted")
		}
	}()
	created, e := databaseRunnerDocker(f.t.Context(), nil, "container", "create", "--interactive", "--name", name, "--label", databaseRunnerLabel+"="+databaseRunnerOwner, "--network", op.Plan.Database.NetworkName, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", imageProof.ID)
	if e != nil {
		f.t.Fatal("private client creation failed")
	}
	id = strings.TrimSpace(string(created))
	if verify(f.t.Context(), id) != nil {
		f.t.Fatal("client identity refused before credential stdin")
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
