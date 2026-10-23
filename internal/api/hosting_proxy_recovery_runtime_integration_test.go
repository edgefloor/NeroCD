//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
)

// This fixture uses the same pinned local test CA and separately compiled fixture
// binary as the route-switch test. It does not qualify production ACME issuance.
func TestHostingProxyMetadataRecoveryCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_PROXY_COMPILED_FIXTURE") != "disposable-dind" {
		t.Skip("requires task-owned Linux compiled proxy fixture")
	}
	root, err := os.MkdirTemp("/var/lib", "nerocd-switch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := switchFixtureTrust(t, root)
	pebbleID := strings.TrimSpace(string(switchFixtureDocker(t, "inspect", "--format", "{{.Id}}", "fixture-pebble")))
	t.Cleanup(func() { switchFixtureDocker(t, "rm", "--force", pebbleID) })
	server, pg, database := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	var selectedOperation atomic.Value
	selectedOperation.Store("")
	var revokeBeforeComplete atomic.Bool
	var loseRecoveryResponse atomic.Bool
	var blockClaims atomic.Bool
	var strictAdmissions atomic.Int32
	var fixtureServerID string
	control := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockClaims.Load() && r.URL.Path == "/api/v1/runners/claim" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/api/v1/runners/proxy/apply-begin" {
			raw, e := io.ReadAll(io.LimitReader(r.Body, 128<<10))
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var a domain.HostingAuthority
			if json.Unmarshal(raw, &a) == nil && a.OperationID == selectedOperation.Load().(string) {
				strictAdmissions.Add(1)
			}
		}
		if r.URL.Path == "/api/v1/runners/proxy/complete" && revokeBeforeComplete.CompareAndSwap(true, false) {
			var grant string
			if e := database.QueryRow(ctx, `SELECT id FROM hosting_proxy_hostname_grants WHERE hostname='switch.example.test'`).Scan(&grant); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			if _, e := server.app.RevokeHostingHostname(admin, fixtureServerID, grant, domain.HostingProxyMutationInput{IdempotencyKey: "recovery-revoke"}); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			receipt := httptest.NewRecorder()
			server.ServeHTTP(receipt, r)
			if receipt.Code != 200 {
				t.Errorf("accepted completion before lost response status=%d", receipt.Code)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/apply") && strings.Contains(r.URL.Path, "/recovery-plans/") && loseRecoveryResponse.CompareAndSwap(true, false) {
			receipt := httptest.NewRecorder()
			server.ServeHTTP(receipt, r)
			if receipt.Code != 200 {
				t.Errorf("accepted recovery before lost response status=%d body=%s", receipt.Code, receipt.Body.String())
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(control.Close)
	cert := filepath.Join(root, "controller.pem")
	if err = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureRoots := filepath.Join(root, "fixture-roots.pem") // The issuance root is fetched by switchFixtureTrust; retain both roots for the engine.
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("https://localhost:15000/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	issuance, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	transport.CloseIdleConnections()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixtureRoots, append(append([]byte(nil), publicRoot...), issuance...), 0600); err != nil {
		t.Fatal(err)
	}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_switch_fixture", RunnerName: "compiled switch fixture", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	token := "ncr_compiled_switch_fixture_only"
	sum := sha256.Sum256([]byte(token))
	enrolled, err := server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.app.CreateServer(admin, app.ServerInput{Name: "switch fixture", RunnerID: enrolled.Runner.ID})
	fixtureServerID = target.Server.ID
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.app.GrantDestinationProject(admin, target.Destination.ID, app.DestinationProjectInput{ProjectID: "proj_platform"}); err != nil {
		t.Fatal(err)
	}
	env, err := server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "switch"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(admin, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "switch", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "127.0.0.1:25000/backend-a:fixture"}}})
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
	stopJobs, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := stopJobs(cleanup); err != nil {
			t.Error(err)
		}
	})
	credential := filepath.Join(root, "credential")
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err = os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "proxy")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	logs := &runtimeFixtureLog{}
	agentCommand := func() *exec.Cmd {
		command := exec.CommandContext(ctx, "/fixture/nerocd-proxy-fixture", "runner", "--server", control.URL, "--credential-file", credential, "--work-dir", work, "--journal-dir", filepath.Join(work, "journal"), "--once", "--capabilities", strings.Join(caps, ","), "--proxy-state-root", state, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch", "--cancel-poll-interval", "100ms")
		command.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+cert, "NEROCD_PROXY_FIXTURE_ROOT="+fixtureRoots)
		command.Stdout, command.Stderr = logs, logs
		return command
	}
	runAgent := func() error { return agentCommand().Run() }
	deploy := func(key string, port int) domain.HostingOperation {
		t.Helper()
		op, e := server.app.RequestHostingDeployment(admin, resource.ID, domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, IdempotencyKey: key, ContainerPort: 8080, HostPort: port, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}})
		if e != nil {
			t.Fatal(e)
		}
		if e = pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		if e = runAgent(); e != nil {
			t.Fatalf("compiled deployment: %v %s", e, logs.String())
		}
		op, e = pg.GetHostingOperation(ctx, op.ID)
		if e != nil || op.State != "succeeded" || op.Result == nil {
			t.Fatalf("deployment %+v %v", op, e)
		}
		return op
	}
	a := deploy("a", 20101)
	t.Cleanup(func() {
		for _, id := range strings.Fields(string(switchFixtureDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+a.BindingID))) {
			label := strings.TrimSpace(string(switchFixtureDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.binding"}}`, id)))
			if label != a.BindingID {
				t.Error("fixture binding identity changed")
				continue
			}
			switchFixtureDocker(t, "rm", "--force", id)
		}
	})

	proxy, err := server.app.ConfigureHostingProxy(admin, target.Server.ID, domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range strings.Fields(string(switchFixtureDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.proxy="+proxy.ID))) {
			label := strings.TrimSpace(string(switchFixtureDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.proxy"}}`, id)))
			if label != proxy.ID {
				t.Error("fixture proxy identity changed")
				continue
			}
			if t.Failed() {
				t.Logf("fixture Traefik: %s", switchFixtureDocker(t, "logs", "--tail", "30", id))
			}
			switchFixtureDocker(t, "rm", "--force", id)
		}
	})
	certdir := filepath.Join(state, proxy.ID, "certificates")
	if err = os.MkdirAll(certdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(certdir, "root.pem"), publicRoot, 0600); err != nil {
		t.Fatal(err)
	}
	applyGeneration := func(id string) domain.HostingProxyOperation {
		t.Helper()
		if e := pg.PrepareProxy(ctx, id); e != nil {
			t.Fatal(e)
		}
		if e := runAgent(); e != nil {
			t.Fatalf("compiled proxy: %v %s", e, logs.String())
		}
		op, e := pg.GetHostingProxyOperation(ctx, id)
		if e != nil || op.State != "applied" {
			t.Fatalf("proxy %+v %v %s", op, e, logs.String())
		}
		return op
	}
	var first string
	if err = database.QueryRow(ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id=$1 ORDER BY generation DESC LIMIT 1`, proxy.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	applyGeneration(first)
	if _, err = server.app.GrantHostingHostname(admin, target.Server.ID, domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "switch.example.test"}); err != nil {
		t.Fatal(err)
	}
	var routes []domain.HostingRoute
	for n, path := range []string{"/", "/unchanged"} {
		route, e := server.app.RequestHostingRoute(admin, resource.ID, domain.HostingRouteInput{IdempotencyKey: fmt.Sprintf("route-%d", n), Hostname: "switch.example.test", PathPrefix: path, ExpectedHealthyOperationID: a.ID})
		if e != nil {
			t.Fatal(e)
		}
		applyGeneration(route.OperationID)
		routes = append(routes, route)
	}
	source := resource.Source
	source.Image.Reference = "127.0.0.1:25000/backend-b:fixture"
	resource, err = server.app.UpdateResource(admin, resource.ID, app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source})
	if err != nil {
		t.Fatal(err)
	}
	b := deploy("b", 20102)
	session := switchFixtureJSON[struct{ Token string }](t, control.Client(), control.URL, "", "POST", "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)

	// B is qualified by deployment, then fails routing before the strict switch.
	switchFixtureDocker(t, "stop", b.Result.ContainerID)
	current, err := server.app.HostingRoute(admin, routes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pg.GetHostingProxy(ctx, proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := switchFixtureJSON[domain.HostingProxyOperationView](t, control.Client(), control.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/route-switches", domain.HostingRouteSwitchInput{IdempotencyKey: "recover-failed-b", ExpectedLoadedGeneration: p.LoadedGeneration, TargetReleaseOperationID: b.ID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: current.ID, ExpectedVersion: current.Version, ExpectedSelectedReleaseOperationID: a.ID}}}, 202)
	selectedOperation.Store(view.ID)
	if err = pg.PrepareProxy(ctx, view.ID); err != nil {
		t.Fatal(err)
	}
	revokeBeforeComplete.Store(true)
	runErr := runAgent()
	original, err := pg.GetHostingProxyOperation(ctx, view.ID)
	if err != nil || original.Result == nil || original.Result.Outcome != "rolled_back" || !original.MetadataConflict || !original.MutationHold || original.CompleteReceipt == nil {
		t.Fatalf("compiled completed conflict state=%s result=%v hold=%t err=%v agent=%v %s", original.State, original.Result != nil, original.MutationHold, err, runErr, logs.String())
	}
	if original.Result.LoadedGeneration != p.LoadedGeneration || len(original.Result.Routes) != 2 {
		t.Fatal("rollback generation/scope mismatch")
	}
	for _, r := range original.Result.Routes {
		if r.RouteID == current.ID && (!r.HTTPReady || !r.HTTPSReady || r.Certificate.Status != "trusted" || r.Certificate.Hostname != current.Hostname) {
			t.Fatal("selected rollback lacks trusted HTTPS")
		}
	}
	journalPath := filepath.Join(work, "journal", "journal.json")
	readJournal := func(expectedVersion int) []runner.JournalProxy {
		t.Helper()
		raw, e := os.ReadFile(journalPath)
		if e != nil {
			t.Fatal(e)
		}
		var j struct {
			Version int                   `json:"version"`
			Proxy   []runner.JournalProxy `json:"proxy"`
		}
		if e = json.Unmarshal(raw, &j); e != nil {
			t.Fatal(e)
		}
		if j.Version != expectedVersion {
			t.Fatalf("strict journal version %d", j.Version)
		}
		return j.Proxy
	}
	found := false
	for _, entry := range readJournal(9) {
		if entry.OperationID == view.ID && entry.Result != nil && entry.Result.Result.Outcome == "rolled_back" {
			found = true
		}
	}
	if !found {
		t.Fatal("lost completion conflict was not durable in original journal")
	}
	status := switchFixtureJSON[domain.HostingProxyRecoveryStatus](t, control.Client(), control.URL, session.Token, "GET", "/api/v1/proxy-operations/"+view.ID+"/recovery", nil, 200)
	if !status.Eligible || status.Review == nil {
		t.Fatalf("recovery eligibility %+v", status)
	}
	review := status.Review
	plan := switchFixtureJSON[domain.HostingProxyRecoveryPlan](t, control.Client(), control.URL, session.Token, "POST", "/api/v1/proxy-operations/"+view.ID+"/recovery-plans", domain.HostingProxyRecoveryInput{IdempotencyKey: "compiled-review", ExpectedLoadedGeneration: review.LoadedGeneration, ExpectedDesiredGeneration: review.DesiredGeneration, ExpectedCompleteReceiptID: review.CompleteReceiptID, ExpectedCompleteReceiptHash: review.CompleteReceiptHash}, 201)
	apply := domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "compiled-apply", PlanHash: plan.PlanHash}
	path := "/api/v1/proxy-operations/" + view.ID + "/recovery-plans/" + plan.ID + "/apply"
	loseRecoveryResponse.Store(true)
	raw, err := json.Marshal(apply)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", control.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+session.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err = control.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("lost recovery response status=%d", response.StatusCode)
	}
	resolution := switchFixtureJSON[domain.HostingProxyRecoveryResolution](t, control.Client(), control.URL, session.Token, "POST", path, apply, 200)
	again := switchFixtureJSON[domain.HostingProxyRecoveryResolution](t, control.Client(), control.URL, session.Token, "POST", path, apply, 200)
	if !reflect.DeepEqual(resolution, again) {
		t.Fatal("recovery replay changed receipt")
	}
	settled, err := pg.GetHostingProxyOperation(ctx, view.ID)
	if err != nil || settled.MutationHold || settled.MetadataConflict || settled.State != "rolled_back" || !reflect.DeepEqual(settled.Result, original.Result) || !reflect.DeepEqual(settled.CompleteReceipt, original.CompleteReceipt) {
		t.Fatalf("historical receipt changed err=%v", err)
	}
	queued, err := pg.GetHostingProxyOperation(ctx, resolution.ConvergenceOperationID)
	if err != nil || len(queued.Snapshot.Routes) != 0 {
		t.Fatalf("revoked routes published err=%v", err)
	}
	for _, route := range routes {
		row, e := pg.GetHostingRoute(ctx, route.ID)
		if e != nil || row.Route.Enabled || row.Route.SelectedReleaseOperationID != a.ID {
			t.Fatalf("disabled metadata %+v %v", row.Route, e)
		}
	}
	dynamic := filepath.Join(state, proxy.ID, "dynamic", "routes.yml")
	// Journal acknowledgement must preserve the engine's committed route bytes.
	before, err := os.ReadFile(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	admissions := strictAdmissions.Load()
	if admissions != 1 {
		t.Fatalf("strict admissions %d", admissions)
	}
	blockClaims.Store(true)
	if err = runAgent(); err != nil {
		t.Fatalf("compiled historical journal replay %v %s", err, logs.String())
	}
	for _, entry := range readJournal(8) {
		if entry.OperationID == view.ID {
			t.Fatal("resolved original journal entry remained")
		}
	}
	if strictAdmissions.Load() != admissions {
		t.Fatal("journal replay reacquired strict apply authority")
	}
	after, err := os.ReadFile(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("journal replay changed engine routes")
	}
	t.Logf("compiled metadata recovery original=%s receipt=%s resolution=%s convergence=%s loaded=%d strict_admissions=%d; local test CA only", view.ID, original.CompleteReceipt.ID, resolution.ID, resolution.ConvergenceOperationID, p.LoadedGeneration, admissions)
}
