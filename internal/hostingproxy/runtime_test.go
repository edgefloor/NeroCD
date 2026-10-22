//go:build linux

package hostingproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	for _, v := range []struct {
		port string
		body string
	}{{"19001", "root"}, {"19002", "api"}} {
		v := v
		l, err := net.Listen("tcp", "127.0.0.1:"+v.port)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
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
	q, _ := Prepare(identity, s, 2, 1, p.Routes())
	r, _ := Prepare(identity, s, 3, 1, p.Routes())
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
	if saved.Generation == 3 {
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
	// Snapshot only with the sole Traefik writer stopped; restore at the same
	// operator-owned path before restart. Never print certificate storage.
	fixtureCommand(t, "stop", p.name())
	acmePath := filepath.Join(s.Directory, "certificates", "acme.json")
	backup, err := os.ReadFile(acmePath)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root, "acme-backup.json")
	if err = os.WriteFile(backupPath, backup, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(acmePath, backup, 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, "start", p.name())
	if err = e.waitLoaded(ctx, current); err != nil {
		t.Fatal(err)
	}
	again, err = e.Observe(ctx, current)
	if err != nil || !again.Routes[0].HTTPSReady {
		t.Fatalf("backup restore not ready: %v", err)
	}
	t.Log("stopped-writer ACME backup/restore retained trusted service and 0600 file")
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
	// Cancel after publication while the real proxy is paused. The newly
	// constructed engine must retain uncertainty even after Traefik loads it.
	canceled, _ := Prepare(identity, s, current.generation+1, current.generation, current.Routes())
	fixtureCommand(t, "pause", p.name())
	cancelCtx, cancel := context.WithCancel(ctx)
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
	cancel()
	interruptedResult := <-interrupted
	fixtureCommand(t, "unpause", p.name())
	if !published || interruptedResult.Outcome != Uncertain {
		t.Fatalf("interrupted publish %+v", interruptedResult)
	}
	again, err = engine2.Observe(ctx, canceled)
	if err == nil || again.Outcome != Uncertain {
		t.Fatal("unfinished intent acknowledged")
	}
	t.Log("cancellation after config publication and fresh-engine observation retained uncertainty")
	st, err := os.Stat(filepath.Join(s.Directory, "certificates", "acme.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("ACME permissions")
	}
	t.Log("ACME persistent state mode 0600 passed")
	// Log only metadata, never ACME storage or private certificate keys.
	metadata, _ := json.Marshal(result.Routes)
	t.Logf("certificate observations %s", metadata)
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
