//go:build linux && nerocd_proxy_fixture

package runnerruntime

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/store"
)

// Source edits need the same operator-owned loopback policy as creation in this
// disposable fixture. Production public HTTPS policy remains unchanged. The
// underlying repository still owns CAS, source revisions and mutation audits.
func (h controllerFixtureHosting) UpdateResource(ctx context.Context, resource domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	previous, err := h.GetResource(ctx, resource.ID)
	if err != nil {
		return domain.Resource{}, err
	}
	if resource.Source.Kind == "git" && resource.Source.Git != nil && previous.Source.Git != nil && resource.Source.Git.Generation > previous.Source.Git.Generation {
		g := resource.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: resource.ID, ProjectID: resource.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: h.policy}))
	}
	return h.HostingRepository.UpdateResource(ctx, resource, expected, opts...)
}

// The embedded interface keeps this fake bounded to the two methods exercised.
type privateFixtureResourceRepository struct {
	store.HostingRepository
	previous, updated domain.Resource
	expected          int64
	options           int
}

func (r *privateFixtureResourceRepository) GetResource(context.Context, string) (domain.Resource, error) {
	return r.previous, nil
}
func (r *privateFixtureResourceRepository) UpdateResource(_ context.Context, value domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	r.updated, r.expected, r.options = value, expected, len(opts)
	return value, nil
}
func TestPrivateFixtureSourceRevisionPolicy(t *testing.T) {
	original := domain.Resource{ID: "resource_fixture", Generation: 1, Name: "before", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: "source_fixture", Generation: 1, GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://example.com/repo.git", RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: "railpack"}}}}}
	repository := &privateFixtureResourceRepository{previous: original}
	adapter := controllerFixtureHosting{HostingRepository: repository}
	named := original
	named.Name = "after"
	audit := store.WithAudit(domain.AuditEvent{})
	if _, err := adapter.UpdateResource(t.Context(), named, 1, audit); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(original.Source)
	after, _ := json.Marshal(repository.updated.Source)
	if string(before) != string(after) || repository.options != 1 || repository.expected != 1 || repository.updated.Name != "after" {
		t.Fatal("name-only fixture update rewrote source/options or lost CAS")
	}
	changed := named
	git := *named.Source.Git
	git.Generation = 2
	changed.Source.Git = &git
	if _, err := adapter.UpdateResource(t.Context(), changed, 1, audit); err != nil {
		t.Fatal(err)
	}
	if repository.options != 2 || repository.expected != 1 || repository.updated.Source.Git.Generation != 2 {
		t.Fatal("source revision did not add exactly its fixture policy option while preserving CAS")
	}
}

// Each private case owns a separate database inside the disposable fixture's
// PostgreSQL container. Never reapply native migrations to a prior case's schema.
func privateRuntimeDatabase(t *testing.T) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" || cfg.ConnConfig.Database != "nerocd_build_controller" {
		t.Fatal("private database requires exact disposable fixture")
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "private_runtime_" + hex.EncodeToString(nonce)
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		defer admin.Close()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("owned private database cleanup", err)
		} else {
			t.Logf("owned private database removed: %s", name)
		}
	})
	u, err := url.Parse(os.Getenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL"))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("fixture requires PostgreSQL URL")
	}
	u.Path = "/" + name
	scoped := u.String()
	verified, err := pgxpool.ParseConfig(scoped)
	if err != nil || verified.ConnConfig.Database != name {
		t.Fatal("scoped fixture database mismatch")
	}
	t.Setenv("NEROCD_BUILD_CONTROLLER_DATABASE_URL", scoped)

}

