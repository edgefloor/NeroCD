//go:build linux && nerocd_proxy_fixture

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingfixture"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerclient"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/source"
	"nerocd/internal/store"
)

// This adapter supplies only the operator-owned disposable source network
// policy. Resource validation, grants, build receipts and admission remain real.
type gitComposeRuntimeHosting struct{ *store.PostgresStore }

func (h gitComposeRuntimeHosting) CreateResource(ctx context.Context, r domain.Resource, opts ...store.MutationOption) (domain.Resource, error) {
	if r.Source.Git != nil {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: gitComposeRuntimePolicy()}))
	}
	return h.PostgresStore.CreateResource(ctx, r, opts...)
}
func (h gitComposeRuntimeHosting) UpdateResource(ctx context.Context, r domain.Resource, expected int64, opts ...store.MutationOption) (domain.Resource, error) {
	previous, err := h.GetResource(ctx, r.ID)
	if err != nil {
		return domain.Resource{}, err
	}
	if r.Source.Git != nil && domain.HostingDocumentHash(previous.Source) != domain.HostingDocumentHash(r.Source) {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: gitComposeRuntimePolicy()}))
	}
	return h.PostgresStore.UpdateResource(ctx, r, expected, opts...)
}
func gitComposeRuntimePolicy() source.RepositoryPolicy {
	return source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.com"}, AllowedCIDRs: []string{"127.0.0.0/8"}}
}

