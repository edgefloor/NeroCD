//go:build linux && nerocd_proxy_fixture

package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/dns/dnsmessage"
	"nerocd/db"
	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
	"nerocd/internal/store"
	"nerocd/web"
)

const controlPebbleImage = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"

func controlDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("owned Linux fixture Docker %s: %v %s", args[0], err, output)
	}
	return output
}

func controlJSON[T any](t *testing.T, server, token, method, path string, body any, status int) T {
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
	response, err := http.DefaultClient.Do(request)
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

func controlDNS(t *testing.T) {
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

func controlDatabase(t *testing.T) (*store.PostgresStore, *pgxpool.Pool) {
	t.Helper()
	address := "postgres://postgres:proxy-fixture-only@127.0.0.1:5432/proxy_fixture?sslmode=disable"
	pool, err := pgxpool.New(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(15 * time.Second)
	for pool.Ping(t.Context()) != nil {
		if time.Now().After(deadline) {
			t.Fatal("isolated PostgreSQL not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := db.Files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(t.Context(), string(content)); err != nil {
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
	}
	seed, err := os.ReadFile("/workspace/db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), string(seed)); err != nil {
		t.Fatal(err)
	}
	for {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		changed, err := hostingjobs.MigrateNextTx(t.Context(), tx)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	pg, err := store.OpenPostgres(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	return pg, pool
}

func controlTrust(t *testing.T, root string) (*x509.CertPool, []byte) {
	t.Helper()
	controlDNS(t)
	controlDocker(t, "run", "-d", "--name", "fixture-pebble", "--network", "host", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/workspace/acceptance/runtime-proxy-control/pebble.json,dst=/pebble-config.json,readonly", controlPebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")
	controlDocker(t, "cp", "fixture-pebble:/test/certs/pebble.minica.pem", filepath.Join(root, "minica.pem"))
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

func TestProxyRuntimeCombined(t *testing.T) {
	if os.Getenv("NEROCD_PROXY_CONTROL_FIXTURE") != "disposable-dind" {
		t.Skip("requires acceptance/runtime-proxy-control/gate.sh")
	}
	root, err := os.MkdirTemp("/var/lib", "nerocd-proxy-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := controlTrust(t, root)
	pg, database := controlDatabase(t)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	stopJobs, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := stopJobs(ctx); err != nil {
			t.Error(err)
		}
	})
	server := api.NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	var losePrepared, loseAdmission, loseCompletion atomic.Bool
	var admissionFault atomic.Int32
	var proxyControlURL, humanCredential string
	losePrepared.Store(true)
	loseAdmission.Store(true)
	loseCompletion.Store(true)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/proxy/apply-begin" {
			if mode := admissionFault.Swap(0); mode != 0 {
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, r)
				var operation domain.HostingProxyOperation
				if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &operation) != nil || !operation.MutationHold {
					t.Error("fault injection requires committed admission")
				}
				if mode == 1 {
					changed, err := database.Exec(r.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second',status='expired' WHERE run_id=$1 AND status='active'`, operation.RunID)
					if err != nil || changed.RowsAffected() != 1 {
						t.Errorf("expire exact owned proxy lease: %v", err)
					}
				} else {
					request, err := http.NewRequestWithContext(r.Context(), "POST", proxyControlURL+"/api/v1/proxy-operations/"+operation.ID+"/cancel", strings.NewReader(`{}`))
					if err != nil {
						t.Error(err)
						w.WriteHeader(503)
						return
					}
					request.Header.Set("Authorization", "Bearer "+humanCredential)
					request.Header.Set("Content-Type", "application/json")
					response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
					if err != nil {
						t.Error(err)
					} else {
						if response.StatusCode != 200 {
							t.Errorf("cancel admitted proxy status=%d", response.StatusCode)
						}
						if err := response.Body.Close(); err != nil {
							t.Error(err)
						}
					}
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		var lost *atomic.Bool
		switch r.URL.Path {
		case "/api/v1/runners/proxy/prepared":
			lost = &losePrepared
		case "/api/v1/runners/proxy/apply-begin":
			lost = &loseAdmission
		case "/api/v1/runners/proxy/complete":
			lost = &loseCompletion
		}
		if lost != nil && lost.CompareAndSwap(true, false) {
			receipt := httptest.NewRecorder()
			server.ServeHTTP(receipt, r)
			if receipt.Code != 200 {
				t.Errorf("fault injection endpoint rejected before response loss: %d", receipt.Code)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		server.ServeHTTP(w, r)
	})
	httpServer := httptest.NewServer(handler)
	proxyControlURL = httpServer.URL
	t.Cleanup(httpServer.Close)
	session := controlJSON[struct{ Token string }](t, httpServer.URL, "", "POST", "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
	humanCredential = session.Token
	enrollment := controlJSON[app.CreatedRunnerEnrollment](t, httpServer.URL, session.Token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_proxy_fixture", RunnerName: "Proxy fixture", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply}}, 201)
	credential := "proxy-control-fixture-only-credential"
	hash := sha256.Sum256([]byte(credential))
	controlJSON[app.ConsumedRunnerEnrollment](t, httpServer.URL, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(hash[:])}, 200)
	target := controlJSON[app.ServerDetail](t, httpServer.URL, session.Token, "POST", "/api/v1/servers", app.ServerInput{Name: "Proxy fixture", RunnerID: "runner_proxy_fixture"}, 201)
	proxyConfig := ProxyConfig{Directory: root, DockerBinary: "/usr/local/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		if err := os.Mkdir(settings.Directory, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if err := os.Mkdir(filepath.Join(settings.Directory, "certificates"), 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		rootPath := filepath.Join(settings.Directory, "certificates/root.pem")
		if existing, err := os.ReadFile(rootPath); err == nil {
			if !bytes.Equal(existing, publicRoot) {
				return nil, fmt.Errorf("fixture public root changed")
			}
		} else if os.IsNotExist(err) {
			if err = os.WriteFile(rootPath, publicRoot, 0600); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
	}}
	work := t.TempDir()
	runAgent := func() error {
		return Run(Config{Proxy: proxyConfig, Server: httpServer.URL, Token: credential, RunnerID: target.Server.RunnerID, WorkDir: work, JournalDir: filepath.Join(work, "journal"), Execute: true, Once: true, CancelPollInterval: 100 * time.Millisecond})
	}
	var resources []domain.Resource
	var releases []domain.HostingOperation
	for index, project := range []string{"proj_platform", "proj_platform", "proj_security"} {
		if index != 1 {
			controlJSON[domain.DestinationProjectGrant](t, httpServer.URL, session.Token, "POST", "/api/v1/destinations/"+target.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: project}, 201)
		}
		environment := controlJSON[domain.ProjectEnvironment](t, httpServer.URL, session.Token, "POST", "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: project, Name: fmt.Sprintf("proxy-%d", index)}, 201)
		image := "127.0.0.1:25000/backend-a:fixture"
		if index == 1 {
			image = "127.0.0.1:25000/backend-b:fixture"
		}
		resource := controlJSON[domain.Resource](t, httpServer.URL, session.Token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: project, ProjectEnvironmentID: environment.ID, Name: fmt.Sprintf("web-%d", index), Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: image}}}, 201)
		controlJSON[domain.ResourceBinding](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: target.Destination.ID}, 201)
		operation := controlJSON[domain.HostingOperation](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, IdempotencyKey: fmt.Sprintf("deploy-%d", index), ContainerPort: 8080, HostPort: 20100 + index, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 202)
		deadline := time.Now().Add(10 * time.Second)
		for {
			operation, err = pg.GetHostingOperation(t.Context(), operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if operation.RunID != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("real River image dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err = runAgent(); err != nil {
			t.Fatal(err)
		}
		operation, err = pg.GetHostingOperation(t.Context(), operation.ID)
		if err != nil || operation.State != "succeeded" {
			t.Fatalf("actual image deployment did not settle: %v %+v", err, operation)
		}
		resources = append(resources, resource)
		releases = append(releases, operation)
	}
	proxy := controlJSON[domain.HostingProxy](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "enable-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	t.Cleanup(func() {
		if t.Failed() {
			response, probeErr := (&http.Client{Timeout: time.Second}).Get("http://127.0.0.1:19080/api/rawdata")
			if probeErr == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
				closeErr := response.Body.Close()
				t.Logf("failed fixture public loaded configuration: %s read=%v close=%v", body, readErr, closeErr)
			} else {
				t.Logf("failed fixture management probe: %v", probeErr)
			}
		}
		ids := strings.Fields(string(controlDocker(t, "container", "ls", "--all", "--filter", "label=io.nerocd.proxy="+proxy.ID, "--format", "{{.ID}}")))
		for _, id := range ids {
			label := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", `{{index .Config.Labels "io.nerocd.proxy"}}`, id)))
			if label != proxy.ID {
				t.Error("owned proxy identity changed during cleanup")
				continue
			}
			if t.Failed() {
				t.Logf("owned fixture Traefik errors: %s", controlDocker(t, "logs", "--tail", "20", id))
			}
			controlDocker(t, "rm", "--force", id)
		}
	})
	awaitProxy := func() domain.HostingProxy {
		deadline := time.Now().Add(15 * time.Second)
		for {
			current, e := pg.GetHostingProxy(t.Context(), proxy.ID)
			if e != nil {
				t.Fatal(e)
			}
			if current.ActiveOperationID != "" {
				op, e := pg.GetHostingProxyOperation(t.Context(), current.ActiveOperationID)
				if e == nil && op.Generation == current.DesiredGeneration {
					return current
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("real River proxy dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []domain.HostingHostnameGrantInput{{ProjectID: "proj_platform", Hostname: "app.example.test"}, {ProjectID: "proj_security", Hostname: "security.example.test"}} {
		controlJSON[domain.HostingHostnameGrant](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy/hostname-grants", grant, 201)
	}
	controlJSON[map[string]any](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_security", Hostname: "example.test", Wildcard: true}, 409)
	controlJSON[map[string]any](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resources[0].ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "unauthorized-host", Hostname: "security.example.test", PathPrefix: "/", ExpectedHealthyOperationID: releases[0].ID}, 403)
	controlJSON[map[string]any](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resources[0].ID+"/routes", map[string]any{"idempotency_key": "arbitrary-upstream", "hostname": "app.example.test", "path_prefix": "/", "expected_healthy_operation_id": releases[0].ID, "host_port": 5432}, 400)
	controlJSON[map[string]any](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resources[0].ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "reserved-acme", Hostname: "app.example.test", PathPrefix: "/.well-known/acme-challenge", ExpectedHealthyOperationID: releases[0].ID}, 400)
	controlJSON[map[string]any](t, httpServer.URL, credential, "POST", "/api/v1/runners/proxy/plan", domain.HostingAuthority{OperationID: "pop_foreign", RunID: "proxy_pop_foreign", LeaseID: "lease_foreign", Attempt: 1, Fence: "foreign"}, 404)
	var writers sync.WaitGroup
	routes := make([]domain.HostingRoute, 3)
	for index, resource := range resources {
		writers.Go(func() {
			hostname, path := "app.example.test", "/"
			if index == 1 {
				path = "/api"
			}
			if index == 2 {
				hostname = "security.example.test"
			}
			routes[index] = controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: fmt.Sprintf("route-%d", index), Hostname: hostname, PathPrefix: path, ExpectedHealthyOperationID: releases[index].ID}, 202)
		})
	}
	writers.Wait()
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		current := controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "GET", "/api/v1/routes/"+route.ID, nil, 200)
		if !current.HTTPReady || !current.HTTPSReady || current.Certificate.Status != "trusted" {
			t.Fatalf("actual route TLS not ready: %+v", current)
		}
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	tryProbe := func(host, path, want string) (string, error) {
		response, err := client.Get("https://" + host + path)
		if err != nil {
			return "", err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if readErr != nil || string(body) != want {
			return "", fmt.Errorf("HTTPS route %s%s body=%q expected=%q err=%v", host, path, body, want, readErr)
		}
		fingerprint := sha256.Sum256(response.TLS.PeerCertificates[0].Raw)
		return hex.EncodeToString(fingerprint[:]), nil
	}
	probe := func(host, path, want string) string {
		t.Helper()
		fingerprint, err := tryProbe(host, path, want)
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	probe("app.example.test", "/", "A:/")
	probe("app.example.test", "/api", "B:/api")
	probe("app.example.test", "/api/x", "B:/api/x")
	probe("app.example.test", "/apix", "A:/apix")
	probe("security.example.test", "/", "A:/")
	var count int
	if err = database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_proxy_operations WHERE state='applied'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected real proxy generations=%d %v", count, err)
	}
	t.Log("real enrolled agent, API/River, three resources, concurrent projects, exact Host/path, trusted HTTP-01 and lost-response receipts passed")
	updateRoute := func(index int, path, key string) domain.HostingRoute {
		t.Helper()
		current := controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "GET", "/api/v1/routes/"+routes[index].ID, nil, 200)
		return controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "PATCH", "/api/v1/routes/"+current.ID, domain.HostingRouteInput{ExpectedRouteVersion: current.Version, IdempotencyKey: key, Hostname: current.Hostname, PathPrefix: path, ExpectedHealthyOperationID: releases[index].ID}, 202)
	}
	// Pause the exact owned process so it cannot load the candidate before the
	// fixture corrupts that generation. The engine must restore and verify its
	// previous file, rather than accepting a parse-only or stale router result.
	containerName := "nerocd-proxy-" + proxy.ID
	dynamicPath := filepath.Join(root, proxy.ID, "dynamic/routes.yml")
	previous, err := os.ReadFile(dynamicPath)
	if err != nil {
		t.Fatal(err)
	}
	controlDocker(t, "pause", containerName)
	paused := true
	t.Cleanup(func() {
		if paused {
			controlDocker(t, "unpause", containerName)
		}
	})
	updateRoute(1, "/replacement", "rejected-config")
	candidate := awaitProxy()
	finished := make(chan error, 1)
	go func() { finished <- runAgent() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		written, readErr := os.ReadFile(dynamicPath)
		if readErr == nil && !bytes.Equal(previous, written) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("candidate file never reached watched directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = os.WriteFile(dynamicPath, []byte(`{"http":{"routers":`), 0600); err != nil {
		t.Fatal(err)
	}
	controlDocker(t, "unpause", containerName)
	paused = false
	if err = <-finished; err == nil {
		t.Fatal("malformed candidate unexpectedly applied")
	}
	rolledBack, err := pg.GetHostingProxyOperation(t.Context(), candidate.ActiveOperationID)
	if err != nil || rolledBack.State != "rolled_back" || rolledBack.MutationHold || rolledBack.CompleteReceipt == nil {
		t.Fatalf("rollback did not settle exact receipt: %+v %v", rolledBack, err)
	}
	probe("app.example.test", "/api/x", "B:/api/x")
	probe("security.example.test", "/", "A:/")
	updateRoute(1, "/api", "after-rollback")
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	t.Log("actual rejected file generation rolled back with receipt and previous trusted routes serving")
	// Withdrawal is a real empty file generation and preserves the installed
	// container and ACME account/certificate state for a later re-enable.
	certificatePath := filepath.Join(root, proxy.ID, "certificates/acme.json")
	acmeBefore, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	containerBefore := strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", containerName)))
	controlJSON[domain.HostingProxy](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy/disable", domain.HostingProxyMutationInput{IdempotencyKey: "disable-empty"}, 202)
	disabled := awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	disabledOperation, err := pg.GetHostingProxyOperation(t.Context(), disabled.ActiveOperationID)
	acmeAfter, readErr := os.ReadFile(certificatePath)
	if err != nil || disabledOperation.State != "applied" || len(disabledOperation.Result.Routes) != 0 || readErr != nil || !bytes.Equal(acmeBefore, acmeAfter) || strings.TrimSpace(string(controlDocker(t, "inspect", "--format", "{{.Id}}", containerName))) != containerBefore {
		t.Fatalf("empty withdrawal changed persistent runtime: %v %v", err, readErr)
	}
	controlJSON[domain.HostingProxy](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "re-enable", Configuration: proxy.Configuration}, 202)
	for index := range routes {
		updateRoute(index, []string{"/", "/api", "/"}[index], fmt.Sprintf("re-enable-route-%d", index))
	}
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	probe("app.example.test", "/api/x", "B:/api/x")
	t.Log("initial and disabled empty generations loaded against pinned Traefik; re-enable reused persistent certificate state")
	controlJSON[domain.HostingHostnameGrant](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+target.Server.ID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "blocked.example.test"}, 201)
	blocked := controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+resources[1].ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "blocked-ca", Hostname: "blocked.example.test", PathPrefix: "/", ExpectedHealthyOperationID: releases[1].ID}, 202)
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	blocked = controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "GET", "/api/v1/routes/"+blocked.ID, nil, 200)
	if !blocked.HTTPReady || blocked.HTTPSReady || blocked.Certificate.Status != "pending" {
		t.Fatalf("rejected ACME authorization invented HTTPS readiness: %+v", blocked)
	}
	probe("app.example.test", "/api/x", "B:/api/x")
	probe("security.example.test", "/", "A:/")
	controlJSON[domain.HostingRoute](t, httpServer.URL, session.Token, "DELETE", "/api/v1/routes/"+blocked.ID, domain.HostingRouteDeletionInput{ExpectedRouteVersion: blocked.Version, IdempotencyKey: "withdraw-blocked"}, 202)
	awaitProxy()
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	t.Log("real ACME failure stayed HTTP-applied/TLS-pending while unrelated trusted routes continued")
	for _, path := range []string{root, filepath.Join(root, proxy.ID), filepath.Join(root, proxy.ID, "certificates"), filepath.Join(root, proxy.ID, "dynamic")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("persistent directory permission: %s %v", path, err)
		}
	}
	info, err := os.Stat(certificatePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("persistent ACME mode: %v", err)
	}
	stopContext, cancelJobs := context.WithTimeout(t.Context(), 10*time.Second)
	if err = stopJobs(stopContext); err != nil {
		cancelJobs()
		t.Fatal(err)
	}
	cancelJobs()
	stopJobs = func(context.Context) error { return nil }
	httpServer.Close()
	controlDocker(t, "pause", "fixture-pebble")
	caPaused := true
	t.Cleanup(func() {
		if caPaused {
			controlDocker(t, "unpause", "fixture-pebble")
		}
	})
	// Force a new TLS handshake after CA isolation. A pooled connection can
	// still carry a pre-renewal leaf while the live server has rotated it.
	transport.CloseIdleConnections()
	oldFingerprint := probe("app.example.test", "/api/x", "B:/api/x")
	controlDocker(t, "restart", containerName)
	transport.CloseIdleConnections()
	deadline = time.Now().Add(15 * time.Second)
	for {
		fingerprint, probeErr := tryProbe("app.example.test", "/api/x", "B:/api/x")
		if probeErr == nil {
			if fingerprint != oldFingerprint {
				t.Fatal("restart did not reuse exact persisted certificate while CA unavailable")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(probeErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	probe("security.example.test", "/", "A:/")
	controlDocker(t, "unpause", "fixture-pebble")
	caPaused = false
	controlDocker(t, "restart", containerName)
	deadline = time.Now().Add(90 * time.Second)
	for {
		transport.CloseIdleConnections()
		fingerprint, probeErr := tryProbe("app.example.test", "/api/x", "B:/api/x")
		if probeErr == nil && fingerprint != oldFingerprint {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("short-lived fixture certificate did not renew: %v", probeErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
	probe("security.example.test", "/", "A:/")
	t.Log("controlplane HTTP server and River consumer stopped: restart reused exact certificate with CA unavailable, then real renewal completed without controlplane")
	httpServer = httptest.NewServer(handler)
	proxyControlURL = httpServer.URL
	t.Cleanup(httpServer.Close)
	stopJobs, err = pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	enrollTarget := func(id string, capabilities []string) (string, app.ServerDetail) {
		t.Helper()
		enrollment := controlJSON[app.CreatedRunnerEnrollment](t, httpServer.URL, session.Token, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: id, RunnerName: "Owned capability fixture", Capabilities: capabilities}, 201)
		credential := "owned-proxy-capability-" + id
		hash := sha256.Sum256([]byte(credential))
		requestHash := sha256.Sum256([]byte("enroll-" + id))
		controlJSON[app.ConsumedRunnerEnrollment](t, httpServer.URL, enrollment.Token, "POST", "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + hex.EncodeToString(requestHash[:16]), CredentialHash: hex.EncodeToString(hash[:])}, 200)
		target := controlJSON[app.ServerDetail](t, httpServer.URL, session.Token, "POST", "/api/v1/servers", app.ServerInput{Name: "Capability fixture " + id, RunnerID: id}, 201)
		return credential, target
	}
	oldCredential, oldTarget := enrollTarget("runner_without_proxy", []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability})
	controlJSON[map[string]any](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+oldTarget.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "old-agent-denied", Configuration: proxy.Configuration}, 409)
	controlJSON[domain.DestinationProjectGrant](t, httpServer.URL, session.Token, "POST", "/api/v1/destinations/"+oldTarget.Destination.ID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
	unrouted := controlJSON[domain.Resource](t, httpServer.URL, session.Token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: resources[0].ProjectEnvironmentID, Name: "unrouted-capability-fixture", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "127.0.0.1:25000/backend-a:fixture"}}}, 201)
	controlJSON[domain.ResourceBinding](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+unrouted.ID+"/binding", app.ResourceBindingInput{DestinationID: oldTarget.Destination.ID}, 201)
	queueImage := func(key string, port int) domain.HostingOperation {
		t.Helper()
		op := controlJSON[domain.HostingOperation](t, httpServer.URL, session.Token, "POST", "/api/v1/resources/"+unrouted.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: unrouted.Generation, IdempotencyKey: key, ContainerPort: 8080, HostPort: port, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, 202)
		deadline := time.Now().Add(15 * time.Second)
		for op.RunID == "" {
			if time.Now().After(deadline) {
				t.Fatal("capability fixture image dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
			var err error
			op, err = pg.GetHostingOperation(t.Context(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return op
	}
	oldWork := t.TempDir()
	oldConfig := Config{Server: httpServer.URL, Token: oldCredential, RunnerID: oldTarget.Server.RunnerID, WorkDir: oldWork, JournalDir: filepath.Join(oldWork, "journal"), Execute: true, Once: true}
	firstImage := queueImage("default-agent-image", 20110)
	updateRoute(1, "/api", "queued-proxy-versus-image")
	queuedProxy := awaitProxy()
	if err = Run(oldConfig); err != nil {
		t.Fatal(err)
	}
	firstImage, err = pg.GetHostingOperation(t.Context(), firstImage.ID)
	if err != nil || firstImage.State != "succeeded" {
		t.Fatalf("default agent starved its supported image: %v %+v", err, firstImage)
	}
	stillQueued, err := pg.GetHostingProxyOperation(t.Context(), queuedProxy.ActiveOperationID)
	if err != nil || stillQueued.State != "dispatched" || stillQueued.MutationHold {
		t.Fatalf("default agent claimed unsupported proxy: %v %+v", err, stillQueued)
	}
	if err = runAgent(); err != nil {
		t.Fatal(err)
	}
	pendingImage := queueImage("proxy-only-must-skip-image", 20111)
	for index, mode := range []string{"expired", "canceled"} {
		secondaryID := "runner_proxy_" + mode
		secondaryCredential, secondaryTarget := enrollTarget(secondaryID, []string{domain.RunTypeComposeDeploy, domain.RunTypeProxyApply})
		secondary := controlJSON[domain.HostingProxy](t, httpServer.URL, session.Token, "POST", "/api/v1/servers/"+secondaryTarget.Server.ID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "authority-" + mode, Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: uint16(19081 + index)}}, 202)
		deadline := time.Now().Add(15 * time.Second)
		for secondary.ActiveOperationID == "" {
			if time.Now().After(deadline) {
				t.Fatal("authority fixture dispatch timeout")
			}
			time.Sleep(20 * time.Millisecond)
			secondary, err = pg.GetHostingProxy(t.Context(), secondary.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		secondaryWork := t.TempDir()
		secondaryConfig := Config{Proxy: proxyConfig, Server: httpServer.URL, Token: secondaryCredential, RunnerID: secondaryID, WorkDir: secondaryWork, JournalDir: filepath.Join(secondaryWork, "journal"), Execute: true, Once: true, CancelPollInterval: 50 * time.Millisecond}
		admissionFault.Store(int32(index + 1))
		if err = Run(secondaryConfig); err == nil {
			t.Fatal("lost admitted authority unexpectedly settled")
		}
		operation, err := pg.GetHostingProxyOperation(t.Context(), secondary.ActiveOperationID)
		if err != nil || !operation.MutationHold || operation.CompleteReceipt != nil || operation.Result != nil {
			t.Fatalf("lost authority cleared independent hold: %+v %v", operation, err)
		}
		journal, err := runner.OpenAttemptJournal(secondaryConfig.JournalDir)
		if err != nil {
			t.Fatal(err)
		}
		entries := journal.Snapshot().Proxy
		if len(entries) != 1 || entries[0].Phase != "apply_intent" {
			t.Fatalf("lost admission response lost durable intent: %+v", entries)
		}
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
		if err = Run(secondaryConfig); err == nil {
			t.Fatal("startup replay cleared unresolved proxy hold")
		}
		if _, err = os.Stat(filepath.Join(root, secondary.ID, "receipt.json")); !os.IsNotExist(err) {
			t.Fatalf("unacknowledged admission ran engine effect: %v", err)
		}
		probe("app.example.test", "/api/x", "B:/api/x")
		probe("security.example.test", "/", "A:/")
	}
	pendingImage, err = pg.GetHostingOperation(t.Context(), pendingImage.ID)
	if err != nil || pendingImage.State != "dispatched" {
		t.Fatalf("proxy-only agent claimed image task: %v %+v", err, pendingImage)
	}
	if err = Run(oldConfig); err != nil {
		t.Fatal(err)
	}
	pendingImage, err = pg.GetHostingOperation(t.Context(), pendingImage.ID)
	if err != nil || pendingImage.State != "succeeded" {
		t.Fatalf("proxy ownership holds starved unrelated image execution: %v %+v", err, pendingImage)
	}
	t.Log("default no-proxy agent served queued image work without claiming proxy; configured proxy-only agents skipped queued image; old-agent proxy configuration refused")
	t.Log("real lease expiry and human cancellation after admission retained durable intents and holds; restart performed no engine effects and existing HTTPS continued")
}
