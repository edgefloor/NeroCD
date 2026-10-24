package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/store/sqlcgen"
)

const applicationDatabasePebbleImage = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"

func applicationDatabaseRoutePortsFree(ports ...int) error {
	for _, port := range ports {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("fixture TCP port %d is not free", port)
		}
		if listener.Close() != nil {
			return fmt.Errorf("fixture TCP port %d probe did not close", port)
		}
		if port == 8053 {
			socket, err := net.ListenPacket("udp4", "127.0.0.1:8053")
			if err != nil {
				return errors.New("fixture DNS UDP port is not free")
			}
			if socket.Close() != nil {
				return errors.New("fixture DNS UDP probe did not close")
			}
		}
	}
	return nil
}

func applicationDatabaseRouteCapacity(t *testing.T) {
	t.Helper()
	if err := applicationDatabaseRoutePortsFree(80, 443, 19080, 14000, 15000, 8053, 20000, 20001, 20002, 20003); err != nil {
		t.Fatal(err)
	}
	t.Log("route fixture fixed listeners and initial app port capacity verified free")
}

func (f *databaseRunnerFixture) cleanupRouteOperation(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	op, err := f.operation(ctx, id)
	if err != nil {
		return errors.New("route cleanup operation unavailable")
	}
	if op.Result == nil || op.Result.ContainerID == "" {
		row, stageErr := sqlcgen.New(f.database).ReadApplicationDatabaseStage(ctx, id)
		var stage domain.HostingApplicationDatabaseStage
		if stageErr == nil && json.Unmarshal(row.Stage, &stage) == nil && op.Plan != nil && op.Release != nil && op.ApplicationDatabase != nil {
			input, inputErr := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
			if inputErr != nil || stage.Validate(*op.Plan, input) != nil {
				return errors.New("route cleanup stage identity invalid")
			}
			op.Result = &domain.HostingResult{ContainerID: stage.Candidate.ContainerID, PlanHash: op.Plan.Hash, ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID}
		} else {
			raw, inventoryErr := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.operation="+id)
			if inventoryErr != nil || len(strings.TrimSpace(string(raw))) != 0 {
				return errors.New("route cleanup candidate identity or absence unknown")
			}
			return nil
		}
	}
	if err = cleanupApplicationDatabaseFinal(ctx, op, func(ctx context.Context, args ...string) ([]byte, error) {
		return databaseRunnerDocker(ctx, nil, args...)
	}); err != nil {
		return err
	}
	if op.Snapshot.HostPort >= 1024 {
		if err = applicationDatabaseRoutePortsFree(op.Snapshot.HostPort); err != nil {
			return err
		}
		f.t.Logf("exact app absent and allocated port free operation=%s port=%d", id, op.Snapshot.HostPort)
	}
	return nil
}

func (f *databaseRunnerFixture) registerRouteOperationCleanup(id string) {
	f.t.Helper()
	f.t.Cleanup(func() {
		f.stop()
		if f.t.Failed() {
			_ = f.emitApplicationDatabaseFailure(id, "go_failure")
		}
		if err := f.cleanupRouteOperation(id); err != nil {
			f.t.Error(err)
		}
	})
}

func (f *databaseRunnerFixture) registerRouteDatabaseCleanup(resource string) {
	f.t.Cleanup(func() {
		f.stop()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var binding, id string
		if f.database.QueryRow(ctx, `SELECT b.id,coalesce(s.healthy->>'operation_id','') FROM resource_bindings b LEFT JOIN hosting_binding_state s ON s.binding_id=b.id WHERE b.resource_id=$1`, resource).Scan(&binding, &id) != nil || id == "" {
			f.t.Error("failure database cleanup healthy identity unavailable")
			return
		}
		if err := f.cleanupRouteOperation(id); err != nil {
			f.t.Error(err)
			return
		}
		raw, err := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.binding="+binding)
		if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
			f.t.Error("failure database binding containers remain")
		}
	})
}

// The DNS responder is local to the owned outer namespace. It cannot resolve
// arbitrary names or forward a fixture challenge to public DNS.
func applicationDatabaseRouteDNSAnswer(raw []byte) []byte {
	var m dnsmessage.Message
	if len(raw) > 4096 || m.Unpack(raw) != nil || m.Response || len(m.Questions) != 1 {
		return nil
	}
	q := m.Questions[0]
	m.Response, m.Authoritative = true, true
	m.Answers, m.Authorities, m.Additionals = nil, nil, nil
	if q.Class != dnsmessage.ClassINET || !strings.EqualFold(q.Name.String(), "connected-postgresql.example.test.") && !strings.EqualFold(q.Name.String(), "connected-redis.example.test.") && !strings.EqualFold(q.Name.String(), "unrelated.example.test.") {
		m.RCode = dnsmessage.RCodeNameError
	} else if q.Type == dnsmessage.TypeA {
		m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}}}
	}
	answer, _ := m.Pack()
	return answer
}