// The existing authenticated registry remains the fixed backend. This TLS
// listener supplies the same trust boundary a publicly trusted registry uses,
// through the production operator CA configuration rather than insecure flags.
func gitComposeRuntimeRegistryTLS(t *testing.T, backendPassword, firstPassword, secondPassword string) (string, string) {
	t.Helper()
	gateway := strings.TrimSpace(string(storageRuntimeDocker(t, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")))
	ip := net.ParseIP(gateway)
	if ip == nil || ip.IsLoopback() || ip.To4() == nil {
		t.Fatal("registry bridge address unavailable")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NeroCD owned registry fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{ip}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "registry-ca.pem")
	if err = os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse("http://127.0.0.1:25001")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) { original(r); r.SetBasicAuth("fixture", backendPassword) }
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		valid := subtle.ConstantTimeCompare([]byte(password), []byte(firstPassword)) | subtle.ConstantTimeCompare([]byte(password), []byte(secondPassword))
		if !ok || username != "build-fixture" || valid != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NeroCD build registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", "0.0.0.0:25002")
	if err != nil {
		t.Fatal("owned TLS registry port unavailable", err)
	}
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return net.JoinHostPort(gateway, "25002"), path
}

// This bounded probe uses the production builder, authentication callback and
// operator CA map without starting an application, browser or API controller.
func TestGitComposePrivateBaseEngine(t *testing.T) {
	if os.Getenv("NEROCD_GIT_COMPOSE_BASE_PROBE") != "disposable-dind" {
		t.Skip("requires owned private base probe")
	}
	root := t.TempDir()
	f := &storageRuntimeFixture{t: t, root: root}
	backendPassword, firstPassword, secondPassword := hostingStorageRemoteRuntimeRandom(t), hostingStorageRemoteRuntimeRandom(t), hostingStorageRemoteRuntimeRandom(t)
	registry, ca := gitComposeRuntimeRegistryTLS(t, backendPassword, firstPassword, secondPassword)
	repository := registryRuntimeProtectedImages(t, f, backendPassword, gitComposeFixtureBase)
	repository = strings.Replace(repository, "127.0.0.1:25001", registry, 1)
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "Dockerfile"), []byte("FROM "+repository+":a\nLABEL nerocd.fixture.privatebase=verified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: hostingbuild.BuildKitImage, StateDir: filepath.Join(root, "state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT"), RegistryCACertificates: map[string]string{registry: ca}}
	engine, err := hostingbuild.New(config)
	if err != nil {
		t.Fatal(err)
	}
	daemon := strings.TrimSpace(string(storageRuntimeDocker(t, "info", "--format", "{{.ID}}")))
	request := hostingbuild.Request{BuildID: "private-base-probe", Checkout: checkout, SourceID: "fixture-source", InputID: "fixture-input", DaemonID: daemon, Strategy: hostingbuild.Dockerfile, Platform: "linux/" + runtime.GOARCH, BuildRegistry: &hostingbuild.RegistryReference{SecretID: "fixture-build-registry", Version: 1}, Limits: hostingbuild.Limits{CPUQuota: 100000, MemoryBytes: 1 << 30, Deadline: 2 * time.Minute, ContextBytes: 1 << 20, ContextFiles: 10, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 512 << 20, UnpackedBytes: 1 << 30, UsageBytes: 3 << 30, ReserveBytes: 256 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}}
	deliveries := 0
	provider := func(context.Context) (hostingbuild.RegistryCredential, error) {
		deliveries++
		return hostingbuild.RegistryCredential{Registry: registry, Username: "build-fixture", Password: firstPassword}, nil
	}
	result, err := engine.BuildWithRegistryCredential(t.Context(), request, nil, provider)
	if err != nil {
		t.Fatal("actual private TLS base build", err)
	}
	if result.ImageID == "" || deliveries != 1 {
		t.Fatal("private base receipt or one-use delivery missing")
	}
	replay, err := engine.BuildWithRegistryCredential(t.Context(), request, nil, provider)
	if err != nil || replay != result || deliveries != 1 {
		t.Fatal("receipt replay reacquired credential or changed result", err)
	}
	request.BuildID = "private-base-pre-builder"
	request.DaemonID = "wrong-daemon"
	if _, err := engine.BuildWithRegistryCredential(t.Context(), request, nil, provider); err == nil {
		t.Fatal("wrong daemon accepted")
	}
	if _, err := os.Lstat(filepath.Join(config.StateDir, request.BuildID, "docker", "config.json")); !os.IsNotExist(err) {
		t.Fatal("early failure retained registry config", err)
	}
	request.DaemonID = daemon
	request.BuildID = "private-base-denied"
	request.BuildRegistry = &hostingbuild.RegistryReference{SecretID: "fixture-wrong-registry", Version: 1}
	denied, err := engine.BuildWithRegistryCredential(t.Context(), request, nil, func(context.Context) (hostingbuild.RegistryCredential, error) {
		return hostingbuild.RegistryCredential{Registry: registry, Username: "build-fixture", Password: "incorrect"}, nil
	})
	if err == nil || denied.ImageID != "" {
		t.Fatal("incorrect private base credential produced an image")
	}
	for _, id := range []string{"private-base-probe", "private-base-denied"} {
		if _, err := os.Lstat(filepath.Join(config.StateDir, id, "docker", "config.json")); !os.IsNotExist(err) {
			t.Fatal("build registry material retained", err)
		}
	}
	t.Log("actual pinned builder verified private TLS FROM, exact operator CA, isolated auth, metadata-only replay and denied fresh replacement")
}

func configureGitComposeRuntime(t *testing.T, f *storageRuntimeFixture, pg *store.PostgresStore) {
	t.Helper()
	sources := filepath.Join(f.root, "sources")
	if err := os.Mkdir(sources, 0700); err != nil {
		t.Fatal(err)
	}
	engine := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE"), StateDir: filepath.Join(f.root, "build-state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}
	if host := os.Getenv("NEROCD_GIT_COMPOSE_REGISTRY_HOST"); host != "" {
		engine.RegistryCACertificates = map[string]string{host: os.Getenv("NEROCD_GIT_COMPOSE_REGISTRY_CA")}
	}
	identity, err := runnerruntime.ComputeBuildToolchainIdentity(t.Context(), engine, "linux/"+runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	policy := gitComposeRuntimeBuildPolicy(identity)
	if err = pg.ConfigureHostingBuild(&policy); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"version": 1, "compose": true, "private_git": runnerruntime.PrivateGitConfig{}, "source_root": sources, "engine": map[string]any{"registry_ca_certificates": engine.RegistryCACertificates, "docker_binary": engine.DockerBinary, "socket": engine.Socket, "buildx_binary": engine.BuildxBinary, "buildx_sha256": engine.BuildxSHA256, "planner_image": engine.PlannerImage, "state_dir": engine.StateDir, "cgroup_root": engine.CgroupRoot}, "target": map[string]string{"runner_id": "runner_storage_runtime_fixture", "server_id": f.serverID, "destination_id": f.destinationID}, "policy": policy, "status_interval_ns": 200 * time.Millisecond, "request_timeout_ns": 5 * time.Second}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.buildConfig = filepath.Join(f.root, "build.json")
	if err = os.WriteFile(f.buildConfig, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func gitComposeRuntimeBuildPolicy(identity string) domain.HostingBuildPolicy {
	return domain.HostingBuildPolicy{Identity: "sha256:" + strings.Repeat("a", 64), ToolchainIdentity: identity, Protocol: domain.HostingBuildProtocol, Platform: "linux/" + runtime.GOARCH, GitCompose: true, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 1 << 30, Deadline: 4 * time.Minute, ContextBytes: 8 << 20, ContextFiles: 1000, PlanBytes: 1 << 20, LogBytes: 8 << 20, ExportBytes: 512 << 20, UnpackedBytes: 1 << 30, UsageBytes: 3 << 30, ReserveBytes: 256 << 20, PollInterval: 100 * time.Millisecond, DiskMode: "monitored"}, SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 8 << 20, MaxWorkspaceBytes: 32 << 20, MinFreeBytes: 256 << 20, MaxEntries: 1000, MaxDepth: 32, PollInterval: 100 * time.Millisecond}}
}

func gitComposeRuntimeRepository(t *testing.T, raw, base, buildBase, password string) (string, func(string, bool, string) string, func(bool), string) {
	t.Helper()
	files := func(marker string, fail bool, registryImage string) map[string]string {
		return gitComposeFixtureFiles(raw, base, buildBase, marker, fail, registryImage)
	}
	url, update, unavailable := hostingfixture.GitHTTPS(t, files("A", false, ""), "fixture-user", password)
	return strings.Replace(url, "127.0.0.1", "example.com", 1), func(marker string, fail bool, registryImage string) string {
		return update(files(marker, fail, registryImage))
	}, unavailable, update(nil)
}

func gitComposeRuntimeInstall(f *storageRuntimeFixture, resource domain.Resource) {
	t := f.t
	t.Helper()
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resource.ID+"/binding", map[string]string{"destination_id": f.destinationID}, 201)
	entries := []domain.HostingRuntimeEntry{}
	for name, value := range map[string]string{"SERVICE_URL_VIKUNJA": "https://catalog-runtime.example.test:25443", "SERVICE_USER_POSTGRESQL": "vikunja", "POSTGRESQL_DATABASE": "vikunja"} {
		v := value
		entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "literal", Value: &v})
	}
	for _, name := range []string{"SERVICE_PASSWORD_POSTGRESQL", "SERVICE_PASSWORD_JWTSECRET"} {
		secret := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", map[string]string{"project_id": resource.ProjectID, "name": "Git Compose " + name, "value": hostingStorageRemoteRuntimeRandom(t)}, 201)
		entries = append(entries, domain.HostingRuntimeEntry{Name: name, Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: secret.ID, Version: 1}})
	}
	current := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resource.ID+"/runtime-configuration", nil, 200)
	storageRuntimeJSON[json.RawMessage](f, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: current.Configuration.RuntimeGeneration, RequestID: strings.Repeat("c", 32), Entries: entries}, 200)
}
func gitComposeRuntimeBuild(f *storageRuntimeFixture, resource domain.Resource, grant, key, want string) domain.PublicHostingBuild {
	f.t.Helper()
	result := storageRuntimeJSON[struct {
		Build domain.PublicHostingBuild `json:"build"`
	}](f, "POST", "/api/v1/resources/"+resource.ID+"/builds", domain.HostingBuildRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, GrantID: grant, IdempotencyKey: key}, 202)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		result = storageRuntimeJSON[struct {
			Build domain.PublicHostingBuild `json:"build"`
		}](f, "GET", "/api/v1/hosting-builds/"+result.Build.ID, nil, 200)
		if result.Build.State == want {
			return result.Build
		}
		if result.Build.State == "failed" || result.Build.State == "succeeded" || result.Build.State == "canceled" {
			code := result.Build.FailureCode
			if code != "" && !domain.ValidHostingBuildFailure(code) {
				code = "unrecognized"
			}
			f.t.Fatalf("Git Compose build settled unexpectedly: state=%s failure_code=%s source_receipt=%t compose_result=%t image_artifact=%t", result.Build.State, code, result.Build.SourceReceipt != nil, result.Build.ComposeResult != nil, result.Build.Artifact != nil)
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("Git Compose build did not settle")
	return domain.PublicHostingBuild{}
}