func privateRuntimeGit(t *testing.T, strategy string) (string, func(string, bool), string, string) {
	t.Helper()
	actual, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	checkoutCommand(t, work, "init", "-q", "-b", "main")
	update := func(body string, fail bool) {
		t.Helper()
		dockerfile := "FROM " + hostingbuild.StaticImage + "\nCOPY --chmod=0644 index.html /usr/share/nginx/html/index.html\n"
		if fail {
			dockerfile += "RUN exit 9\n"
		}
		files := map[string]string{"Dockerfile": dockerfile, "index.html": body}
		if strategy == "railpack" {
			delete(files, "Dockerfile")
			files["Staticfile"] = "root: .\n"
			if fail {
				files["railpack.json"] = `{ "steps": `
			} else {
				if err := os.Remove(filepath.Join(work, "railpack.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
		for name, value := range files {
			if e := os.WriteFile(filepath.Join(work, name), []byte(value), 0600); e != nil {
				t.Fatal(e)
			}
		}
		checkoutCommand(t, work, "add", ".")
		checkoutCommand(t, work, "commit", "-qm", "fixture update")
	}
	update("local-A\n", false)
	checkoutCommand(t, root, "clone", "--bare", work, "repo.git")
	username := "fixture-user"
	password := fmt.Sprintf("fixture-pat-%x", time.Now().UnixNano())
	backend := &cgi.Handler{Path: actual, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="private fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	for _, wrong := range []bool{false, true} {
		req, e := http.NewRequest(http.MethodGet, server.URL+"/repo.git/info/refs?service=git-upload-pack", nil)
		if e != nil {
			t.Fatal(e)
		}
		if wrong {
			req.SetBasicAuth(username, "wrong-password")
		}
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal("private fixture allowed anonymous/wrong auth")
		}
	}

	t.Cleanup(server.Close)
	cert := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n{ printf '%s\\n' \"$@\"; env; } >> "+shellQuote(filepath.Join(root, "transport.trace"))+"\nexec "+shellQuote(actual)+" -c http.sslCAInfo="+shellQuote(cert)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(root, "transport.trace"))
		if err != nil {
			t.Error(err)
			return
		}
		for _, pattern := range []string{password, username + ":" + password} {
			if strings.Contains(string(raw), pattern) {
				t.Error("credential entered Git argv/environment")
			}
		}
	})
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return strings.Replace(server.URL, "127.0.0.1", "example.com", 1) + "/repo.git", func(body string, fail bool) {
		update(body, fail)
		checkoutCommand(t, work, "push", filepath.Join(root, "repo.git"), "main")
	}, username, password
}

func testPrivateBuildProductionHTTPS(t *testing.T, strategy string) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-hosting-local-build/gate.sh")
	}
	privateRuntimeDatabase(t)
	f := newControllerBuildFixture(t, domain.HostingPrivateGitCapability, domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability)
	var lost atomic.Bool
	var deliveryMu sync.Mutex
	var deliveries []domain.HostingSourceCredentialRequest
	original := f.server.Config.Handler
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/builds/source-credential" {
			original.ServeHTTP(w, r)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		var in domain.HostingSourceCredentialRequest
		if json.Unmarshal(raw, &in) != nil {
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		deliveryMu.Lock()
		deliveries = append(deliveries, in)
		deliveryMu.Unlock()
		recorded := httptest.NewRecorder()
		original.ServeHTTP(recorded, r)
		defer clear(recorded.Body.Bytes())
		if recorded.Code == 200 && lost.CompareAndSwap(false, true) {
			w.WriteHeader(503)
			return
		}
		for key, values := range recorded.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
	}))
	t.Cleanup(f.server.Close)
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
	privateRuntimeFreshRegistration(t, f, extra)
	var cliOutput []byte
	cli := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		out, err := f.agentCommand(ctx, extra...).CombinedOutput()
		cliOutput = append(cliOutput, out...)
		if err != nil {
			t.Logf("bounded CLI diagnostic: %.1500s", out)
		}
		return err
	}
	url, _, username, password := privateRuntimeGit(t, strategy)
	var resource domain.Resource
	f.request(t, "POST", "/api/v1/resources", f.admin, map[string]any{"project_id": f.environment.ProjectID, "project_environment_id": f.environment.ID, "name": "local-artifact", "kind": "application", "source": map[string]any{"kind": "git", "git": domain.GitResourceConfiguration{RepositoryURL: url, RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: strategy}}}}, 201, &resource)
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/binding", f.admin, map[string]string{"destination_id": f.target.Destination.ID}, 201, nil)
	secret, grant := privateRuntimeGrant(t, f, resource, username, password)
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
		b := privateRuntimeBuild(t, f, resource, grant.ID, key)
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
	_ = controlJSON[domain.HostingRoute](t, f.server.URL, f.admin, "POST", "/api/v1/resources/"+resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "local-route-A", Hostname: "local.example.test", PathPrefix: "/", ExpectedHealthyOperationID: first.ID}, 202)
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
	deliveryMu.Lock()
	if len(deliveries) != 2 || deliveries[0] != deliveries[1] || !lost.Load() {
		t.Error("credential response-loss changed delivery identity")
	}
	deliveryMu.Unlock()
	publicDTO, err := json.Marshal(domain.PublicBuild(firstBuild))
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{password, username + ":" + password, secret.ID} {
		if strings.Contains(string(publicDTO), pattern) {
			t.Fatal("secret leaked into public build DTO")
		}
	}
	if strings.Contains(string(cliOutput), password) || strings.Contains(string(cliOutput), username+":"+password) {
		t.Fatal("credential leaked into compiled CLI output")
	}
	privateRuntimeNoSecrets(t, f, firstBuild.ID, password, username+":"+password)
	t.Logf("real private TLS Git %s -> compiled CLI -> same-host artifact -> trusted-test-CA HTTPS; anonymous/wrong auth denied", strategy)
}
func TestPrivateBuildProductionHTTPS(t *testing.T) {
	for _, strategy := range []string{"dockerfile", "railpack"} {
		t.Run(strategy, func(t *testing.T) { testPrivateBuildProductionHTTPS(t, strategy) })
	}
}