func applicationDatabaseRouteDNS(t *testing.T) {
	t.Helper()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:8053")
	if err != nil {
		t.Fatal("owned fixture DNS UDP unavailable")
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:8053")
	if err != nil {
		_ = udp.Close()
		t.Fatal("owned fixture DNS TCP unavailable")
	}
	var workers sync.WaitGroup
	t.Cleanup(func() {
		_ = udp.Close()
		_ = tcp.Close()
		workers.Wait()
		if err := applicationDatabaseRoutePortsFree(8053); err != nil {
			t.Error(err)
		} else {
			t.Log("fixture DNS TCP/UDP listener absence verified")
		}
	})
	workers.Go(func() {
		buffer := make([]byte, 4097)
		for {
			n, peer, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			if answer := applicationDatabaseRouteDNSAnswer(buffer[:n]); answer != nil {
				_, _ = udp.WriteTo(answer, peer)
			}
		}
	})
	workers.Go(func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var size [2]byte
			if _, err = io.ReadFull(conn, size[:]); err == nil {
				n := int(binary.BigEndian.Uint16(size[:]))
				if n <= 4096 {
					query := make([]byte, n)
					if _, err = io.ReadFull(conn, query); err == nil {
						if answer := applicationDatabaseRouteDNSAnswer(query); answer != nil {
							binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
							_, _ = conn.Write(append(size[:], answer...))
						}
					}
				}
			}
			_ = conn.Close()
		}
	})
}

type applicationDatabaseRouteFixture struct {
	roots          *x509.CertPool
	publicRoot     []byte
	proxy          domain.HostingProxy
	route          domain.HostingRoute
	hostname       string
	f              *databaseRunnerFixture
	unrelated      domain.HostingOperation
	unrelatedRoute domain.HostingRoute
	daemonID       string
}

func applicationDatabaseRouteReceiptHash(path string, raw []byte) ([32]byte, error) {
	var projection map[string]json.RawMessage
	if json.Unmarshal(raw, &projection) != nil {
		return [32]byte{}, errors.New("receipt response invalid")
	}
	fields := []string{"application_database_yield", "application_database_route_continuity"}
	if path == "/api/v1/runners/proxy/complete" {
		fields = []string{"complete_receipt", "result"}
	}
	selected := make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		value := projection[field]
		if len(value) == 0 || string(value) == "null" {
			return [32]byte{}, errors.New("immutable receipt absent")
		}
		selected[field] = value
	}
	canonical, err := json.Marshal(selected)
	return sha256.Sum256(canonical), err
}

// Commit the actual yield, withhold its first ACK, and require a byte-identical
// request under the same saved authority and an unchanged immutable receipt.
// No fixture endpoint mutates task state.
type applicationDatabaseRouteYieldWire struct {
	operation                   string
	path                        string
	entered, finished, replayed chan struct{}
	mu                          sync.Mutex
	requestHash, responseHash   [32]byte
	calls                       int
	exact                       bool
}

func newApplicationDatabaseRouteYieldWire(operation string) *applicationDatabaseRouteYieldWire {
	return &applicationDatabaseRouteYieldWire{operation: operation, path: "/api/v1/runners/hosting/database-binding/yield", entered: make(chan struct{}), finished: make(chan struct{}), replayed: make(chan struct{})}
}

