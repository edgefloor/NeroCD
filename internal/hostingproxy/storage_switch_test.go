package hostingproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type interruptedLoadedFixture struct {
	mu     sync.Mutex
	active Plan
	status map[string]int
	fault  string
}

// These tests execute the actual loaded-state guard and HTTP/TLS probes using
// owned scripted endpoints. Full Apply remains Linux-only and is not invoked.
func interruptedLoadedEngine(t *testing.T, verifiedPrior bool) (*Engine, Plan, Plan, *interruptedLoadedFixture) {
	t.Helper()
	state := &interruptedLoadedFixture{status: map[string]int{"selected": http.StatusBadGateway, "other": http.StatusOK}}
	dir, err := os.MkdirTemp("", "proxy-storage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/info" {
			t.Error("loaded guard attempted daemon mutation")
			w.WriteHeader(500)
			return
		}
		state.mu.Lock()
		fault := state.fault
		state.mu.Unlock()
		id := testIdentity().DaemonID
		if fault == "daemon" {
			id = "foreign"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ID": id, "OSType": "linux", "Architecture": "arm64"})
	})}
	done := make(chan error, 1)
	go func() { done <- daemon.Serve(listener) }()
	t.Cleanup(func() {
		_ = daemon.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		active, fault := state.active, state.fault
		state.mu.Unlock()
		if r.URL.Path == "/_nerocd/proxy-generation" {
			hash := active.hash
			if fault == "generation_marker" {
				hash = digest("foreign")
			}
			w.Header().Set("X-NeroCD-Proxy-Generation", hash)
			return
		}
		if r.URL.Path != "/api/rawdata" {
			w.WriteHeader(404)
			return
		}
		var dynamic struct {
			HTTP struct{ Routers, Services map[string]map[string]any }
		}
		if json.Unmarshal(active.dynamic(), &dynamic) != nil {
			t.Error("invalid dynamic fixture")
			w.WriteHeader(500)
			return
		}
		routers, services := map[string]any{}, map[string]any{}
		for name, router := range dynamic.HTTP.Routers {
			router["status"] = "enabled"
			if middlewares, ok := router["middlewares"].([]any); ok {
				for i, middleware := range middlewares {
					middlewares[i] = middleware.(string) + "@file"
				}
			}
			routers[name+"@file"] = router
		}
		for name, service := range dynamic.HTTP.Services {
			service["status"] = "enabled"
			balancer := service["loadBalancer"].(map[string]any)
			if transport, ok := balancer["serversTransport"].(string); ok {
				balancer["serversTransport"] = transport + "@file"
			}
			if fault == "missing_transport" {
				delete(balancer, "serversTransport")
			}
			if fault == "foreign_transport" {
				balancer["serversTransport"] = "foreign@file"
			}
			if fault == "foreign_service" {
				service["loadBalancer"] = map[string]any{"servers": []map[string]string{{"url": "http://127.0.0.1:29999"}}}
			}
			services[name+"@file"] = service
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"routers": routers, "services": services})
	}))
	t.Cleanup(management.Close)
	_, port, err := net.SplitHostPort(management.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	managementPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	settings := testSettings()
	settings.Directory = dir
	settings.DockerSocket = socket
	settings.ManagementPort = uint16(managementPort)
	routes := []Route{{ID: "selected", Hostname: "app.example.test", PathPrefix: "/", HostPort: 21001}, {ID: "other", Hostname: "other.example.test", PathPrefix: "/", HostPort: 22001}}
	generation := uint64(3)
	var prior Plan
	if verifiedPrior {
		generation = 4
		prior, err = PrepareVerifiedSwitch(testIdentity(), settings, generation, generation-1, routes, []string{"selected", "other"})
	} else {
		prior, err = Prepare(testIdentity(), settings, generation, generation-1, routes)
	}
	if err != nil {
		t.Fatal(err)
	}
	routes[0].HostPort++
	next, err := PrepareVerifiedSwitch(testIdentity(), settings, generation+1, generation, routes, []string{"selected"})
	if err != nil {
		t.Fatal(err)
	}
	// Portable management tests retain legacy policy; the current owned-file
	// guard is exercised without skips by the Linux test binary.
	if runtime.GOOS != "linux" {
		prior.transportPolicy = 0
		prior.rehash()
		next.transportPolicy = 0
		next.rehash()
	}
	state.active = prior
	for _, subdir := range []string{"dynamic", "certificates", "docker-config"} {
		if err = os.MkdirAll(filepath.Join(dir, subdir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(dir, "dynamic/routes.yml"), prior.dynamic(), 0600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, route := range state.active.routes {
			if r.Host != route.Hostname || r.URL.Path != route.PathPrefix {
				continue
			}
			hash, id := state.active.hash, route.ID
			if state.fault == "route_generation" {
				hash = digest("foreign")
			}
			if state.fault == "route_id" {
				id = "foreign"
			}
			if state.fault != "missing_route_marker" {
				w.Header().Set("X-NeroCD-Proxy-Generation", hash)
				w.Header().Set("X-NeroCD-Proxy-Route", id)
			}
			w.WriteHeader(state.status[route.ID])
			return
		}
		w.WriteHeader(404)
	})
	httpRoutes := httptest.NewServer(handler)
	t.Cleanup(httpRoutes.Close)
	certificate, roots := interruptedLoadedCertificate(t)
	tlsRoutes := httptest.NewUnstartedServer(handler)
	tlsRoutes.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	tlsRoutes.StartTLS()
	t.Cleanup(tlsRoutes.Close)
	engine := &Engine{settings: settings, fixtureRoots: roots, fixtureProbeDial: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		target := httpRoutes.Listener.Addr().String()
		if port == "443" {
			target = tlsRoutes.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}}
	return engine, prior, next, state
}

func interruptedLoadedCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Loaded guard test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "app.example.test"}, DNSNames: []string{"app.example.test", "other.example.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, parsed, leafKey.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}, roots
}

