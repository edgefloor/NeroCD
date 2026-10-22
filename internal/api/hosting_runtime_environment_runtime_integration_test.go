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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