func TestGitComposeFixtureStartupPolicyAndAdmission(t *testing.T) {
	t.Setenv("NEROCD_GIT_COMPOSE_RUNTIME", "disposable-dind")
	t.Setenv("NEROCD_CATALOG_BROWSER_ORIGIN", "https://127.0.0.1:25444")
	_, pg, database := newHostingBuildAPIFixture(t)
	server := catalogRuntimeServer(t, pg)
	control := httptest.NewTLSServer(server)
	t.Cleanup(control.Close)
	f := &storageRuntimeFixture{t: t, control: control, client: control.Client()}
	f.token = createTestSession(t, server)
	env := storageRuntimeJSON[domain.ProjectEnvironment](f, "POST", "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "Git Compose startup"}, 201)
	resource := storageRuntimeJSON[domain.Resource](f, "POST", "/api/v1/resources", gitComposeFixtureCreateRequest{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "Compose Vikunja acceptance", Kind: "application", Source: gitComposeFixtureSource{Kind: "git", Git: &domain.GitResourceConfiguration{RepositoryURL: "https://example.com:443/repo.git", RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: "compose", ContextDir: "project", ComposeFile: "deploy/compose.yaml", PublicComponent: "vikunja", ContainerPort: 3456, BuildVariables: []domain.HostingBuildVariable{{Name: "CONFIGURED", Value: "operator-value"}, {Name: "MULTILINE", Value: gitComposeFixtureMultiline}}}}}}, 201)
	// Keep the fixture's current credential-file capability profile. Trigger
	// revision support remains optional for existing registered build runners.
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType, domain.HostingPrivateGitCapability, domain.HostingGitComposeBuildCapability, domain.HostingGitComposeArgsCapability, domain.HostingBuildRegistryCapability}
	enrollment := storageRuntimeJSON[app.CreatedRunnerEnrollment](f, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_storage_runtime_fixture", RunnerName: "owned storage fixture", Capabilities: caps}, 201)
	credential := "ncr_git_compose_startup_fixture"
	enrolled := storageRuntimeAuthorizedJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("e", 32), CredentialHash: fmt.Sprintf("%x", sha256.Sum256([]byte(credential)))}, 200)
	client := runnerclient.New(control.URL, credential)
	client.HTTP = control.Client()
	id, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{Build: true, ComposeBuild: true, PrivateGit: true})
	if err != nil || id != enrolled.Runner.ID {
		t.Fatalf("actual enrolled Git build identity: id=%q error=%v", id, err)
	}
	target := storageRuntimeJSON[app.ServerDetail](f, "POST", "/api/v1/servers", app.ServerInput{Name: "Git Compose startup", RunnerID: id}, 201)
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/destinations/"+target.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: resource.ProjectID}, 201)
	f.serverID, f.destinationID = target.Server.ID, target.Destination.ID
	gitComposeRuntimeInstall(f, resource)
	emptyGrants := storageRuntimeJSON[struct {
		Items []domain.PublicHostingSourceGrant `json:"items"`
	}](f, "GET", "/api/v1/resources/"+resource.ID+"/source-grants?limit=20&offset=0", nil, 200)
	if len(emptyGrants.Items) != 0 {
		t.Fatal("unexpected initial source grants")
	}
	for {
		tx, e := database.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		changed, e := hostingjobs.MigrateNextTx(t.Context(), tx)
		if e != nil {
			if rollbackErr := storageRuntimeRollback(tx); rollbackErr != nil {
				t.Errorf("queue migration rollback: %v", rollbackErr)
			}
			t.Fatal(e)
		}
		if e = tx.Commit(t.Context()); e != nil {
			t.Fatal(e)
		}
		if !changed {
			break
		}
	}
	policy := gitComposeRuntimeBuildPolicy("sha256:" + strings.Repeat("d", 64))
	startStorageRuntimeExecution(t, pg, func() {
		if err := pg.ConfigureHostingBuild(&policy); err != nil {
			t.Fatal("Git Compose policy must be configured before worker startup:", err)
		}
	})
	if err := pg.ConfigureHostingBuild(&policy); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("late policy mutation must remain rejected: %v", err)
	}
	secret := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: resource.ProjectID, Name: "Git Compose source", Value: `{"username":"fixture-user","password":"fixture-password"}`}, 201)
	grant := storageRuntimeJSON[struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}](f, "POST", "/api/v1/resources/"+resource.ID+"/source-grants", domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, SecretID: secret.ID, SecretVersion: 1, RequestID: strings.Repeat("b", 32)}, 201)
	current := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+resource.ID, nil, 200)
	resources := storageRuntimeJSON[struct {
		Items []domain.Resource `json:"items"`
	}](f, "GET", "/api/v1/resources?project_id="+resource.ProjectID+"&project_environment_id="+resource.ProjectEnvironmentID, nil, 200)
	var listedResource *domain.Resource
	for i := range resources.Items {
		if resources.Items[i].ID == resource.ID {
			listedResource = &resources.Items[i]
			break
		}
	}
	if listedResource == nil {
		t.Fatal("created resource missing from actual UI list")
	}
	if listedResource.Source.Kind != current.Source.Kind || listedResource.Source.Git == nil || current.Source.Git == nil || !reflect.DeepEqual(listedResource.Source.Git, current.Source.Git) {
		t.Fatal("resource list source differs from actual saved source")
	}

	listed := storageRuntimeJSON[struct {
		Items []domain.PublicHostingSourceGrant `json:"items"`
	}](f, "GET", "/api/v1/resources/"+resource.ID+"/source-grants?limit=20&offset=0", nil, 200)
	if len(listed.Items) != 1 {
		t.Fatal("eligible grant list must contain the created grant")
	}
	g := listed.Items[0]
	if current.Source.Kind != "git" || current.Source.Git == nil || current.Source.Git.CredentialKind != "https_basic" || g.ResourceID != current.ID || g.SourceID != current.Source.Git.SourceID || g.SourceGeneration <= 0 || g.SourceGeneration != current.Source.Git.Generation || g.Version != 1 || g.Kind != "https_basic" || g.Revoked || g != grant.Grant {
		t.Fatal("real listed grant fails UI eligibility shape")
	}
	build := storageRuntimeJSON[struct {
		Build domain.PublicHostingBuild `json:"build"`
	}](f, "POST", "/api/v1/resources/"+resource.ID+"/builds", domain.HostingBuildRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, GrantID: grant.Grant.ID, IdempotencyKey: "git-compose-startup"}, 202).Build
	deadline := time.Now().Add(10 * time.Second)
	for build.RunID == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		build = storageRuntimeJSON[struct {
			Build domain.PublicHostingBuild `json:"build"`
		}](f, http.MethodGet, "/api/v1/hosting-builds/"+build.ID, nil, 200).Build
	}
	if build.RunID == "" {
		t.Fatal("configured Git Compose build was not dispatched")
	}
	claim, err := server.app.ClaimRun(auth.WithPrincipal(t.Context(), auth.Principal{ID: id, Provider: domain.PrincipalRunner}))
	if err != nil || claim.Run.ID != build.RunID {
		t.Fatalf("Git Compose build claim mismatch: %v", err)
	}
}