func TestInterruptedStorageLoadedGuardAcceptsOnlyStoppedSelectedBackend(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(strconv.FormatBool(verified), func(t *testing.T) {
			e, prior, next, state := interruptedLoadedEngine(t, verified)
			if err := e.loadedInterruptedStoragePredecessor(t.Context(), prior, next); err != nil {
				t.Fatal("exact interrupted predecessor config rejected", err)
			}
			if verified && e.loaded(t.Context(), prior) == nil {
				t.Fatal("ordinary verified loaded guard accepted stopped backend")
			}
			if e.probe(t.Context(), prior, Route{ID: "selected", Hostname: "app.example.test", PathPrefix: "/"}, false).HTTPReady && verified {
				t.Fatal("marker-only config check published backend readiness")
			}
			state.mu.Lock()
			state.active = next
			if err := os.WriteFile(filepath.Join(e.settings.Directory, "dynamic/routes.yml"), next.dynamic(), 0600); err != nil {
				t.Fatal(err)
			}
			state.mu.Unlock()
			if result, err := e.verifiedObservation(t.Context(), next, "proxy"); err == nil || result.Outcome != Uncertain {
				t.Fatal("candidate readiness accepted marker-bearing502", result, err)
			}
			state.mu.Lock()
			state.status["selected"] = http.StatusOK
			state.mu.Unlock()
			if result, err := e.verifiedObservation(t.Context(), next, "proxy"); err != nil || result.Outcome != Applied {
				t.Fatal("trusted healthy candidate failed", result, err)
			}
		})
	}
}

