package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/secretcrypto"
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
func newPushHTTPFixture(t *testing.T) *pushHTTPFixture {
	t.Helper()
	_, pg, database := newHostingBuildAPIFixture(t)
	ring, err := secretcrypto.New("push", map[string][]byte{"push": bytes.Repeat([]byte{8}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	f := &pushHTTPFixture{t: t, pg: pg, db: database, service: service, head: strings.Repeat("a", 40), secret: strings.Repeat("s", 48), runnerToken: "ncr_push_fixture_runner"}
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
	enrollment, err := service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_push_fixture", RunnerName: "push runner", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType, domain.HostingBuildTriggerRevisionCapability, domain.HostingReleasePortsCapability}})
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
