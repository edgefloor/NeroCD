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
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/web"
)

type observationFixtureAgent struct {
	command *exec.Cmd
	done    chan error
	stop    sync.Once
}

func (a *observationFixtureAgent) Stop(t *testing.T) {
	t.Helper()
	a.stop.Do(func() {
		if a.command.Process != nil {
			_ = a.command.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-a.done:
		case <-time.After(3 * time.Second):
			_ = a.command.Process.Kill()
			<-a.done
			t.Error("observer did not join after SIGTERM")
		}
	})
}

func TestHostingObservationCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_OBSERVATION_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-observation/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("requires Linux fixture")
	}
	server, pg, database := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "observation runtime"})
	if err != nil {
		t.Fatal(err)
	}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_observation_runtime", RunnerName: "observer", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	token := "ncr_observation_fixture_credential"
	sum := sha256.Sum256([]byte(token))
	enrolled, err := server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.app.CreateServer(admin, app.ServerInput{Name: "observation runtime", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.app.GrantDestinationProject(admin, target.Destination.ID, app.DestinationProjectInput{ProjectID: env.ProjectID}); err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "observation workload", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: os.Getenv("NEROCD_OBSERVATION_IMAGE")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: target.Destination.ID}); err != nil {
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
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(c); e != nil {
			t.Error(e)
		}
	})
	control := httptest.NewTLSServer(server)
	t.Cleanup(control.Close)
	work := t.TempDir()
	certificate := filepath.Join(work, "controller-ca.pem")
	if err = os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(work, "credential")
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	logs := &runtimeFixtureLog{}
	command := func(observe bool) *exec.Cmd {
		args := []string{"runner", "--server", control.URL, "--credential-file", credential, "--work-dir", work, "--journal-dir", filepath.Join(work, "journal"), "--poll-interval", "200ms"}
		capabilities := append([]string(nil), caps...)
		if observe {
			args = append(args, "--hosting-observation")
			capabilities = append(capabilities, domain.HostingObservationCapability)
		} else {
			args = append(args, "--once")
		}
		args = append(args, "--capabilities", strings.Join(capabilities, ","))
		c := exec.CommandContext(ctx, "/fixture/nerocd", args...)
		c.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+certificate)
		if observe {
			c.Env = append(c.Env, "PATH=/fixture/observer-bin:/usr/local/bin:/usr/bin:/bin")
		}
		c.Stdout, c.Stderr = logs, logs
		return c
	}
	deploy := func(key string) domain.HostingOperation {
		t.Helper()
		op, e := server.app.RequestHostingDeployment(admin, resource.ID, domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: key, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 3}})
		if e != nil {
			t.Fatal(e)
		}
		if e = pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		if e = command(false).Run(); e != nil {
			t.Fatal("compiled deployment fixture failed")
		}
		op, e = server.app.HostingOperation(admin, op.ID)
		if e != nil || op.State != "succeeded" || op.Result == nil {
			t.Fatal("deployment fixture did not settle")
		}
		return op
	}
	initial := deploy("initial-observation-workload")
	docker := func(args ...string) []byte {
		t.Helper()
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, e := exec.CommandContext(c, "/usr/local/bin/docker", args...).CombinedOutput()
		if e != nil {
			t.Fatal("owned fixture Docker operation failed")
		}
		return out
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, e := exec.CommandContext(c, "/usr/local/bin/docker", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+initial.BindingID).Output()
		if e != nil {
			t.Error(e)
			return
		}
		for _, id := range strings.Fields(string(out)) {
			label, e := exec.CommandContext(c, "/usr/local/bin/docker", "inspect", "--format", `{{index .Config.Labels "io.nerocd.binding"}}`, id).Output()
			if e != nil || strings.TrimSpace(string(label)) != initial.BindingID {
				t.Error("owned workload identity changed")
				continue
			}
			if e = exec.CommandContext(c, "/usr/local/bin/docker", "rm", "--force", id).Run(); e != nil {
				t.Error(e)
			}
		}
	})
	setCapability := func(enabled bool) {
		t.Helper()
		sql := `UPDATE runners SET capabilities=array_append(capabilities,'hosting_observation_v1') WHERE id=$1`
		if !enabled {
			sql = `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_observation_v1') WHERE id=$1`
		}
		if _, e := database.Exec(ctx, sql, enrolled.Runner.ID); e != nil {
			t.Fatal(e)
		}
	}
	setCapability(true)
	start := func() *observationFixtureAgent {
		t.Helper()
		a := &observationFixtureAgent{command: command(true), done: make(chan error, 1)}
		if e := a.command.Start(); e != nil {
			t.Fatal(e)
		}
		go func() { a.done <- a.command.Wait() }()
		t.Cleanup(func() { a.Stop(t) })
		return a
	}
	waitStatus := func(name string, predicate func(domain.HostingRuntimeStatus) bool) domain.HostingRuntimeStatus {
		t.Helper()
		until := time.Now().Add(16 * time.Second)
		for {
			status, e := server.app.HostingRuntimeStatus(admin, resource.ID)
			if e != nil {
				t.Fatal(e)
			}
			if predicate(status) {
				return status
			}
			if time.Now().After(until) {
				t.Fatalf("%s status timeout: freshness=%s state=%s readiness=%s error=%s", name, status.Freshness, status.ContainerState, status.Readiness, status.CollectionError)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	agent := start()
	healthy := waitStatus("healthy", func(s domain.HostingRuntimeStatus) bool {
		return s.Freshness == "fresh" && s.Readiness == "ready" && s.ContainerState == "running"
	})
	if healthy.ObservedContainerID != initial.Result.ContainerID || healthy.MutationHold || healthy.RestoreOwnershipHeld {
		t.Fatal("collector changed authority or current identity")
	}
	id := initial.Result.ContainerID
	if got := strings.TrimSpace(string(docker("inspect", "--format", `{{.Id}} {{index .Config.Labels "io.nerocd.binding"}}`, id))); got != id+" "+initial.BindingID {
		t.Fatal("stop target identity unavailable")
	}
	if os.Getenv("NEROCD_OBSERVATION_BROWSER") == "1" {
		browserAgent := agent
		browserCurrent := initial
		runHostingObservationBrowser(ctx, t, server, database, env, resource, initial, func(stage string) map[string]string {
			switch stage {
			case "stop":
				docker("stop", "--time", "1", id)
				waitStatus("browser verified exit", func(s domain.HostingRuntimeStatus) bool {
					return s.Freshness == "fresh" && s.ContainerState == "exited" && s.ExitCode != nil
				})
			case "disconnect":
				browserAgent.Stop(t)
			case "confirm_stale":
				stale := waitStatus("browser stale", func(s domain.HostingRuntimeStatus) bool {
					return s.Freshness == "stale" && s.AgentReachability == "unknown"
				})
				if stale.ContainerState != "unknown" || stale.Readiness != "unknown" || stale.LastObservation == nil || stale.LastObservation.Observation.ContainerState != "exited" {
					t.Fatal("browser stale status fabricated current evidence")
				}
			case "resume":
				docker("start", id)
				browserAgent = start()
				waitStatus("browser resumed", func(s domain.HostingRuntimeStatus) bool {
					return s.Freshness == "fresh" && s.Readiness == "ready" && s.ObservedContainerID == id
				})
			case "replace":
				browserAgent.Stop(t)
				client := runnerclient.New(control.URL, token)
				client.HTTP = control.Client()
				challenge, e := client.HostingObservationTargets(ctx, domain.HostingObservationTargetsInput{Limit: 4})
				if e != nil || len(challenge.Targets) != 1 || challenge.Targets[0].ContainerID != id {
					t.Fatal("browser old target challenge missing")
				}
				old := challenge.Targets[0]
				setCapability(false)
				browserCurrent = deploy("browser-replacement")
				setCapability(true)
				if !time.Now().Before(old.ExpiresAt) {
					t.Fatal("browser replacement exceeded challenge window")
				}
				if _, e = client.ReportHostingObservation(ctx, domain.HostingRuntimeObservation{BindingID: old.BindingID, SampleID: old.SampleID, ContainerState: "running", Readiness: "ready"}); !runnerclient.HTTPStatus(e, http.StatusConflict) {
					t.Fatal("browser old target report accepted")
				}
				waitStatus("browser changed target", func(s domain.HostingRuntimeStatus) bool {
					return s.Reason == "target_changed" && s.CurrentContainerID == browserCurrent.Result.ContainerID
				})
			case "observe_replacement":
				browserAgent = start()
				waitStatus("browser replacement observed", func(s domain.HostingRuntimeStatus) bool {
					return s.Freshness == "fresh" && s.Readiness == "ready" && s.ObservedContainerID == browserCurrent.Result.ContainerID
				})
			}
			return map[string]string{"container_id": browserCurrent.Result.ContainerID, "operation_id": browserCurrent.ID}
		})
		browserAgent.Stop(t)
		trace, e := os.ReadFile("/fixture/observation-commands")
		if e != nil || len(trace) == 0 {
			t.Fatal("browser collector command trace missing")
		}
		for _, action := range strings.Fields(string(trace)) {
			if action != "info" && action != "inspect" && action != "ls" {
				t.Fatal("browser collector attempted mutation")
			}
		}
		if logs.contains("observation-env-never-inspected") || bytes.Contains(trace, []byte("observation-env-never-inspected")) {
			t.Fatal("browser observer exposed workload environment")
		}
		current, e := server.app.HostingOperation(admin, browserCurrent.ID)
		if e != nil || current.State != "succeeded" || current.MutationHold {
			t.Fatal("browser collector changed deployment outcome")
		}
		t.Log("real browser and compiled collector: healthy, fixture-stopped/exited, disconnected stale unknown, resumed, replacement history, old target rejection; readonly command trace and canary checks passed")
		return
	}
	docker("stop", "--time", "1", id)
	stopped := waitStatus("verified exit", func(s domain.HostingRuntimeStatus) bool {
		return s.Freshness == "fresh" && s.ContainerState == "exited" && s.ExitCode != nil
	})
	if stopped.Readiness != "not_checked" || stopped.MutationHold {
		t.Fatal("verified exit fabricated readiness or mutation")
	}
	agent.Stop(t)
	deadline := time.Now().Add(34 * time.Second)
	var stale domain.HostingRuntimeStatus
	for {
		stale, err = server.app.HostingRuntimeStatus(admin, resource.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stale.Freshness == "stale" && stale.AgentReachability == "unknown" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent disappearance did not become stale")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if stale.ContainerState != "unknown" || stale.Readiness != "unknown" || stale.AgentReachability != "unknown" || stale.LastObservation == nil || stale.LastObservation.Observation.ContainerState != "exited" {
		t.Fatal("stale evidence presented as current health")
	}
	docker("start", id)
	resumed := start()
	waitStatus("resumed", func(s domain.HostingRuntimeStatus) bool {
		return s.Freshness == "fresh" && s.Readiness == "ready" && s.ObservedContainerID == id
	})
	resumed.Stop(t)
	client := runnerclient.New(control.URL, token)
	client.HTTP = control.Client()
	challenge, e := client.HostingObservationTargets(ctx, domain.HostingObservationTargetsInput{Limit: 4})
	if e != nil || len(challenge.Targets) != 1 || challenge.Targets[0].ContainerID != id {
		t.Fatal("replacement challenge missing")
	}
	oldChallenge := challenge.Targets[0]
	oldReport := domain.HostingRuntimeObservation{BindingID: oldChallenge.BindingID, SampleID: oldChallenge.SampleID, ContainerState: "running", Readiness: "ready"}
	setCapability(false)
	replacement := deploy("replacement-observation-workload")
	if replacement.Result.ContainerID == id {
		t.Fatal("replacement did not change exact container identity")
	}
	setCapability(true)
	if !time.Now().Before(oldChallenge.ExpiresAt) {
		t.Fatal("replacement proof exceeded original challenge window")
	}
	if _, e := client.ReportHostingObservation(ctx, oldReport); !runnerclient.HTTPStatus(e, http.StatusConflict) {
		t.Fatal("old observation accepted after replacement")
	}
	final := start()
	waitStatus("replacement", func(s domain.HostingRuntimeStatus) bool {
		return s.Freshness == "fresh" && s.Readiness == "ready" && s.ObservedContainerID == replacement.Result.ContainerID
	})
	final.Stop(t)
	trace, e := os.ReadFile("/fixture/observation-commands")
	if e != nil || len(trace) == 0 {
		t.Fatal("readonly command trace missing")
	}
	for _, action := range strings.Fields(string(trace)) {
		if action != "info" && action != "inspect" && action != "ls" {
			t.Fatal("collector attempted mutation or overbroad collection")
		}
	}
	const canary = "observation-env-never-inspected"
	if logs.contains(canary) || bytes.Contains(trace, []byte(canary)) {
		t.Fatal("environment or raw diagnostics reached controlled output")
	}
	current, e := server.app.HostingOperation(admin, replacement.ID)
	if e != nil || current.MutationHold || current.State != "succeeded" {
		t.Fatal("collector settled or held deployment state")
	}
	t.Logf("compiled CLI observer: healthy -> fixture-only stop -> verified exited; SIGTERM joined; stale unknown after %.1fs; resumed; replacement rejects old report; readonly info/inspect/list trace only", stale.ServerTime.Sub(*stopped.ObservedAt).Seconds())
}

// Browser mode reuses the same collector and fixture-only lifecycle operations.
// No runtime-status response is fabricated or rewritten by this fixture.
func runHostingObservationBrowser(ctx context.Context, t *testing.T, server *Server, database *pgxpool.Pool, environment domain.ProjectEnvironment, resource domain.Resource, initial domain.HostingOperation, action func(string) map[string]string) {
	t.Helper()
	origin := os.Getenv("NEROCD_OBSERVATION_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	if err != nil || parsed == nil {
		t.Fatal("invalid observation browser origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Scheme != "http" || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || origin != "http://"+parsed.Host {
		t.Fatal("browser fixture requires an exact loopback HTTP origin")
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	owner, err := server.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "status-owner@example.local", Name: "Status Fixture Owner", Password: "status-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []app.ProjectMemberInput{{ProjectID: environment.ProjectID, Email: owner.Email, Role: "owner"}, {ProjectID: environment.ProjectID, Email: "viewer@example.local", Role: "viewer"}} {
		if _, err = server.app.UpsertProjectMember(admin, member); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	hiddenProject, err := server.app.CreateProject(admin, app.ProjectInput{Name: "hidden observation fixture"})
	if err != nil {
		t.Fatal(err)
	}
	hiddenEnvironment, err := server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: hiddenProject.ID, Name: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	hiddenResource, err := server.app.CreateResource(admin, app.ResourceInput{ProjectID: hiddenProject.ID, ProjectEnvironmentID: hiddenEnvironment.ID, Name: "hidden status", Kind: "application", Source: resource.Source})
	if err != nil {
		t.Fatal(err)
	}
	browserAPI := NewServerWithConfig(server.app, slog.Default(), web.Static(), ServerConfig{AllowInsecureCookies: true, PublicOrigin: origin})
	browserCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	prefix := "/api/v1/fixture/observation/" + hex.EncodeToString(nonce)
	type stepRequest struct {
		name string
		done chan map[string]string
	}
	steps := make(chan stepRequest)
	done := make(chan struct{})
	var finish sync.Once
	ownerOnly := browserAPI.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := server.app.CurrentPrincipal(r.Context())
		if err != nil || principal.ID != owner.ID || r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/complete" {
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, prefix+"/step/")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		step := stepRequest{name: name, done: make(chan map[string]string, 1)}
		select {
		case steps <- step:
		case <-r.Context().Done():
			return
		case <-browserCtx.Done():
			return
		}
		select {
		case result := <-step.done:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-r.Context().Done():
		case <-browserCtx.Done():
		}
	}))
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			ownerOnly.ServeHTTP(w, r)
			return
		}
		browserAPI.ServeHTTP(w, r)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	metadata, err := json.Marshal(map[string]string{"origin": origin, "project_id": environment.ProjectID, "environment": environment.Name, "resource_id": resource.ID, "hidden_resource_id": hiddenResource.ID, "initial_container_id": initial.Result.ContainerID, "initial_operation_id": initial.ID, "complete_path": prefix + "/complete", "step_path": prefix + "/step/"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("OBSERVATION_BROWSER_READY %s\n", metadata)
	expected := []string{"stop", "disconnect", "confirm_stale", "resume", "replace", "observe_replacement"}
	position := 0
	for {
		select {
		case <-browserCtx.Done():
			t.Fatal("observation browser fixture timed out")
		case <-done:
			if position != len(expected) {
				t.Fatal("observation browser journey ended before required stages")
			}
			return
		case step := <-steps:
			if position >= len(expected) || step.name != expected[position] {
				t.Fatal("observation browser requested an unexpected lifecycle stage")
			}
			result := action(step.name)
			position++
			step.done <- result
		}
	}
}
