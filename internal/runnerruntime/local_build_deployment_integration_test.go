//go:build linux && nerocd_proxy_fixture

package runnerruntime

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingfixture"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
)

func localBuildFixtureHTTP(t *testing.T, op domain.HostingOperation, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(op.Snapshot.HostPort)+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != want {
		t.Fatalf("ordinary HTTP body=%q want=%q err=%v", body, want, err)
	}
}

func mutableLocalBuildGit(t *testing.T) (string, func(string, bool)) {
	t.Helper()
	files := func(body string, fail bool) map[string]string {
		dockerfile := "FROM " + hostingbuild.StaticImage + "\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\n"
		if fail {
			dockerfile += "RUN exit 9\n"
		}
		return map[string]string{"Dockerfile": dockerfile, "index.html": body}
	}
	url, update, _ := hostingfixture.GitHTTPS(t, files("local-A\n", false), "", "")
	return strings.Replace(url, "127.0.0.1", "example.com", 1), func(body string, fail bool) { update(files(body, fail)) }
}

func TestLocalBuildProductionHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-hosting-local-build/gate.sh")
	}
	f := newControllerBuildFixture(t, domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability)
	root, err := os.MkdirTemp("/var/lib", "nerocd-local-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := controlTrust(t, root)
	pebbleID := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", "fixture-pebble")))
	t.Cleanup(func() {
		identity := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}} {{.Config.Image}}", pebbleID)))
		if identity != pebbleID+" "+controlPebbleImage {
			t.Error("owned Pebble identity changed")
			return
		}
		controlDocker(t, "rm", "--force", pebbleID)
	})
	proxyConfig := ProxyConfig{Directory: root, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		if err := os.MkdirAll(filepath.Join(settings.Directory, "certificates"), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(settings.Directory, "certificates/root.pem"), publicRoot, 0600); err != nil {
			return nil, err
		}
		return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
	}}
	extra := []string{"--local-build-deploy", "--proxy-state-root", root, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion}
	cli := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		out, err := f.agentCommand(ctx, extra...).CombinedOutput()
		if err != nil {
			t.Logf("bounded CLI diagnostic: %.1500s", out)
		}
		return err
	}
	var probePreparation atomic.Bool
	prepared := make(chan domain.HostingOperation, 1)
	proceed := make(chan struct{})
	var releasePreparation sync.Once
	original := f.server.Config.Handler
	completionLoss := &hostingCompletionLossFixture{original: original}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/prepared" && probePreparation.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			original.ServeHTTP(recorded, r)
			var op domain.HostingOperation
			if recorded.Code != 200 || json.Unmarshal(recorded.Body.Bytes(), &op) != nil {
				t.Error("fence fixture did not commit preparation")
				w.WriteHeader(503)
				return
			}
			prepared <- op
			select {
			case <-proceed:
			case <-r.Context().Done():
				return
			}
			for key, values := range recorded.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			completionLoss.ServeHTTP(w, r)
			return
		}
		original.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	url, update := mutableLocalBuildGit(t)
	var resource domain.Resource
	f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": "local-artifact", "kind": "application", "source": map[string]any{"kind": "git", "git": domain.GitResourceConfiguration{RepositoryURL: url, RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}, 201, &resource)
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/binding", f.admin, map[string]string{"destination_id": f.target.Destination.ID}, 201, nil)
	awaitOperation := func(id string) domain.HostingOperation {
		deadline := time.Now().Add(15 * time.Second)
		for {
			op, e := f.pg.GetHostingOperation(t.Context(), id)
			if e != nil {
				t.Fatal(e)
			}
			if op.RunID != "" {
				return op
			}
			if time.Now().After(deadline) {
				t.Fatal("real River local dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	build := func(key string) domain.HostingBuild {
		b := f.build(t, resource, key)
		if err := cli(); err != nil {
			t.Fatal(err)
		}
		full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", b.ID)
		if err != nil || full.State != "succeeded" || full.Artifact == nil {
			t.Fatal("actual public Git build did not persist artifact")
		}
		return full
	}
	deploy := func(b domain.HostingBuild, key string) domain.HostingOperation {
		var op domain.HostingOperation
		f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/deployments/from-build", f.admin, domain.HostingBuildDeploymentInput{BuildID: b.ID, ExpectedSourceGeneration: resource.Source.Git.Generation, ExpectedGeneration: resource.Generation, IdempotencyKey: key, ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 202, &op)
		return awaitOperation(op.ID)
	}
	firstBuild := build("local-build-A")
	first := deploy(firstBuild, "local-deploy-A")
	if err = cli(); err != nil {
		t.Fatal(err)
	}
	first, err = f.pg.GetHostingOperation(t.Context(), first.ID)
	if err != nil || first.State != "succeeded" || first.Result == nil || first.Release == nil || first.Release.LocalBuild != *firstBuild.Artifact {
		t.Fatal("CLI local deployment failed exact artifact contract")
	}
	owned := []string{first.Result.ContainerID}
	t.Cleanup(func() {
		for _, id := range owned {
			out := controlDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.binding"}}`, id)
			if strings.TrimSpace(string(out)) != first.BindingID {
				t.Error("owned local workload identity changed")
				continue
			}
			controlDocker(t, "rm", "--force", id)
		}
	})
	localBuildFixtureHTTP(t, first, "local-A\n")
	proxy := controlJSON[domain.HostingProxy](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: f.environment.ProjectID, IdempotencyKey: "local-enable-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	proxyRun := func() {
		deadline := time.Now().Add(15 * time.Second)
		for {
			p, e := f.pg.GetHostingProxy(t.Context(), proxy.ID)
			if e != nil {
				t.Fatal(e)
			}
			if p.ActiveOperationID != "" {
				op, e := f.pg.GetHostingProxyOperation(t.Context(), p.ActiveOperationID)
				if e == nil && op.Generation == p.DesiredGeneration {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("real River proxy dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if e := RunContext(t.Context(), Config{Build: &f.config, LocalBuildDeploy: true, Proxy: proxyConfig, Server: f.server.URL, Token: f.credential, RunnerID: f.config.Target.RunnerID, WorkDir: f.work, JournalDir: f.journal, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond}); e != nil {
			t.Fatal(e)
		}
	}
	proxyRun()
	proxyID := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", "nerocd-proxy-"+proxy.ID)))
	t.Cleanup(func() {
		if strings.TrimSpace(string(controlDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.proxy"}}`, proxyID))) != proxy.ID {
			t.Error("owned proxy identity changed")
			return
		}
		controlDocker(t, "rm", "--force", proxyID)
	})
	controlJSON[domain.HostingHostnameGrant](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: f.environment.ProjectID, Hostname: "local.example.test"}, 201)
	route := controlJSON[domain.HostingRoute](t, f.server.URL, f.admin, "POST", "/api/v1/resources/"+resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "local-route-A", Hostname: "local.example.test", PathPrefix: "/", ExpectedHealthyOperationID: first.ID}, 202)
	proxyRun()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	probe := func(want string) {
		t.Helper()
		response, e := client.Get("https://local.example.test/")
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if e != nil || response.StatusCode != 200 || string(body) != want {
			t.Fatalf("managed HTTPS body=%q wanted%q err=%v", body, want, e)
		}
	}
	probe("local-A\n")
	update("local-B\n", false)
	secondBuild := build("local-build-B")
	second := deploy(secondBuild, "local-deploy-B")
	completionLoss.enable()
	probePreparation.Store(true)
	completed := make(chan error, 1)
	go func() { completed <- cli() }()
	joined := false
	defer func() {
		releasePreparation.Do(func() { close(proceed) })
		if !joined {
			<-completed
		}
	}()
	select {
	case <-prepared:
	case <-time.After(15 * time.Second):
		t.Fatal("actual CLI did not reach fenced preparation")
	}
	var authority domain.HostingAuthority
	authority.OperationID = second.ID
	authority.RunID = second.RunID
	if err = f.db.QueryRow(t.Context(), `SELECT id,attempt,fence FROM run_leases WHERE run_id=$1 AND status='active'`, second.RunID).Scan(&authority.LeaseID, &authority.Attempt, &authority.Fence); err != nil {
		t.Fatal(err)
	}
	expectedCompletionAuthority := authority
	authority.Fence = "stale-" + authority.Fence
	// Wrong fenced authority is deliberately opaque at the production endpoint.
	f.request(t, "POST", "/api/v1/runners/hosting/apply-begin", f.credential, authority, 404, nil)
	if current, e := f.pg.GetHostingOperation(t.Context(), second.ID); e != nil || current.MutationHold {
		t.Fatal("stale fence acquired mutation authority")
	}
	probe("local-A\n")
	releasePreparation.Do(func() { close(proceed) })
	completionErr := <-completed
	joined = true
	if completionErr == nil {
		t.Fatal("lost deployment completion response was not retained")
	}
	proof := completionLoss.snapshot()
	if !proof.Committed || proof.Failure != "" || proof.Request.HostingAuthority != expectedCompletionAuthority || proof.RequestHash != domain.HostingDocumentHash(proof.Request) || proof.Suppressed < 1 {
		t.Fatalf("completion loss lacked exact real commit proof: %+v", proof)
	}
	committed, e := f.pg.GetHostingOperation(t.Context(), second.ID)
	if e != nil || committed.State != "succeeded" || committed.CompleteReceipt == nil || !sameCompletionFixtureReceipt(*committed.CompleteReceipt, proof.Receipt) || committed.Result == nil || *committed.Result != proof.Request.Result {
		t.Fatal("lost response did not follow persisted exact completion", e)
	}
	pendingJournal, e := runner.OpenAttemptJournal(f.journal)
	if e != nil {
		t.Fatal(e)
	}
	pendingResults := 0
	for _, entry := range pendingJournal.Snapshot().Hosting {
		if entry.OperationID == second.ID && entry.Phase == "result" {
			if entry.Result == nil || *entry.Result != proof.Request {
				t.Error("pending result differs from committed lost response")
			}
			pendingResults++
		}
	}
	if e = pendingJournal.Close(); e != nil {
		t.Fatal(e)
	}
	if pendingResults != 1 {
		t.Fatalf("lost response did not retain exactly one journal result: %d", pendingResults)
	}
	t.Logf("real completion committed before response loss: operation=%s receipt=%s request_hash=%s result_hash=%s suppressed_responses=%d; exact journal result remains pending", second.ID, proof.Receipt.ID, proof.RequestHash, proof.Receipt.BodyHash, proof.Suppressed)
	completionLoss.disable()
	if err = cli(); err != nil {
		t.Fatalf("exact local deployment replay: %v", err)
	}
	pendingJournal, e = runner.OpenAttemptJournal(f.journal)
	if e != nil {
		t.Fatal(e)
	}
	pendingAfterReplay := false
	for _, entry := range pendingJournal.Snapshot().Hosting {
		if entry.OperationID == second.ID {
			pendingAfterReplay = true
		}
	}
	if e = pendingJournal.Close(); e != nil {
		t.Fatal(e)
	}
	if pendingAfterReplay {
		t.Fatal("exact startup replay did not acknowledge local deployment journal")
	}
	t.Logf("startup replay acknowledged the exact committed completion: operation=%s receipt=%s", second.ID, proof.Receipt.ID)
	second, err = f.pg.GetHostingOperation(t.Context(), second.ID)
	if err != nil || second.State != "succeeded" || second.Result == nil || *second.Result != proof.Request.Result || second.CompleteReceipt == nil || !sameCompletionFixtureReceipt(*second.CompleteReceipt, proof.Receipt) || second.Snapshot.HostPort == first.Snapshot.HostPort {
		t.Fatal("second local release did not preserve independent port")
	}
	owned = append(owned, second.Result.ContainerID)
	localBuildFixtureHTTP(t, second, "local-B\n")
	probe("local-A\n")
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/retained-releases/"+first.ID+"/cleanup", f.admin, domain.HostingCleanupInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "protected-A"}, 409, nil)
	route = controlJSON[domain.HostingRoute](t, f.server.URL, f.admin, "GET", "/api/v1/routes/"+route.ID, nil, 200)
	controlJSON[domain.HostingRoute](t, f.server.URL, f.admin, "PATCH", "/api/v1/routes/"+route.ID, domain.HostingRouteInput{ExpectedRouteVersion: route.Version, IdempotencyKey: "local-route-B", Hostname: route.Hostname, PathPrefix: "/", ExpectedHealthyOperationID: second.ID}, 202)
	proxyRun()
	probe("local-B\n")
	var cleanup domain.HostingOperation
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/retained-releases/"+first.ID+"/cleanup", f.admin, domain.HostingCleanupInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "cleanup-A"}, 202, &cleanup)
	awaitOperation(cleanup.ID)
	if err = cli(); err != nil {
		t.Fatal(err)
	}
	cleanup, err = f.pg.GetHostingOperation(t.Context(), cleanup.ID)
	if err != nil || cleanup.State != "succeeded" {
		t.Fatal("retained local cleanup did not settle")
	}
	owned = owned[1:]
	probe("local-B\n")
	update("local-C\n", false)
	missing := build("local-build-missing")
	if ids := buildRuntimeDocker(t, "ps", "--all", "--quiet", "--filter", "ancestor="+missing.Artifact.ImageID); ids != "" {
		t.Fatal("missing-image fixture image unexpectedly used")
	}
	buildRuntimeDocker(t, "image", "rm", missing.Artifact.ImageID)
	missingOp := deploy(missing, "local-deploy-missing")
	if cli() == nil {
		t.Fatal("missing exact image returned CLI success")
	}
	missingOp, err = f.pg.GetHostingOperation(t.Context(), missingOp.ID)
	if err != nil || missingOp.State != "failed" || missingOp.MutationHold {
		t.Fatal("missing image did not fail before mutation")
	}
	probe("local-B\n")
	update("failed-source\n", true)
	failed := f.build(t, resource, "local-failed-build")
	if cli() == nil {
		t.Fatal("failed build unexpectedly succeeded")
	}
	failedFull, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", failed.ID)
	if err != nil || failedFull.State != "failed" || failedFull.Artifact != nil {
		t.Fatal("failed build produced artifact")
	}
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/deployments/from-build", f.admin, domain.HostingBuildDeploymentInput{BuildID: failed.ID, ExpectedSourceGeneration: resource.Source.Git.Generation, ExpectedGeneration: resource.Generation, IdempotencyKey: "failed-build-deploy", ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 409, nil)
	probe("local-B\n")
	// Reproduce the durable binding marker restored backups establish, on this
	// exact owned binding only. There is deliberately no unlock in this fixture.
	if _, err = f.db.Exec(t.Context(), `INSERT INTO hosting_binding_restore_holds(binding_id) VALUES($1)`, second.BindingID); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/deployments/from-build", f.admin, domain.HostingBuildDeploymentInput{BuildID: secondBuild.ID, ExpectedSourceGeneration: resource.Source.Git.Generation, ExpectedGeneration: resource.Generation, IdempotencyKey: "restored-build-deploy", ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 409, nil)
	var attempt runner.AttemptIdentity
	attempt.RunID = second.RunID
	if err = f.db.QueryRow(t.Context(), `SELECT id,attempt,fence,created_at,expires_at FROM run_leases WHERE run_id=$1 ORDER BY attempt DESC LIMIT 1`, second.RunID).Scan(&attempt.LeaseID, &attempt.Attempt, &attempt.Fence, &attempt.CreatedAt, &attempt.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	journal, err := runner.OpenAttemptJournal(f.journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AppendHosting(runner.JournalHosting{ID: "restored-local-intent", Attempt: attempt, OperationID: second.ID, Phase: "apply_intent", Plan: second.Plan, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	beforeHeartbeat := f.heartbeats.Load()
	if cli() == nil || f.heartbeats.Load() != beforeHeartbeat {
		t.Fatal("restored local ownership entered normal runner traffic")
	}
	journal, err = runner.OpenAttemptJournal(f.journal)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Snapshot().Hosting) != 1 {
		t.Fatal("restored local journal intent lost")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	probe("local-B\n")
	t.Logf("real public build→compiled CLI local deployment→ordinary HTTP→managed Traefik/Pebble HTTPS; A=%s B=%s ports=%d/%d, exact response-loss replay, protected cleanup, missing image and failed build preserved B", first.ID, second.ID, first.Snapshot.HostPort, second.Snapshot.HostPort)
}

// TestLocalBuildBrowser exposes only this disposable fixture's authenticated API
// and current embedded UI. The host gate publishes its listener on loopback.
func TestLocalBuildBrowser(t *testing.T) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_LOCAL_BUILD_BROWSER") != "1" {
		t.Skip("requires opt-in local build browser gate")
	}
	stored := os.Getenv("NEROCD_STORED_DOCKERFILE_BROWSER") == "1"
	capabilities := []string{domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}
	if stored {
		capabilities = append(capabilities, domain.HostingStoredDockerfileCapability)
	}
	f := newControllerBuildFixture(t, capabilities...)
	root, err := os.MkdirTemp("/var/lib", "nerocd-browser-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := controlTrust(t, root)
	proxyConfig := ProxyConfig{Directory: root, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		if err := os.MkdirAll(filepath.Join(settings.Directory, "certificates"), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(settings.Directory, "certificates/root.pem"), publicRoot, 0600); err != nil {
			return nil, err
		}
		return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
	}}
	var resource domain.Resource
	if !stored {
		url, _ := mutableLocalBuildGit(t)
		f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": "Browser local artifact", "kind": "application", "source": map[string]any{"kind": "git", "git": domain.GitResourceConfiguration{RepositoryURL: url, RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}, 201, &resource)
		f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/binding", f.admin, map[string]string{"destination_id": f.target.Destination.ID}, 201, nil)
	}
	f.request(t, "POST", "/api/v1/project-members", f.admin, map[string]string{"project_id": f.environment.ProjectID, "email": "viewer@example.local", "role": "viewer"}, 200, nil)
	// Infrastructure setup is separate from the browser's build/deploy/route journey.
	proxy := controlJSON[domain.HostingProxy](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: f.environment.ProjectID, IdempotencyKey: "browser-enable-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	controlJSON[domain.HostingHostnameGrant](t, f.server.URL, f.admin, "POST", "/api/v1/servers/"+f.target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: f.environment.ProjectID, Hostname: "local.example.test"}, 201)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	runProxy := func() error {
		return RunContext(ctx, Config{Build: &f.config, LocalBuildDeploy: true, Proxy: proxyConfig, Server: f.server.URL, Token: f.credential, RunnerID: f.config.Target.RunnerID, WorkDir: f.work, JournalDir: f.journal, Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond})
	}
	// Wait for real River dispatch before asking the authenticated runner to claim.
	waitDispatch := func(kind, id string) error {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			if kind == "build" {
				b, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", id)
				if err != nil {
					return err
				}
				if b.RunID != "" {
					return nil
				}
			} else if kind == "deploy" {
				op, err := f.pg.GetHostingOperation(ctx, id)
				if err != nil {
					return err
				}
				if op.RunID != "" {
					return nil
				}
			} else {
				if id == "" {
					current, err := f.pg.GetHostingProxy(ctx, proxy.ID)
					if err != nil {
						return err
					}
					id = current.ActiveOperationID
				}
				op, err := f.pg.GetHostingProxyOperation(ctx, id)
				if err == nil && op.RunID != "" {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("browser fixture dispatch timeout for %s", kind)
			case <-tick.C:
			}
		}
	}
	// River makes the initial proxy operation active when its run is dispatched.
	initialProxy, err := f.pg.GetHostingProxy(ctx, proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = waitDispatch("proxy", initialProxy.ActiveOperationID); err != nil {
		t.Fatal(err)
	}
	if err = runProxy(); err != nil {
		t.Fatal(err)
	}
	type work struct{ kind, id string }
	queue := make(chan work, 1)
	results := make(chan error, 1)
	var seen sync.Map
	done := make(chan struct{})
	var finish sync.Once
	nonce := make([]byte, 24)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	completePath := "/fixture/browser-complete/" + hex.EncodeToString(nonce)
	origin := os.Getenv("NEROCD_LOCAL_BUILD_BROWSER_ORIGIN")
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	defer transport.CloseIdleConnections()
	probePath := completePath + "/https"
	handler := f.server.Config.Handler
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stored && r.URL.Path == probePath {
			if r.Method != http.MethodGet || r.Header.Get("Origin") != origin {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://local.example.test/", nil)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(request)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
			if err != nil || response.StatusCode != http.StatusOK {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		if r.URL.Path == completePath {
			if r.Method != "POST" || r.Header.Get("Origin") != origin {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		if stored && r.Method == http.MethodPost && r.URL.Path == "/api/v1/resources" {
			recorded := httptest.NewRecorder()
			handler.ServeHTTP(recorded, r)
			if recorded.Code == http.StatusCreated {
				if err := json.Unmarshal(recorded.Body.Bytes(), &resource); err != nil {
					t.Error(err)
				}
			}
			for key, values := range recorded.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		routeEdit := stored && r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/routes/")
		if !routeEdit && (r.Method != "POST" || !(strings.HasSuffix(r.URL.Path, "/builds") || strings.HasSuffix(r.URL.Path, "/deployments/from-build") || strings.HasSuffix(r.URL.Path, "/routes"))) {
			handler.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, r)
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
		if recorded.Code != http.StatusAccepted {
			return
		}
		item := work{}
		if strings.HasSuffix(r.URL.Path, "/builds") {
			var response struct {
				Build domain.PublicHostingBuild `json:"build"`
			}
			if json.Unmarshal(recorded.Body.Bytes(), &response) != nil {
				return
			}
			item = work{"build", response.Build.ID}
		} else if strings.HasSuffix(r.URL.Path, "/from-build") {
			var op domain.HostingOperation
			if json.Unmarshal(recorded.Body.Bytes(), &op) != nil {
				return
			}
			item = work{"deploy", op.ID}
		} else {
			var route domain.HostingRoute
			if json.Unmarshal(recorded.Body.Bytes(), &route) != nil {
				return
			}
			item = work{"proxy", route.OperationID}
		}
		if _, duplicate := seen.LoadOrStore(item.id, true); duplicate {
			return
		}
		select {
		case queue <- item:
		case <-ctx.Done():
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	exposed.Listener = listener
	exposed.Start()
	t.Cleanup(exposed.Close)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-queue:
				err := waitDispatch(item.kind, item.id)
				if err == nil && item.kind == "proxy" {
					err = runProxy()
				} else if err == nil {
					// Build and local workload execution use the compiled production CLI.
					command := f.agentCommand(ctx, "--local-build-deploy", "--proxy-state-root", root, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion)
					out, runErr := command.CombinedOutput()
					err = runErr
					if err != nil && stored && item.kind == "build" {
						failed, readErr := f.pg.GetHostingBuild(ctx, "usr_bootstrap", item.id)
						if readErr == nil && failed.ID == item.id && failed.ResourceID == resource.ID && failed.Snapshot.Source.Kind == "dockerfile" && failed.Snapshot.Source.Content == storedDockerfileFixtureSource("stored-failed", true) && failed.State == "failed" && failed.Artifact == nil && failed.FailureCode == "build_failed" {
							output, outputErr := f.pg.ListHostingBuildLogs(ctx, "usr_bootstrap", item.id, 0, 200)
							var messages strings.Builder
							for _, row := range output.Items {
								messages.WriteString(row.Message)
							}
							if outputErr == nil && strings.Contains(messages.String(), "did not complete successfully: exit code: 9") {
								err = nil
							}
						}
					}
					if err != nil {
						t.Logf("bounded browser CLI diagnostic: %.1500s", out)
					}
				}
				if err != nil {
					select {
					case results <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-workerDone })
	metadata, err := json.Marshal(map[string]any{"origin": origin, "project_id": f.environment.ProjectID, "environment": f.environment.Name, "resource_id": resource.ID, "complete_path": completePath, "stored_dockerfile": stored, "probe_path": probePath, "destination_id": f.target.Destination.ID, "content_a": storedDockerfileFixtureSource("stored-A", false), "content_b": storedDockerfileFixtureSource("stored-B", false), "content_failed": storedDockerfileFixtureSource("stored-failed", true)})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("LOCAL_BUILD_BROWSER_READY %s\n", metadata)
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case err = <-results:
		t.Fatal(err)
	case <-done:
	}
	// The browser reports success only after recorded HTTPS readiness. Independently
	// probe the actual proxy with its fixture CA and ordinary application bytes.
	response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://local.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	wantBody := "local-A\n"
	if stored {
		wantBody = "stored-B\n"
	}
	if err != nil || response.StatusCode != 200 || string(body) != wantBody {
		t.Fatalf("browser HTTPS actual response %d %q err=%v", response.StatusCode, body, err)
	}
}