// This bounded prerequisite uses the exact constructor and compiled CLI used by
// the application qualification, without queuing a build or deploying a stack.
func TestGitComposeFixtureCompiledRunnerStartup(t *testing.T) {
	if os.Getenv("NEROCD_GIT_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact approved Git Compose runtime profile")
	}
	f := newStorageRuntimeFixture(t, true)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if exited, category, code := storageRuntimeRunnerExit(f.process); exited {
			t.Fatalf("compiled Git runner exited before polling: category=%s code=%d", category, code)
		}
		if f.heartbeats.Load() >= 2 && f.emptyClaims.Load() >= 2 {
			if f.archiveConfig != "" {
				t.Fatal("Git profile unexpectedly configures standalone archive execution")
			}
			for _, capability := range f.capabilities {
				switch capability {
				case domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, domain.RunTypeStorageBackupTransfer,
					domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability:
					t.Fatal("Git profile unexpectedly advertises standalone archive/database execution")
				}
			}
			t.Log("assembled Git CLI authenticated, completed two heartbeat/empty-claim cycles, and remains alive")
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("assembled Git CLI did not complete heartbeat/empty-claim polling")
}

// This isolates the real compiled runner's private source/build receipt without
// the browser, deployment, routing, or application startup qualification.
func TestGitComposeFixtureCompiledRunnerPrivateBuild(t *testing.T) {
	if os.Getenv("NEROCD_GIT_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact approved Git Compose runtime profile")
	}
	raw, err := os.ReadFile("../../examples/compose/vikunja-postgresql.yaml")
	if err != nil {
		t.Fatal(err)
	}
	password := hostingStorageRemoteRuntimeRandom(t)
	repository, _, _, commit := gitComposeRuntimeRepository(t, string(raw), gitComposeFixtureBase, gitComposeFixtureBase, password)
	f := newStorageRuntimeFixture(t, true)
	resource := storageRuntimeJSON[domain.Resource](f, "POST", "/api/v1/resources", gitComposeFixtureCreateRequest{
		ProjectID: "proj_platform", ProjectEnvironmentID: f.environmentID, Name: "Git Compose build prerequisite", Kind: "application",
		Source: gitComposeFixtureSource{Kind: "git", Git: &domain.GitResourceConfiguration{RepositoryURL: repository, RequestedRef: "main", CredentialKind: "https_basic", Settings: domain.HostingBuildSettings{Strategy: "compose", ContextDir: "project", ComposeFile: "deploy/compose.yaml", PublicComponent: "vikunja", ContainerPort: 3456, BuildVariables: []domain.HostingBuildVariable{{Name: "CONFIGURED", Value: "operator-value"}, {Name: "MULTILINE", Value: gitComposeFixtureMultiline}}}}},
	}, 201)
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destinationID}, 201)
	credential, err := json.Marshal(map[string]string{"username": "fixture-user", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	secret := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: resource.ProjectID, Name: "Git Compose build prerequisite", Value: string(credential)}, 201)
	grant := storageRuntimeJSON[struct {
		Grant domain.PublicHostingSourceGrant `json:"grant"`
	}](f, "POST", "/api/v1/resources/"+resource.ID+"/source-grants", domain.HostingSourceGrantRequest{ExpectedResourceGeneration: resource.Generation, ExpectedSourceGeneration: resource.Source.Git.Generation, SecretID: secret.ID, SecretVersion: 1, RequestID: strings.Repeat("b", 32)}, 201)
	built := gitComposeRuntimeBuild(f, resource, grant.Grant.ID, "git-compose-build-prerequisite", "succeeded")
	if built.SourceReceipt == nil || built.SourceReceipt.Commit != commit || built.ComposeResult == nil || built.Artifact != nil {
		t.Fatal("private mixed Compose build omitted exact immutable source/result")
	}
	t.Log("private mixed Compose source/build receipt succeeded without deploying or starting an application")
}
