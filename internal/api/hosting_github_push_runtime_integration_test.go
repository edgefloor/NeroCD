package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
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
	"nerocd/internal/githubpush"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/source"
	"nerocd/internal/store"
	"nerocd/web"
)

type pushHTTPFixture struct {
	t                                     *testing.T
	pg                                    *store.PostgresStore
	db                                    *pgxpool.Pool
	service                               *app.Service
	api                                   *Server
	resource                              domain.Resource
	target                                app.ServerDetail
	signing, provider                     domain.ManagedSecret
	accountID, owner, viewer, runnerToken string
	mu                                    sync.Mutex
	head, providerFailure                 string
	secret                                string
}

func pushTestPolicy() domain.HostingBuildPolicy {
	d := "sha256:" + strings.Repeat("a", 64)
	return domain.HostingBuildPolicy{Identity: d, ToolchainIdentity: d, Protocol: domain.HostingBuildProtocol, Platform: "linux/amd64", SourceLimits: domain.HostingSourceLimits{MaxTreeBytes: 1 << 20, MaxWorkspaceBytes: 20 << 20, MinFreeBytes: 1 << 20, MaxEntries: 100, MaxDepth: 32, PollInterval: time.Second}, Limits: domain.HostingBuildLimits{CPUQuota: 100000, MemoryBytes: 512 << 20, Deadline: time.Minute, ContextBytes: 1 << 20, ContextFiles: 100, PlanBytes: 1 << 20, LogBytes: 1 << 20, ExportBytes: 20 << 20, UnpackedBytes: 20 << 20, UsageBytes: 40 << 20, ReserveBytes: 10 << 20, PollInterval: time.Second, DiskMode: "monitored"}}
}

type pushFixtureOptions struct {
	policy      *domain.HostingBuildPolicy
	head        string
	internalGit bool
}

// pushFixtureHosting changes only trusted fixture routing, retaining the real
// repository's authorization, generations, audit and transaction behavior.
type pushFixtureHosting struct{ store.HostingRepository }

