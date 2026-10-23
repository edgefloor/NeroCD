//go:build linux

package hostingproxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const pebbleImage = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"

func fixtureCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Docker %s failed: %v %s", args[0], err, out)
	}
	return out
}
func fixtureDNS(t *testing.T) {
	t.Helper()
	answer := func(raw []byte) []byte {
		var m dnsmessage.Message
		if m.Unpack(raw) != nil {
			return nil
		}
		m.Response = true
		m.Authoritative = true
		m.RecursionAvailable = true
		for _, q := range m.Questions {
			if q.Type == dnsmessage.TypeA {
				m.Answers = append(m.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
			}
		}
		out, _ := m.Pack()
		return out
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
	var wg sync.WaitGroup
	wg.Add(2)
	t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close(); wg.Wait() })
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, addr, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(answer(buf[:n]), addr)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			var size [2]byte
			if _, err = io.ReadFull(c, size[:]); err == nil {
				raw := make([]byte, int(binary.BigEndian.Uint16(size[:])))
				if _, err = io.ReadFull(c, raw); err == nil {
					out := answer(raw)
					binary.BigEndian.PutUint16(size[:], uint16(len(out)))
					_, _ = c.Write(append(size[:], out...))
				}
			}
			_ = c.Close()
		}
	}()
}
func TestRuntimeProxy(t *testing.T) {
	if os.Getenv("NEROCD_PROXY_FIXTURE") != "1" {
		t.Skip("isolated Linux daemon fixture only")
	}
	ctx := context.Background()
	fixtureDNS(t)
	root, err := os.MkdirTemp("/var/lib", "nerocd-proxy-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s := testSettings()
	s.Directory = filepath.Join(root, "state")
	s.DockerBinary = "/usr/local/bin/docker"
	identity := testIdentity()
	identity.DaemonID = strings.TrimSpace(string(fixtureCommand(t, "info", "--format", "{{.ID}}")))
	p, err := Prepare(identity, s, 1, 0, []Route{{"root", "app.example.test", "/", 19001}, {"api", "app.example.test", "/api", 19002}})
	if err != nil {
		t.Fatal(err)
	}
	// Backends bind the Linux namespace's loopback, exactly as settled binding ports do.
	health := map[string]*atomic.Int64{}
	for _, v := range []struct {
		port string
		body string
	}{{"19001", "root"}, {"19002", "api"}, {"19003", "replacement"}} {
		v := v
		status := &atomic.Int64{}
		status.Store(404)
		health[v.port] = status
		l, err := net.Listen("tcp", "127.0.0.1:"+v.port)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(int(status.Load()))
			_, _ = fmt.Fprintf(w, "%s:%s", v.body, r.URL.Path)
		}), ReadHeaderTimeout: time.Second}
		done := make(chan struct{})
		go func() { defer close(done); _ = srv.Serve(l) }()
		t.Cleanup(func() { _ = srv.Close(); <-done })
	}
	fixtureCommand(t, "run", "-d", "--name", "pebble", "--network", "host", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/workspace/acceptance/runtime-proxy/pebble.json,dst=/pebble-config.json,readonly", pebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")
	t.Cleanup(func() { fixtureCommand(t, "rm", "-f", "pebble") })
	// These are public fixture trust anchors, never the CA's private key.
	fixtureCommand(t, "cp", "pebble:/test/certs/pebble.minica.pem", filepath.Join(root, "minica.pem"))
	if err = secureDirectory(s.Directory); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(s.Directory, "certificates"), 0700); err != nil {
		t.Fatal(err)
	}
	minica, err := os.ReadFile(filepath.Join(root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Directory, "certificates", "root.pem"), minica, 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(minica) {
		t.Fatal("invalid Pebble public root")
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
	var issuanceRoot []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, x := client.Get("https://localhost:15000/roots/0")
		if x == nil {
			issuanceRoot, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !roots.AppendCertsFromPEM(issuanceRoot) {
		t.Fatal("Pebble issuance root unavailable")
	}
	e, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	e.fixtureCA = "https://localhost:14000/dir"
	e.fixtureRoots = roots
	e.fixtureDockerArgs = []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"}
	// Refuse preexisting foreign containers and port occupancy without touching them.
	fixtureCommand(t, "pull", Image)
	fixtureCommand(t, "create", "--name", p.name(), Image)
	if v, x := e.Install(ctx, p); x == nil || v.Outcome != Refused {
		t.Fatal("foreign container accepted")
	}
	fixtureCommand(t, "inspect", p.name())
	fixtureCommand(t, "rm", p.name())
	for _, port := range []string{"80", "443"} {
		listener, x := net.Listen("tcp", "0.0.0.0:"+port)
		if x != nil {
			t.Fatal(x)
		}
		v, x := e.Install(ctx, p)
		_ = listener.Close()
		if x == nil || v.Outcome != Refused {
			t.Fatalf("port %s collision accepted", port)
		}
	}
	t.Log("foreign container and independent 80/443 collisions refused without adoption or deletion")
	result, err := e.Install(ctx, p)
	if err != nil {
		t.Logf("proxy diagnostics: %s", fixtureCommand(t, "logs", p.name(), "--tail", "20"))
		t.Fatalf("install: %v", err)
	}
	if result.Outcome != Applied || result.LoadedGeneration != 1 {
		t.Fatalf("install result %+v", result)
	}
	t.Cleanup(func() { fixtureCommand(t, "rm", "-f", p.name()) })
	t.Log("routing generation 1 applied with application 404; ACME may be pending")
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		result, err = e.Observe(ctx, p)
		if err == nil && len(result.Routes) == 2 && result.Routes[0].HTTPSReady && result.Routes[1].HTTPSReady {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil || len(result.Routes) != 2 || !result.Routes[0].HTTPSReady || !result.Routes[1].HTTPSReady {
		t.Fatalf("trusted TLS not ready: %+v %v", result, err)
	}
	t.Log("HTTP01 issuance, SNI hostname, trust chain and expiry passed")
	// Verify segment matching and preservation, including the /apix negative case.
	for _, tc := range []struct{ path, want string }{{"/api", "api:/api"}, {"/api/child", "api:/api/child"}, {"/apix", "root:/apix"}, {"/", "root:/"}} {
		req, _ := http.NewRequest("GET", "http://127.0.0.1"+tc.path, nil)
		req.Host = "app.example.test"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(b) != tc.want {
			t.Fatalf("%s got %s", tc.path, b)
		}
	}
	t.Log("Host/path longest-prefix, exact /api, /api/ segment and unchanged path passed")
	bad := identity
	bad.DaemonID = "wrong"
	badPlan, _ := Prepare(bad, s, 1, 0, p.Routes())
	badEngine, _ := New(s)
	if _, err = badEngine.Install(ctx, badPlan); err == nil {
		t.Fatal("daemon mismatch accepted")
	}
	t.Log("daemon mismatch refused before mutation")
	// Concurrent engines have one CAS winner. The losing request cannot overwrite it.
	q, _ := Prepare(identity, s, p.generation+1, p.generation, p.Routes())
	r, _ := Prepare(identity, s, p.generation+2, p.generation, p.Routes())
	var wg sync.WaitGroup
	out := make(chan Result, 2)
	for _, candidate := range []Plan{q, r} {
		candidate := candidate
		wg.Add(1)
		go func() { defer wg.Done(); v, _ := e.Apply(ctx, candidate); out <- v }()
	}
	wg.Wait()
	close(out)
	applied := 0
	for v := range out {
		if v.Outcome == Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("concurrent CAS winners=%d", applied)
	}
	store, err := openStorage(ctx, s.Directory)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.load()
	store.close()
	if err != nil {
		t.Fatal(err)
	}
	current := q
	if saved.Generation == r.generation {
		current = r
	}
	t.Log("concurrent apply serialized with exactly one generation CAS winner")
	// Corrupt a published candidate while Traefik is paused, then let its real file
	// provider reject it. This is fixture fault injection, never a project input.
	next, _ := Prepare(identity, s, current.generation+1, current.generation, current.Routes())
	fixtureCommand(t, "pause", p.name())
	failed := make(chan struct {
		result Result
		err    error
	}, 1)
	e.verifyTimeout = 3 * time.Second
	go func() {
		v, x := e.Apply(ctx, next)
		failed <- struct {
			result Result
			err    error
		}{v, x}
	}()
	deadline = time.Now().Add(2 * time.Second)
	published := false
	for time.Now().Before(deadline) {
		raw, x := os.ReadFile(filepath.Join(s.Directory, "dynamic", "routes.yml"))
		if x == nil && strings.Contains(string(raw), next.hash) {
			published = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !published {
		fixtureCommand(t, "unpause", p.name())
		t.Fatal("candidate not published")
	}
	if err = os.WriteFile(filepath.Join(s.Directory, "dynamic", "routes.yml"), []byte("http: [malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, "unpause", p.name())
	failedResult := <-failed
	if failedResult.err == nil || failedResult.result.Outcome != RolledBack || failedResult.result.LoadedGeneration != current.generation {
		t.Fatalf("rollback result %+v %v", failedResult.result, failedResult.err)
	}
	e.verifyTimeout = 15 * time.Second
	t.Log("real malformed config rejected; previous generation atomically restored and runtime verified")
	fixtureCommand(t, "restart", p.name())
	if err = e.waitLoaded(ctx, current); err != nil {
		t.Fatal(err)
	}
	again, err := e.Observe(ctx, current)
	if err != nil || !again.Routes[0].HTTPSReady {
		t.Fatalf("restart lost trusted certificate: %v %+v", err, again)
	}
	t.Log("container restart reused file routes and persistent certificate with controller absent")
	// The deliberately short-lived Pebble certificate is inside Traefik's
	// renewal window. Restart triggers the actual built-in ACME renewal path.
	originalFingerprint := result.Routes[0].Certificate.Fingerprint
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		again, err = e.Observe(ctx, current)
		if err == nil && again.Routes[0].HTTPSReady && again.Routes[0].Certificate.Fingerprint != originalFingerprint {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !again.Routes[0].HTTPSReady || again.Routes[0].Certificate.Fingerprint == originalFingerprint {
		t.Fatal("real ACME renewal not observed")
	}
	t.Log("short-lived certificate renewed by Traefik startup or periodic renewal, trusted fingerprint changed")
	// Fault injection replaces the certificate cache, so run it after the
	// independent legacy renewal assertion rather than perturbing that timer.
	current = fixtureVerifiedSwitch(t, e, current, health)
	// Disable issuance before recording the certificate to be recovered. A new
	// certificate cannot conceal a missing account or certificate backup.
	fixtureCommand(t, "stop", "pebble")
	if response, x := client.Get("https://localhost:14000/dir"); x == nil {
		_ = response.Body.Close()
		t.Fatal("fixture CA remains reachable during recovery")
	}
	again, err = e.Observe(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	expectedCertificates := map[string]string{}
	for _, route := range again.Routes {
		if !route.HTTPSReady {
			t.Fatal("certificate not ready before backup")
		}
		expectedCertificates[route.RouteID] = route.Certificate.Fingerprint
	}
	fixtureCommand(t, "stop", p.name())
	func() {
		// Preserve the root, bind-mounted directories, and live lock inode. Only
		// task-owned payload files are removed during this simulated state loss.
		held, x := openStorage(ctx, s.Directory)
		if x != nil {
			t.Fatal(x)
		}
		defer held.close()
		lockBefore, x := held.lock.Stat()
		if x != nil {
			t.Fatal(x)
		}
		rootBefore, x := os.Stat(s.Directory)
		if x != nil {
			t.Fatal(x)
		}
		fixtureAssertEngineExcluded(t, e, current)
		expectedReceipt, x := held.load()
		if x != nil {
			t.Fatal(x)
		}
		backupDirectory := filepath.Join(root, "persisted-state-backup")
		fixtureCopyState(t, s.Directory, backupDirectory, false)
		savedState := fixtureStateMetadata(t, backupDirectory)
		if !reflect.DeepEqual(savedState, fixtureStateMetadata(t, s.Directory)) {
			t.Fatal("persisted backup differs from complete stopped state")
		}
		for _, required := range []string{"certificates/acme.json", "dynamic/routes.yml", "receipt.json"} {
			if entry, ok := savedState[required]; !ok || !entry.Mode.IsRegular() {
				t.Fatalf("backup missing required file %s", required)
			}
		}
		for name, entry := range savedState {
			if name == "lock" || entry.Mode.IsDir() {
				continue
			}
			if x = os.Remove(filepath.Join(s.Directory, name)); x != nil {
				t.Fatal(x)
			}
		}
		for name, entry := range savedState {
			if name == "lock" || entry.Mode.IsDir() {
				continue
			}
			if _, x = os.Stat(filepath.Join(s.Directory, name)); !errors.Is(x, os.ErrNotExist) {
				t.Fatalf("original payload %s survived simulated state loss", name)
			}
		}
		// Prove a restore which only rewrites in-memory ACME bytes would fail: both
		// routing and the generation receipt are absent before saved files are read.
		if lost, x := held.load(); x != nil || lost.Generation != 0 {
			t.Fatal("original generation receipt survived state loss")
		}
		fixtureAssertEngineExcluded(t, e, current)
		fixtureCopyState(t, backupDirectory, s.Directory, true)
		if !reflect.DeepEqual(savedState, fixtureStateMetadata(t, s.Directory)) {
			t.Fatal("restored bytes, modes, ownership, or file set differ from persisted backup")
		}
		restored, x := held.load()
		if x != nil || !reflect.DeepEqual(restored, expectedReceipt) || restored.Identity != identity || restored.Generation != current.generation || restored.Hash != current.hash || !reflect.DeepEqual(restored.Routes, current.Routes()) {
			t.Fatal("restored identity, routes, or generation receipt mismatch")
		}
		lockAfter, x := os.Stat(filepath.Join(s.Directory, "lock"))
		if x != nil || !os.SameFile(lockBefore, lockAfter) {
			t.Fatal("live lock inode changed during restore")
		}
		rootAfter, x := os.Stat(s.Directory)
		if x != nil || !os.SameFile(rootBefore, rootAfter) {
			t.Fatal("state root inode changed during restore")
		}
		fixtureAssertEngineExcluded(t, e, current)
	}()
	fixtureCommand(t, "start", p.name())
	if err = e.waitLoaded(ctx, current); err != nil {
		t.Fatal(err)
	}
	again, err = e.Observe(ctx, current)
	if err != nil {
		t.Fatalf("backup restore not ready: %v", err)
	}
	for _, route := range again.Routes {
		if !route.HTTPSReady || route.Certificate.Fingerprint != expectedCertificates[route.RouteID] {
			t.Fatal("restored route did not reuse its exact saved certificate with CA offline")
		}
	}
	if len(again.Routes) != len(expectedCertificates) {
		t.Fatal("restored route count changed")
	}
	t.Log("persisted complete backup restored after payload loss; exact certificates reused with CA offline, receipts/routes/modes verified, competing engine excluded throughout unchanged lock inode")
	fixtureCommand(t, "start", "pebble")
	// A failed new hostname must not interrupt an already trusted route.
	routes := append(current.Routes(), Route{"blocked", "blocked.example.test", "/", 19001})
	pending, _ := Prepare(identity, s, current.generation+1, current.generation, routes)
	again, err = e.Apply(ctx, pending)
	if err != nil || again.Outcome != Applied {
		t.Fatalf("ACME failure rolled back routes: %v", err)
	}
	trusted, pendingTLS := false, false
	for _, v := range again.Routes {
		if v.RouteID == "blocked" {
			pendingTLS = !v.HTTPSReady && v.Certificate.Status == "pending"
		} else if v.HTTPSReady {
			trusted = true
		}
	}
	if !trusted || !pendingTLS {
		t.Fatalf("ACME failure disrupted old certificate %+v", again)
	}
	current = pending
	t.Log("blocked ACME hostname remained TLS pending while existing valid route continued")
	engine2, _ := New(s)
	engine2.fixtureCA = e.fixtureCA
	engine2.fixtureRoots = roots
	engine2.fixtureDockerArgs = e.fixtureDockerArgs
	again, err = engine2.Apply(ctx, current)
	if err != nil || again.Outcome != Applied {
		t.Fatalf("process restart replay failed: %v", err)
	}
	t.Log("new engine replay acknowledged same generation/hash")
	// A strict candidate with an unhealthy target cannot commit. Cancel after
	// publication, or independently break its old backend to prevent rollback.
	cancelRoutes := current.Routes()
	for n := range cancelRoutes {
		if cancelRoutes[n].ID == "root" {
			cancelRoutes[n].HostPort = 19001
		}
	}
	health["19001"].Store(503)
	canceled, err := PrepareVerifiedSwitch(identity, s, current.generation+1, current.generation, cancelRoutes, []string{"root"})
	if err != nil {
		t.Fatal(err)
	}
	e.verifyTimeout = 2 * time.Second
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupted := make(chan Result, 1)
	go func() { v, _ := e.Apply(cancelCtx, canceled); interrupted <- v }()
	deadline = time.Now().Add(2 * time.Second)
	published = false
	for time.Now().Before(deadline) {
		raw, x := os.ReadFile(filepath.Join(s.Directory, "dynamic", "routes.yml"))
		if x == nil && strings.Contains(string(raw), canceled.hash) {
			published = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if os.Getenv("NEROCD_PROXY_SWITCH_FAULT") == "rollback" {
		health["19003"].Store(503)
	} else {
		cancel()
	}
	interruptedResult := <-interrupted
	if !published || interruptedResult.Outcome != Uncertain {
		t.Fatalf("interrupted publish %+v", interruptedResult)
	}
	again, err = engine2.Observe(ctx, canceled)
	if err == nil || again.Outcome != Uncertain {
		t.Fatal("unfinished intent acknowledged")
	}
	again, err = engine2.Apply(ctx, canceled)
	if err == nil || again.Outcome != Uncertain {
		t.Fatal("fresh engine replay downgraded unfinished strict intent")
	}
	held, err := openStorage(ctx, s.Directory)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := held.load()
	held.close()
	if err != nil || intent.Phase != "intent" || !reflect.DeepEqual(intent.Verification, canceled.verification) {
		t.Fatal("durable strict policy lost")
	}
	t.Logf("strict %s after publication retained Uncertain and versioned intent; fresh-engine Observe and replay refused acknowledgement", os.Getenv("NEROCD_PROXY_SWITCH_FAULT"))
	st, err := os.Stat(filepath.Join(s.Directory, "certificates", "acme.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("ACME permissions")
	}
	t.Log("ACME persistent state mode 0600 passed")
	// Log only metadata, never ACME storage or private certificate keys.
	metadata, _ := json.Marshal(result.Routes)
	t.Logf("certificate observations %s", metadata)
}

func fixtureVerifiedSwitch(t *testing.T, e *Engine, old Plan, health map[string]*atomic.Int64) Plan {
	t.Helper()
	ctx := context.Background()
	routes := old.Routes()
	for n := range routes {
		if routes[n].ID == "root" {
			routes[n].HostPort = 19003
		}
	}
	next, err := PrepareVerifiedSwitch(old.identity, old.settings, old.generation+1, old.generation, routes, []string{"root"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(e.settings.Directory, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := e.Install(ctx, next); err == nil || result.Outcome != Refused {
		t.Fatal("strict Install accepted")
	}
	// All scope refusals happen without changing the receipt or dynamic file.
	dynamicBefore, err := os.ReadFile(filepath.Join(e.settings.Directory, "dynamic", "routes.yml"))
	if err != nil {
		t.Fatal(err)
	}
	badRoutes := next.Routes()
	for n := range badRoutes {
		if badRoutes[n].ID == "api" {
			badRoutes[n].HostPort++
		}
	}
	bad, _ := PrepareVerifiedSwitch(old.identity, old.settings, next.generation, old.generation, badRoutes, []string{"root"})
	if result, err := e.Apply(ctx, bad); err == nil || result.Outcome != Refused {
		t.Fatal("unselected mutation accepted")
	}
	// Loss of trust before publication must refuse even when HTTP is healthy.
	trustedRoots := e.fixtureRoots
	e.fixtureRoots = x509.NewCertPool()
	if result, err := e.Apply(ctx, next); err == nil || result.Outcome != Refused {
		t.Fatal("untrusted predecessor accepted")
	}
	e.fixtureRoots = trustedRoots
	after, err := os.ReadFile(filepath.Join(e.settings.Directory, "receipt.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("refusal changed receipt")
	}
	after, err = os.ReadFile(filepath.Join(e.settings.Directory, "dynamic", "routes.yml"))
	if err != nil || string(dynamicBefore) != string(after) {
		t.Fatal("refusal changed routes")
	}
	// An unhealthy B is rejected, and A is reported only after both probes pass.
	health["19003"].Store(503)
	e.verifyTimeout = 2 * time.Second
	result, err := e.Apply(ctx, next)
	if err == nil || result.Outcome != RolledBack || result.LoadedGeneration != old.generation || len(result.Routes) != len(old.routes) {
		t.Fatalf("strict rollback: %+v %v", result, err)
	}
	for _, route := range result.Routes {
		if route.RouteID == "root" && (!route.HTTPReady || !route.HTTPSReady) {
			t.Fatal("unverified A reported rolled back")
		}
	}
	after, err = os.ReadFile(filepath.Join(e.settings.Directory, "receipt.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("rollback did not restore exact prior receipt")
	}
	t.Log("strict B route-health failure restored exact A receipt and verified HTTP plus trusted HTTPS")
	fixtureRejectedSwitchCertificate(t, e, old, next, health)
	health["19003"].Store(404)
	e.verifyTimeout = 15 * time.Second
	result, err = e.Apply(ctx, next)
	if err != nil || result.Outcome != Applied {
		t.Fatalf("strict switch: %+v %v", result, err)
	}
	for _, route := range result.Routes {
		if route.RouteID == "root" && (!route.HTTPReady || !route.HTTPSReady) {
			t.Fatal("strict success lacks trusted TLS")
		}
	}
	// Distinct content proves B actually serves the selected route, while the
	// unselected /api backend and every unrelated route field remain unchanged.
	for _, tc := range []struct{ path, body string }{{"/", "replacement:/"}, {"/api", "api:/api"}} {
		req, err := http.NewRequestWithContext(ctx, "GET", "http://app.example.test"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:80")
		}}
		response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		if err != nil || string(body) != tc.body {
			t.Fatalf("strict backend content %s: %s %v", tc.path, body, err)
		}
	}
	restarted, err := New(e.settings)
	if err != nil {
		t.Fatal(err)
	}
	restarted.fixtureCA, restarted.fixtureDockerArgs = e.fixtureCA, e.fixtureDockerArgs
	restarted.fixtureRoots = trustedRoots
	if result, err = restarted.Apply(ctx, next); err != nil || result.Outcome != Applied {
		t.Fatalf("strict replay failed: %+v %v", result, err)
	}
	// A fresh process cannot turn an already committed strict plan's failed TLS
	// observation into legacy Applied/pending, even on an exact replay.
	restarted.fixtureRoots = x509.NewCertPool()
	if result, err = restarted.Observe(ctx, next); err == nil || result.Outcome != Uncertain {
		t.Fatal("strict Observe downgraded failed TLS")
	}
	if result, err = restarted.Apply(ctx, next); err == nil || result.Outcome != Uncertain {
		t.Fatal("strict replay downgraded failed TLS")
	}
	t.Log("strict A to B committed after HTTP and trusted HTTPS; B content and unchanged /api proved; fresh-engine replay/Observe rejected failed certificate trust")
	return next
}

func fixtureRejectedSwitchCertificate(t *testing.T, e *Engine, old, next Plan, health map[string]*atomic.Int64) {
	t.Helper()
	// Keep the private test CA offline so an empty cache cannot conceal loss of
	// the saved certificate. No production endpoint or public DNS is involved.
	fixtureCommand(t, "stop", "pebble")
	t.Cleanup(func() { fixtureCommand(t, "start", "pebble") })
	acmePath := filepath.Join(e.settings.Directory, "certificates", "acme.json")
	saved, err := os.ReadFile(acmePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.verifyTimeout = 10 * time.Second
	type attempt struct {
		result Result
		err    error
	}
	finished := make(chan attempt, 1)
	done := make(chan struct{})
	go func() { defer close(done); result, err := e.Apply(ctx, next); finished <- attempt{result, err} }()
	t.Cleanup(func() { cancel(); <-done })
	waitFile := func(hash string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(filepath.Join(e.settings.Directory, "dynamic", "routes.yml"))
			if err == nil && strings.Contains(string(b), hash) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("certificate fault generation was not published")
	}
	waitFile(next.hash)
	fixtureCommand(t, "stop", "--time", "1", old.name())
	if err = os.WriteFile(acmePath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	health["19003"].Store(404)
	fixtureCommand(t, "start", old.name())
	if err = e.waitLoaded(ctx, next); err != nil {
		t.Fatalf("certificate fault HTTP generation: %v", err)
	}
	for _, route := range next.routes {
		if next.required(route.ID) && e.probe(ctx, next, route, true).HTTPSReady {
			t.Fatal("default self-signed certificate was trusted")
		}
	}
	waitFile(old.hash)
	fixtureCommand(t, "stop", "--time", "1", old.name())
	if err = os.WriteFile(acmePath, saved, 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, "start", old.name())
	got := <-finished
	if got.err == nil || got.result.Outcome != RolledBack || got.result.LoadedGeneration != old.generation {
		t.Fatalf("certificate failure rollback: %+v %v", got.result, got.err)
	}
	for _, route := range got.result.Routes {
		if next.required(route.RouteID) && (!route.HTTPReady || !route.HTTPSReady) {
			t.Fatal("certificate rollback lacked trusted A observations")
		}
	}
	fixtureCommand(t, "start", "pebble")
	t.Log("strict candidate HTTP loaded with untrusted default certificate; no commit; saved A certificate restored with test CA offline and HTTP/trusted HTTPS verified")
}

func TestRuntimeLostDockerResponse(t *testing.T) {
	if os.Getenv("NEROCD_PROXY_FIXTURE") != "1" {
		t.Skip("isolated Linux daemon fixture only")
	}
	root, err := os.MkdirTemp("/var/lib", "nerocd-proxy-lost-response-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s := testSettings()
	s.Directory = filepath.Join(root, "state")
	s.DockerBinary = filepath.Join(root, "docker-wrapper")
	marker := filepath.Join(root, "launched")
	wrapper := "#!/bin/sh\ncase \" $* \" in\n *\" run \"*) /usr/local/bin/docker \"$@\" || exit $?; echo $$ > " + marker + "; sleep 60 ;;\n *) exec /usr/local/bin/docker \"$@\" ;;\nesac\n"
	if err = os.WriteFile(s.DockerBinary, []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity()
	identity.ProxyID = "lost-response"
	identity.DaemonID = strings.TrimSpace(string(fixtureCommand(t, "info", "--format", "{{.ID}}")))
	p, err := Prepare(identity, s, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	// An unreachable fixture-only CA prevents even accidental production ACME traffic.
	e.fixtureCA = "https://127.0.0.1:14000/dir"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() { result, _ := e.Install(ctx, p); done <- result }()
	deadline := time.Now().Add(20 * time.Second)
	launched := false
	for time.Now().Before(deadline) {
		if _, err = os.Stat(marker); err == nil {
			launched = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	result := <-done
	if !launched {
		t.Fatal("Docker never reached lost-response fault point")
	}
	t.Cleanup(func() { fixtureCommand(t, "rm", "-f", p.name()) })
	if result.Outcome != Uncertain {
		t.Fatalf("lost response result %+v", result)
	}
	// The actual container exists despite the lost CLI response. The durable
	// intent prevents the next process from reporting an acknowledged install.
	fixtureCommand(t, "inspect", p.name())
	if _, err = e.Observe(context.Background(), p); err == nil {
		t.Fatal("lost response acknowledged")
	}
	t.Log("Docker run completed but response was lost; persisted intent and fresh observation remained uncertain")
}

// fixtureStateEntry describes persisted bytes without retaining certificate or
// account key material in a manifest or ordinary test output.
type fixtureStateEntry struct {
	Mode fs.FileMode
	Sum  [sha256.Size]byte
}

func fixtureStateMetadata(t *testing.T, root string) map[string]fixtureStateEntry {
	t.Helper()
	entries := map[string]fixtureStateEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err = owned(info); err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := fixtureStateEntry{Mode: info.Mode()}
		if info.IsDir() {
			if info.Mode().Perm() != 0700 {
				return fmt.Errorf("unsafe fixture directory mode for %s", name)
			}
		} else if info.Mode().IsRegular() {
			if info.Mode().Perm() != 0600 {
				return fmt.Errorf("unsafe fixture file mode for %s", name)
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			copy(entry.Sum[:], hash.Sum(nil))
		} else {
			return fmt.Errorf("unexpected fixture file type for %s", name)
		}
		entries[name] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
func fixtureCopyState(t *testing.T, source, destination string, preserveLock bool) {
	t.Helper()
	var directories []string
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, name)
		if info.IsDir() {
			if err = os.Mkdir(target, info.Mode().Perm()); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			directories = append(directories, target)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected backup entry %s", name)
		}
		if preserveLock && name == "lock" {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(directories) - 1; i >= 0; i-- {
		directory, err := os.Open(directories[i])
		if err != nil {
			t.Fatal(err)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			t.Fatal(syncErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
func fixtureAssertEngineExcluded(t *testing.T, e *Engine, p Plan) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	competing, err := New(e.settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = competing.Observe(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("competing engine was not excluded by held lock: %v", err)
	}
}