func (v *applicationDatabaseRouteYieldWire) serve(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	if r.Method != "POST" || r.URL.Path != v.path {
		return false
	}
	limit := int64(16 << 10)
	if v.path == "/api/v1/runners/proxy/complete" {
		limit = 128 << 10
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	defer clear(raw)
	var authority domain.HostingAuthority
	if err != nil || int64(len(raw)) > limit || json.Unmarshal(raw, &authority) != nil {
		w.WriteHeader(400)
		return true
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	if authority.OperationID != v.operation {
		return false
	}
	receipt := httptest.NewRecorder()
	next.ServeHTTP(receipt, r)
	requestHash := sha256.Sum256(raw)
	responseHash, receiptErr := applicationDatabaseRouteReceiptHash(v.path, receipt.Body.Bytes())
	v.mu.Lock()
	v.calls++
	first := v.calls == 1
	if first {
		v.requestHash, v.responseHash = requestHash, responseHash
	} else {
		v.exact = requestHash == v.requestHash && responseHash == v.responseHash && receipt.Code == 200 && receiptErr == nil
	}
	v.mu.Unlock()
	if receipt.Code != 200 || receiptErr != nil {
		status := receipt.Code
		if receiptErr != nil && status == 200 {
			status = 502
		}
		w.WriteHeader(status)
		return true
	}
	if first {
		defer close(v.finished)
		close(v.entered)
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		return true
	}
	for key, values := range receipt.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(receipt.Code)
	_, _ = w.Write(receipt.Body.Bytes())
	select {
	case <-v.replayed:
	default:
		close(v.replayed)
	}
	return true
}

func (r *applicationDatabaseRouteFixture) deployment(in domain.HostingDeploymentInput) any {
	r.awaitProxy()
	raw, err := json.Marshal(in)
	if err != nil {
		r.f.t.Fatal("route deployment input unavailable")
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		r.f.t.Fatal("route deployment input malformed")
	}
	out["application_database_route_continuity"] = map[string]any{"expected_loaded_generation": r.proxy.LoadedGeneration, "expected_loaded_proxy_operation_id": r.proxy.ActiveOperationID, "routes": []domain.HostingRouteSwitchSelection{{RouteID: r.route.ID, ExpectedVersion: r.route.Version, ExpectedSelectedReleaseOperationID: r.route.SelectedReleaseOperationID}}}
	return out
}

func (r *applicationDatabaseRouteFixture) recoverYield(candidate, previous domain.HostingOperation, revision int64) {
	f := r.f
	v := newApplicationDatabaseRouteYieldWire(candidate.ID)
	f.routeYield.Store(v)
	f.start()
	ctx, cancel := context.WithTimeout(f.t.Context(), 180*time.Second)
	defer cancel()
	if reason := waitApplicationDatabaseComplete(ctx, f.process, v.entered, nil); reason != "" {
		f.t.Fatal("route preparation did not yield under original attempt: " + reason)
	}
	f.stop()
	select {
	case <-v.finished:
	case <-time.After(10 * time.Second):
		f.t.Fatal("yield response-loss handler did not join")
	}
	op, err := f.operation(f.t.Context(), candidate.ID)
	row, stageErr := sqlcgen.New(f.database).ReadApplicationDatabaseStage(f.t.Context(), candidate.ID)
	var stage domain.HostingApplicationDatabaseStage
	if err != nil || stageErr != nil || op.Plan == nil || op.ApplicationDatabase == nil || json.Unmarshal(row.Stage, &stage) != nil || stage.Phase != "ready" || op.State == "succeeded" || !op.MutationHold || op.CompleteReceipt != nil {
		f.t.Fatal("ready candidate was settled or lost its hold before route switch")
	}
	input, inputErr := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	if inputErr != nil || stage.Validate(*op.Plan, input) != nil {
		f.t.Fatal("yield lacks exact valid ready candidate proof")
	}
	var healthy, hold string
	if f.database.QueryRow(f.t.Context(), `SELECT healthy->>'operation_id',coalesce(hold_operation_id,'') FROM hosting_binding_state WHERE binding_id=$1`, previous.BindingID).Scan(&healthy, &hold) != nil || healthy != previous.ID || hold != candidate.ID {
		f.t.Fatal("candidate readiness changed healthy predecessor or mutation hold")
	}
	connection := f.connection(previous.ResourceID)
	if connection.MembershipRevision != revision || connection.HoldOperationID != candidate.ID {
		f.t.Fatal("candidate readiness changed active database membership or network hold")
	}
	r.assertSelected(previous)
	r.record(f.t.Context(), previous, "GET", "a", "retained_a")
	f.applicationContainer(previous)
	// The candidate endpoint is usable before it becomes current healthy or the
	// route target; record identity proves that A and B are distinct live apps.
	proof := op
	proof.Result = &domain.HostingResult{ContainerID: stage.Candidate.ContainerID}
	if applicationDatabaseHTTPRecord(f.t.Context(), proof, "GET", "a", "retained_a") != nil {
		f.t.Fatal("yielded candidate cannot read retained records")
	}
	public := databaseRunnerJSON[struct {
		Continuity struct {
			ProxyOperationID string `json:"proxy_operation_id"`
		} `json:"application_database_route_continuity"`
	}](f, f.token, "GET", "/api/v1/hosting-operations/"+candidate.ID, nil, 200)
	if public.Continuity.ProxyOperationID == "" {
		f.t.Fatal("candidate omitted immutable proxy continuation identity")
	}
	proxyWire := newApplicationDatabaseRouteYieldWire(public.Continuity.ProxyOperationID)
	proxyWire.path = "/api/v1/runners/proxy/complete"
	f.routeProxyComplete.Store(proxyWire)
	deliveries := f.runtimeDeliveryRequests.Load()
	f.start()
	select {
	case <-v.replayed:
	case <-time.After(45 * time.Second):
		f.t.Fatal("lost yield ACK did not replay")
	}
	v.mu.Lock()
	exact := v.exact
	v.mu.Unlock()
	if !exact {
		f.t.Fatal("yield replay changed original request authority or accepted response")
	}
	if reason := waitApplicationDatabaseComplete(ctx, f.process, proxyWire.entered, nil); reason != "" {
		f.t.Fatal("strict proxy continuation did not commit: " + reason)
	}
	f.stop()
	select {
	case <-proxyWire.finished:
	case <-time.After(10 * time.Second):
		f.t.Fatal("proxy Complete response-loss handler did not join")
	}
	r.assertSelected(proof)
	r.record(f.t.Context(), proof, "GET", "a", "retained_a")
	f.applicationContainer(previous)
	connection = f.connection(previous.ResourceID)
	if connection.MembershipRevision != revision || connection.HoldOperationID != candidate.ID || f.runtimeDeliveryRequests.Load() != deliveries {
		f.t.Fatal("proxy switch settled membership, released hold or repeated credential delivery before finalize")
	}
	f.start()
	select {
	case <-proxyWire.replayed:
	case <-time.After(45 * time.Second):
		f.t.Fatal("lost proxy Complete ACK did not replay")
	}
	proxyWire.mu.Lock()
	exact = proxyWire.exact
	proxyWire.mu.Unlock()
	if !exact {
		f.t.Fatal("proxy Complete replay changed original authority or accepted response")
	}
}

func newApplicationDatabaseRouteTrust(t *testing.T) (*x509.CertPool, []byte) {
	t.Helper()
	applicationDatabaseRouteDNS(t)
	name := databaseRunnerOwner + "-pebble"
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	raw, err := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		t.Fatal("fixture CA name is not proven free")
	}
	raw, err = databaseRunnerDocker(ctx, nil, "run", "--detach", "--name", name, "--label", databaseRunnerLabel+"="+databaseRunnerOwner, "--network", "host", "--env", "PEBBLE_VA_NOSLEEP=1", "--env", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/fixture/pebble.json,dst=/pebble-config.json,readonly", applicationDatabasePebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")
	id := strings.TrimSpace(string(raw))
	if err != nil || len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatal("fixture CA exact create identity unavailable")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		raw, err := databaseRunnerDocker(cleanup, nil, "inspect", id, "--format", `{"Id":{{json .Id}},"Image":{{json .Config.Image}},"Owner":{{json (index .Config.Labels "io.nerocd.database-runner.owner")}}}`)
		var proof struct{ ID, Image, Owner string }
		if err != nil || json.Unmarshal(raw, &proof) != nil || proof.ID != id || proof.Image != applicationDatabasePebbleImage || proof.Owner != databaseRunnerOwner {
			t.Error("fixture CA cleanup identity changed")
			return
		}
		if _, err = databaseRunnerDocker(cleanup, nil, "rm", "--force", id); err != nil {
			t.Error("fixture CA removal failed")
			return
		}
		raw, err = databaseRunnerDocker(cleanup, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
		if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
			t.Error("fixture CA absence unconfirmed")
		}
		if err := applicationDatabaseRoutePortsFree(14000, 15000); err != nil {
			t.Error(err)
		} else {
			t.Log("exact fixture CA absent and CA listener ports free")
		}
	})
	root := filepath.Join(t.TempDir(), "roots.pem")
	if _, err = databaseRunnerDocker(ctx, nil, "cp", id+":/test/certs/pebble.minica.pem", root); err != nil {
		t.Fatal("fixture public CA unavailable")
	}
	public, err := os.ReadFile(root)
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(public) {
		t.Fatal("fixture public CA invalid")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://localhost:15000/roots/0", nil)
		response, e := client.Do(req)
		if e == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			closeErr := response.Body.Close()
			if response.StatusCode == 200 && readErr == nil && closeErr == nil && len(body) <= 1<<20 && roots.AppendCertsFromPEM(body) {
				bundle := append(append([]byte(nil), public...), body...)
				if os.WriteFile(root, bundle, 0600) != nil {
					t.Fatal("fixture trust bundle unavailable")
				}
				t.Setenv("NEROCD_PROXY_COMPILED_FIXTURE", "disposable-dind")
				t.Setenv("NEROCD_PROXY_FIXTURE_ROOT", root)
				return roots, public
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture issuance CA did not become available")
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture CA setup canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *databaseRunnerFixture) initialApplicationDatabaseRoute(a domain.HostingOperation, engine string, roots *x509.CertPool, public []byte) *applicationDatabaseRouteFixture {
	f.t.Helper()
	f.stop()
	r := &applicationDatabaseRouteFixture{f: f, roots: roots, publicRoot: public, hostname: "connected-" + engine + ".example.test", daemonID: a.Release.DaemonID}
	r.proxy = databaseRunnerJSON[domain.HostingProxy](f, f.adminToken, "POST", "/api/v1/servers/"+f.serverID+"/proxy", domain.HostingProxyInput{ControlProjectID: a.ProjectID, IdempotencyKey: "connected_route_proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	certificates := filepath.Join(f.root, "proxy", r.proxy.ID, "certificates")
	if os.MkdirAll(certificates, 0700) != nil || os.WriteFile(filepath.Join(certificates, "root.pem"), public, 0600) != nil {
		f.t.Fatal("fixture proxy trust root setup failed")
	}
	f.t.Cleanup(func() { r.cleanup() })
	f.start()
	r.awaitProxy()
	databaseRunnerJSON[domain.HostingHostnameGrant](f, f.adminToken, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: a.ProjectID, Hostname: r.hostname}, 201)
	r.route = databaseRunnerJSON[domain.HostingRoute](f, f.token, "POST", "/api/v1/resources/"+a.ResourceID+"/routes", domain.HostingRouteInput{IdempotencyKey: "connected_route_a", Hostname: r.hostname, PathPrefix: "/", ExpectedHealthyOperationID: a.ID}, 202)
	r.awaitProxy()
	r.assertSelected(a)
	r.record(f.t.Context(), a, "GET", "a", "retained_a")
	r.record(f.t.Context(), a, "PUT", "https_a", "retained_https_a")
	r.record(f.t.Context(), a, "GET", "https_a", "retained_https_a")
	r.addUnrelated(a)
	f.routes = r
	return r
}

func (r *applicationDatabaseRouteFixture) addUnrelated(a domain.HostingOperation) {
	f := r.f
	f.stop()
	resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: a.ProjectID, ProjectEnvironmentID: f.environment, Name: "unrelated route", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")}}}, 201)
	databaseRunnerJSON[domain.ResourceBinding](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
	engine, namespace := "unrelated", "route_isolation"
	config := databaseRunnerJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("7", 32), Entries: []domain.HostingRuntimeEntry{{Name: "FIXTURE_DATABASE_ENGINE", Kind: "literal", Value: &engine}, {Name: "FIXTURE_DATABASE_NAMESPACE", Kind: "literal", Value: &namespace}}}, 200).Configuration
	op := databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "unrelated_route", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}, 202)
	f.registerRouteOperationCleanup(op.ID)
	f.start()
	settled, reason := waitDatabaseRunnerOperation(f.t.Context(), f.process, func(ctx context.Context) (domain.HostingOperation, error) { return f.operation(ctx, op.ID) })
	if reason != "" || settled.State != "succeeded" || settled.Result == nil || !settled.Result.Ready || settled.Result.Uncertain || settled.ApplicationDatabase != nil {
		f.t.Fatal("unrelated ordinary image deployment did not settle")
	}
	r.unrelated = settled
	databaseRunnerJSON[domain.HostingHostnameGrant](f, f.adminToken, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: a.ProjectID, Hostname: "unrelated.example.test"}, 201)
	r.unrelatedRoute = databaseRunnerJSON[domain.HostingRoute](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "unrelated_initial_route", Hostname: "unrelated.example.test", PathPrefix: "/", ExpectedHealthyOperationID: settled.ID}, 202)
	r.awaitProxy()
	r.unrelatedRoute = databaseRunnerJSON[domain.HostingRoute](f, f.token, "GET", "/api/v1/routes/"+r.unrelatedRoute.ID, nil, 200)
	r.assertUnrelated()
}

