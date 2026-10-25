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
	"strconv"
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
	state.active = prior
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