// Exercise the production CLI's default registration composition, rather than
// hiding missing capabilities behind the fixture's pre-enrolled build identity.
func privateRuntimeFreshRegistration(t *testing.T, f *controllerBuildFixture, extra []string) {
	t.Helper()
	before, err := f.pg.GetRunnerByID(t.Context(), f.config.Target.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	id := "private_fresh_" + hex.EncodeToString(nonce)
	raw, err := os.ReadFile(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("invalid fixture configuration")
	}
	document["target"].(map[string]any)["runner_id"] = id
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(f.work, "fresh-registration.json")
	if err = os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := f.agentCommand(ctx, extra...)
	for i := 1; i < len(command.Args)-1; i++ {
		switch command.Args[i] {
		case "--credential-file":
			command.Args[i] = "--token"
			command.Args[i+1] = f.admin
		case "--build-config-file":
			command.Args[i+1] = config
		case "--journal-dir":
			command.Args[i+1] = filepath.Join(f.work, "fresh-registration-journal")
		}
	}
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh private runner registration: %v %.500s", err, out)
	}
	if !strings.Contains(string(out), "no matching queued run") {
		t.Fatal("fresh registered runner did not finish empty claim")
	}
	registered, err := f.pg.GetRunnerByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{domain.HostingBuildTaskType: true, domain.HostingPrivateGitCapability: true, domain.RunTypeLocalBuildDeploy: true, domain.RunTypeProxyApply: true}
	if len(registered.Capabilities) != len(expected) {
		t.Fatal("fresh registration capability count changed")
	}
	for _, capability := range registered.Capabilities {
		if !expected[capability] {
			t.Fatal("fresh registration capability changed")
		}
	}
	after, err := f.pg.GetRunnerByID(t.Context(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := json.Marshal(before.Capabilities)
	current, _ := json.Marshal(after.Capabilities)
	if string(prior) != string(current) {
		t.Fatal("registration mutated existing runner capabilities")
	}
	var leases int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM run_leases WHERE runner_id=$1`, id).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("fresh registration unexpectedly acquired work", err)
	}
	t.Log("fresh compiled CLI registration persisted exact private/build/proxy/local capabilities without shell or a claim")
}

func privateRuntimeSecret(t *testing.T, f *controllerBuildFixture, resource domain.Resource, username, password string) domain.ManagedSecret {
	t.Helper()
	value, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	var secret domain.ManagedSecret
	f.request(t, "POST", "/api/v1/managed-secrets", f.admin, app.ManagedSecretInput{ProjectID: resource.ProjectID, Name: "private-git", Value: string(value)}, 201, &secret)
	return secret
}
func privateRuntimeGrant(t *testing.T, f *controllerBuildFixture, resource domain.Resource, username, password string) (domain.ManagedSecret, domain.PublicHostingSourceGrant) {
	t.Helper()
	secret := privateRuntimeSecret(t, f, resource, username, password)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}
	f.request(t, "POST", "/api/v1/resources/"+resource.ID+"/source-grants", f.admin, domain.HostingSourceGrantRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, SecretID: secret.ID, SecretVersion: 1, RequestID: hex.EncodeToString(nonce)}, 201, &result)
	return secret, result.Grant
}
func privateRuntimeBuild(t *testing.T, f *controllerBuildFixture, r domain.Resource, grant, key string) domain.PublicHostingBuild {
	t.Helper()
	var out struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	f.request(t, "POST", "/api/v1/resources/"+r.ID+"/builds", f.admin, domain.HostingBuildRequest{ExpectedResourceGeneration: r.Generation, ExpectedSourceGeneration: r.Source.Git.Generation, IdempotencyKey: key, GrantID: grant}, 202, &out)
	deadline := time.Now().Add(15 * time.Second)
	for {
		b, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", out.Build.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.RunID != "" {
			return domain.PublicBuild(b)
		}
		if time.Now().After(deadline) {
			t.Fatal("private dispatch timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func privateRuntimeNoSecrets(t *testing.T, f *controllerBuildFixture, build string, patterns ...string) {
	t.Helper()
	rows, err := f.db.Query(t.Context(), `SELECT row_to_json(r)::text FROM run_logs r UNION ALL SELECT row_to_json(a)::text FROM audit_events a`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		if err = rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			if strings.Contains(row, pattern) {
				t.Fatal("credential persisted in logs/audit")
			}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(f.journal, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			for _, pattern := range patterns {
				if strings.Contains(string(raw), pattern) {
					t.Fatal("credential persisted in journal")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.config.SourceRoot, build)); !os.IsNotExist(err) {
		t.Fatal("private checkout cleanup unconfirmed")
	}
}
func TestPrivateGitBrowser(t *testing.T) {
	if os.Getenv("NEROCD_LOCAL_BUILD_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_PRIVATE_GIT_BROWSER") != "1" {
		t.Skip("requires opt-in local build browser gate")
	}
	strategy := os.Getenv("NEROCD_PRIVATE_GIT_STRATEGY")
	if strategy == "" {
		strategy = "dockerfile"
	}
	if strategy != "dockerfile" && strategy != "railpack" {
		t.Fatal("unsupported fixed private browser strategy")
	}
	privateRuntimeDatabase(t)
	f := newControllerBuildFixture(t, domain.HostingPrivateGitCapability, domain.HostingBuildTaskType, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability)
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
	repositoryURL, updateGit, username, password := privateRuntimeGit(t, strategy)
	secret := privateRuntimeSecret(t, f, domain.Resource{ProjectID: f.environment.ProjectID}, username, password)
	var ownerID string
	if err := f.db.QueryRow(t.Context(), `SELECT id FROM users WHERE email='browser-bootstrap@example.local'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	patterns := []string{password, username + ":" + password, base64.StdEncoding.EncodeToString([]byte(username + ":" + password)), url.QueryEscape(password), url.PathEscape(password), f.credential, f.admin}
	var failureEnabled atomic.Bool
	var recordsMu sync.Mutex
	var buildIDs []string
	var cliOutput []byte
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
			switch kind {
			case "build":
				b, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", id)
				if err != nil {
					return err
				}
				if b.RunID != "" {
					return nil
				}
			case "deploy":
				op, err := f.pg.GetHostingOperation(ctx, id)
				if err != nil {
					return err
				}
				if op.RunID != "" {
					return nil
				}
			default:
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
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	defer transport.CloseIdleConnections()
	probe := func() {
		response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://local.example.test/")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil || response.StatusCode != 200 || string(body) != "local-A\n" {
			t.Fatalf("browser HTTPS actual response %d %q err=%v", response.StatusCode, body, err)
		}
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
	completePath := "/api/v1/fixture/private-browser/" + hex.EncodeToString(nonce) + "/complete"
	failurePath := strings.TrimSuffix(completePath, "/complete") + "/fail-source"
	stages := make(chan chan struct{})
	origin := os.Getenv("NEROCD_LOCAL_BUILD_BROWSER_ORIGIN")
	handler := f.server.Config.Handler
	exposed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == completePath || r.URL.Path == failurePath {
			cookie, cookieErr := r.Cookie("nerocd_session")
			authorized := false
			if cookieErr == nil && r.Header.Get("Authorization") == "" {
				check := httptest.NewRequestWithContext(r.Context(), "GET", "/api/v1/me", nil)
				check.AddCookie(cookie)
				recorded := httptest.NewRecorder()
				handler.ServeHTTP(recorded, check)
				var principal auth.Principal
				authorized = recorded.Code == 200 && json.Unmarshal(recorded.Body.Bytes(), &principal) == nil && principal.ID == ownerID
			}
			if r.Method != "POST" || r.Header.Get("Origin") != origin || !authorized {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.URL.Path == failurePath {
				acknowledgement := make(chan struct{})
				select {
				case stages <- acknowledgement:
				case <-ctx.Done():
					return
				case <-r.Context().Done():
					return
				}
				select {
				case <-acknowledgement:
					w.WriteHeader(http.StatusNoContent)
				case <-ctx.Done():
				case <-r.Context().Done():
				}
				return
			}
			w.WriteHeader(http.StatusNoContent)
			finish.Do(func() { close(done) })
			return
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/builds") && !strings.HasSuffix(r.URL.Path, "/deployments/from-build") && !strings.HasSuffix(r.URL.Path, "/routes") {
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
			recordsMu.Lock()
			buildIDs = append(buildIDs, response.Build.ID)
			recordsMu.Unlock()
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
					recordsMu.Lock()
					cliOutput = append(cliOutput, out...)
					recordsMu.Unlock()
					credentialLeak := false
					for _, pattern := range patterns {
						if strings.Contains(string(out), pattern) {
							credentialLeak = true
							runErr = fmt.Errorf("known credential in CLI output")
							out = nil
						}
					}
					err = runErr
					if err != nil && !credentialLeak && item.kind == "build" && failureEnabled.Load() {
						full, readErr := f.pg.GetHostingBuild(ctx, "usr_bootstrap", item.id)
						if readErr == nil && full.State == "failed" && full.Artifact == nil && full.FailureCode == "build_failed" {
							err = nil
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
	metadata, err := json.Marshal(map[string]any{"origin": origin, "project_id": f.environment.ProjectID, "environment": f.environment.Name, "complete_path": completePath, "secret_id": secret.ID, "secret_version": int64(1), "source_repository_url": repositoryURL, "strategy": strategy, "destination_id": f.target.Destination.ID, "failure_path": failurePath})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PRIVATE_GIT_BROWSER_READY %s\n", metadata)
journey:
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case err = <-results:
			t.Fatal(err)
		case ack := <-stages:
			recordsMu.Lock()
			count := len(buildIDs)
			recordsMu.Unlock()
			if failureEnabled.Load() || count != 1 {
				t.Fatal("unexpected private fixture stage order")
			}
			probe()
			updateGit("local-B\n", true)
			failureEnabled.Store(true)
			close(ack)
		case <-done:
			break journey
		}
	}
	cancel()
	<-workerDone
	recordsMu.Lock()
	ids := append([]string(nil), buildIDs...)
	output := append([]byte(nil), cliOutput...)
	recordsMu.Unlock()
	if len(ids) != 2 || !failureEnabled.Load() {
		t.Fatal("browser did not complete success and genuine failure builds")
	}
	for i, id := range ids {
		full, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (full.State != "succeeded" || full.Artifact == nil) {
			t.Fatal("browser success artifact missing")
		}
		if i == 1 && (full.State != "failed" || full.Artifact != nil || full.FailureCode != "build_failed") {
			t.Fatal("genuine failed build published an artifact or unexpected status")
		}
		privateRuntimeNoSecrets(t, f, id, patterns...)
		var typed domain.HostingBuildLogPage
		f.request(t, "GET", "/api/v1/hosting-builds/"+id+"/logs?limit=200", f.admin, nil, 200, &typed)
		raw, err := json.Marshal(typed)
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			if strings.Contains(string(raw), pattern) || strings.Contains(string(output), pattern) {
				t.Fatal("known credential reached typed output or compiled CLI")
			}
		}
	}
	probe()
	t.Logf("private %s browser: actual successful artifact/HTTPS, genuine failed build without artifact preserves old HTTPS bytes; known credentials absent logs/audit/journal/typed output/CLI; source cleanup passed", strategy)
}