func (h pushFixtureHosting) CreateResource(ctx context.Context, r domain.Resource, opts ...store.MutationOption) (domain.Resource, error) {
	if r.Source.Kind == "git" && r.Source.Git != nil {
		g := r.Source.Git
		opts = append(opts, store.WithHostingGitSource(domain.HostingGitSource{ID: g.SourceID, ResourceID: r.ID, ProjectID: r.ProjectID, Generation: g.Generation, RepositoryURL: g.RepositoryURL, RequestedRef: g.RequestedRef, CredentialKind: g.CredentialKind, Settings: g.Settings, Policy: source.RepositoryPolicy{Version: 1, State: "configured", Mode: "internal", AllowInternal: true, AllowedSchemes: []string{"https"}, AllowedHosts: []string{"github.com"}, AllowedCIDRs: []string{"127.0.0.0/8"}}}))
	}
	return h.HostingRepository.CreateResource(ctx, r, opts...)
}
func newPushHTTPFixture(t *testing.T, options ...pushFixtureOptions) *pushHTTPFixture {
	t.Helper()
	_, pg, database := newHostingBuildAPIFixture(t)
	ring, err := secretcrypto.New("push", map[string][]byte{"push": bytes.Repeat([]byte{8}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	var option pushFixtureOptions
	if len(options) > 0 {
		option = options[0]
	}
	var hosting store.HostingRepository = pg
	if option.internalGit {
		hosting = pushFixtureHosting{pg}
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: hosting, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	f := &pushHTTPFixture{t: t, pg: pg, db: database, service: service, head: strings.Repeat("a", 40), secret: strings.Repeat("s", 48), runnerToken: "ncr_push_fixture_runner"}
	if option.head != "" {
		f.head = option.head
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(f.providerRequest))
	t.Cleanup(provider.Close)
	client := provider.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	address := strings.TrimPrefix(provider.URL, "https://")
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client.Transport = transport
	verified, err := githubpush.New(client)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureHostingGitHubPush(verified)
	f.api = NewServerWithConfig(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: "https://control.example"})
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := service.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "push-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	f.resource, err = service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "GitHub push application", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/acme/push-fixture.git", RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_push_fixture", RunnerName: "push runner", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType, domain.HostingBuildTriggerRevisionCapability, domain.HostingReleasePortsCapability, domain.RunTypeLocalBuildDeploy}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(f.runnerToken))
	enrolled, err := service.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("f", 32), CredentialHash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	f.target, err = service.CreateServer(admin, app.ServerInput{Name: "push fixture", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantDestinationProject(admin, f.target.Destination.ID, app.DestinationProjectInput{ProjectID: env.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindResourceDestination(admin, f.resource.ID, app.ResourceBindingInput{DestinationID: f.target.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	account, err := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: env.ProjectID, Name: "push-build-automation", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	f.accountID = account.ServiceAccount.ID
	f.signing, err = service.CreateManagedSecret(admin, app.ManagedSecretInput{ProjectID: env.ProjectID, Name: "push-webhook-signing", Value: f.secret})
	if err != nil {
		t.Fatal(err)
	}
	f.provider, err = service.CreateManagedSecret(admin, app.ManagedSecretInput{ProjectID: env.ProjectID, Name: "push-provider-read", Value: `{"username":"fixture","password":"push-provider-fixture"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "push_viewer", ProjectID: env.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	f.owner = createTestSession(t, f.api)
	f.viewer = createTestSessionFor(t, f.api, "viewer@example.local", "viewer")
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
	policy := pushTestPolicy()
	if option.policy != nil {
		policy = *option.policy
	}
	if err = pg.ConfigureHostingBuild(&policy); err != nil {
		t.Fatal(err)
	}
	if err = pg.ConfigureGitHubPushProcessor(service.ProcessGitHubPushDelivery); err != nil {
		t.Fatal(err)
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(ctx); e != nil {
			t.Error(e)
		}
	})
	return f
}
func (f *pushHTTPFixture) providerRequest(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Host != "api.github.com" || r.Header.Get("X-GitHub-Api-Version") != githubpush.APIVersion {
		f.t.Error("provider host/version changed")
	}
	if r.Header.Get("Authorization") != "Bearer push-provider-fixture" {
		f.t.Error("provider credential purpose not selected")
	}
	if f.providerFailure != "" {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	var result any
	switch {
	case r.URL.Path == "/repos/acme/push-fixture":
		result = map[string]any{"id": 42, "full_name": "acme/push-fixture", "private": false, "fork": false}
	case r.URL.Path == "/repos/acme/push-fixture/branches/main":
		result = map[string]any{"name": "main", "commit": map[string]string{"sha": f.head}}
	case strings.HasPrefix(r.URL.Path, "/repos/acme/push-fixture/compare/"):
		identity := strings.TrimPrefix(r.URL.Path, "/repos/acme/push-fixture/compare/")
		base, head, ok := strings.Cut(identity, "...")
		if !ok || head != f.head || r.URL.RawQuery != "per_page=100&page=1" {
			f.t.Error("compare request not immutable")
		}
		result = map[string]any{"base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": base}, "status": "ahead", "ahead_by": 1, "behind_by": 0, "total_commits": 1, "commits": []map[string]string{{"sha": head}}}
	default:
		f.t.Error("unexpected provider path", r.URL.Path)
		w.WriteHeader(404)
		return
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		f.t.Error(err)
	}
}
func (f *pushHTTPFixture) request(method, path, token string, body any, want int, out any) []byte {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.api.ServeHTTP(rec, req)
	if rec.Code != want {
		f.t.Fatalf("%s %s got%d want%d %s", method, path, rec.Code, want, rec.Body.String())
	}
	if out != nil && json.Unmarshal(rec.Body.Bytes(), out) != nil {
		f.t.Fatal("invalid actual API response")
	}
	return rec.Body.Bytes()
}
func pushAPIUUID(n int) string { return fmt.Sprintf("10000000-0000-0000-0000-%012x", n) }
func (f *pushHTTPFixture) configured(t *testing.T) domain.GitHubPushPolicy {
	t.Helper()
	path := "/api/v1/resources/" + f.resource.ID + "/github-push-policy"
	var p domain.GitHubPushPolicy
	f.request("GET", path, f.owner, nil, 200, &p)
	if p.ID != "" || p.Status != "unconfigured" {
		t.Fatal("absent policy synthesized")
	}
	in := domain.GitHubPushConfigure{ExpectedResourceGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, RequestID: pushAPIUUID(1), ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: f.accountID, WebhookSecret: domain.GitHubPushSecretRef{ID: f.signing.ID, Version: 1}, ProviderReadSecret: &domain.GitHubPushSecretRef{ID: f.provider.ID, Version: 1}}}
	f.request("PUT", path, f.viewer, in, 403, nil)
	f.request("PUT", path, f.owner, in, 200, &p)
	if p.Enabled || p.OwnerConfiguration == nil || p.CallbackURL != "https://control.example/api/v1/hosting/github-push/"+p.ID {
		t.Fatal("configuration/callback", p)
	}
	f.request("POST", path+"/verify", f.owner, domain.GitHubPushCommand{ExpectedPolicyGeneration: p.Generation, RequestID: pushAPIUUID(2)}, 200, &p)
	if p.Status != "verified" || p.DesiredCommit != strings.Repeat("a", 40) {
		t.Fatal("verify", p)
	}
	f.request("POST", path+"/enabled", f.owner, domain.GitHubPushEnable{ExpectedPolicyGeneration: p.Generation, RequestID: pushAPIUUID(3), Enabled: true}, 200, &p)
	return p
}
func (f *pushHTTPFixture) push(p domain.GitHubPushPolicy, n int, after string) []byte {
	f.t.Helper()
	raw, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "before": strings.Repeat("a", 40), "after": after, "deleted": false, "forced": false, "repository": map[string]any{"id": 42, "fork": false, "url": "https://untrusted.invalid"}})
	mac := hmac.New(sha256.New, []byte(f.secret))
	_, _ = mac.Write(raw)
	req := httptest.NewRequest("POST", "/api/v1/hosting/github-push/"+p.ID, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Delivery", pushAPIUUID(n))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	f.api.ServeHTTP(rec, req)
	if rec.Code != 202 {
		f.t.Fatal(rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}
func (f *pushHTTPFixture) delivery(n int, state string) domain.GitHubPushDelivery {
	f.t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		page, err := f.pg.ListGitHubPushDeliveries(f.t.Context(), "usr_bootstrap", f.resource.ID, "", 50)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, d := range page.Items {
			if d.ID == pushAPIUUID(n) && d.State == state {
				return d
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("durable delivery did not reach", state)
	return domain.GitHubPushDelivery{}
}
func TestGitHubPushActualRegisteredAPIAndRiver(t *testing.T) {
	f := newPushHTTPFixture(t)
	p := f.configured(t)
	b := strings.Repeat("b", 40)
	f.mu.Lock()
	f.head = b
	f.mu.Unlock()
	first := f.push(p, 10, b)
	same := f.push(p, 10, b)
	if !bytes.Equal(first, same) {
		t.Fatal("delivery replay changed admission receipt")
	}
	delivery := f.delivery(10, "queued")
	build, err := f.pg.GetHostingBuild(t.Context(), "usr_bootstrap", delivery.BuildID)
	if err != nil || build.Snapshot.TriggerRevision == nil || build.Snapshot.TriggerRevision.ExpectedCommit != b {
		t.Fatal("actual API lost signed revision", err)
	}
	f.push(p, 11, strings.Repeat("a", 40))
	delayed := f.delivery(11, "ignored")
	if delayed.Reason != "obsolete" || delayed.BuildID != "" || delayed.VerifiedCommit != b {
		t.Fatal("delayed delivery", delayed)
	}
	f.push(p, 12, b)
	duplicate := f.delivery(12, "coalesced")
	if duplicate.BuildID != delivery.BuildID {
		t.Fatal("duplicate queued another build")
	}
	var viewer domain.GitHubPushPolicy
	raw := f.request("GET", "/api/v1/resources/"+f.resource.ID+"/github-push-policy", f.viewer, nil, 200, &viewer)
	if viewer.CanManage || viewer.OwnerConfiguration != nil || bytes.Contains(raw, []byte(f.signing.ID)) || viewer.DesiredCommit != b {
		t.Fatal("viewer configuration leaked or desired regressed")
	}
	f.mu.Lock()
	f.providerFailure = "rate_limited"
	f.mu.Unlock()
	f.push(p, 13, strings.Repeat("c", 40))
	failure := f.delivery(13, "retry_wait")
	if failure.Reason != "rate_limited" {
		t.Fatal(failure)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 1 {
		t.Fatal("provider failure admitted", count, err)
	}
	var metadata string
	if err = f.db.QueryRow(t.Context(), `SELECT coalesce(string_agg(metadata::text,''),'') FROM audit_events`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.secret, "push-provider-fixture", "X-Hub-Signature", "untrusted.invalid"} {
		if strings.Contains(metadata, secret) {
			t.Fatal("sensitive webhook data audited")
		}
	}
}

// pushRuntimeGit serves real Git CGI for the unchanged exact GitHub.com URL.
// DNS and CA changes exist only inside the disposable fixture process/container.
func pushRuntimeGit(t *testing.T) ([3]string, func(int)) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.local", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git failed %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	run(work, "init", "-q", "-b", "main")
	var commits [3]string
	for i, letter := range []string{"A", "B", "C"} {
		dockerfile := "FROM " + hostingbuild.StaticImage + "\nCOPY index.html /usr/share/nginx/html/index.html\n"
		if i == 2 {
			dockerfile += "RUN false\n"
		}
		if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(work, "index.html"), []byte("push fixture "+letter+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		run(work, "add", ".")
		run(work, "commit", "-qm", letter)
		commits[i] = run(work, "rev-parse", "HEAD")
	}
	acme := filepath.Join(root, "acme")
	if err = os.Mkdir(acme, 0700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(acme, "push-fixture.git")
	run(root, "clone", "--bare", work, bare)
	run(bare, "update-ref", "refs/heads/main", commits[0])
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(963), Subject: pkix.Name{CommonName: "owned GitHub fixture"}, DNSNames: []string{"github.com"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// All paths are generated fixture paths. No source URL or argument is rewritten.
	wrapper := "#!/bin/sh\nexec '" + git + "' -c http.sslCAInfo='" + ca + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return commits, func(i int) { run(bare, "update-ref", "refs/heads/main", commits[i]) }
}

type pushRunnerProcess struct {
	command *exec.Cmd
	done    chan error
	cancel  context.CancelFunc
}

func TestHostingGitHubPushCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_GITHUB_PUSH_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-github-push/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("disposable Linux fixture required")
	}
	signalCtx, stopSignals := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, 15*time.Minute)
	defer cancel()
	commits, move := pushRuntimeGit(t)
	work := t.TempDir()
	engine := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE"), StateDir: filepath.Join(work, "state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}
	policy := pushTestPolicy()
	policy.Platform = "linux/" + runtime.GOARCH
	policy.Limits.MemoryBytes = 1 << 30
	policy.Limits.Deadline = 3 * time.Minute
	policy.Limits.ExportBytes = 256 << 20
	policy.Limits.UnpackedBytes = 512 << 20
	policy.Limits.UsageBytes = 3 << 30
	policy.Limits.ReserveBytes = 128 << 20
	var err error
	policy.ToolchainIdentity, err = runnerruntime.ComputeBuildToolchainIdentity(ctx, engine, policy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	f := newPushHTTPFixture(t, pushFixtureOptions{policy: &policy, head: commits[0], internalGit: true})
	sourceRoot := filepath.Join(work, "sources")
	if err = os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	config := runnerruntime.BuildConfig{SourceRoot: sourceRoot, Engine: engine, Target: runnerruntime.BuildTarget{RunnerID: f.target.Server.RunnerID, ServerID: f.target.Server.ID, DestinationID: f.target.Destination.ID}, Policy: policy, StatusInterval: 200 * time.Millisecond, RequestTimeout: 5 * time.Second}
	if err = config.Validate(); err != nil {
		t.Fatal("enforced local build prerequisites", err)
	}
	document := map[string]any{"version": 1, "source_root": sourceRoot, "engine": map[string]string{"docker_binary": engine.DockerBinary, "socket": engine.Socket, "buildx_binary": engine.BuildxBinary, "buildx_sha256": engine.BuildxSHA256, "planner_image": engine.PlannerImage, "state_dir": engine.StateDir, "cgroup_root": engine.CgroupRoot}, "target": map[string]string{"runner_id": config.Target.RunnerID, "server_id": config.Target.ServerID, "destination_id": config.Target.DestinationID}, "policy": policy, "status_interval_ns": int64(config.StatusInterval), "request_timeout_ns": int64(config.RequestTimeout)}
	configFile := filepath.Join(work, "build.json")
	raw, _ := json.Marshal(document)
	if err = os.WriteFile(configFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(work, "credential")
	if err = os.WriteFile(credentialFile, []byte(f.runnerToken), 0600); err != nil {
		t.Fatal(err)
	}
	runnerAPI := httptest.NewTLSServer(f.api)
	defer runnerAPI.Close()
	ca := filepath.Join(work, "api-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: runnerAPI.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	logs := &runtimeFixtureLog{}
	var process *pushRunnerProcess
	stopRunner := func() error {
		if process == nil {
			return nil
		}
		process.cancel()
		select {
		case <-process.done:
			process = nil
			return nil
		case <-time.After(10 * time.Second):
			return fmt.Errorf("owned runner did not join")
		}
	}
	startRunner := func() error {
		runCtx, stop := context.WithCancel(ctx)
		cmd := exec.CommandContext(runCtx, "/fixture/nerocd", "runner", "--server", runnerAPI.URL, "--credential-file", credentialFile, "--build-config-file", configFile, "--local-build-deploy", "--journal-dir", filepath.Join(work, "journal"), "--work-dir", filepath.Join(work, "runner"), "--poll-interval", "100ms", "--cancel-poll-interval", "100ms")
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+ca, "NEROCD_MODE=development", "NEROCD_TOKEN=")
		cmd.Stdout = logs
		cmd.Stderr = logs
		if err := cmd.Start(); err != nil {
			stop()
			return err
		}
		process = &pushRunnerProcess{command: cmd, done: make(chan error, 1), cancel: stop}
		current := process
		go func() { current.done <- cmd.Wait() }()
		return nil
	}
	if err = startRunner(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := stopRunner(); e != nil {
			t.Error(e)
		}
	})
	// Readiness includes the enrolled compiled process's actual heartbeat.
	readyUntil := time.Now().Add(10 * time.Second)
	ready := false
	for !ready && time.Now().Before(readyUntil) {
		err = f.db.QueryRow(ctx, `SELECT last_heartbeat_at>registered_at FROM runners WHERE id=$1`, config.Target.RunnerID).Scan(&ready)
		if err != nil {
			t.Fatal(err)
		}
		if !ready {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !ready {
		t.Fatal("compiled runner not ready")
	}
	awaitBuild := func(commit, state string) domain.HostingBuild {
		t.Helper()
		until := time.Now().Add(4 * time.Minute)
		for time.Now().Before(until) {
			rows, e := f.pg.ListHostingBuilds(ctx, "usr_bootstrap", f.resource.ID, store.Page{Limit: 50})
			if e != nil {
				t.Fatal(e)
			}
			for _, b := range rows.Items {
				if b.SourceReceipt != nil && b.SourceReceipt.Commit == commit && b.State == state {
					return b
				}
				if b.Snapshot.TriggerRevision != nil && b.Snapshot.TriggerRevision.ExpectedCommit == commit && b.State == "failed" && state != "failed" {
					t.Fatalf("real build failed: reason=%s", b.FailureCode)
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture deadline")
			case <-time.After(50 * time.Millisecond):
			}
		}
		t.Fatal("real build did not reach", state)
		return domain.HostingBuild{}
	}
	// The browser drives only normal application APIs and a closed step enum.
	if os.Getenv("NEROCD_GITHUB_PUSH_BROWSER") == "1" {
		runPushBrowserFixture(t, ctx, f, commits, move, startRunner, stopRunner, awaitBuild)
		return
	}
	p := func() domain.GitHubPushPolicy {
		path := "/api/v1/resources/" + f.resource.ID + "/github-push-policy"
		var p domain.GitHubPushPolicy
		f.request("PUT", path, f.owner, domain.GitHubPushConfigure{ExpectedResourceGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, RequestID: pushAPIUUID(1), ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: f.accountID, WebhookSecret: domain.GitHubPushSecretRef{ID: f.signing.ID, Version: 1}, ProviderReadSecret: &domain.GitHubPushSecretRef{ID: f.provider.ID, Version: 1}}}, 200, &p)
		f.request("POST", path+"/verify", f.owner, domain.GitHubPushCommand{ExpectedPolicyGeneration: p.Generation, RequestID: pushAPIUUID(2)}, 200, &p)
		if p.DesiredCommit != commits[0] {
			t.Fatal("baseline did not bind actual Git commit")
		}
		f.request("POST", path+"/enabled", f.owner, domain.GitHubPushEnable{ExpectedPolicyGeneration: p.Generation, RequestID: pushAPIUUID(3), Enabled: true}, 200, &p)
		return p
	}()
	move(1)
	f.mu.Lock()
	f.head = commits[1]
	f.mu.Unlock()
	f.push(p, 10, commits[1])
	f.delivery(10, "queued")
	buildB := awaitBuild(commits[1], "succeeded")
	f.push(p, 11, commits[1])
	f.delivery(11, "coalesced")
	f.push(p, 12, commits[0])
	f.delivery(12, "ignored")
	var operation domain.HostingOperation
	f.request("POST", "/api/v1/resources/"+f.resource.ID+"/deployments/from-build", f.owner, domain.HostingBuildDeploymentInput{BuildID: buildB.ID, ExpectedGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, IdempotencyKey: "manual-B", ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 10}}, 202, &operation)
	until := time.Now().Add(time.Minute)
	for time.Now().Before(until) {
		operation, err = f.pg.GetHostingOperation(ctx, operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State == "succeeded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if operation.State != "succeeded" {
		t.Fatal("manual deployment failed", operation.State)
	}
	move(2)
	f.mu.Lock()
	f.head = commits[2]
	f.mu.Unlock()
	f.push(p, 13, commits[2])
	awaitBuild(commits[2], "failed")
	probePushApplication(t, ctx, f, buildB.ID)
	if err = stopRunner(); err != nil {
		t.Fatal(err)
	}
	if err = startRunner(); err != nil {
		t.Fatal(err)
	}
	probePushApplication(t, ctx, f, buildB.ID)
	t.Logf("simulated GitHub provider; real TLS Git/compiled runner/River: exactB=%s artifact=%s failedC=%s oldB still serves", commits[1], buildB.Artifact.ImageID, commits[2])
}

func probePushApplication(t *testing.T, ctx context.Context, f *pushHTTPFixture, buildID string) {
	t.Helper()
	var raw []byte
	if err := f.db.QueryRow(ctx, `SELECT s.healthy FROM hosting_binding_state s JOIN resource_bindings b ON b.id=s.binding_id WHERE b.resource_id=$1`, f.resource.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(raw, &healthy) != nil || healthy.Snapshot.LocalBuild.Artifact.BuildID != buildID || healthy.ContainerID == "" || healthy.Snapshot.HostPort < 1 {
		t.Fatal("healthy target does not bind exact B artifact")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	image, err := exec.CommandContext(commandCtx, "/usr/local/bin/docker", "inspect", "--format", "{{.Image}}", healthy.ContainerID).Output()
	if err != nil || strings.TrimSpace(string(image)) != healthy.Snapshot.LocalBuild.Artifact.ImageID {
		t.Fatal("serving container image differs from exact B artifact")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(healthy.Snapshot.HostPort)+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("independent declared HTTP probe unavailable", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || response.StatusCode != 200 || string(body) != "push fixture B\n" {
		t.Fatal("exact B application did not remain serving")
	}
}
func preparePushBrowserIdentity(t *testing.T, ctx context.Context, f *pushHTTPFixture) {
	t.Helper()
	// Development seeds create users, but only supported bootstrap completes the
	// durable lifecycle state consumed by the real sign-in page.
	if _, err := f.service.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "push-bootstrap@example.local", Name: "Push fixture bootstrap", Password: "push-browser-bootstrap-password"}); err != nil {
		t.Fatal(err)
	}
	password, err := auth.HashPassword("push-browser-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ id, email string }{{"usr_bootstrap", "push-owner@example.local"}, {"usr_viewer", "push-viewer@example.local"}} {
		if _, err = f.db.Exec(ctx, `UPDATE users SET email=$2,password_hash=$3,global_role='user' WHERE id=$1`, account.id, account.email, password); err != nil {
			t.Fatal(err)
		}
		user, err := f.pg.GetUserByEmail(ctx, account.email)
		if err != nil || user.ID != account.id || user.GlobalRole != domain.RoleUser {
			t.Fatalf("browser account %s must retain ordinary user authority: %v", account.id, err)
		}
	}
	if _, err = f.pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "push_owner_member", ProjectID: f.resource.ProjectID, UserID: "usr_bootstrap", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	var status app.BootstrapStatus
	f.request(http.MethodGet, "/api/v1/bootstrap-status", "", nil, http.StatusOK, &status)
	if status.Status != "complete" {
		t.Fatalf("browser fixture bootstrap status = %q, want complete", status.Status)
	}
}

func TestGitHubPushBrowserBootstrapWithOrdinaryProjectMembers(t *testing.T) {
	f := newPushHTTPFixture(t)
	var before app.BootstrapStatus
	f.request(http.MethodGet, "/api/v1/bootstrap-status", "", nil, http.StatusOK, &before)
	if before.Status != "required" {
		t.Fatalf("seeded fixture bootstrap status = %q, want required", before.Status)
	}
	preparePushBrowserIdentity(t, t.Context(), f)
	for _, account := range []struct{ id, email, role string }{{"usr_bootstrap", "push-owner@example.local", domain.RoleOwner}, {"usr_viewer", "push-viewer@example.local", domain.RoleViewer}} {
		token := createTestSessionFor(t, f.api, account.email, "push-browser-fixture-password")
		principal, err := f.service.AuthenticateSessionToken(t.Context(), token)
		if err != nil || principal.ID != account.id || len(principal.Roles) != 0 {
			t.Fatalf("browser login %s must authenticate as ordinary user: %+v %v", account.id, principal, err)
		}
		var role string
		if err := f.db.QueryRow(t.Context(), `SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, f.resource.ProjectID, account.id).Scan(&role); err != nil || role != account.role {
			t.Fatalf("browser member %s role = %q, want %q: %v", account.id, role, account.role, err)
		}
	}
}

func runPushBrowserFixture(t *testing.T, ctx context.Context, f *pushHTTPFixture, commits [3]string, move func(int), startRunner, stopRunner func() error, awaitBuild func(string, string) domain.HostingBuild) {
	t.Helper()
	origin := os.Getenv("NEROCD_GITHUB_PUSH_BROWSER_ORIGIN")
	parsed, err := http.NewRequest(http.MethodGet, origin, nil)
	if err != nil || parsed.URL.Scheme != "http" || !strings.HasPrefix(parsed.URL.Host, "127.0.0.1:") || parsed.URL.Path != "" || parsed.URL.RawQuery != "" || parsed.URL.Fragment != "" {
		t.Fatal("exact loopback browser origin required")
	}
	port, err := strconv.Atoi(parsed.URL.Port())
	if err != nil || port < 1 || port > 65535 || origin != "http://127.0.0.1:"+strconv.Itoa(port) {
		t.Fatal("browser origin port invalid")
	}
	preparePushBrowserIdentity(t, ctx, f)
	if _, err = f.db.Exec(ctx, `UPDATE projects SET name='GitHub push fixture' WHERE id=$1`, f.resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	f.api = NewServerWithConfig(f.service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: origin, AllowInsecureCookies: true})
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	stepPath := "/api/v1/fixture/github-push/" + hex.EncodeToString(nonce) + "/"
	type step struct {
		command string
		result  chan int
	}
	commands := make(chan step)
	stepHandler := f.api.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := f.service.CurrentPrincipal(r.Context())
		if e != nil || principal.ID != "usr_bootstrap" || r.Method != http.MethodPost || r.Header.Get("Origin") != origin || r.URL.RawQuery != "" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 1))
		if e != nil || len(raw) != 0 {
			http.Error(w, "empty body required", 400)
			return
		}
		command := strings.TrimPrefix(r.URL.Path, stepPath)
		switch command {
		case "push-b", "duplicate-b", "delayed-a", "provider-unavailable", "provider-recover", "fail-next-build", "push-c", "restart-runner", "complete":
		default:
			http.Error(w, "unknown fixture command", 400)
			return
		}
		item := step{command: command, result: make(chan int, 1)}
		select {
		case commands <- item:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		select {
		case status := <-item.result:
			w.WriteHeader(status)
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	listener, err := net.Listen("tcp4", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, stepPath) {
			stepHandler.ServeHTTP(w, r)
			return
		}
		f.api.ServeHTTP(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
		if e := <-done; e != nil && e != http.ErrServerClosed {
			t.Error(e)
		}
	}()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://127.0.0.1:18080/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("embedded assets unavailable")
	}
	metadata := map[string]string{"base_url": origin, "project_id": f.resource.ProjectID, "environment_name": "push-fixture", "resource_id": f.resource.ID, "expected_commit_a": commits[0], "expected_commit_b": commits[1], "expected_commit_c": commits[2], "step_path": stepPath}
	raw, _ := json.Marshal(metadata)
	fmt.Printf("GITHUB_PUSH_BROWSER_READY %s\n", raw)
	stage := 0
	var buildB string
	for {
		select {
		case <-ctx.Done():
			t.Fatal("actual browser did not complete bounded fixture")
		case item := <-commands:
			policy, e := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.resource.ID)
			if e != nil {
				item.result <- 403
				continue
			}
			valid := true
			switch item.command {
			case "push-b":
				if stage != 0 || !policy.Enabled || policy.DesiredCommit != commits[0] {
					valid = false
					break
				}
				move(1)
				f.mu.Lock()
				f.head = commits[1]
				f.mu.Unlock()
				f.push(policy, 10, commits[1])
				stage = 1
			case "duplicate-b":
				if stage != 1 {
					valid = false
					break
				}
				f.push(policy, 10, commits[1])
				stage = 2
			case "delayed-a":
				if stage != 2 {
					valid = false
					break
				}
				f.push(policy, 12, commits[0])
				stage = 3
			case "provider-unavailable":
				if stage != 3 {
					valid = false
					break
				}
				f.mu.Lock()
				f.providerFailure = "rate_limited"
				f.mu.Unlock()
				stage = 4
			case "provider-recover":
				if stage != 4 {
					valid = false
					break
				}
				f.mu.Lock()
				f.providerFailure = ""
				f.mu.Unlock()
				stage = 3
			case "fail-next-build":
				if stage != 3 {
					valid = false
					break
				}
				b := awaitBuild(commits[1], "succeeded")
				buildB = b.ID
				probePushApplication(t, ctx, f, buildB)
				stage = 5
			case "push-c":
				if stage != 5 {
					valid = false
					break
				}
				move(2)
				f.mu.Lock()
				f.head = commits[2]
				f.mu.Unlock()
				f.push(policy, 13, commits[2])
				stage = 6
			case "restart-runner":
				if stage != 6 {
					valid = false
					break
				}
				if e = stopRunner(); e == nil {
					e = startRunner()
				}
				if e != nil {
					t.Fatal("owned runner restart", e)
				}
				stage = 7
			case "complete":
				if stage != 6 && stage != 7 {
					valid = false
					break
				}
				awaitBuild(commits[2], "failed")
				probePushApplication(t, ctx, f, buildB)
				item.result <- 204
				return
			}
			if valid {
				item.result <- 204
			} else {
				item.result <- 409
			}
		}
	}
}