func (r *applicationDatabaseRouteFixture) assertUnrelated() {
	r.f.t.Helper()
	current := databaseRunnerJSON[domain.HostingRoute](r.f, r.f.token, "GET", "/api/v1/routes/"+r.unrelatedRoute.ID, nil, 200)
	if current.ID != r.unrelatedRoute.ID || current.Version != r.unrelatedRoute.Version || current.SelectedReleaseOperationID != r.unrelated.ID || current.Certificate != r.unrelatedRoute.Certificate || !current.Enabled || !current.HTTPReady || !current.HTTPSReady || current.Certificate.Status != "trusted" {
		r.f.t.Fatal("unrelated route selection or trusted certificate changed")
	}
	if applicationDatabaseRouteHTTPRecord(r.f.t.Context(), r.roots, "unrelated.example.test", current.Certificate.Fingerprint, r.unrelated, "GET", "unrelated", "unrelated_route") != nil {
		r.f.t.Fatal("unrelated HTTPS served backend changed")
	}
}

func (r *applicationDatabaseRouteFixture) awaitProxy() {
	f := r.f
	deadline := time.Now().Add(90 * time.Second)
	for {
		p := databaseRunnerJSON[domain.HostingProxy](f, f.adminToken, "GET", "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
		if p.LoadedGeneration == p.DesiredGeneration && p.LoadedGeneration > 0 && p.ActiveOperationID != "" && p.HoldOperationID == "" && !p.RestoreOwnershipHeld {
			r.proxy = p
			return
		}
		if time.Now().After(deadline) || f.process.status().Exited {
			f.t.Fatal("managed route generation failed to load")
		}
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("managed route wait canceled")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (r *applicationDatabaseRouteFixture) assertSelected(op domain.HostingOperation) {
	r.f.t.Helper()
	current := databaseRunnerJSON[domain.HostingRoute](r.f, r.f.token, "GET", "/api/v1/routes/"+r.route.ID, nil, 200)
	if current.SelectedReleaseOperationID != op.ID || !current.Enabled || !current.HTTPReady || !current.HTTPSReady || current.Certificate.Status != "trusted" || current.Certificate.Hostname != r.hostname {
		r.f.t.Fatal("route selection, HTTP readiness and trusted HTTPS readiness disagree")
	}
	r.route = current
	if r.unrelated.ID != "" {
		r.assertUnrelated()
	}
}

func applicationDatabaseRouteHTTPRecord(ctx context.Context, roots *x509.CertPool, hostname, fingerprint string, op domain.HostingOperation, method, key, value string) error {
	return applicationDatabaseRouteHTTPRecordAt(ctx, roots, hostname, fingerprint, op, method, key, value, "127.0.0.1:443")
}

// The parameterized loopback dial permits real TLS trust and identity negative
// tests on unprivileged ports. Physical probes always call the fixed-443 wrapper.
func applicationDatabaseRouteHTTPRecordAt(ctx context.Context, roots *x509.CertPool, hostname, fingerprint string, op domain.HostingOperation, method, key, value, target string) error {
	canonical, hostErr := domain.NormalizeHostingHostname(hostname)
	ip, _, targetErr := net.SplitHostPort(target)
	if roots == nil || op.Result == nil || len(op.Result.ContainerID) != 64 || len(fingerprint) != 64 || hostErr != nil || canonical != hostname {
		return errors.New("invalid trusted route identity")
	}
	if targetErr != nil || ip != "127.0.0.1" {
		return errors.New("fixture dial target refused")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != hostname+":443" {
			return nil, errors.New("fixture dial target refused")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", target)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+hostname+"/records/"+key, strings.NewReader(value))
	if err != nil {
		return errors.New("fixture HTTPS request invalid")
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("verified fixture HTTPS request failed")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	defer clear(body)
	closeErr := response.Body.Close()
	if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || len(response.TLS.PeerCertificates) == 0 {
		return errors.New("fixture HTTPS lacks verified certificate")
	}
	hash := sha256.Sum256(response.TLS.PeerCertificates[0].Raw)
	if readErr != nil || closeErr != nil || len(body) > 4096 || response.StatusCode != 200 || string(body) != value || response.Header.Get("X-NeroCD-Fixture-Instance") != op.Result.ContainerID[:12] || hex.EncodeToString(hash[:]) != fingerprint {
		return errors.New("fixture HTTPS served wrong instance, certificate or record")
	}
	return nil
}

func (r *applicationDatabaseRouteFixture) record(ctx context.Context, op domain.HostingOperation, method, key, value string) {
	r.f.t.Helper()
	if applicationDatabaseRouteHTTPRecord(ctx, r.roots, r.hostname, r.route.Certificate.Fingerprint, op, method, key, value) != nil {
		r.f.t.Fatal("trusted HTTPS app identity/read/write proof failed")
	}
}

func (r *applicationDatabaseRouteFixture) cleanup() {
	f := r.f
	f.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	name := "nerocd-proxy-" + r.proxy.ID
	daemon, err := databaseRunnerDocker(ctx, nil, "info", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(daemon)) != r.daemonID {
		f.t.Error("proxy cleanup daemon identity changed")
		return
	}
	raw, err := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	id := strings.TrimSpace(string(raw))
	if err != nil {
		f.t.Error("proxy cleanup inventory unavailable")
		return
	}
	if id == "" {
		if err := applicationDatabaseRoutePortsFree(80, 443, 19080); err != nil {
			f.t.Error(err)
		}
		return
	}
	raw, err = databaseRunnerDocker(ctx, nil, "inspect", id, "--format", `{"Id":{{json .Id}},"Image":{{json .Config.Image}},"Proxy":{{json (index .Config.Labels "io.nerocd.proxy")}},"Daemon":{{json (index .Config.Labels "io.nerocd.daemon")}},"Server":{{json (index .Config.Labels "io.nerocd.server")}},"Destination":{{json (index .Config.Labels "io.nerocd.destination")}},"Runner":{{json (index .Config.Labels "io.nerocd.runner")}}}`)
	var proof struct{ ID, Image, Proxy, Daemon, Server, Destination, Runner string }
	if err != nil || json.Unmarshal(raw, &proof) != nil || proof.ID != id || proof.Proxy != r.proxy.ID || proof.Image != hostingproxy.Image || proof.Daemon != r.daemonID || proof.Server != r.proxy.ServerID || proof.Destination != r.proxy.DestinationID || proof.Runner != r.proxy.RunnerID {
		f.t.Error("proxy cleanup identity changed")
		return
	}
	if _, err = databaseRunnerDocker(ctx, nil, "rm", "--force", id); err != nil {
		f.t.Error("proxy cleanup failed")
		return
	}
	raw, err = databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	if err != nil || len(strings.TrimSpace(string(raw))) != 0 {
		f.t.Error("proxy cleanup absence unconfirmed")
	}
	if err := applicationDatabaseRoutePortsFree(80, 443, 19080); err != nil {
		f.t.Error(err)
	} else {
		f.t.Log("exact managed proxy absent and proxy listener ports free")
	}
}

func TestApplicationDatabaseRouteRuntimeDNS(t *testing.T) {
	for _, host := range []string{"connected-postgresql.example.test.", "connected-redis.example.test.", "unrelated.example.test.", "public.example.org."} {
		t.Run(host, func(t *testing.T) {
			name, err := dnsmessage.NewName(host)
			if err != nil {
				t.Fatal(err)
			}
			query, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 17}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}).Pack()
			if err != nil {
				t.Fatal(err)
			}
			var response dnsmessage.Message
			if response.Unpack(applicationDatabaseRouteDNSAnswer(query)) != nil || response.ID != 17 || !response.Response {
				t.Fatal("DNS response invalid")
			}
			if host == "public.example.org." {
				if response.RCode != dnsmessage.RCodeNameError || len(response.Answers) != 0 {
					t.Fatal("foreign DNS resolved")
				}
			} else if len(response.Answers) != 1 || response.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{127, 0, 0, 1} {
				t.Fatal("fixture DNS did not pin loopback")
			}
			if applicationDatabaseRouteDNSAnswer(make([]byte, 4097)) != nil {
				t.Fatal("oversized DNS accepted")
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimePortIsolation(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	if applicationDatabaseRoutePortsFree(port) == nil {
		t.Fatal("occupied app port was mistaken for clean schema capacity")
	}
	if listener.Close() != nil {
		t.Fatal("owned app listener did not close")
	}
	if err := applicationDatabaseRoutePortsFree(port); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationDatabaseRouteRuntimeTrustedServedIdentity(t *testing.T) {
	for _, mode := range []string{"exact", "wrong_instance", "missing_instance", "untrusted", "wrong_fingerprint", "wrong_hostname", "wrong_value", "redirect", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.Repeat("a", 64)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				instance := id[:12]
				if mode == "wrong_instance" {
					instance = strings.Repeat("b", 12)
				}
				if mode != "missing_instance" {
					w.Header().Set("X-NeroCD-Fixture-Instance", instance)
				}
				if mode == "redirect" {
					w.Header().Set("Location", "https://public.example.org")
					w.WriteHeader(302)
					return
				}
				body := "retained_a"
				if mode == "wrong_value" {
					body = "retained_b"
				}
				if mode == "oversized" {
					body = strings.Repeat("a", 4097)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			if mode != "untrusted" {
				roots.AddCert(server.Certificate())
			}
			hash := sha256.Sum256(server.Certificate().Raw)
			fingerprint, hostname := hex.EncodeToString(hash[:]), "example.com"
			if mode == "wrong_fingerprint" {
				fingerprint = strings.Repeat("b", 64)
			}
			if mode == "wrong_hostname" {
				hostname = "foreign.example.test"
			}
			err := applicationDatabaseRouteHTTPRecordAt(t.Context(), roots, hostname, fingerprint, domain.HostingOperation{Result: &domain.HostingResult{ContainerID: id}}, "GET", "a", "retained_a", server.Listener.Addr().String())
			if (err == nil) != (mode == "exact") {
				t.Fatalf("trusted served identity check mode=%s accepted=%t", mode, err == nil)
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimeReceiptReplay(t *testing.T) {
	for _, path := range []string{"/api/v1/runners/hosting/database-binding/yield", "/api/v1/runners/proxy/complete"} {
		for _, mode := range []string{"exact", "changed_request", "changed_receipt", "mutable_state", "missing_receipt"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				wire := newApplicationDatabaseRouteYieldWire("operation")
				wire.path = path
				raw := `{"operation_id":"operation","run_id":"run","lease_id":"lease","attempt":1,"fence":"PRIVATE-CANARY"}`
				calls := 0
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					out := map[string]any{"state": "applying", "updated_at": "first", "application_database_yield": map[string]string{"receipt_id": "yield"}, "application_database_route_continuity": map[string]string{"hash": "continuation"}, "complete_receipt": map[string]string{"id": "complete"}, "result": map[string]string{"outcome": "applied"}}
					if calls > 1 {
						if mode == "mutable_state" {
							out["state"] = "verifying"
							out["updated_at"] = "second"
						}
						if mode == "changed_receipt" {
							out["application_database_yield"] = map[string]string{"receipt_id": "foreign"}
							out["complete_receipt"] = map[string]string{"id": "foreign"}
						}
						if mode == "missing_receipt" {
							delete(out, "application_database_yield")
							delete(out, "complete_receipt")
						}
					}
					writeJSON(w, 200, out)
				})
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan bool, 1)
				go func() {
					done <- wire.serve(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "POST", path, strings.NewReader(raw)), next)
				}()
				select {
				case <-wire.entered:
				case <-time.After(time.Second):
					cancel()
					t.Fatal("yield observer did not commit before response loss")
				}
				cancel()
				select {
				case handled := <-done:
					if !handled {
						t.Fatal("wire did not handle exact first receipt")
					}
				case <-time.After(time.Second):
					t.Fatal("yield observer did not join")
				}
				if mode == "changed_request" {
					raw = strings.Replace(raw, "PRIVATE-CANARY", "FOREIGN-CANARY", 1)
				}
				response := httptest.NewRecorder()
				if !wire.serve(response, httptest.NewRequest("POST", path, strings.NewReader(raw)), next) {
					t.Fatal("replay bypassed observer")
				}
				wire.mu.Lock()
				exact := wire.exact
				wire.mu.Unlock()
				if exact != (mode == "exact" || mode == "mutable_state") || calls != 2 {
					t.Fatal("wire replay did not preserve immutable receipt and full original authority")
				}
				if strings.Contains(response.Body.String(), "CANARY") {
					t.Fatal("wire response exposed authority")
				}
			})
		}
	}
}

// Separate schemas keep a deliberate readiness or strict-switch failure from
// supplying authority to a later success case. All cases share only owned DinD.
func TestHostingApplicationDatabaseRouteFailuresCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_DATABASE_CONNECTION_ROUTES") != "1" {
		t.Skip("requires approved combined route fixture profile")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || applicationDatabaseFixtureImage(os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")) != nil {
		t.Fatal("route failures require native owned image fixture")
	}
	for _, engine := range []string{"postgresql", "redis"} {
		for _, failure := range []string{"readiness", "cutover"} {
			if !t.Run(engine+"/"+failure, func(t *testing.T) {
				applicationDatabaseRouteCapacity(t)
				roots, public := newApplicationDatabaseRouteTrust(t)
				f := newDatabaseRunnerFixtureWithProfile(t, true, true, true)
				view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseCreateInput(engine, f.environment, f.destination), 201)
				f.registerRouteDatabaseCleanup(view.Resource.ID)
				f.await(f.deploy(view, "route_failure_source").ID)
				resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "route failure " + engine, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")}}}, 201)
				databaseRunnerJSON[domain.ResourceBinding](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
				namespace := "connected_" + engine
				config := databaseRunnerJSON[struct {
					Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
				}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("a", 32), Entries: []domain.HostingRuntimeEntry{{Name: "FIXTURE_DATABASE_ENGINE", Kind: "literal", Value: &engine}, {Name: "FIXTURE_DATABASE_NAMESPACE", Kind: "literal", Value: &namespace}}}, 200).Configuration
				deploy := domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "route_failure_a", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}
				f.stop()
				connection := databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connections", domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: view.Resource.ID, ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: config.RuntimeGeneration, ExpectedDatabaseGeneration: view.Resource.Generation, RequestID: strings.Repeat("b", 32)}, Deployment: deploy}, 202)
				f.registerRouteOperationCleanup(connection.ApplicationOperationID)
				f.start()
				active := f.activeConnection(resource.ID)
				a := f.awaitConnectionOperation(connection.ApplicationOperationID)
				f.appRecord(a, "PUT", "a", "retained_a")
				r := f.initialApplicationDatabaseRoute(a, engine, roots, public)
				before := f.applicationContainer(a)
				f.stop()
				deploy.IdempotencyKey = "route_failure_b"
				deploy.ExpectedRuntimeConfigurationGeneration = a.Snapshot.RuntimeConfiguration.Generation
				if failure == "readiness" {
					deploy.Readiness = domain.HostingReadiness{Path: "/never-ready", TimeoutSeconds: 3}
				}
				candidate := databaseRunnerJSON[domain.HostingOperation](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connection/deployments", r.deployment(deploy), 202)
				f.registerRouteOperationCleanup(candidate.ID)
				var wire *applicationDatabaseRouteYieldWire
				if failure == "cutover" {
					wire = newApplicationDatabaseRouteYieldWire(candidate.ID)
					f.routeYield.Store(wire)
				}
				f.start()
				deadline := time.Now().Add(120 * time.Second)
				var staged domain.HostingApplicationDatabaseStage
				for {
					row, err := sqlcgen.New(f.database).ReadApplicationDatabaseStage(t.Context(), candidate.ID)
					if err == nil && json.Unmarshal(row.Stage, &staged) == nil && (failure == "readiness" && staged.Phase == "uncertain" || failure == "cutover" && staged.Phase == "ready") {
						break
					}
					if time.Now().After(deadline) || f.process.status().Exited {
						t.Fatal("route failure candidate did not reach exact expected stage")
					}
					select {
					case <-t.Context().Done():
						t.Fatal("route failure wait canceled")
					case <-time.After(100 * time.Millisecond):
					}
				}
				if wire != nil {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					reason := waitApplicationDatabaseComplete(ctx, f.process, wire.entered, nil)
					cancel()
					if reason != "" {
						t.Fatal("cutover failure did not commit original ready yield")
					}
				}
				f.stop()
				if wire != nil {
					select {
					case <-wire.finished:
					case <-time.After(10 * time.Second):
						t.Fatal("cutover fault handler did not join")
					}
				}
				op, err := f.operation(t.Context(), candidate.ID)
				if err != nil || op.Plan == nil || op.Release == nil || staged.Candidate.ContainerID == "" {
					t.Fatal("route failure exact candidate identity unavailable")
				}
				if failure == "cutover" {
					// Recheck exact create labels and image before the deliberate stop.
					var actual databaseRunnerContainer
					if json.Unmarshal(f.docker("inspect", staged.Candidate.ContainerID, "--format", `{"Id":{{json .Id}},"Image":{{json .Image}},"Config":{"Labels":{{json .Config.Labels}}},"State":{{json .State}}}`), &actual) != nil || actual.ID != staged.Candidate.ContainerID || actual.Image != op.Release.ImageID || !actual.State.Running || actual.Config.Labels["io.nerocd.operation"] != op.ID || actual.Config.Labels["io.nerocd.binding"] != op.BindingID || actual.Config.Labels["io.nerocd.plan"] != op.Plan.Hash {
						t.Fatal("cutover stop target identity changed")
					}
					f.docker("stop", "--time", "1", staged.Candidate.ContainerID)
					public := databaseRunnerJSON[struct {
						Continuity struct {
							ProxyOperationID string `json:"proxy_operation_id"`
						} `json:"application_database_route_continuity"`
					}](f, f.token, "GET", "/api/v1/hosting-operations/"+candidate.ID, nil, 200)
					if public.Continuity.ProxyOperationID == "" {
						t.Fatal("cutover failure proxy identity absent")
					}
					f.start()
					deadline = time.Now().Add(120 * time.Second)
					for {
						proxy := databaseRunnerJSON[domain.HostingProxyOperationView](f, f.token, "GET", "/api/v1/resources/"+resource.ID+"/route-switches/"+public.Continuity.ProxyOperationID, nil, 200)
						if proxy.State == "rolled_back" && proxy.EngineOutcome == "rolled_back" {
							break
						}
						if time.Now().After(deadline) || f.process.status().Exited {
							t.Fatal("strict cutover failure did not prove rollback")
						}
						select {
						case <-t.Context().Done():
							t.Fatal("rollback wait canceled")
						case <-time.After(100 * time.Millisecond):
						}
					}
					f.stop()
				}
				r.assertSelected(a)
				r.record(t.Context(), a, "GET", "a", "retained_a")
				r.record(t.Context(), a, "PUT", "after_failure", "retained_after_failure")
				r.record(t.Context(), a, "GET", "after_failure", "retained_after_failure")
				if !reflect.DeepEqual(before, f.applicationContainer(a)) {
					t.Fatal("failure changed exact old app identity or endpoint")
				}
				current := f.connection(resource.ID)
				failed, failedErr := f.operation(t.Context(), candidate.ID)
				if failedErr != nil || failed.State == "succeeded" || !failed.MutationHold || current.HoldOperationID != candidate.ID {
					t.Fatal("failed routed candidate falsely settled or released its safety holds")
				}
				var healthy string
				if current.MembershipRevision != active.MembershipRevision || f.database.QueryRow(t.Context(), `SELECT healthy->>'operation_id' FROM hosting_binding_state WHERE binding_id=$1`, a.BindingID).Scan(&healthy) != nil || healthy != a.ID {
					t.Fatal("failure settled candidate as healthy or active database member")
				}
				var retirements int
				if f.database.QueryRow(t.Context(), `SELECT count(*) FROM hosting_application_database_retirements WHERE operation_id=$1`, candidate.ID).Scan(&retirements) != nil || retirements != 0 {
					t.Fatal("failure retired old app")
				}
				fmt.Println("DATABASE_CONNECTION_ROUTE_FAILURE_PASS " + engine + " " + failure)
			}) {
				return
			}
			applicationDatabaseRouteCapacity(t)
			fmt.Println("DATABASE_CONNECTION_ROUTE_CAPACITY_PASS " + engine + " " + failure)
		}
	}
}
