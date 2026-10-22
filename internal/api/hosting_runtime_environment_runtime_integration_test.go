package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

type runtimeFixtureLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *runtimeFixtureLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *runtimeFixtureLog) contains(value string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.Bytes(), []byte(value))
}

func TestHostingRuntimeEnvironmentCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_ENVIRONMENT_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-environment/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("fixture requires Linux")
	}
	base, pg, database := newHostingBuildAPIFixture(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	ring, err := secretcrypto.New("runtime", map[string][]byte{"runtime": key})
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	_ = base
	controlled := &runtimeFixtureLog{}
	handler := NewServer(service, slog.New(slog.NewTextHandler(controlled, nil)), web.Static())
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := service.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "environment runtime"})
	if err != nil {
		t.Fatal(err)
	}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability}
	enrollment, err := service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_runtime_environment", RunnerName: "runtime", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	credential := "ncr_runtime_fixture_credential"
	hash := sha256.Sum256([]byte(credential))
	enrolled, err := service.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateServer(admin, app.ServerInput{Name: "runtime", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantDestinationProject(admin, target.Destination.ID, app.DestinationProjectInput{ProjectID: env.ProjectID}); err != nil {
		t.Fatal(err)
	}
	for {
		tx, e := database.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		changed, e := hostingjobs.MigrateNextTx(ctx, tx)
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
	}
	d := "sha256:" + strings.Repeat("a", 64)
	policy := domain.HostingBuildPolicy{Identity: d, ToolchainIdentity: d, Protocol: domain.HostingBuildProtocol, Platform: "linux/" + runtime.GOARCH, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 10 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 10 << 20, UnpackedBytes: 10 << 20, UsageBytes: 20 << 20, ReserveBytes: 10 << 20, PollInterval: time.Second, DiskMode: "monitored"}}
	if err = pg.ConfigureHostingBuild(&policy); err != nil {
		t.Fatal(err)
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := stop(c); err != nil {
			t.Error(err)
		}
	})
	var environmentRequests, applyRequests atomic.Int32
	var loseResponse atomic.Bool
	var hookMu sync.Mutex
	var preparedHook func()
	deliveries := map[string]domain.HostingRuntimeEnvironmentRequest{}
	completions := map[string]domain.HostingCompleteRequest{}
	var deliveryMu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request domain.HostingCompleteRequest
			if json.Unmarshal(raw, &request) != nil {
				t.Error("invalid result request")
			}
			deliveryMu.Lock()
			completions[request.OperationID] = request
			deliveryMu.Unlock()
		}
		if r.URL.Path == "/api/v1/runners/hosting/apply-begin" {
			applyRequests.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/runtime-environment" {
			environmentRequests.Add(1)
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request domain.HostingRuntimeEnvironmentRequest
			if json.Unmarshal(raw, &request) != nil {
				t.Error("invalid delivery request")
			}
			deliveryMu.Lock()
			if old, ok := deliveries[request.Authority.OperationID]; ok && old != request {
				t.Error("delivery retry changed exact request")
			}
			deliveries[request.Authority.OperationID] = request
			deliveryMu.Unlock()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			defer clear(rec.Body.Bytes())
			if rec.Code == 200 && loseResponse.CompareAndSwap(true, false) {
				connection, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = connection.Close()
				return
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		if r.URL.Path == "/api/v1/runners/hosting/prepared" {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code == 200 {
				hookMu.Lock()
				hook := preparedHook
				preparedHook = nil
				hookMu.Unlock()
				if hook != nil {
					hook()
				}
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	work := t.TempDir()
	cert := filepath.Join(work, "controller-ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(work, "credential")
	if err = os.WriteFile(credentialFile, []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(work, "journal")
	secretValue := "runtime-managed-exact #=$ 東京  "
	secret, err := service.CreateManagedSecret(admin, app.ManagedSecretInput{ProjectID: env.ProjectID, Name: "runtime secret", Value: secretValue})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"APP_EMPTY": "", "APP_UNICODE": "árvíztűrő 東京", "APP_HASH": "#literal#", "APP_EQUALS": "a=b=c", "APP_DOLLAR": "$HOME ${DOCKER_HOST} $$", "APP_SPACE": " \t leading and trailing \t ", "APP_SECRET": secretValue}
	entries := []domain.HostingRuntimeEntry{}
	for name, value := range values {
		if name == "APP_SECRET" {
			entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: secret.ID, Version: 1}})
		} else {
			v := value
			entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "literal", Value: &v})
		}
	}
	image := os.Getenv("NEROCD_HOSTING_ENVIRONMENT_IMAGE")
	if image == "" {
		t.Fatal("missing owned registry image")
	}
	resource, err := service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "runtime image", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: target.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	save := func(resourceID string, resourceGeneration, runtimeGeneration int64, items []domain.HostingRuntimeEntry) domain.HostingRuntimeConfiguration {
		t.Helper()
		id := make([]byte, 16)
		if _, e := rand.Read(id); e != nil {
			t.Fatal(e)
		}
		out, e := service.PutHostingRuntimeConfiguration(admin, resourceID, domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resourceGeneration, ExpectedRuntimeGeneration: runtimeGeneration, RequestID: hex.EncodeToString(id), Entries: items})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	configuration := save(resource.ID, 1, 0, entries)
	localConfigured := false
	buildConfigFile := ""
	run := func(extra ...string) ([]byte, error) {
		t.Helper()
		c, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		args := []string{"runner", "--server", server.URL, "--credential-file", credentialFile, "--journal-dir", journal, "--work-dir", work, "--once", "--cancel-poll-interval", "100ms", "--capabilities", strings.Join(caps, ",")}
		if localConfigured {
			args = append(args, "--local-build-deploy")
		}
		if buildConfigFile != "" {
			args = append(args, "--build-config-file", buildConfigFile)
		}
		args = append(args, extra...)
		command := exec.CommandContext(c, "/fixture/nerocd", args...)
		command.Env = append(os.Environ(), "SSL_CERT_FILE="+cert, "NEROCD_MODE=development", "NEROCD_TOKEN=")
		output, e := command.CombinedOutput()
		if bytes.Contains(output, []byte(secretValue)) {
			t.Error("managed secret leaked to runner output")
		}
		_, _ = controlled.Write(output)
		return output, e
	}
	request := func(key, path string) domain.HostingOperation {
		t.Helper()
		op, e := service.RequestHostingDeployment(admin, resource.ID, domain.HostingDeploymentInput{ExpectedGeneration: configuration.ResourceGeneration, ExpectedRuntimeConfigurationGeneration: configuration.RuntimeGeneration, IdempotencyKey: key, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: 2}})
		if e != nil {
			t.Fatal(e)
		}
		if e = pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		return op
	}
	operation := request("initial", "/")
	if _, err = run(); err == nil {
		t.Fatal("compiled runner accepted persisted env capability without opt-in")
	}
	loseResponse.Store(true)
	if _, err = run("--hosting-runtime-env"); err != nil {
		t.Fatal("compiled runner failed initial runtime environment deployment")
	}
	operation, err = service.HostingOperation(admin, operation.ID)
	if err != nil || operation.State != "succeeded" || operation.Result == nil {
		t.Fatal("initial deployment did not settle")
	}
	if environmentRequests.Load() != 2 {
		t.Fatal("response loss did not produce exactly one same-ID retry")
	}
	previousID := operation.Result.ContainerID
	docker := func(args ...string) []byte {
		t.Helper()
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		out, e := exec.CommandContext(c, "docker", args...).CombinedOutput()
		if e != nil {
			t.Fatal("owned Docker inspection failed")
		}
		return out
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, e := exec.CommandContext(c, "docker", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+operation.BindingID).Output()
		if e != nil {
			t.Error("owned container listing failed")
			return
		}
		for _, id := range strings.Fields(string(out)) {
			if e := exec.CommandContext(c, "docker", "rm", "--force", id).Run(); e != nil {
				t.Error("owned runtime container cleanup failed")
			}
		}
	})
	assertProof := func(op domain.HostingOperation) {
		t.Helper()
		r, e := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", op.Snapshot.HostPort))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = r.Body.Close() }()
		var proof map[string]string
		if json.NewDecoder(r.Body).Decode(&proof) != nil || len(proof) != len(values) {
			t.Fatal("invalid digest proof")
		}
		for name, value := range values {
			sum := sha256.Sum256([]byte(value))
			if proof[name] != hex.EncodeToString(sum[:]) {
				t.Fatalf("environment digest mismatch for %s", name)
			}
		}
	}
	assertProof(operation)
	before := docker("inspect", "--format", "{{.Id}} {{.Image}} {{.State.Running}}", previousID)
	failed := request("readiness-failure", "/unready")
	if _, err = run("--hosting-runtime-env"); err == nil {
		t.Fatal("readiness failure reported success")
	}
	failed, err = service.HostingOperation(admin, failed.ID)
	if err != nil || failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain || failed.Result.PreviousContainerID != previousID {
		t.Fatal("readiness failure did not preserve exact previous container")
	}
	if !bytes.Equal(before, docker("inspect", "--format", "{{.Id}} {{.Image}} {{.State.Running}}", previousID)) {
		t.Fatal("readiness failure changed previous container")
	}
	assertProof(operation)
	// A config edit after preparation rejects delivery before apply admission.
	rejected := request("changed-configuration", "/")
	baselineApply := applyRequests.Load()
	hookMu.Lock()
	preparedHook = func() {
		configuration = save(resource.ID, configuration.ResourceGeneration, configuration.RuntimeGeneration, entries)
	}
	hookMu.Unlock()
	if _, err = run("--hosting-runtime-env"); err == nil {
		t.Fatal("changed config unexpectedly applied")
	}
	if applyRequests.Load() != baselineApply {
		t.Fatal("changed config reached apply admission")
	}
	assertProof(operation)
	// Restart reconciles durable evidence without fetching again or creating.
	baselineDelivery := environmentRequests.Load()
	if _, err = run("--hosting-runtime-env"); err != nil {
		t.Fatal("evidence-only restart failed")
	}
	if environmentRequests.Load() != baselineDelivery || applyRequests.Load() != baselineApply {
		t.Fatal("startup reacquired environment or reapplied")
	}
	if _, err = service.CancelHostingOperation(admin, rejected.ID); err != nil {
		t.Fatal(err)
	}
	// Retain a separately prepared operation and rotate its selected secret.
	rotated := request("rotated-secret", "/")
	hookMu.Lock()
	preparedHook = func() {
		if _, e := service.RotateManagedSecret(admin, secret.ID, app.ManagedSecretRotateInput{ExpectedVersion: 1, Value: "rotated-fixture-only"}); e != nil {
			t.Error(e)
		}
	}
	hookMu.Unlock()
	if out, e := run("--hosting-runtime-env"); e != nil {
		t.Fatalf("pinned version failed after rotation: %.1000s", bytes.ReplaceAll(out, []byte(secretValue), []byte("[redacted]")))
	}
	rotated, err = service.HostingOperation(admin, rotated.ID)
	if err != nil || rotated.State != "succeeded" || rotated.Result == nil {
		t.Fatal("rotation changed pinned version outcome")
	}
	if applyRequests.Load() != baselineApply+1 {
		t.Fatal("rotation did not apply exactly once")
	}
	assertProof(rotated)
	// A trusted fixture producer records the already-built real image as a Git
	// artifact. This tests deployment of persisted artifacts, not Git/build tooling.
	gitResource, e := service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "runtime Git artifact", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/example/runtime", RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.BindResourceDestination(admin, gitResource.ID, app.ResourceBindingInput{DestinationID: target.Destination.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_build_v1') WHERE id=$1`, enrolled.Runner.ID); e != nil {
		t.Fatal(e)
	}
	requested, e := service.RequestHostingBuild(admin, gitResource.ID, domain.HostingBuildRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: "existing-image-artifact"})
	if e != nil {
		t.Fatal(e)
	}
	build, e := pg.GetHostingBuild(ctx, "usr_bootstrap", requested.ID)
	if e != nil {
		t.Fatal(e)
	}
	for until := time.Now().Add(10 * time.Second); build.RunID == ""; {
		if time.Now().After(until) {
			t.Fatal("artifact producer dispatch missing")
		}
		time.Sleep(20 * time.Millisecond)
		build, e = pg.GetHostingBuild(ctx, "usr_bootstrap", requested.ID)
		if e != nil {
			t.Fatal(e)
		}
	}
	claim, e := pg.ClaimRun(ctx, enrolled.Runner.ID, time.Now(), time.Minute)
	if e != nil || claim.Run.ID != build.RunID {
		t.Fatal("artifact producer claim mismatch")
	}
	authority := domain.HostingBuildAuthority{BuildID: build.ID, RunID: build.RunID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	build, e = pg.BeginHostingBuildSource(ctx, enrolled.Runner.ID, authority, build.Revision)
	if e != nil {
		t.Fatal(e)
	}
	receipt := domain.HostingBuildSourceReceipt{BuildID: build.ID, SourceID: build.Snapshot.Source.ID, InputID: build.Snapshot.InputID, Commit: strings.Repeat("b", 40), PolicyHash: domain.HostingDocumentHash(build.Snapshot.Source.Policy), TreeDigest: d}
	build, e = pg.CommitHostingBuildSource(ctx, enrolled.Runner.ID, authority, "runtime-source", receipt)
	if e != nil {
		t.Fatal(e)
	}
	daemon := strings.TrimSpace(string(docker("info", "--format", "{{.ID}}")))
	build, e = pg.PrepareHostingBuild(ctx, enrolled.Runner.ID, authority, build.Revision, daemon)
	if e != nil {
		t.Fatal(e)
	}
	imageID := strings.TrimSpace(string(docker("image", "inspect", image, "--format", "{{.Id}}")))
	artifact := domain.HostingBuildArtifact{Origin: "local_build", BuildID: build.ID, SourceID: build.Snapshot.Source.ID, InputID: build.Snapshot.InputID, SourceReceiptHash: domain.HostingDocumentHash(build.SourceReceipt), ContextDigest: d, RequestDigest: d, DaemonID: daemon, ImageID: imageID, IndexDigest: imageID, ManifestDigest: imageID, ConfigDigest: imageID, Platform: "linux/" + runtime.GOARCH}
	build, e = pg.CompleteHostingBuild(ctx, enrolled.Runner.ID, authority, "runtime-artifact", build.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'local_build_deploy_v1') WHERE id=$1`, enrolled.Runner.ID); e != nil {
		t.Fatal(e)
	}
	caps = append(caps, domain.RunTypeLocalBuildDeploy)
	localConfigured = true
	caps = append(caps, domain.HostingBuildTaskType)
	sourceRoot := filepath.Join(work, "sources")
	if e = os.Mkdir(sourceRoot, 0700); e != nil {
		t.Fatal(e)
	}
	engineConfig := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: imageID, StateDir: filepath.Join(work, "build-state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}
	localPolicy := policy
	localPolicy.ToolchainIdentity, e = runnerruntime.ComputeBuildToolchainIdentity(ctx, engineConfig, localPolicy.Platform)
	if e != nil {
		t.Fatal(e)
	}
	buildDocument := map[string]any{"version": 1, "source_root": sourceRoot, "engine": map[string]string{"docker_binary": engineConfig.DockerBinary, "socket": engineConfig.Socket, "buildx_binary": engineConfig.BuildxBinary, "buildx_sha256": engineConfig.BuildxSHA256, "planner_image": engineConfig.PlannerImage, "state_dir": engineConfig.StateDir, "cgroup_root": engineConfig.CgroupRoot}, "target": map[string]string{"runner_id": enrolled.Runner.ID, "server_id": target.Server.ID, "destination_id": target.Destination.ID}, "policy": localPolicy, "status_interval_ns": int64(200 * time.Millisecond), "request_timeout_ns": int64(5 * time.Second)}
	encoded, e := json.Marshal(buildDocument)
	if e != nil {
		t.Fatal(e)
	}
	buildConfigFile = filepath.Join(work, "build.json")
	if e = os.WriteFile(buildConfigFile, encoded, 0600); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("NEROCD_HOSTING_ENVIRONMENT_BROWSER") == "1" {
		runHostingRuntimeEnvironmentBrowser(ctx, t, runtimeEnvironmentBrowserFixture{service: service, store: pg, database: database, environment: env, gitResource: gitResource, buildID: build.ID, destination: target.Destination.ID, image: image, imageID: imageID, secretID: secret.ID, secretValue: secretValue, controlled: controlled, run: run})
		return
	}
	localConfiguration := save(gitResource.ID, 1, 0, entries)
	localOp, e := service.RequestHostingBuildDeployment(admin, gitResource.ID, domain.HostingBuildDeploymentInput{BuildID: build.ID, ExpectedGeneration: localConfiguration.ResourceGeneration, ExpectedRuntimeConfigurationGeneration: localConfiguration.RuntimeGeneration, ExpectedSourceGeneration: 1, IdempotencyKey: "runtime-local-artifact", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 2}})
	if e != nil {
		t.Fatal(e)
	}
	if e = pg.PrepareDeployment(ctx, localOp.ID); e != nil {
		t.Fatal(e)
	}
	if out, e := run("--hosting-runtime-env"); e != nil {
		t.Fatalf("compiled local runtime deployment failed: %.1000s", bytes.ReplaceAll(out, []byte(secretValue), []byte("[redacted]")))
	}
	localOp, e = service.HostingOperation(admin, localOp.ID)
	if e != nil || localOp.State != "succeeded" || localOp.Result == nil || localOp.Result.ImageID != imageID || localOp.Snapshot.LocalBuild.Artifact != artifact {
		t.Fatal("local runtime artifact identity changed")
	}
	assertProof(localOp)
	if current := strings.TrimSpace(string(docker("image", "inspect", image, "--format", "{{.Id}}"))); current != imageID {
		t.Fatal("runtime configuration rebuilt image")
	}
	localContainer := localOp.Result.ContainerID
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := exec.CommandContext(c, "docker", "rm", "--force", localContainer).Run(); e != nil {
			t.Error("owned local artifact container cleanup failed")
		}
	})
	t.Log("registry and persisted Git artifact used the same real Docker image ID without rebuild; trusted fixture producer supplies build provenance")

	// Cancellation and lease expiry after preparation cannot reach admission.
	for _, mode := range []string{"cancelled", "expired"} {
		rejectedOperation := request(mode, "/")
		beforeApply := applyRequests.Load()
		hookMu.Lock()
		preparedHook = func() {
			if mode == "cancelled" {
				if _, e := service.CancelHostingOperation(admin, rejectedOperation.ID); e != nil {
					t.Error(e)
				}
			} else {
				if _, e := database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`, "run_"+rejectedOperation.ID); e != nil {
					t.Error(e)
				}
			}
		}
		hookMu.Unlock()
		if _, e := run("--hosting-runtime-env"); e == nil {
			t.Fatal("terminated authority unexpectedly succeeded")
		}
		if applyRequests.Load() != beforeApply {
			t.Fatal("terminated authority reached admission")
		}
		assertProof(rotated)
		if mode == "expired" {
			if _, e := service.CancelHostingOperation(admin, rejectedOperation.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	revokedOperation := request("revoked-secret", "/")
	beforeRevocation := applyRequests.Load()
	hookMu.Lock()
	preparedHook = func() {
		if _, e := service.RevokeManagedSecret(admin, secret.ID, app.ManagedSecretRevokeInput{ExpectedVersion: 2}); e != nil {
			t.Error(e)
		}
	}
	hookMu.Unlock()
	if _, e := run("--hosting-runtime-env"); e == nil {
		t.Fatal("revoked secret unexpectedly applied")
	}
	if applyRequests.Load() != beforeRevocation {
		t.Fatal("revoked secret reached admission")
	}
	assertProof(rotated)
	assertProof(localOp)
	if _, e := service.CancelHostingOperation(admin, revokedOperation.ID); e != nil {
		t.Fatal(e)
	}
	deliveryMu.Lock()
	completion := completions[localOp.ID]
	deliveryMu.Unlock()
	raw, e := json.Marshal(completion)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/runners/hosting/complete", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	replay.Header.Set("Authorization", "Bearer "+credential)
	replay.Header.Set("Content-Type", "application/json")
	response, e := server.Client().Do(replay)
	if e != nil {
		t.Fatal(e)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("committed result receipt rejected after revocation")
	}
	// Controlled diagnostics contain no selected secret value.
	if controlled.contains(secretValue) {
		t.Fatal("controlled logs contain managed secret")
	}
	if err = filepath.WalkDir(journal, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if bytes.Contains(raw, []byte(secretValue)) {
				return fmt.Errorf("journal secret exposure")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(docker("inspect", "--format", "{{.State.Pid}}", previousID)))
	processes, processErr := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
	if processErr != nil {
		t.Fatal("owned workload process argv unavailable")
	}
	if bytes.Contains(processes, []byte(secretValue)) {
		t.Fatal("workload argv contains secret")
	}
	if bytes.Contains(docker("logs", previousID), []byte(secretValue)) {
		t.Fatal("controlled workload logs contain secret")
	}
	var audit string
	if err = database.QueryRow(ctx, `SELECT coalesce(string_agg(metadata::text,''),'') FROM audit_events`).Scan(&audit); err != nil || strings.Contains(audit, secretValue) {
		t.Fatal("audit contains selected secret")
	}
	t.Log("compiled Linux runner: TLS protected delivery, same-ID response-loss retry, literal/managed digest proof, readiness failure preserved previous container/environment, config changes, revocation and terminated authority rejected before admission; rotation retained pinned v1, restart evidence-only; controlled output/journal/audit/argv scans passed")
}

type runtimeEnvironmentBrowserFixture struct {
	service     *app.Service
	store       *store.PostgresStore
	database    *pgxpool.Pool
	environment domain.ProjectEnvironment
	gitResource domain.Resource
	buildID     string
	destination string
	image       string
	imageID     string
	secretID    string
	secretValue string
	controlled  *runtimeFixtureLog
	run         func(...string) ([]byte, error)
}

// This extends the existing compiled-runner gate. Infrastructure, accounts and
// trusted persisted artifact provenance are setup; runtime saves and deployment
// admissions must traverse the actual browser session and API.
func runHostingRuntimeEnvironmentBrowser(ctx context.Context, t *testing.T, fixture runtimeEnvironmentBrowserFixture) {
	t.Helper()
	origin := os.Getenv("NEROCD_HOSTING_ENVIRONMENT_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	port, portErr := strconv.Atoi(parsedPort(parsed))
	if err != nil || portErr != nil || port < 1 || port > 65535 || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || origin != "http://"+parsed.Host {
		t.Fatal("browser fixture requires an exact loopback HTTP origin")
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	owner, err := fixture.service.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "runtime-owner@example.local", Name: "Runtime Owner", Password: "runtime-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: fixture.environment.ProjectID, Email: owner.Email, Role: "owner"}, {ProjectID: fixture.environment.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		if _, err = fixture.service.UpsertProjectMember(admin, member); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise persisted owner membership, not a system-admin UI bypass.
	if _, err = fixture.database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	imageResource, err := fixture.service.CreateResource(admin, app.ResourceInput{ProjectID: fixture.environment.ProjectID, ProjectEnvironmentID: fixture.environment.ID, Name: "Runtime browser image", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: fixture.image}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.service.BindResourceDestination(admin, imageResource.ID, app.ResourceBindingInput{DestinationID: fixture.destination}); err != nil {
		t.Fatal(err)
	}
	browserAPI := NewServerWithConfig(fixture.service, slog.New(slog.NewTextHandler(fixture.controlled, nil)), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	browserCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/runtime-environment/" + hex.EncodeToString(nonce)
	done, workerDone := make(chan struct{}), make(chan struct{})
	results := make(chan error, 1)
	queue := make(chan string)
	var finish sync.Once
	var seen sync.Map
	var mu sync.Mutex
	operations := map[string]domain.HostingOperation{}
	saveBodies := map[string][32]byte{}
	var successfulSaves, saveReplays, deploymentReplays, conflicts, reveals int
	proof := func(requestCtx context.Context, operation domain.HostingOperation) (map[string]string, error) {
		if operation.Result == nil || operation.Result.ContainerID == "" {
			return nil, fmt.Errorf("missing runtime container result")
		}
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", operation.Snapshot.HostPort), nil)
		if err != nil {
			return nil, err
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			return nil, fmt.Errorf("runtime digest endpoint unavailable")
		}
		defer func() { _ = response.Body.Close() }()
		var digest map[string]string
		if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&digest) != nil || len(digest) != 2 {
			return nil, fmt.Errorf("invalid runtime digest proof")
		}
		for name, value := range map[string]string{"APP_VISIBLE": "browser literal #=$ 東京  ", "APP_SECRET": fixture.secretValue} {
			sum := sha256.Sum256([]byte(value))
			if digest[name] != hex.EncodeToString(sum[:]) {
				return nil, fmt.Errorf("runtime digest mismatch for %s", name)
			}
		}
		return digest, nil
	}
	ownerOnly := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := fixture.service.CurrentPrincipal(r.Context())
		if err != nil || principal.ID != owner.ID || (r.Method != http.MethodGet && r.Method != http.MethodPost) || r.Header.Get("Origin") != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/complete" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, prefix+"/proof/"); ok && r.Method == http.MethodGet {
			mu.Lock()
			operation, exists := operations[id]
			mu.Unlock()
			if !exists || operation.State != "succeeded" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			digests, err := proof(r.Context(), operation)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"digests": digests, "verified": true, "container_id": operation.Result.ContainerID, "image_id": operation.Result.ImageID})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			ownerOnly.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/reveal") {
			mu.Lock()
			reveals++
			mu.Unlock()
		}
		isSave := r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/runtime-configuration")
		isDeployment := r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/deployments") || strings.HasSuffix(r.URL.Path, "/deployments/from-build"))
		if !isSave && !isDeployment {
			browserAPI.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || bytes.Contains(raw, []byte(fixture.secretValue)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		recorded := httptest.NewRecorder()
		browserAPI.ServeHTTP(recorded, r)
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
		if isSave && recorded.Code == http.StatusOK {
			var input domain.HostingRuntimeConfigurationInput
			if json.Unmarshal(raw, &input) != nil {
				return
			}
			mu.Lock()
			successfulSaves++
			if old, exists := saveBodies[input.RequestID]; exists {
				if old != sha256.Sum256(raw) {
					t.Error("browser save retry changed exact body")
				}
				saveReplays++
			}
			saveBodies[input.RequestID] = sha256.Sum256(raw)
			mu.Unlock()
		}
		if isDeployment && recorded.Code == http.StatusConflict {
			mu.Lock()
			conflicts++
			mu.Unlock()
		}
		if !isDeployment || recorded.Code != http.StatusAccepted {
			return
		}
		var operation domain.HostingOperation
		if json.Unmarshal(recorded.Body.Bytes(), &operation) != nil {
			return
		}
		if _, duplicate := seen.LoadOrStore(operation.ID, true); duplicate {
			mu.Lock()
			deploymentReplays++
			mu.Unlock()
			return
		}
		select {
		case queue <- operation.ID:
		case <-browserCtx.Done():
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	go func() {
		defer close(workerDone)
		var previous *domain.HostingOperation
		for {
			select {
			case <-browserCtx.Done():
				return
			case id := <-queue:
				err := fixture.store.PrepareDeployment(browserCtx, id)
				if err == nil {
					_, err = fixture.run("--hosting-runtime-env")
				}
				operation, readErr := fixture.store.GetHostingOperation(browserCtx, id)
				if readErr == nil && operation.State == "failed" && operation.Result != nil && operation.Result.Restored && previous != nil && operation.Result.PreviousContainerID == previous.Result.ContainerID {
					_, err = proof(browserCtx, *previous)
				} else if err == nil && readErr == nil && operation.State == "succeeded" && operation.Result != nil {
					_, err = proof(browserCtx, operation)
					if operation.Result.ImageID != fixture.imageID || (operation.ResourceID == fixture.gitResource.ID && (operation.Snapshot.LocalBuild.Artifact.BuildID != fixture.buildID)) {
						err = fmt.Errorf("browser deployment changed persisted image or artifact identity")
					}
					if operation.ResourceID == imageResource.ID {
						previous = &operation
					}
				} else if err == nil {
					err = fmt.Errorf("browser deployment failed to settle")
				}
				if err != nil || readErr != nil {
					select {
					case results <- fmt.Errorf("browser compiled runner or digest verification failed"):
					case <-browserCtx.Done():
					}
					return
				}
				mu.Lock()
				operations[id] = operation
				mu.Unlock()
			}
		}
	}()
	// Cleanup exact binding-owned containers, including failure paths. The outer
	// gate also removes this fixture's entire identity-verified disposable daemon.
	t.Cleanup(func() {
		cancel()
		<-workerDone
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, resource := range []domain.Resource{imageResource, fixture.gitResource} {
			binding, err := fixture.service.GetResourceBinding(auth.WithPrincipal(cleanup, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}}), resource.ID)
			if err != nil {
				t.Error("browser binding cleanup lookup failed")
				continue
			}
			out, err := exec.CommandContext(cleanup, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=io.nerocd.binding="+binding.ID).Output()
			if err != nil {
				t.Error("browser container cleanup listing failed")
				continue
			}
			for _, id := range strings.Fields(string(out)) {
				if exec.CommandContext(cleanup, "docker", "rm", "-f", id).Run() != nil {
					t.Error("browser container cleanup failed")
				}
			}
		}
	})
	metadata, err := json.Marshal(map[string]any{"origin": origin, "project_id": fixture.environment.ProjectID, "environment": fixture.environment.Name, "image_resource_id": imageResource.ID, "git_resource_id": fixture.gitResource.ID, "build_id": fixture.buildID, "secret_id": fixture.secretID, "secret_version": 1, "complete_path": prefix + "/complete", "proof_path": prefix + "/proof/"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RUNTIME_ENVIRONMENT_BROWSER_READY %s\n", metadata)
	select {
	case <-browserCtx.Done():
		t.Fatal("runtime browser fixture timed out")
	case <-results:
		t.Fatal("runtime browser fixture runner failed")
	case <-done:
	}
	mu.Lock()
	defer mu.Unlock()
	var imageSucceeded, localSucceeded, restored int
	for _, operation := range operations {
		if operation.State == "succeeded" {
			if operation.ResourceID == fixture.gitResource.ID {
				localSucceeded++
			} else {
				imageSucceeded++
			}
		} else if operation.Result != nil && operation.Result.Restored {
			restored++
		}
	}
	if imageSucceeded != 1 || localSucceeded != 1 || restored != 1 || successfulSaves < 4 || saveReplays != 1 || deploymentReplays != 1 || conflicts != 1 || reveals != 0 || fixture.controlled.contains(fixture.secretValue) {
		t.Fatal("browser journey missing exact save/deploy replay, digest, continuity, denial, or no-reveal evidence")
	}
	t.Log("real browser/API/compiled runner/Docker: literal and exact secret reference saves; same-receipt save/deployment retries; image and trusted persisted Git artifact digest proofs without rebuild; stale generation denied; readiness failure kept prior environment; viewer no write or reveal")
}

func parsedPort(value *url.URL) string {
	if value == nil {
		return ""
	}
	return value.Port()
}