func TestInterruptedStorageLoadedGuardRejectsUnrelatedReadinessAndForeignConfiguration(t *testing.T) {
	for _, fault := range []string{"unrelated_502", "generation_marker", "route_generation", "route_id", "missing_route_marker", "foreign_service", "daemon", "unrelated_port", "unverified_scope"} {
		t.Run(fault, func(t *testing.T) {
			e, prior, next, state := interruptedLoadedEngine(t, true)
			state.mu.Lock()
			if fault == "unrelated_502" {
				state.status["other"] = http.StatusBadGateway
			} else {
				state.fault = fault
			}
			state.mu.Unlock()
			if fault == "unrelated_port" {
				routes := next.Routes()
				for i := range routes {
					if routes[i].ID == "other" {
						routes[i].HostPort++
					}
				}
				var err error
				next, err = PrepareVerifiedSwitch(next.identity, next.settings, next.generation, next.previous, routes, []string{"selected"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if fault == "unverified_scope" {
				var err error
				next, err = Prepare(next.identity, next.settings, next.generation, next.previous, next.Routes())
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := e.loadedInterruptedStoragePredecessor(t.Context(), prior, next); err == nil {
				t.Fatal("invalid configuration or unrelated unavailable backend admitted")
			}
		})
	}
}

func interruptedReceiptEngine(t *testing.T) (*Engine, Plan, Plan, *interruptedLoadedFixture) {
	t.Helper()
	e, prior, next, fixture := interruptedLoadedEngine(t, true)
	directory, err := filepath.EvalSymlinks(e.settings.Directory)
	if err != nil {
		t.Fatal(err)
	}
	settings := e.settings
	settings.Directory = directory
	settings.DockerSocket = filepath.Join(directory, "docker.sock")
	settings.DockerBinary = filepath.Join(directory, "docker-script")
	// Only the two read-only inspection commands are implemented. No process may
	// silently accept run, rm, stop, network, or any other effect command.
	script := `#!/bin/sh
owned_dir=${0%/*}
printf '%s %s\n' "$5" "$6" >> "$owned_dir/commands"
case "$5:$6" in
container:ls) printf 'owned-proxy\n';;
container:inspect) exec /bin/cat "$owned_dir/inspect.json";;
*) exit 93;;
esac
`
	if err = os.WriteFile(settings.DockerBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	prior, err = PrepareVerifiedSwitch(prior.identity, settings, prior.generation, prior.previous, prior.Routes(), prior.verification.RequiredRouteIDs)
	if err != nil {
		t.Fatal(err)
	}
	next, err = PrepareVerifiedSwitch(next.identity, settings, next.generation, next.previous, next.Routes(), next.verification.RequiredRouteIDs)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		prior.transportPolicy = 0
		prior.rehash()
		next.transportPolicy = 0
		next.rehash()
	}
	e.settings = settings
	fixture.mu.Lock()
	fixture.active = prior
	fixture.mu.Unlock()
	c := container{ID: "owned-proxy"}
	c.Config.Image = Image
	c.Config.Labels = prior.labels()
	c.Config.Cmd = e.staticArgs()
	c.HostConfig.NetworkMode = "host"
	c.HostConfig.RestartPolicy.Name = "unless-stopped"
	c.State.Running = true
	c.Mounts = append(c.Mounts, struct {
		Source, Destination string
		RW                  bool
	}{filepath.Join(directory, "dynamic"), "/dynamic", false}, struct {
		Source, Destination string
		RW                  bool
	}{filepath.Join(directory, "certificates"), "/certificates", true})
	raw, err := json.Marshal([]container{c})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "inspect.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	saveInterruptedReceipt(t, e, prior, "committed")
	return e, prior, next, fixture
}

func saveInterruptedReceipt(t *testing.T, e *Engine, p Plan, phase string) {
	t.Helper()
	r := receipt{Identity: p.identity, StaticHash: p.staticHash, Hash: p.hash, Generation: p.generation, Routes: p.Routes(), Phase: phase, Verification: p.verification, TransportPolicy: p.transportPolicy}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"dynamic", "certificates", "docker-config"} {
		if err = os.MkdirAll(filepath.Join(e.settings.Directory, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string][]byte{"receipt.json": raw, "dynamic/routes.yml": p.dynamic(), "certificates/acme.json": []byte("{}")} {
		if err = os.WriteFile(filepath.Join(e.settings.Directory, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func readInterruptedTestReceipt(e *Engine) (receipt, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(e.settings.Directory, "receipt.json"))
	if err != nil {
		return receipt{}, nil, err
	}
	var local receipt
	if err = json.Unmarshal(raw, &local); err != nil {
		return receipt{}, nil, err
	}
	file, err := os.ReadFile(filepath.Join(e.settings.Directory, "dynamic/routes.yml"))
	return local, file, err
}
func verifyInterruptedTestReceipt(ctx context.Context, e *Engine, current, prior Plan, failed *Plan, outcome Outcome, generation uint64) error {
	local, file, err := readInterruptedTestReceipt(e)
	if err != nil {
		return err
	}
	return e.verifyInterruptedStorageReceipt(ctx, current, prior, failed, outcome, generation, local, file)
}

func TestInterruptedStorageReceiptVerifiesClosedPriorWithoutBackendReadiness(t *testing.T) {
	e, prior, current, _ := interruptedReceiptEngine(t)
	if e.loaded(t.Context(), prior) == nil {
		t.Fatal("ordinary managed receipt loaded guard accepted stopped verified backend")
	}
	before := map[string]string{}
	for _, name := range []string{"receipt.json", "dynamic/routes.yml", "certificates/acme.json"} {
		raw, err := os.ReadFile(filepath.Join(e.settings.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(raw)
	}
	if err := verifyInterruptedTestReceipt(t.Context(), e, current, prior, nil, Refused, prior.generation); err != nil {
		t.Fatal("exact closed interrupted refusal rejected", err)
	}
	for name, body := range before {
		raw, err := os.ReadFile(filepath.Join(e.settings.Directory, name))
		if err != nil || string(raw) != body {
			t.Fatal("verification mutated settled evidence", name, err)
		}
	}
	if runtime.GOOS == "linux" {
		if e.VerifyManagedReceipt(t.Context(), current, Refused, prior.generation) == nil {
			t.Fatal("ordinary public managed verifier accepted stopped verified backend")
		}
		if err := e.VerifyInterruptedStorageReceipt(t.Context(), current, prior, nil, Refused, prior.generation); err != nil {
			t.Fatal(err)
		}
	} else if e.VerifyInterruptedStorageReceipt(t.Context(), current, prior, nil, Refused, prior.generation) == nil {
		t.Fatal("public verifier broadened Linux eligibility")
	}
	calls, err := os.ReadFile(filepath.Join(e.settings.Directory, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.FieldsFunc(string(calls), func(r rune) bool { return r == '\n' }) {
		if call != "container ls" && call != "container inspect" {
			t.Fatal("verification attempted effect", call)
		}
	}
}

func TestInterruptedStorageReceiptRejectsForeignOrUnresolvedEvidence(t *testing.T) {
	for _, mode := range []string{"receipt_hash", "receipt_routes", "receipt_verification", "receipt_generation", "receipt_phase", "file", "missing_receipt", "generation", "outcome_uncertain", "outcome_rollback", "foreign_prior", "daemon", "unrelated502", "generation_marker", "route_id", "missing_route_marker", "foreign_service", "container_labels", "container_mount", "container_stopped", "missing_container", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			e, prior, current, fixture := interruptedReceiptEngine(t)
			outcome, generation := Refused, prior.generation
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "receipt_hash", "receipt_routes", "receipt_verification", "receipt_generation", "receipt_phase":
				local, _, err := readInterruptedTestReceipt(e)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "receipt_hash":
					local.Hash = digest("foreign")
				case "receipt_routes":
					local.Routes[0].HostPort++
				case "receipt_verification":
					local.Verification = nil
				case "receipt_generation":
					local.Generation--
				case "receipt_phase":
					local.Phase = "intent"
				}
				raw, marshalErr := json.Marshal(local)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(filepath.Join(e.settings.Directory, "receipt.json"), raw, 0600)
				if err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(e.settings.Directory, "dynamic/routes.yml"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing_receipt":
				if err := os.Remove(filepath.Join(e.settings.Directory, "receipt.json")); err != nil {
					t.Fatal(err)
				}
			case "generation":
				generation++
			case "outcome_uncertain":
				outcome = Uncertain
			case "outcome_rollback":
				outcome = RolledBack
			case "foreign_prior":
				prior.hash = digest("foreign")
			case "container_labels", "container_mount", "container_stopped", "missing_container":
				path := filepath.Join(e.settings.Directory, "inspect.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var cs []container
				if err = json.Unmarshal(raw, &cs); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "container_labels":
					cs[0].Config.Labels["io.nerocd.daemon"] = "foreign"
				case "container_mount":
					cs[0].Mounts[0].Source = "/foreign"
				case "container_stopped":
					cs[0].State.Running = false
				case "missing_container":
					cs = nil
				}
				raw, err = json.Marshal(cs)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			default:
				fixture.mu.Lock()
				if mode == "unrelated502" {
					fixture.status["other"] = 502
				} else {
					fixture.fault = mode
				}
				fixture.mu.Unlock()
			}
			if err := verifyInterruptedTestReceipt(ctx, e, current, prior, nil, outcome, generation); err == nil {
				t.Fatal("foreign or unresolved receipt accepted")
			}
		})
	}
}

func TestInterruptedStorageAppliedReceiptStillRequiresTrustedCandidate(t *testing.T) {
	e, prior, current, fixture := interruptedReceiptEngine(t)
	saveInterruptedReceipt(t, e, current, "committed")
	fixture.mu.Lock()
	fixture.active = current
	fixture.mu.Unlock()
	if verifyInterruptedTestReceipt(t.Context(), e, current, prior, nil, Applied, current.generation) == nil {
		t.Fatal("applied receipt accepted candidate502")
	}
	fixture.mu.Lock()
	fixture.status["selected"] = 200
	fixture.mu.Unlock()
	if err := verifyInterruptedTestReceipt(t.Context(), e, current, prior, nil, Applied, current.generation); err != nil {
		t.Fatal("healthy trusted candidate rejected", err)
	}
	// Even an otherwise identical receipt cannot omit the committed strict policy.
	local, _, err := readInterruptedTestReceipt(e)
	if err != nil {
		t.Fatal(err)
	}
	local.Verification = nil
	raw, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.settings.Directory, "receipt.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if verifyInterruptedTestReceipt(t.Context(), e, current, prior, nil, Applied, current.generation) == nil {
		t.Fatal("applied verification policy omitted")
	}
}

func TestInterruptedStorageReceiptForwardRecoveryRetainsExactFailedHistory(t *testing.T) {
	e, prior, failed, _ := interruptedReceiptEngine(t)
	current, err := PrepareVerifiedSwitch(failed.identity, failed.settings, failed.generation+1, prior.generation, failed.Routes(), failed.verification.RequiredRouteIDs)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyInterruptedTestReceipt(t.Context(), e, current, prior, &failed, Refused, prior.generation); err != nil {
		t.Fatal("exact closed prior refused recovery rejected", err)
	}
	saveInterruptedReceipt(t, e, failed, "committed")
	if verifyInterruptedTestReceipt(t.Context(), e, current, prior, &failed, Refused, prior.generation) == nil {
		t.Fatal("failed generation receipt substituted for closed refused prior")
	}
	saveInterruptedReceipt(t, e, prior, "committed")
	foreign := failed
	foreign.routes = append([]Route(nil), failed.routes...)
	foreign.routes[0].HostPort++
	if verifyInterruptedTestReceipt(t.Context(), e, current, prior, &foreign, Refused, prior.generation) == nil {
		t.Fatal("changed failed history accepted")
	}
}

func TestLoopbackTransportReadbackBindsPolicyAndLegacyBytes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux owned-file validation; execute Linux test binary")
	}
	for _, mode := range []string{"current", "legacy", "legacy_foreign_transport", "missing_transport", "foreign_transport", "changed_file", "unknown_policy"} {
		t.Run(mode, func(t *testing.T) {
			e, p, _, state := interruptedLoadedEngine(t, false)
			state.status["selected"] = http.StatusOK
			if mode == "legacy" || mode == "legacy_foreign_transport" {
				p.transportPolicy = 0
				p.rehash()
				state.active = p
			}
			saveInterruptedReceipt(t, e, p, "committed")
			if mode == "missing_transport" || mode == "foreign_transport" {
				state.fault = mode
			}
			if mode == "legacy_foreign_transport" {
				state.fault = "foreign_transport"
			}
			if mode == "changed_file" {
				if err := os.WriteFile(filepath.Join(e.settings.Directory, "dynamic/routes.yml"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unknown_policy" {
				p.transportPolicy = 2
			}
			err := e.loaded(t.Context(), p)
			if (mode == "current" || mode == "legacy") != (err == nil) {
				t.Fatalf("unexpected loaded policy result: %v", err)
			}
			if mode == "legacy" {
				current := p
				current.transportPolicy = 1
				current.rehash()
				if e.loaded(t.Context(), current) == nil {
					t.Fatal("legacy state certified new policy")
				}
			}
		})
	}
}
