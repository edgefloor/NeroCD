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

type applicationDatabaseRetirementPhase struct {
	OperationID, Phase, PredecessorOperationID, PredecessorContainerID string
}

func applicationDatabaseRetirementPhasesExact(phases []applicationDatabaseRetirementPhase, operation, previous, container string) bool {
	if len(phases) != 2 || operation == "" || previous == "" || container == "" {
		return false
	}
	seen := map[string]bool{}
	for _, phase := range phases {
		if phase.OperationID != operation || phase.PredecessorOperationID != previous || phase.PredecessorContainerID != container || phase.Phase != "retire_intent" && phase.Phase != "detached" || seen[phase.Phase] {
			return false
		}
		seen[phase.Phase] = true
	}
	return seen["retire_intent"] && seen["detached"]
}

func (f *databaseRunnerFixture) assertApplicationDatabaseRetirement(op, previous domain.HostingOperation) {
	f.t.Helper()
	if op.ApplicationDatabase == nil || len(op.ApplicationDatabase.Members) != 1 || previous.Result == nil {
		f.t.Fatal("retirement predecessor evidence unavailable")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	var total int
	if f.database.QueryRow(ctx, `SELECT count(*) FROM hosting_application_database_retirements WHERE operation_id=$1`, op.ID).Scan(&total) != nil || total != 2 {
		f.t.Fatal("replacement requires exactly one retirement intent and one detachment")
	}
	q := sqlcgen.New(f.database)
	var firstPlan domain.HostingDatabaseBindingEndpointPlan
	var intent domain.HostingDatabaseBindingEndpointReceipt
	phases := make([]applicationDatabaseRetirementPhase, 0, 2)
	for _, phase := range []string{"retire_intent", "detached"} {
		row, err := q.ReadApplicationDatabaseRetirementPhase(ctx, sqlcgen.ReadApplicationDatabaseRetirementPhaseParams{OperationID: op.ID, Phase: phase})
		clear(row.Authority)
		var plan domain.HostingDatabaseBindingEndpointPlan
		var receipt domain.HostingDatabaseBindingEndpointReceipt
		if err != nil || json.Unmarshal(row.Plan, &plan) != nil || json.Unmarshal(row.Receipt, &receipt) != nil || row.OperationID != op.ID || row.Phase != phase || row.ReceiptID != receipt.ReceiptID || row.ReceiptHash != receipt.Hash || receipt.Phase != phase || receipt.Validate(plan, op.ApplicationDatabase.Binding) != nil || plan.Action != "retire" || plan.Target != op.ApplicationDatabase.Members[0].EndpointPlan.Target || plan.MembershipRevision != op.ApplicationDatabase.MembershipRevision+2 {
			f.t.Fatal("retirement phase lacks exact immutable predecessor evidence")
		}
		phases = append(phases, applicationDatabaseRetirementPhase{OperationID: row.OperationID, Phase: phase, PredecessorOperationID: plan.Target.ContainerOperationID, PredecessorContainerID: plan.Target.ContainerID})
		if phase == "retire_intent" {
			if receipt.ValidateTransition(nil, plan, op.ApplicationDatabase.Binding) != nil {
				f.t.Fatal("retirement intent is invalid")
			}
			firstPlan, intent = plan, receipt
		} else if plan != firstPlan || receipt.ValidateTransition(&intent, plan, op.ApplicationDatabase.Binding) != nil {
			f.t.Fatal("detachment does not settle the exact retirement intent")
		}
	}
	if !applicationDatabaseRetirementPhasesExact(phases, op.ID, previous.ID, previous.Result.ContainerID) {
		f.t.Fatal("retirement phases did not identify the exact replaced operation and container")
	}
}

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

// Historical A/B or a completed explicit cleanup may already have removed the
// container. Both the exact ID and operation inventory must prove absence on the
// pinned daemon. An inspect error alone never proves retirement.
func cleanupApplicationDatabaseRetained(ctx context.Context, op domain.HostingOperation, docker func(context.Context, ...string) ([]byte, error), portsFree func(...int) error) error {
	invalid := errors.New("retained fixture cleanup identity or absence unconfirmed")
	if !applicationDatabaseCleanupIdentity(op) {
		return invalid
	}
	checkDaemon := func() bool {
		raw, err := docker(ctx, "info", "--format", "{{.ID}}")
		return err == nil && strings.TrimSpace(string(raw)) == op.Release.DaemonID
	}
	inventory := func(filter string) (string, error) {
		raw, err := docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", filter)
		return strings.TrimSpace(string(raw)), err
	}
	if !checkDaemon() {
		return invalid
	}
	exact, err := inventory("id=" + op.Result.ContainerID)
	if err != nil {
		return invalid
	}
	owned, err := inventory("label=io.nerocd.operation=" + op.ID)
	if err != nil {
		return invalid
	}
	if exact != "" || owned != "" {
		if exact != op.Result.ContainerID || owned != op.Result.ContainerID {
			return invalid
		}
		if cleanupApplicationDatabaseFinal(ctx, op, docker) != nil {
			return invalid
		}
	}
	exact, err = inventory("id=" + op.Result.ContainerID)
	if err != nil || exact != "" {
		return invalid
	}
	owned, err = inventory("label=io.nerocd.operation=" + op.ID)
	if err != nil || owned != "" || !checkDaemon() {
		return invalid
	}
	if op.Snapshot.HostPort >= 1024 {
		if err := portsFree(op.Snapshot.HostPort); err != nil {
			return err
		}
	}
	return nil
}

// An interrupted database or ordinary-image operation may have created its sole
// candidate before Complete supplies Result. Derive only its cleanup identity
// from the exact operation inventory and retained plan/release, then enforce all
// ownership checks. This does not write or invent controller completion state.
func cleanupApplicationDatabasePreComplete(ctx context.Context, op domain.HostingOperation, docker func(context.Context, ...string) ([]byte, error), portsFree func(...int) error) error {
	if op.ID == "" || op.Result != nil && op.Result.ContainerID != "" {
		return errors.New("pre-Complete cleanup requires an exact incomplete operation")
	}
	raw, err := docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.operation="+op.ID)
	ids := strings.Fields(string(raw))
	if err != nil || len(ids) > 1 {
		return errors.New("route cleanup candidate identity or absence unknown")
	}
	if len(ids) == 0 {
		if op.Snapshot.HostPort >= 1024 {
			return portsFree(op.Snapshot.HostPort)
		}
		return nil
	}
	if op.Plan == nil || op.Release == nil {
		return errors.New("route cleanup candidate lacks retained plan and release")
	}
	op.Result = &domain.HostingResult{ContainerID: ids[0], PlanHash: op.Plan.Hash, ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID}
	return cleanupApplicationDatabaseRetained(ctx, op, docker, portsFree)
}

func (f *databaseRunnerFixture) cleanupRouteOperation(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	op, err := f.operation(ctx, id)
	if err != nil {
		// Reserved workflow children need not have been admitted before a test
		// aborts. Only successful exact operation inventory can prove no effect.
		var exists bool
		if id == "" || f.database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_operations WHERE id=$1)`, id).Scan(&exists) != nil || exists {
			return errors.New("route cleanup operation unavailable")
		}
		raw, inventoryErr := databaseRunnerDocker(ctx, nil, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=io.nerocd.operation="+id)
		if inventoryErr != nil || len(strings.TrimSpace(string(raw))) != 0 {
			return errors.New("route cleanup operation or absence unavailable")
		}
		return nil
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
			return cleanupApplicationDatabasePreComplete(ctx, op, func(ctx context.Context, args ...string) ([]byte, error) {
				return databaseRunnerDocker(ctx, nil, args...)
			}, applicationDatabaseRoutePortsFree)
		}
	}
	if err = cleanupApplicationDatabaseRetained(ctx, op, func(ctx context.Context, args ...string) ([]byte, error) {
		return databaseRunnerDocker(ctx, nil, args...)
	}, applicationDatabaseRoutePortsFree); err != nil {
		return err
	}
	if op.Snapshot.HostPort >= 1024 {
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

func applicationDatabaseRouteProxySettled(p, expected domain.HostingProxy, daemon string) bool {
	return expected.ID != "" && daemon != "" && p.ID == expected.ID && p.ServerID == expected.ServerID && p.DestinationID == expected.DestinationID && p.RunnerID == expected.RunnerID && p.ControlProjectID == expected.ControlProjectID && reflect.DeepEqual(p.Configuration, expected.Configuration) && p.Enabled && p.DaemonID == daemon && p.LoadedGeneration == p.DesiredGeneration && p.LoadedGeneration > 0 && p.ActiveOperationID == "" && p.HoldOperationID == "" && !p.RestoreOwnershipHeld
}

func applicationDatabaseRouteContinuity(p domain.HostingProxy, route domain.HostingRoute, options domain.HostingRouteSwitchOptions) (domain.HostingApplicationDatabaseRouteContinuityInput, error) {
	var zero domain.HostingApplicationDatabaseRouteContinuityInput
	if !applicationDatabaseRouteProxySettled(p, p, p.DaemonID) || !route.Enabled || !options.Available || options.Reason != "available" || options.PendingOperationID != "" || options.ResourceID != route.ResourceID || options.LoadedGeneration == nil || *options.LoadedGeneration != p.LoadedGeneration || options.LoadedProxyOperationID == nil || len(options.Routes) != 1 {
		return zero, errors.New("public route continuation options do not match settled proxy")
	}
	selected := options.Routes[0]
	if selected.ID != route.ID || selected.Version != route.Version || selected.SelectedReleaseOperationID != route.SelectedReleaseOperationID || selected.Hostname != route.Hostname || selected.PathPrefix != route.PathPrefix {
		return zero, errors.New("public route continuation selection changed")
	}
	healthy := false
	for _, target := range options.Targets {
		if target.Kind == "current_healthy" {
			if healthy || target.OperationID != route.SelectedReleaseOperationID {
				return zero, errors.New("public route continuation healthy predecessor changed")
			}
			healthy = true
		}
	}
	if !healthy {
		return zero, errors.New("public route continuation lacks healthy predecessor")
	}
	return domain.CanonicalHostingApplicationDatabaseRouteContinuityInput(domain.HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: *options.LoadedGeneration, ExpectedLoadedProxyOperationID: *options.LoadedProxyOperationID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: selected.ID, ExpectedVersion: selected.Version, ExpectedSelectedReleaseOperationID: selected.SelectedReleaseOperationID}}})
}

func (r *applicationDatabaseRouteFixture) deployment(in domain.HostingDeploymentInput) any {
	r.awaitProxy()
	options := databaseRunnerJSON[domain.HostingRouteSwitchOptions](r.f, r.f.token, "GET", "/api/v1/resources/"+r.route.ResourceID+"/route-switches", nil, 200)
	continuity, err := applicationDatabaseRouteContinuity(r.proxy, r.route, options)
	if err != nil {
		r.f.t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		r.f.t.Fatal("route deployment input unavailable")
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		r.f.t.Fatal("route deployment input malformed")
	}
	out["application_database_route_continuity"] = continuity
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
		if applicationDatabaseRouteProxySettled(p, r.proxy, r.daemonID) {
			r.proxy = p
			return
		}
		if time.Now().After(deadline) || f.process.status().Exited {
			f.t.Fatalf("managed route generation failed to load: loaded=%d desired=%d active=%t held=%t restore_held=%t identity_match=%t daemon_match=%t", p.LoadedGeneration, p.DesiredGeneration, p.ActiveOperationID != "", p.HoldOperationID != "", p.RestoreOwnershipHeld, p.ID == r.proxy.ID && p.ServerID == r.proxy.ServerID && p.DestinationID == r.proxy.DestinationID && p.RunnerID == r.proxy.RunnerID, p.DaemonID == r.daemonID)
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

func TestApplicationDatabaseRouteRuntimeSettledProxy(t *testing.T) {
	expected := domain.HostingProxy{ID: "proxy_fixture", ServerID: "server_fixture", DestinationID: "destination_fixture", RunnerID: "runner_fixture", ControlProjectID: "proj_fixture", Enabled: true, DaemonID: "daemon_fixture", LoadedGeneration: 3, DesiredGeneration: 3}
	for _, test := range []struct {
		name   string
		change func(*domain.HostingProxy)
		want   bool
	}{
		{"settled", func(*domain.HostingProxy) {}, true},
		{"active", func(p *domain.HostingProxy) { p.ActiveOperationID = "pop_running" }, false},
		{"held", func(p *domain.HostingProxy) { p.HoldOperationID = "pop_held" }, false},
		{"restore held", func(p *domain.HostingProxy) { p.RestoreOwnershipHeld = true }, false},
		{"unloaded", func(p *domain.HostingProxy) { p.LoadedGeneration = 0; p.DesiredGeneration = 0 }, false},
		{"pending generation", func(p *domain.HostingProxy) { p.DesiredGeneration++ }, false},
		{"proxy identity", func(p *domain.HostingProxy) { p.ID = "proxy_other" }, false},
		{"server identity", func(p *domain.HostingProxy) { p.ServerID = "server_other" }, false},
		{"destination identity", func(p *domain.HostingProxy) { p.DestinationID = "destination_other" }, false},
		{"runner identity", func(p *domain.HostingProxy) { p.RunnerID = "runner_other" }, false},
		{"project identity", func(p *domain.HostingProxy) { p.ControlProjectID = "proj_other" }, false},
		{"daemon identity", func(p *domain.HostingProxy) { p.DaemonID = "daemon_other" }, false},
		{"disabled", func(p *domain.HostingProxy) { p.Enabled = false }, false},
		{"configuration", func(p *domain.HostingProxy) { p.Configuration.ManagementPort = 19081 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := expected
			test.change(&p)
			if got := applicationDatabaseRouteProxySettled(p, expected, expected.DaemonID); got != test.want {
				t.Fatalf("settled=%t want=%t", got, test.want)
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimeLoadedPredecessor(t *testing.T) {
	p := domain.HostingProxy{ID: "proxy_fixture", Enabled: true, DaemonID: "daemon_fixture", LoadedGeneration: 3, DesiredGeneration: 3}
	route := domain.HostingRoute{ID: "route_fixture", ResourceID: "resource_fixture", Version: 2, SelectedReleaseOperationID: "hop_previous", Hostname: "connected-postgresql.example.test", PathPrefix: "/", Enabled: true}
	valid := func() domain.HostingRouteSwitchOptions {
		generation, operation := p.LoadedGeneration, "pop_loaded"
		return domain.HostingRouteSwitchOptions{ResourceID: route.ResourceID, LoadedGeneration: &generation, LoadedProxyOperationID: &operation, Available: true, Reason: "available", Routes: []domain.HostingRouteSwitchRoute{{ID: route.ID, Version: route.Version, SelectedReleaseOperationID: route.SelectedReleaseOperationID, Hostname: route.Hostname, PathPrefix: route.PathPrefix}}, Targets: []domain.HostingRouteSwitchTarget{{OperationID: route.SelectedReleaseOperationID, Kind: "current_healthy"}}}
	}
	t.Run("public loaded predecessor", func(t *testing.T) {
		options := valid()
		wire, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		var decoded domain.HostingRouteSwitchOptions
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		in, err := applicationDatabaseRouteContinuity(p, route, decoded)
		if err != nil || in.ExpectedLoadedProxyOperationID != "pop_loaded" || in.ExpectedLoadedGeneration != 3 || len(in.Routes) != 1 || in.Routes[0] != (domain.HostingRouteSwitchSelection{RouteID: route.ID, ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "hop_previous"}) {
			t.Fatal("continuity did not use the exact public loaded predecessor and route")
		}
	})
	for _, test := range []struct {
		name   string
		change func(*domain.HostingRouteSwitchOptions)
	}{
		{"unavailable", func(o *domain.HostingRouteSwitchOptions) { o.Available = false }},
		{"busy reason", func(o *domain.HostingRouteSwitchOptions) { o.Reason = "busy" }},
		{"pending", func(o *domain.HostingRouteSwitchOptions) { o.PendingOperationID = "pop_pending" }},
		{"cross resource", func(o *domain.HostingRouteSwitchOptions) { o.ResourceID = "resource_other" }},
		{"no generation", func(o *domain.HostingRouteSwitchOptions) { o.LoadedGeneration = nil }},
		{"stale generation", func(o *domain.HostingRouteSwitchOptions) { *o.LoadedGeneration-- }},
		{"no predecessor", func(o *domain.HostingRouteSwitchOptions) { o.LoadedProxyOperationID = nil }},
		{"empty predecessor", func(o *domain.HostingRouteSwitchOptions) { *o.LoadedProxyOperationID = "" }},
		{"malformed predecessor", func(o *domain.HostingRouteSwitchOptions) { *o.LoadedProxyOperationID = "../foreign" }},
		{"no route", func(o *domain.HostingRouteSwitchOptions) { o.Routes = nil }},
		{"extra route", func(o *domain.HostingRouteSwitchOptions) { o.Routes = append(o.Routes, o.Routes[0]) }},
		{"route identity", func(o *domain.HostingRouteSwitchOptions) { o.Routes[0].ID = "route_other" }},
		{"route version", func(o *domain.HostingRouteSwitchOptions) { o.Routes[0].Version++ }},
		{"route predecessor", func(o *domain.HostingRouteSwitchOptions) { o.Routes[0].SelectedReleaseOperationID = "hop_other" }},
		{"hostname", func(o *domain.HostingRouteSwitchOptions) { o.Routes[0].Hostname = "other.example.test" }},
		{"path", func(o *domain.HostingRouteSwitchOptions) { o.Routes[0].PathPrefix = "/other" }},
		{"missing healthy", func(o *domain.HostingRouteSwitchOptions) { o.Targets = nil }},
		{"retained target", func(o *domain.HostingRouteSwitchOptions) { o.Targets[0].Kind = "retained" }},
		{"other healthy", func(o *domain.HostingRouteSwitchOptions) { o.Targets[0].OperationID = "hop_other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := valid()
			test.change(&options)
			if _, err := applicationDatabaseRouteContinuity(p, route, options); err == nil {
				t.Fatal("mismatched public route choices accepted")
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimeDeploymentPublicProjection(t *testing.T) {
	p := domain.HostingProxy{ID: "proxy_fixture", ServerID: "server_fixture", DestinationID: "destination_fixture", RunnerID: "runner_fixture", ControlProjectID: "proj_fixture", Enabled: true, DaemonID: "daemon_fixture", LoadedGeneration: 3, DesiredGeneration: 3}
	route := domain.HostingRoute{ID: "route_fixture", ResourceID: "resource_fixture", Version: 2, SelectedReleaseOperationID: "hop_previous", Hostname: "connected-postgresql.example.test", PathPrefix: "/", Enabled: true}
	loaded := "pop_loaded"
	options := domain.HostingRouteSwitchOptions{ResourceID: route.ResourceID, LoadedGeneration: &p.LoadedGeneration, LoadedProxyOperationID: &loaded, Available: true, Reason: "available", Routes: []domain.HostingRouteSwitchRoute{{ID: route.ID, Version: route.Version, SelectedReleaseOperationID: route.SelectedReleaseOperationID, Hostname: route.Hostname, PathPrefix: route.PathPrefix}}, Targets: []domain.HostingRouteSwitchTarget{{OperationID: route.SelectedReleaseOperationID, Kind: "current_healthy"}}}
	requests := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/api/v1/servers/server_fixture/proxy":
			if r.Header.Get("Authorization") != "Bearer fixture_admin" {
				t.Error("proxy read lost admin authentication")
			}
			body = p
		case "/api/v1/resources/resource_fixture/route-switches":
			if r.Header.Get("Authorization") != "Bearer fixture_owner" {
				t.Error("route options lost owner authentication")
			}
			body = options
		default:
			t.Error("unexpected fixture request")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			t.Error("route projection read mutated state")
		}
		requests <- r.URL.Path
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	f := &databaseRunnerFixture{t: t, server: server, client: server.Client(), token: "fixture_owner", adminToken: "fixture_admin", serverID: p.ServerID}
	r := &applicationDatabaseRouteFixture{f: f, proxy: p, route: route, daemonID: p.DaemonID}
	input := domain.HostingDeploymentInput{ExpectedGeneration: 9, IdempotencyKey: "redeploy_fixture", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}
	wire, err := json.Marshal(r.deployment(input))
	if err != nil {
		t.Fatal(err)
	}
	var got domain.HostingDeploymentInput
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	want := input
	want.ApplicationDatabaseRouteContinuity = &domain.HostingApplicationDatabaseRouteContinuityInput{ExpectedLoadedGeneration: 3, ExpectedLoadedProxyOperationID: loaded, Routes: []domain.HostingRouteSwitchSelection{{RouteID: route.ID, ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: route.SelectedReleaseOperationID}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("deployment lost reviewed public predecessor or existing deployment settings")
	}
	for _, path := range []string{"/api/v1/servers/server_fixture/proxy", "/api/v1/resources/resource_fixture/route-switches"} {
		select {
		case got := <-requests:
			if got != path {
				t.Fatal("deployment did not read public proxy and resource projection in order")
			}
		default:
			t.Fatal("deployment skipped public projection")
		}
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

func TestApplicationDatabaseRouteRuntimeRetirementPhases(t *testing.T) {
	exact := []applicationDatabaseRetirementPhase{{"b", "retire_intent", "a", "old_a"}, {"b", "detached", "a", "old_a"}}
	for _, mode := range []string{"exact", "missing_intent", "missing_detached", "duplicate_intent", "duplicate_detached", "extra_phase", "foreign_operation", "foreign_previous_operation", "foreign_previous_container"} {
		t.Run(mode, func(t *testing.T) {
			phases := append([]applicationDatabaseRetirementPhase(nil), exact...)
			switch mode {
			case "missing_intent":
				phases = phases[1:]
			case "missing_detached":
				phases = phases[:1]
			case "duplicate_intent":
				phases[1].Phase = "retire_intent"
			case "duplicate_detached":
				phases[0].Phase = "detached"
			case "extra_phase":
				phases = append(phases, phases[0])
			case "foreign_operation":
				phases[1].OperationID = "c"
			case "foreign_previous_operation":
				phases[1].PredecessorOperationID = "foreign"
			case "foreign_previous_container":
				phases[1].PredecessorContainerID = "foreign"
			}
			if applicationDatabaseRetirementPhasesExact(phases, "b", "a", "old_a") != (mode == "exact") {
				t.Fatal("retirement phase cardinality or predecessor correlation was accepted incorrectly")
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimeRetainedCleanup(t *testing.T) {
	for _, mode := range []string{"present", "retired", "retired_port_occupied", "foreign_daemon", "changed_daemon", "id_inventory_error", "operation_inventory_error", "extra_operation_container", "changed_operation_label", "inspect_error", "foreign_image", "remove_error", "residue", "pre_complete", "pre_complete_empty_result", "pre_complete_absent", "pre_complete_foreign_image", "pre_complete_extra_operation_container", "pre_complete_operation_inventory_error", "pre_complete_no_plan"} {
		t.Run(mode, func(t *testing.T) {
			id, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
			op := domain.HostingOperation{ID: "owned", BindingID: "binding", Snapshot: domain.HostingSnapshot{HostPort: 20002}, Plan: &domain.HostingApplyPlan{Hash: "plan"}, Release: &domain.HostingRelease{Hash: "release", ImageID: "image", DaemonID: "daemon"}, Result: &domain.HostingResult{ContainerID: id, PlanHash: "plan", ImageID: "image", DaemonID: "daemon"}}
			preComplete := strings.HasPrefix(mode, "pre_complete")
			if preComplete {
				op.Result = nil
			}
			if mode == "pre_complete_empty_result" {
				op.Result = &domain.HostingResult{}
			}
			if mode == "pre_complete_no_plan" {
				op.Plan = nil
			}
			originalResult := op.Result
			present := !strings.HasPrefix(mode, "retired") && mode != "pre_complete_absent"
			removed, daemonChecks, portChecks := 0, 0, 0
			docker := func(_ context.Context, args ...string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"info", "--format", "{{.ID}}"}) {
					daemonChecks++
					if mode == "foreign_daemon" || mode == "changed_daemon" && daemonChecks > 1 {
						return []byte("foreign"), nil
					}
					return []byte("daemon"), nil
				}
				if len(args) == 7 && reflect.DeepEqual(args[:6], []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter"}) {
					switch args[6] {
					case "id=" + id:
						if mode == "id_inventory_error" {
							return nil, errors.New("PRIVATE-CANARY")
						}
						if present {
							return []byte(id), nil
						}
						return nil, nil
					case "label=io.nerocd.operation=owned":
						if strings.HasSuffix(mode, "operation_inventory_error") {
							return nil, errors.New("PRIVATE-CANARY")
						}
						if strings.HasSuffix(mode, "extra_operation_container") {
							return []byte(id + "\n" + other), nil
						}
						if present && mode != "changed_operation_label" {
							return []byte(id), nil
						}
						return nil, nil
					}
				}
				if len(args) == 5 && reflect.DeepEqual(args[:4], []string{"container", "inspect", id, "--format"}) {
					if mode == "inspect_error" {
						return nil, errors.New("PRIVATE-CANARY")
					}
					image := "image"
					if strings.HasSuffix(mode, "foreign_image") {
						image = "foreign"
					}
					return json.Marshal(map[string]any{"Id": id, "Image": image, "Config": map[string]any{"Labels": map[string]string{"io.nerocd.binding": "binding", "io.nerocd.operation": "owned", "io.nerocd.plan": "plan", "io.nerocd.release": "release"}}})
				}
				if reflect.DeepEqual(args, []string{"container", "rm", "--force", id}) {
					removed++
					if mode == "remove_error" {
						return nil, errors.New("PRIVATE-CANARY")
					}
					if mode != "residue" {
						present = false
					}
					return nil, nil
				}
				t.Fatal("cleanup attempted a command outside exact owned identity")
				return nil, errors.New("unexpected")
			}
			portsFree := func(ports ...int) error {
				portChecks++
				if !reflect.DeepEqual(ports, []int{20002}) {
					t.Fatal("cleanup probed unrelated capacity")
				}
				if mode == "retired_port_occupied" {
					return errors.New("owned port occupied")
				}
				return nil
			}
			var err error
			if preComplete {
				err = cleanupApplicationDatabasePreComplete(t.Context(), op, docker, portsFree)
			} else {
				err = cleanupApplicationDatabaseRetained(t.Context(), op, docker, portsFree)
			}
			want := mode == "present" || mode == "retired" || mode == "pre_complete" || mode == "pre_complete_empty_result" || mode == "pre_complete_absent"
			if (err == nil) != want || err != nil && strings.Contains(err.Error(), "CANARY") {
				t.Fatal("retained cleanup failed to preserve exact ownership and absence evidence")
			}
			if mode == "retired" && (removed != 0 || portChecks != 1) {
				t.Fatal("already retired predecessor was mutated or its port went unverified")
			}
			if mode == "present" && (removed != 1 || present || portChecks != 1) {
				t.Fatal("live fallback did not remove exactly once and release its port")
			}
			if preComplete && (op.Result != originalResult || op.CompleteReceipt != nil) {
				t.Fatal("pre-Complete cleanup invented controller completion state")
			}
			if mode == "pre_complete" || mode == "pre_complete_empty_result" {
				if removed != 1 || present || portChecks != 1 {
					t.Fatal("pre-Complete abort leaked its exact owned candidate or allocated port")
				}
			}
			if mode == "pre_complete_absent" && (removed != 0 || portChecks != 1) {
				t.Fatal("pre-create abort did not prove absence and free capacity")
			}
			if !want && mode != "remove_error" && mode != "residue" && removed != 0 {
				t.Fatal("cleanup mutated after missing or mismatched ownership evidence")
			}
		})
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

func applicationDatabaseRouteFailureObserved(scenario, phase string, exit databaseRunnerExit) bool {
	// VerifyReady is read-only: its failure leaves the last acknowledged attached
	// stage and exits the runner. A transient attached stage is not failure proof.
	return scenario == "readiness" && phase == "attached" && exit.Exited && exit.Kind == "process_exit" && exit.Code == 1 || scenario == "cutover" && phase == "ready" && !exit.Exited
}

func applicationDatabaseRouteReadinessRejected(ctx context.Context, snapshot domain.HostingSnapshot, container string) error {
	if snapshot.HostPort < 1024 || snapshot.HostPort > 65535 || snapshot.Readiness.Path != "/never-ready" || len(container) != 64 || strings.Trim(container, "0123456789abcdef") != "" {
		return errors.New("invalid fixture readiness rejection target")
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", snapshot.HostPort, snapshot.Readiness.Path), nil)
	if err != nil {
		return errors.New("fixture readiness rejection request invalid")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("fixture readiness rejection request failed")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	defer clear(body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 4096 || response.StatusCode != http.StatusNotFound || response.Header.Get("X-NeroCD-Fixture-Instance") != container[:12] {
		return errors.New("fixture readiness rejection lacks exact non-ready candidate")
	}
	return nil
}

func TestApplicationDatabaseRouteRuntimeFailureStage(t *testing.T) {
	failed := databaseRunnerExit{Exited: true, Kind: "process_exit", Code: 1}
	for _, test := range []struct {
		name, scenario, phase string
		exit                  databaseRunnerExit
		want                  bool
	}{
		{"readiness failure retains attached", "readiness", "attached", failed, true},
		{"transient attached is not failed readiness", "readiness", "attached", databaseRunnerExit{}, false},
		{"mutation uncertainty is not failed readiness", "readiness", "uncertain", failed, false},
		{"ready is not failed readiness", "readiness", "ready", failed, false},
		{"clean exit is not failed readiness", "readiness", "attached", databaseRunnerExit{Exited: true, Kind: "success", Code: 0}, false},
		{"unknown exit is not failed readiness", "readiness", "attached", databaseRunnerExit{Exited: true, Kind: "wait_error", Code: -1}, false},
		{"cutover needs ready and live runner", "cutover", "ready", databaseRunnerExit{}, true},
		{"cutover cannot use attached", "cutover", "attached", databaseRunnerExit{}, false},
		{"cutover cannot use uncertainty", "cutover", "uncertain", databaseRunnerExit{}, false},
		{"cutover cannot use failed runner", "cutover", "ready", failed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := applicationDatabaseRouteFailureObserved(test.scenario, test.phase, test.exit); got != test.want {
				t.Fatalf("observed=%t want=%t", got, test.want)
			}
		})
	}
}

func TestApplicationDatabaseRouteRuntimeReadinessRejection(t *testing.T) {
	container := strings.Repeat("a", 64)
	for _, mode := range []string{"exact", "ready", "wrong_instance", "missing_instance", "redirect", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/never-ready" {
					t.Error("readiness probe changed method or followed a redirect")
				}
				if mode != "missing_instance" {
					w.Header().Set("X-NeroCD-Fixture-Instance", container[:12])
				}
				if mode == "wrong_instance" {
					w.Header().Set("X-NeroCD-Fixture-Instance", strings.Repeat("b", 12))
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/unexpected", http.StatusFound)
					return
				}
				status := http.StatusNotFound
				if mode == "ready" {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				if mode == "oversized" {
					_, _ = io.WriteString(w, strings.Repeat("x", 4097))
				}
			}))
			t.Cleanup(server.Close)
			snapshot := domain.HostingSnapshot{HostPort: server.Listener.Addr().(*net.TCPAddr).Port, Readiness: domain.HostingReadiness{Path: "/never-ready", TimeoutSeconds: 3}}
			if err := applicationDatabaseRouteReadinessRejected(t.Context(), snapshot, container); (err == nil) != (mode == "exact") {
				t.Fatal("readiness rejection accepted wrong candidate or ready response, or rejected exact404")
			}
		})
	}
}

func applicationDatabaseRouteFailureCreateInput(engine, scenario, environment, destination string) domain.HostingDatabaseCreateInput {
	in := applicationDatabaseCreateInput(engine, environment, destination)
	// SQL schema isolation does not isolate Docker's retained volume namespace.
	// Resource/binding/storage identities derive from the project and request ID.
	in.RequestID = domain.HostingDocumentHash(struct{ Fixture, Engine, Scenario, Environment, Destination string }{"connected_route_failure", engine, scenario, environment, destination})[7:39]
	return in
}

func TestApplicationDatabaseRouteRuntimeFailureStorageIsolation(t *testing.T) {
	volumeFor := func(in domain.HostingDatabaseCreateInput) string {
		resource := domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID)
		_, volume := domain.HostingStorageIdentity("binding_db_" + strings.TrimPrefix(resource, "res_db_"))
		return volume
	}
	seen := make(map[string]string)
	for _, engine := range []string{"postgresql", "redis"} {
		seen[volumeFor(applicationDatabaseCreateInput(engine, "env_success", "dest_success"))] = "success/" + engine
	}
	for _, environment := range []string{"env_first", "env_next"} {
		for _, destination := range []string{"dest_first", "dest_next"} {
			for _, engine := range []string{"postgresql", "redis"} {
				for _, scenario := range []string{"readiness", "cutover"} {
					name := environment + "/" + destination + "/" + engine + "/" + scenario
					t.Run(name, func(t *testing.T) {
						in := applicationDatabaseRouteFailureCreateInput(engine, scenario, environment, destination)
						if err := in.Validate(); err != nil {
							t.Fatal(err)
						}
						if again := applicationDatabaseRouteFailureCreateInput(engine, scenario, environment, destination); in != again {
							t.Fatal("same-case database retry changed request identity")
						}
						volume := volumeFor(in)
						if previous, exists := seen[volume]; exists {
							t.Fatalf("independent database case reused retained volume from %s", previous)
						}
						seen[volume] = name
					})
				}
			}
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
				view := databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", applicationDatabaseRouteFailureCreateInput(engine, failure, f.environment, f.destination), 201)
				f.registerRouteDatabaseCleanup(view.Resource.ID)
				f.stop()
				source := f.deploy(view, "route_failure_source")
				f.registerRouteOperationCleanup(source.ID)
				f.start()
				f.await(source.ID)
				resource := databaseRunnerJSON[domain.Resource](f, f.token, "POST", "/api/v1/resources", app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: f.environment, Name: "route failure " + engine, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: os.Getenv("NEROCD_DATABASE_HTTP_APP_IMAGE")}}}, 201)
				databaseRunnerJSON[domain.ResourceBinding](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destination}, 201)
				namespace := "connected_" + engine
				config := databaseRunnerJSON[struct {
					Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
				}](f, f.token, "PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("a", 32), Entries: []domain.HostingRuntimeEntry{{Name: "FIXTURE_DATABASE_ENGINE", Kind: "literal", Value: &engine}, {Name: "FIXTURE_DATABASE_NAMESPACE", Kind: "literal", Value: &namespace}}}, 200).Configuration
				deploy := domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: config.RuntimeGeneration, IdempotencyKey: "route_failure_a", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}
				f.stop()
				connection := databaseRunnerJSON[domain.HostingDatabaseConnection](f, f.token, "POST", "/api/v1/resources/"+resource.ID+"/database-connections", domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: view.Resource.ID, ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: config.RuntimeGeneration, ExpectedDatabaseGeneration: view.Resource.Generation, RequestID: strings.Repeat("b", 32)}, Deployment: deploy}, 202)
				f.registerRouteOperationCleanup(connection.UpgradeOperationID)
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
					exit := f.process.status()
					if err == nil && json.Unmarshal(row.Stage, &staged) == nil && applicationDatabaseRouteFailureObserved(failure, staged.Phase, exit) {
						break
					}
					if time.Now().After(deadline) || exit.Exited {
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
				if err != nil || op.Plan == nil || op.Release == nil || op.ApplicationDatabase == nil || staged.Candidate.ContainerID == "" || op.Result != nil || op.CompleteReceipt != nil {
					t.Fatal("route failure exact candidate identity unavailable")
				}
				proof, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
				if err != nil || staged.Validate(*op.Plan, proof) != nil {
					t.Fatal("route failure stage does not match exact operation, plan and endpoint proof")
				}
				if failure == "readiness" && (op.ApplicationDatabaseYield != nil || applicationDatabaseRouteReadinessRejected(t.Context(), op.Snapshot, staged.Candidate.ContainerID) != nil) {
					t.Fatal("readiness failure yielded or lacks exact candidate HTTP rejection")
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
