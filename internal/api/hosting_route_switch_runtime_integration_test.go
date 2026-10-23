//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/runner"
)

const switchFixturePebbleImage = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"

func switchFixtureDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("owned Linux fixture Docker %s: %v %s", args[0], err, output)
	}
	return output
}

func switchFixtureJSON[T any](t *testing.T, client *http.Client, server, token, method, path string, body any, status int) T {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, server+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != status {
		t.Fatalf("%s %s status=%d expected=%d", method, path, response.StatusCode, status)
	}
	var result T
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func switchFixtureDNS(t *testing.T) {
	t.Helper()
	answer := func(raw []byte) []byte {
		var message dnsmessage.Message
		if message.Unpack(raw) != nil {
			return nil
		}
		message.Response, message.Authoritative, message.RecursionAvailable = true, true, true
		for _, question := range message.Questions {
			if question.Type == dnsmessage.TypeA {
				message.Answers = append(message.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
			}
		}
		encoded, _ := message.Pack()
		return encoded
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:8053")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:8053")
	if err != nil {
		_ = udp.Close()
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close(); workers.Wait() })
	workers.Go(func() {
		buffer := make([]byte, 4096)
		for {
			n, peer, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(answer(buffer[:n]), peer)
		}
	})
	workers.Go(func() {
		for {
			connection, err := tcp.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			var size [2]byte
			if _, err = io.ReadFull(connection, size[:]); err == nil {
				query := make([]byte, int(binary.BigEndian.Uint16(size[:])))
				if _, err = io.ReadFull(connection, query); err == nil {
					response := answer(query)
					binary.BigEndian.PutUint16(size[:], uint16(len(response)))
					_, _ = connection.Write(append(size[:], response...))
				}
			}
			_ = connection.Close()
		}
	})
}

func switchFixtureTrust(t *testing.T, root string) (*x509.CertPool, []byte) {
	t.Helper()
	switchFixtureDNS(t)
	switchFixtureDocker(t, "run", "-d", "--name", "fixture-pebble", "--network", "host", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/workspace/acceptance/runtime-proxy-control/pebble.json,dst=/pebble-config.json,readonly", switchFixturePebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")
	switchFixtureDocker(t, "cp", "fixture-pebble:/test/certs/pebble.minica.pem", filepath.Join(root, "minica.pem"))
	publicRoot, err := os.ReadFile(filepath.Join(root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(publicRoot) {
		t.Fatal("invalid fixture root")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("https://localhost:15000/roots/0")
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == 200 && readErr == nil && roots.AppendCertsFromPEM(body) {
				return roots, publicRoot
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated Pebble issuance root unavailable")
	return nil, nil
}

// TestHostingVerifiedSwitchCompiledRunner uses a distinctly tagged fixture binary
// and a local Pebble authority. It does not qualify production ACME issuance.
func TestHostingVerifiedSwitchCompiledRunner(t *testing.T) {
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
	server, pg, database := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	var lost atomic.Bool
	control := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/proxy/complete" && lost.CompareAndSwap(true, false) {
			receipt := httptest.NewRecorder()
			server.ServeHTTP(receipt, r)
			if receipt.Code != 200 {
				t.Errorf("completion before response loss status=%d body=%s", receipt.Code, receipt.Body.String())
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
	switchTo := func(key, targetRelease, outcome string, lose bool) domain.HostingProxyOperation {
		t.Helper()
		current, e := server.app.HostingRoute(admin, routes[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		p, e := pg.GetHostingProxy(ctx, proxy.ID)
		if e != nil {
			t.Fatal(e)
		}
		input := domain.HostingRouteSwitchInput{IdempotencyKey: key, ExpectedLoadedGeneration: p.LoadedGeneration, TargetReleaseOperationID: targetRelease, Routes: []domain.HostingRouteSwitchSelection{{RouteID: current.ID, ExpectedVersion: current.Version, ExpectedSelectedReleaseOperationID: current.SelectedReleaseOperationID}}}
		view := switchFixtureJSON[domain.HostingProxyOperationView](t, control.Client(), control.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/route-switches", input, 202)
		if e = pg.PrepareProxy(ctx, view.ID); e != nil {
			t.Fatal(e)
		}
		lost.Store(lose)
		runErr := runAgent()
		if runErr != nil && !lose && outcome == "applied" {
			t.Fatalf("compiled switch %v %s", runErr, logs.String())
		}
		if lose {
			if e = runAgent(); e != nil {
				t.Fatalf("compiled journal replay %v %s", e, logs.String())
			}
		}
		op, e := pg.GetHostingProxyOperation(ctx, view.ID)
		if e != nil || op.Result == nil || op.Result.Outcome != outcome || op.MutationHold {
			t.Fatalf("switch %+v %v %s", op, e, logs.String())
		}
		expectedLoaded := op.Generation
		if outcome == "rolled_back" {
			expectedLoaded = p.LoadedGeneration
		}
		if op.Result.LoadedGeneration != expectedLoaded || len(op.Result.Routes) != len(op.Snapshot.Routes) {
			t.Fatalf("incomplete or wrong generation receipt %+v", op.Result)
		}
		selectedVerified := false
		for _, observation := range op.Result.Routes {
			if observation.RouteID == current.ID {
				selectedVerified = observation.HTTPReady && observation.HTTPSReady && observation.Certificate.Hostname == current.Hostname && observation.Certificate.Status == "trusted" && len(observation.Certificate.Fingerprint) == 64 && observation.Certificate.ExpiresAt.After(time.Now())
			}
		}
		if !selectedVerified {
			t.Fatalf("selected trusted HTTPS missing %+v", op.Result)
		}
		t.Logf("compiled switch key=%s outcome=%s generation=%d loaded=%d receipt_routes=%d", key, op.Result.Outcome, op.Generation, op.Result.LoadedGeneration, len(op.Result.Routes))
		return op
	}
	probeTransport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(c context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(c, network, net.JoinHostPort("127.0.0.1", port))
	}}
	t.Cleanup(probeTransport.CloseIdleConnections)
	probe := func(path, want string) {
		t.Helper()
		response, e := (&http.Client{Transport: probeTransport, Timeout: 3 * time.Second}).Get("https://switch.example.test" + path)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if e != nil || string(body) != want {
			t.Fatalf("trusted HTTPS path=%s body=%s want=%s err=%v", path, body, want, e)
		}
	}
	if os.Getenv("NEROCD_ROUTE_SWITCH_BROWSER") == "1" {
		runHostingRouteSwitchBrowser(t, switchBrowserFixture{server: server, pg: pg, database: database, environment: env, resource: resource, proxy: proxy, a: a, b: b, routes: routes, runAgent: runAgent, agentCommand: agentCommand, probe: probe, state: state, work: work})
		return
	}
	switchTo("a-to-b", b.ID, "applied", true)
	probe("/", "B:/")
	probe("/unchanged", "A:/unchanged")
	switchTo("retained-a", a.ID, "applied", false)
	probe("/", "A:/")
	retained := strings.TrimSpace(string(switchFixtureDocker(t, "inspect", "--format", "{{.State.Running}}", a.Result.ContainerID)))
	if retained != "true" {
		t.Fatal("retained A was restarted or removed")
	}
	switchFixtureDocker(t, "stop", b.Result.ContainerID)
	switchTo("failed-b", b.ID, "rolled_back", false)
	probe("/", "A:/")
	probe("/unchanged", "A:/unchanged")
	t.Logf("retained A container=%s B container=%s; no build/restart operation was requested", a.Result.ContainerID, b.Result.ContainerID)
	// Cancel only after the candidate dynamic generation is durably published.
	current, e := server.app.HostingRoute(admin, routes[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	p, e := pg.GetHostingProxy(ctx, proxy.ID)
	if e != nil {
		t.Fatal(e)
	}
	cancelView := switchFixtureJSON[domain.HostingProxyOperationView](t, control.Client(), control.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/route-switches", domain.HostingRouteSwitchInput{IdempotencyKey: "cancel-published", ExpectedLoadedGeneration: p.LoadedGeneration, TargetReleaseOperationID: b.ID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: current.ID, ExpectedVersion: current.Version, ExpectedSelectedReleaseOperationID: a.ID}}}, 202)
	if e = pg.PrepareProxy(ctx, cancelView.ID); e != nil {
		t.Fatal(e)
	}
	command := agentCommand()
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	joined := false
	t.Cleanup(func() {
		if !joined {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("compiled canceled agent did not join")
			}
		}
	})
	dynamicPath := filepath.Join(state, proxy.ID, "dynamic", "routes.yml")
	receiptPath := filepath.Join(state, proxy.ID, "receipt.json")
	deadline := time.Now().Add(15 * time.Second)
	for {
		dynamic, _ := os.ReadFile(dynamicPath)
		raw, _ := os.ReadFile(receiptPath)
		var receipt struct {
			Phase      string
			Generation int64
		}
		_ = json.Unmarshal(raw, &receipt)
		if receipt.Phase == "intent" && bytes.Contains(dynamic, []byte("20102")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("candidate publication handshake timed out %s", logs.String())
		}
		select {
		case e := <-done:
			joined = true
			t.Fatalf("agent ended before publication: %v %s", e, logs.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e = command.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
		joined = true
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not join compiled agent")
	}
	canceled, e := pg.GetHostingProxyOperation(ctx, cancelView.ID)
	if e != nil || !canceled.MutationHold || canceled.Result != nil && canceled.Result.Outcome == "applied" {
		t.Fatalf("cancellation invented delivery %+v %v", canceled, e)
	}
	rawIntent, e := os.ReadFile(receiptPath)
	if e != nil {
		t.Fatal(e)
	}
	var unfinished struct {
		Phase        string
		Verification json.RawMessage
	}
	if e = json.Unmarshal(rawIntent, &unfinished); e != nil || unfinished.Phase != "intent" || len(unfinished.Verification) == 0 {
		t.Fatalf("strict intent lost across cancellation: %v", e)
	}
	rawJournal, e := os.ReadFile(filepath.Join(work, "journal", "journal.json"))
	if e != nil {
		t.Fatal(e)
	}
	var durable struct {
		Version int                   `json:"version"`
		Proxy   []runner.JournalProxy `json:"proxy,omitempty"`
	}
	if e = json.Unmarshal(rawJournal, &durable); e != nil {
		t.Fatal(e)
	}
	uncertainResult := false
	for _, entry := range durable.Proxy {
		if entry.OperationID == cancelView.ID && entry.Result != nil && entry.Result.Result.Outcome == "uncertain" && entry.Plan.Snapshot.VerifiedSwitch != nil {
			uncertainResult = true
		}
	}
	if durable.Version != 9 || !uncertainResult {
		t.Fatal("cancellation did not durably record strict uncertainty")
	}
	before, e := os.ReadFile(dynamicPath)
	if e != nil {
		t.Fatal(e)
	}
	// Startup may replay its durable result, but must never re-Apply unfinished intent.
	_ = runAgent()
	after, e := os.ReadFile(dynamicPath)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatalf("restart mutated unfinished dynamic generation: %v", e)
	}
	canceled, e = pg.GetHostingProxyOperation(ctx, cancelView.ID)
	if e != nil || !canceled.MutationHold || canceled.Result != nil && canceled.Result.Outcome == "applied" {
		t.Fatalf("restart cleared uncertainty %+v %v", canceled, e)
	}
	t.Logf("compiled SIGTERM after publication and restart retained hold operation=%s state=%s", cancelView.ID, canceled.State)

}
