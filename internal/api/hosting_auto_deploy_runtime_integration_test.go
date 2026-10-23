package api

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/netutil"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runnerruntime"
	"nerocd/internal/store"
)

// autoGitBridge is fixture routing only. Git still verifies the original
// github.com TLS peer end to end; the bridge never chooses a caller's upstream.
func autoGitBridge(parent context.Context, target string) (string, func() error, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return "", nil, errors.New("fixture target must be fixed loopback")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	var calls atomic.Uint32
	var relays sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	track := func(conn net.Conn, add bool) {
		mu.Lock()
		defer mu.Unlock()
		if add {
			connections[conn] = true
		} else {
			delete(connections, conn)
		}
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.RequestURI != "github.com:443" || r.Host != "github.com:443" || r.URL.Host != "github.com:443" || r.URL.Scheme != "" || r.ContentLength > 0 || r.Header.Get("Proxy-Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if calls.Add(1) > 64 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		relays.Add(1)
		defer relays.Done()
		upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", target)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		track(client, true)
		track(upstream, true)
		defer track(client, false)
		defer track(upstream, false)
		deadline := time.Now().Add(15 * time.Second)
		if client.SetDeadline(deadline) != nil || upstream.SetDeadline(deadline) != nil {
			return
		}
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if buffer.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.CopyN(upstream, buffer, 4<<20)
			_ = upstream.Close()
			_ = client.Close()
		}()
		_, _ = io.CopyN(client, upstream, 4<<20)
		_ = client.Close()
		_ = upstream.Close()
		<-done
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(netutil.LimitListener(listener, 8)) }()
	var once sync.Once
	var closeErr error
	closeBridge := func() error {
		once.Do(func() {
			cancel()
			shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			closeErr = server.Shutdown(shutdown)
			if closeErr != nil {
				closeErr = errors.Join(closeErr, server.Close())
			}
			mu.Lock()
			owned := make([]net.Conn, 0, len(connections))
			for conn := range connections {
				owned = append(owned, conn)
			}
			mu.Unlock()
			for _, conn := range owned {
				_ = conn.Close()
			}
			relays.Wait()
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				closeErr = errors.Join(closeErr, err)
			}
		})
		return closeErr
	}
	return "http://" + listener.Addr().String(), closeBridge, nil
}

func TestAutomaticDeploymentGitBridgeFixedTarget(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	var dialed atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		dialed.Add(1)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var b [7]byte
		if _, err = io.ReadFull(conn, b[:]); err == nil {
			_, _ = conn.Write(b[:])
		}
	}()
	proxy, closeBridge, err := autoGitBridge(t.Context(), upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeBridge(); err != nil {
			t.Error(err)
		}
	})
	address := strings.TrimPrefix(proxy, "http://")
	for _, request := range []string{
		"GET / HTTP/1.1\r\nHost: github.com:443\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: github.com:443\r\nContent-Length: 0\r\n\r\n",
		"CONNECT alternate.test:443 HTTP/1.1\r\nHost: alternate.test:443\r\n\r\n",
		"CONNECT github.com:444 HTTP/1.1\r\nHost: github.com:444\r\n\r\n",
	} {
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 && response.StatusCode != 405 {
			t.Fatal("alternate bridge request accepted", response.StatusCode)
		}
	}
	if dialed.Load() != 0 {
		t.Fatal("rejected caller selected an upstream")
	}
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = fmt.Fprint(conn, "CONNECT github.com:443 HTTP/1.1\r\nHost: ignored.invalid:444\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// net/http uses the CONNECT authority as Request.Host. An unrelated Host
	// header is neither an upstream selector nor forwarded into the tunnel.
	buffer := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffer, nil)
	if err != nil || response.StatusCode != 200 {
		t.Fatal("fixed bridge rejected", err)
	}
	if _, err = io.WriteString(conn, "fixture"); err != nil {
		t.Fatal(err)
	}
	var body [7]byte
	if _, err = io.ReadFull(buffer, body[:]); err != nil || string(body[:]) != "fixture" {
		t.Fatal("fixed upstream bytes", err)
	}
	<-done
	if dialed.Load() != 1 {
		t.Fatal("unexpected fixed upstream connection count")
	}
	if _, _, err = autoGitBridge(t.Context(), "192.0.2.1:443"); err == nil {
		t.Fatal("nonloopback fixture target accepted")
	}
}

func automaticRuntimeGit(t *testing.T) ([4]string, func(int)) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.local", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git failed %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	run(work, "init", "-q", "-b", "main")
	var commits [4]string
	for i, letter := range []string{"A", "B", "C", "D"} {
		dockerfile := "FROM " + hostingbuild.StaticImage + "\nCOPY index.html /usr/share/nginx/html/index.html\n"
		if i == 2 {
			dockerfile += "RUN false\n"
		}
		if i == 3 {
			dockerfile += "COPY default.conf /etc/nginx/conf.d/default.conf\n"
			if err = os.WriteFile(filepath.Join(work, "default.conf"), []byte("server { listen 80; location / { return 503; } }\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(work, "index.html"), []byte("push fixture "+letter+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		run(work, "add", ".")
		run(work, "commit", "-qm", letter)
		commits[i] = run(work, "rev-parse", "HEAD")
	}
	acme := filepath.Join(root, "acme")
	if err = os.Mkdir(acme, 0700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(acme, "push-fixture.git")
	run(root, "clone", "--bare", work, bare)
	run(bare, "update-ref", "refs/heads/main", commits[0])
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(963), Subject: pkix.Name{CommonName: "owned GitHub fixture"}, DNSNames: []string{"github.com"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	proxy, closeBridge, err := autoGitBridge(t.Context(), listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeBridge(); err != nil {
			t.Error(err)
		}
	})
	// All paths are generated fixture paths. No source URL or argument is rewritten.
	wrapper := "#!/bin/sh\nexec '" + git + "' -c http.sslCAInfo='" + ca + "' -c http.https://github.com:443/.proxy='" + proxy + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return commits, func(i int) { run(bare, "update-ref", "refs/heads/main", commits[i]) }
}

type automaticApplyBarrier struct {
	entered, release, finished chan struct{}
	status                     atomic.Int32
}

type automaticRuntimeFixture struct {
	Control                                                      *pushHTTPFixture
	Commits                                                      [4]string
	move                                                         func(int)
	t                                                            *testing.T
	ctx                                                          context.Context
	root, buildConfig, credential, apiCA, proxyRoots, proxyState string
	server                                                       *httptest.Server
	process                                                      *pushRunnerProcess
	logs                                                         *runtimeFixtureLog
	roots                                                        *x509.CertPool
	lost                                                         atomic.Bool
	lostCount                                                    atomic.Int32
	applyBarrier                                                 atomic.Pointer[automaticApplyBarrier]
	bootstrap                                                    domain.HostingOperation
	unrelated                                                    domain.Resource
}

func newAutomaticRuntimeFixture(t *testing.T) *automaticRuntimeFixture {
	t.Helper()
	if os.Getenv("NEROCD_AUTO_DEPLOY_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned Linux automatic deployment fixture")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Linux fixture required")
	}
	signals, stopSignals := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(signals, 22*time.Minute)
	t.Cleanup(func() { cancel(); stopSignals() })
	root, err := os.MkdirTemp("/var/lib", "nerocd-auto-deploy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, publicRoot := automaticFixtureTrust(t, root)
	commits, move := automaticRuntimeGit(t)
	engine := hostingbuild.Config{DockerBinary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock", BuildxBinary: "/fixture/buildx", BuildxSHA256: os.Getenv("NEROCD_BUILDX_SHA256"), PlannerImage: os.Getenv("NEROCD_PLANNER_IMAGE"), StateDir: filepath.Join(root, "build-state"), CgroupRoot: os.Getenv("NEROCD_CGROUP_ROOT")}
	policy := pushTestPolicy()
	policy.Platform = "linux/" + runtime.GOARCH
	policy.Limits.MemoryBytes = 1 << 30
	policy.Limits.Deadline = 3 * time.Minute
	policy.Limits.ExportBytes = 256 << 20
	policy.Limits.UnpackedBytes = 512 << 20
	policy.Limits.UsageBytes = 3 << 30
	policy.Limits.ReserveBytes = 128 << 20
	policy.ToolchainIdentity, err = runnerruntime.ComputeBuildToolchainIdentity(ctx, engine, policy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	f := newPushHTTPFixture(t, pushFixtureOptions{policy: &policy, head: commits[0], internalGit: true})
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingBuildTaskType, domain.HostingBuildTriggerRevisionCapability, domain.HostingReleasePortsCapability, domain.RunTypeLocalBuildDeploy, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability}
	enrollment, err := f.service.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_automatic_fixture", RunnerName: "automatic deployment fixture", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	f.runnerToken = "ncr_automatic_fixture_credential_only"
	hash := sha256.Sum256([]byte(f.runnerToken))
	enrolled, err := f.service.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("d", 32), CredentialHash: hex.EncodeToString(hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	f.target, err = f.service.CreateServer(admin, app.ServerInput{Name: "automatic deployment", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.GrantDestinationProject(admin, f.target.Destination.ID, app.DestinationProjectInput{ProjectID: "proj_platform"}); err != nil {
		t.Fatal(err)
	}
	env, err := f.service.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "automatic-applications"})
	if err != nil {
		t.Fatal(err)
	}
	f.resource, err = f.service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "automatic GitHub application", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/acme/push-fixture.git", RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.BindResourceDestination(admin, f.resource.ID, app.ResourceBindingInput{DestinationID: f.target.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	unrelated, err := f.service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "unrelated HTTPS application", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: hostingbuild.StaticImage}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.BindResourceDestination(admin, unrelated.ID, app.ResourceBindingInput{DestinationID: f.target.Destination.ID}); err != nil {
		t.Fatal(err)
	}
	out := &automaticRuntimeFixture{Control: f, Commits: commits, move: move, t: t, ctx: ctx, root: root, logs: &runtimeFixtureLog{}, roots: roots, unrelated: unrelated, buildConfig: filepath.Join(root, "build.json"), credential: filepath.Join(root, "credential"), apiCA: filepath.Join(root, "api-ca.pem"), proxyRoots: filepath.Join(root, "proxy-roots.pem"), proxyState: filepath.Join(root, "proxy")}
	out.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/apply-begin" {
			if barrier := out.applyBarrier.Swap(nil); barrier != nil {
				close(barrier.entered)
				defer close(barrier.finished)
				select {
				case <-barrier.release:
				case <-r.Context().Done():
					return
				}
				recorded := httptest.NewRecorder()
				f.api.ServeHTTP(recorded, r)
				barrier.status.Store(int32(recorded.Code))
				for key, values := range recorded.Header() {
					w.Header()[key] = append([]string(nil), values...)
				}
				w.WriteHeader(recorded.Code)
				_, _ = w.Write(recorded.Body.Bytes())
				return
			}
		}
		if r.URL.Path == "/api/v1/runners/proxy/complete" && out.lost.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			f.api.ServeHTTP(recorded, r)
			if recorded.Code != http.StatusOK {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			out.lostCount.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.api.ServeHTTP(w, r)
	}))
	t.Cleanup(out.server.Close)
	if err = os.WriteFile(out.apiCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: out.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("https://localhost:15000/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	issuance, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	transport.CloseIdleConnections()
	if readErr != nil || closeErr != nil || response.StatusCode != 200 {
		t.Fatal("fixture issuance root unavailable")
	}
	if err = os.WriteFile(out.proxyRoots, append(append([]byte(nil), publicRoot...), issuance...), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "sources"), out.proxyState, filepath.Join(root, "runner")} {
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	buildConfig := runnerruntime.BuildConfig{SourceRoot: filepath.Join(root, "sources"), Engine: engine, Target: runnerruntime.BuildTarget{RunnerID: f.target.Server.RunnerID, ServerID: f.target.Server.ID, DestinationID: f.target.Destination.ID}, Policy: policy, StatusInterval: 200 * time.Millisecond, RequestTimeout: 5 * time.Second}
	if err = buildConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"version": 1, "source_root": buildConfig.SourceRoot, "engine": map[string]string{"docker_binary": engine.DockerBinary, "socket": engine.Socket, "buildx_binary": engine.BuildxBinary, "buildx_sha256": engine.BuildxSHA256, "planner_image": engine.PlannerImage, "state_dir": engine.StateDir, "cgroup_root": engine.CgroupRoot}, "target": map[string]string{"runner_id": buildConfig.Target.RunnerID, "server_id": buildConfig.Target.ServerID, "destination_id": buildConfig.Target.DestinationID}, "policy": policy, "status_interval_ns": int64(buildConfig.StatusInterval), "request_timeout_ns": int64(buildConfig.RequestTimeout)}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out.buildConfig, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out.credential, []byte(f.runnerToken), 0600); err != nil {
		t.Fatal(err)
	}
	proxy, err := f.service.ConfigureHostingProxy(admin, f.target.Server.ID, domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "automatic-proxy-bootstrap", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}})
	if err != nil {
		t.Fatal(err)
	}
	certDir := filepath.Join(out.proxyState, proxy.ID, "certificates")
	if err = os.MkdirAll(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(certDir, "root.pem"), publicRoot, 0600); err != nil {
		t.Fatal(err)
	}
	if err = out.StartRunner(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := out.StopRunner(); err != nil {
			t.Error(err)
		}
		if out.logs.contains(f.runnerToken) || out.logs.contains(f.secret) || out.logs.contains("push-provider-fixture") {
			t.Error("fixture credential reached runner logs")
		}
	})
	var build struct {
		Build domain.PublicHostingBuild `json:"build"`
	}
	f.request(http.MethodPost, "/api/v1/resources/"+f.resource.ID+"/builds", f.owner, domain.HostingBuildRequest{ExpectedResourceGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, IdempotencyKey: "automatic-manual-A"}, http.StatusAccepted, &build)
	built := out.AwaitBuild(commits[0], "succeeded")
	if built.ID != build.Build.ID || built.Artifact == nil {
		t.Fatal("manual A artifact identity")
	}
	var operation domain.HostingOperation
	f.request(http.MethodPost, "/api/v1/resources/"+f.resource.ID+"/deployments/from-build", f.owner, domain.HostingBuildDeploymentInput{BuildID: built.ID, ExpectedGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, IdempotencyKey: "manual-A", ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}, http.StatusAccepted, &operation)
	out.bootstrap = out.awaitOperation(operation.ID, "succeeded")
	other, err := f.service.RequestHostingDeployment(admin, unrelated.ID, domain.HostingDeploymentInput{ExpectedGeneration: unrelated.Generation, IdempotencyKey: "unrelated-manual", ContainerPort: 80, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}})
	if err != nil {
		t.Fatal(err)
	}
	other = out.awaitOperation(other.ID, "succeeded")
	for _, item := range []struct {
		resource  domain.Resource
		operation domain.HostingOperation
		host      string
	}{{f.resource, out.bootstrap, "automatic.example.test"}, {unrelated, other, "unrelated.example.test"}} {
		if _, err = f.service.GrantHostingHostname(admin, f.target.Server.ID, domain.HostingHostnameGrantInput{ProjectID: env.ProjectID, Hostname: item.host}); err != nil {
			t.Fatal(err)
		}
		route, err := f.service.RequestHostingRoute(admin, item.resource.ID, domain.HostingRouteInput{IdempotencyKey: "manual-" + item.host, Hostname: item.host, PathPrefix: "/", ExpectedHealthyOperationID: item.operation.ID})
		if err != nil {
			t.Fatal(err)
		}
		out.awaitProxy(route.OperationID, "applied")
	}
	var initial domain.GitHubPushPolicy
	f.request(http.MethodGet, "/api/v1/resources/"+f.resource.ID+"/github-push-policy", f.owner, nil, http.StatusOK, &initial)
	if initial.Status != "unconfigured" || initial.OwnerAutomaticDeploymentOptions == nil || !initial.OwnerAutomaticDeploymentOptions.Available {
		t.Fatal("actual ordinary HTTPS bootstrap does not qualify options")
	}
	out.ProbeHTTPS("push fixture A\n")
	return out
}

func (f *automaticRuntimeFixture) StartRunner() error {
	if f.process != nil {
		return errors.New("fixture runner already started")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	command := exec.CommandContext(ctx, "/fixture/nerocd-proxy-fixture", "runner", "--server", f.server.URL, "--credential-file", f.credential, "--build-config-file", f.buildConfig, "--local-build-deploy", "--journal-dir", filepath.Join(f.root, "journal"), "--work-dir", filepath.Join(f.root, "runner"), "--poll-interval", "100ms", "--cancel-poll-interval", "100ms", "--proxy-state-root", f.proxyState, "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", "/var/run/docker.sock", "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch")
	command.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+f.apiCA, "NEROCD_PROXY_FIXTURE_ROOT="+f.proxyRoots, "NEROCD_PROXY_COMPILED_FIXTURE=disposable-dind")
	command.Stdout, command.Stderr = f.logs, f.logs
	if err := command.Start(); err != nil {
		cancel()
		return err
	}
	process := &pushRunnerProcess{command: command, done: make(chan error, 1), cancel: cancel}
	f.process = process
	go func() { process.done <- command.Wait() }()
	return nil
}
func (f *automaticRuntimeFixture) StopRunner() error {
	if f.process == nil {
		return nil
	}
	f.process.cancel()
	select {
	case <-f.process.done:
		f.process = nil
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("owned automatic fixture runner did not join")
	}
}
func (f *automaticRuntimeFixture) Move(index int) {
	f.t.Helper()
	if index < 0 || index >= len(f.Commits) {
		f.t.Fatal("invalid fixed fixture revision")
	}
	f.move(index)
	f.Control.mu.Lock()
	f.Control.head = f.Commits[index]
	f.Control.mu.Unlock()
}
func (f *automaticRuntimeFixture) SignedPush(n, index int) {
	f.t.Helper()
	f.Move(index)
	var policy domain.GitHubPushPolicy
	f.Control.request(http.MethodGet, "/api/v1/resources/"+f.Control.resource.ID+"/github-push-policy", f.Control.owner, nil, http.StatusOK, &policy)
	f.Control.push(policy, n, f.Commits[index])
}
func (f *automaticRuntimeFixture) AwaitBuild(commit, state string) domain.HostingBuild {
	f.t.Helper()
	until := time.Now().Add(4 * time.Minute)
	for time.Now().Before(until) && f.ctx.Err() == nil {
		page, err := f.Control.pg.ListHostingBuilds(f.ctx, "usr_bootstrap", f.Control.resource.ID, store.Page{Limit: 50})
		if err != nil {
			f.t.Fatal(err)
		}
		for _, build := range page.Items {
			if build.SourceReceipt != nil && build.SourceReceipt.Commit == commit && build.State == state {
				return build
			}
			if build.Snapshot.TriggerRevision != nil && build.Snapshot.TriggerRevision.ExpectedCommit == commit && build.State == "failed" && state != "failed" {
				f.t.Fatalf("source-specific build failed: %s", build.FailureCode)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("source-specific build deadline", state)
	return domain.HostingBuild{}
}
func (f *automaticRuntimeFixture) AwaitDemand(state string) domain.HostingAutoDeployStatus {
	f.t.Helper()
	until := time.Now().Add(2 * time.Minute)
	var last *domain.HostingAutoDeployStatus
	for time.Now().Before(until) && f.ctx.Err() == nil {
		var policy domain.GitHubPushPolicy
		f.Control.request(http.MethodGet, "/api/v1/resources/"+f.Control.resource.ID+"/github-push-policy", f.Control.owner, nil, http.StatusOK, &policy)
		last = policy.AutomaticDeployment
		if last != nil && last.State == state {
			return *last
		}
		if last != nil && (last.State == "blocked" || last.State == "expired" || last.State == "failed") && state != "failed" {
			f.t.Fatalf("automatic demand stopped: %s %s", last.State, last.Reason)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last != nil {
		f.t.Fatalf("automatic demand deadline: %s %s", last.State, last.Reason)
	}
	f.t.Fatal("automatic demand absent")
	return domain.HostingAutoDeployStatus{}
}
func (f *automaticRuntimeFixture) awaitOperation(id, state string) domain.HostingOperation {
	f.t.Helper()
	until := time.Now().Add(time.Minute)
	for time.Now().Before(until) && f.ctx.Err() == nil {
		op, err := f.Control.pg.GetHostingOperation(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if op.State == state {
			return op
		}
		if op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
			f.t.Fatal("fixture workload failed", op.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("fixture workload deadline")
	return domain.HostingOperation{}
}
func (f *automaticRuntimeFixture) awaitProxy(id, state string) domain.HostingProxyOperation {
	f.t.Helper()
	until := time.Now().Add(90 * time.Second)
	for time.Now().Before(until) && f.ctx.Err() == nil {
		op, err := f.Control.pg.GetHostingProxyOperation(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if op.State == state {
			return op
		}
		if op.State == "uncertain" || op.State == "refused" || op.State == "rolled_back" || op.State == "canceled" {
			f.t.Fatal("fixture proxy failed", op.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("fixture proxy deadline")
	return domain.HostingProxyOperation{}
}
func (f *automaticRuntimeFixture) ProbeHTTPS(expectedMarker string) {
	f.t.Helper()
	if expectedMarker != "push fixture A\n" && expectedMarker != "push fixture B\n" {
		f.t.Fatal("unknown fixed HTTPS marker")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, item := range []struct{ host, marker string }{{"automatic.example.test", expectedMarker}, {"unrelated.example.test", "Welcome to nginx!"}} {
		request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, "https://"+item.host+"/", nil)
		if err != nil {
			f.t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			f.t.Fatal("independent fixture-CA HTTPS probe", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8192))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != 200 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || !strings.Contains(string(body), item.marker) {
			f.t.Fatal("exact app or unrelated HTTPS route changed", item.host, response.StatusCode)
		}
	}
}

func TestHostingAutomaticDeploymentCompiledRunner(t *testing.T) {
	f := newAutomaticRuntimeFixture(t)
	control := f.Control
	path := "/api/v1/resources/" + control.resource.ID + "/github-push-policy"
	var current domain.GitHubPushPolicy
	control.request(http.MethodGet, path, control.owner, nil, http.StatusOK, &current)
	baseline := current.OwnerAutomaticDeploymentOptions.Baseline
	configuration := domain.HostingAutoDeployConfiguration{Mode: domain.HostingAutoDeployMode, ExpectedHealthyOperationID: baseline.HealthyOperationID, ExpectedLoadedProxyOperationID: baseline.LoadedProxyOperationID, ExpectedLoadedGeneration: baseline.LoadedGeneration, ExpectedRuntimeConfigurationGeneration: baseline.RuntimeConfigurationGeneration, ContainerPort: baseline.ContainerPort, Readiness: baseline.Readiness}
	for _, route := range baseline.Routes {
		configuration.Routes = append(configuration.Routes, domain.HostingRouteSwitchSelection{RouteID: route.RouteID, ExpectedVersion: route.Version, ExpectedSelectedReleaseOperationID: route.SelectedReleaseOperationID})
	}
	input := domain.GitHubPushConfigure{ExpectedResourceGeneration: control.resource.Generation, ExpectedSourceGeneration: control.resource.Source.Git.Generation, ExpectedPolicyGeneration: current.Generation, RequestID: pushAPIUUID(200), ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: control.accountID, WebhookSecret: domain.GitHubPushSecretRef{ID: control.signing.ID, Version: 1}, ProviderReadSecret: &domain.GitHubPushSecretRef{ID: control.provider.ID, Version: 1}, AutomaticDeployment: &configuration}}
	control.request(http.MethodPut, path, control.viewer, input, http.StatusForbidden, nil)
	current = domain.GitHubPushPolicy{} // Decode a fresh receipt; omitted owner options must not retain prior GET fields.
	control.request(http.MethodPut, path, control.owner, input, http.StatusOK, &current)
	if current.Enabled || current.OwnerAutomaticDeploymentOptions != nil {
		t.Fatal("review save skipped disabled state or exposed stale options")
	}
	control.request(http.MethodPost, path+"/verify", control.owner, domain.GitHubPushCommand{ExpectedPolicyGeneration: current.Generation, RequestID: pushAPIUUID(201)}, http.StatusOK, &current)
	if current.DesiredCommit != f.Commits[0] {
		t.Fatal("actual A baseline not verified")
	}
	control.request(http.MethodPost, path+"/enabled", control.owner, domain.GitHubPushEnable{ExpectedPolicyGeneration: current.Generation, RequestID: pushAPIUUID(202), Enabled: true}, http.StatusOK, &current)
	f.lost.Store(true)
	f.SignedPush(210, 1)
	b := f.AwaitBuild(f.Commits[1], "succeeded")
	status := f.AwaitDemand("succeeded")
	if status.DesiredCommit != f.Commits[1] || status.BuildID != b.ID || status.Reason != "https_verified" || status.SwitchOperationID == "" || b.Artifact == nil {
		t.Fatal("B did not reach its own exact HTTPS success")
	}
	deployment, err := control.pg.GetHostingOperation(f.ctx, status.DeploymentOperationID)
	if err != nil || deployment.Result == nil || deployment.Result.ContainerID == "" || deployment.Snapshot.HostPort == f.bootstrap.Snapshot.HostPort || deployment.Snapshot.LocalBuild.Artifact != *b.Artifact || b.Artifact.Platform != "linux/"+runtime.GOARCH {
		t.Fatal("exact artifact/daemon/native-platform/fresh-port deployment mismatch", err)
	}
	image, err := exec.CommandContext(f.ctx, "/usr/local/bin/docker", "inspect", "--format", "{{.Image}}", deployment.Result.ContainerID).Output()
	if err != nil || strings.TrimSpace(string(image)) != b.Artifact.ImageID {
		t.Fatal("serving container differs from exact B artifact")
	}
	f.ProbeHTTPS("push fixture B\n")
	if f.lostCount.Load() != 1 {
		t.Fatal("fixture did not lose exactly one committed switch response")
	}
	if err = f.StopRunner(); err != nil {
		t.Fatal(err)
	}
	if err = f.StartRunner(); err != nil {
		t.Fatal(err)
	}
	replayed := f.AwaitDemand("succeeded")
	if replayed.DeploymentOperationID != status.DeploymentOperationID || replayed.SwitchOperationID != status.SwitchOperationID {
		t.Fatal("restart replaced stable automatic operation identities")
	}
	f.ProbeHTTPS("push fixture B\n")
	var receipts int
	if err = control.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_proxy_receipts WHERE operation_id=$1 AND phase='complete'`, status.SwitchOperationID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("completion response loss duplicated immutable receipt", receipts, err)
	}
	// Delivery order cannot replace current B with a duplicate or delayed A.
	control.push(current, 214, f.Commits[1])
	control.delivery(214, "coalesced")
	control.push(current, 215, f.Commits[0])
	control.delivery(215, "ignored")
	unchanged := f.AwaitDemand("succeeded")
	if unchanged.DeploymentOperationID != status.DeploymentOperationID || unchanged.SwitchOperationID != status.SwitchOperationID {
		t.Fatal("duplicate or delayed push replaced B")
	}
	continuousPID := f.process.command.Process.Pid
	f.SignedPush(211, 2)
	c := f.AwaitBuild(f.Commits[2], "failed")
	failed := f.AwaitDemand("failed")
	if c.Artifact != nil || failed.Reason != "build_failed" || failed.DesiredCommit != f.Commits[2] {
		t.Fatal("failed C created automatic deployment")
	}
	page, err := control.pg.ListHostingBuildLogs(f.ctx, "usr_bootstrap", c.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, item := range page.Items {
		output.WriteString(item.Message)
		output.WriteByte('\n')
	}
	if !strings.Contains(output.String(), "false") || !strings.Contains(output.String(), "exit code: 1") {
		t.Fatalf("C did not prove its source-owned RUN false execution: failure=%s events=%d", c.FailureCode, len(page.Items))
	}
	if err = control.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_auto_deploy_origins WHERE build_id=$1`, c.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("failed C admitted an operation", receipts, err)
	}
	f.ProbeHTTPS("push fixture B\n")
	f.SignedPush(212, 3)
	f.AwaitBuild(f.Commits[3], "succeeded")
	readiness := f.AwaitDemand("failed")
	if f.process == nil || f.process.command.Process.Pid != continuousPID {
		t.Fatal("C to D required a different runner process")
	}
	t.Logf("settled failed C followed by real D build/deployment on same running PID=%d", continuousPID)
	if readiness.Reason != "deployment_failed" || readiness.DesiredCommit != f.Commits[3] {
		t.Fatal("503 candidate did not fail workload readiness", readiness.Reason)
	}
	if err = control.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_auto_deploy_origins WHERE operation_id=$1`, readiness.SwitchOperationID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("readiness failure admitted switch", receipts, err)
	}
	f.ProbeHTTPS("push fixture B\n")
	// Pause one accepted operation before its first API mutation admission. The
	// fixed signed newer desired revision must retire it without Docker effects.
	barrier := &automaticApplyBarrier{entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()
	f.applyBarrier.Store(barrier)
	// A readiness failure may terminate the bare CLI; explicit process recovery
	// is separately checked below before requesting another fixed revision.
	if err = f.StopRunner(); err != nil {
		t.Fatal(err)
	}
	if err = f.StartRunner(); err != nil {
		t.Fatal(err)
	}
	f.SignedPush(216, 1)
	select {
	case <-barrier.entered:
	case <-time.After(4 * time.Minute):
		t.Fatal("actual accepted automatic apply did not reach first-mutation barrier")
	}
	var pending domain.GitHubPushPolicy
	control.request(http.MethodGet, path, control.owner, nil, http.StatusOK, &pending)
	if pending.AutomaticDeployment == nil || pending.AutomaticDeployment.DesiredCommit != f.Commits[1] || pending.AutomaticDeployment.DeploymentOperationID == "" {
		t.Fatal("barrier did not bind an accepted exact B automatic operation")
	}
	blockedID := pending.AutomaticDeployment.DeploymentOperationID
	f.SignedPush(217, 2)
	until := time.Now().Add(3 * time.Second)
	for {
		var latest domain.GitHubPushPolicy
		control.request(http.MethodGet, path, control.owner, nil, http.StatusOK, &latest)
		if latest.DesiredCommit == f.Commits[2] && latest.AutomaticDeployment != nil && latest.AutomaticDeployment.DesiredCommit == f.Commits[2] {
			break
		}
		if time.Now().After(until) {
			t.Fatal("newer signed desired revision was not durably accepted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	release()
	select {
	case <-barrier.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("superseded first-mutation request did not finish")
	}
	if barrier.status.Load() != http.StatusConflict {
		t.Fatal("superseded first mutation was not denied", barrier.status.Load())
	}
	if err = control.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$1`, blockedID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("superseded operation obtained a Docker mutation admission", receipts, err)
	}
	blocked, err := control.pg.GetHostingOperation(f.ctx, blockedID)
	if err != nil || blocked.MutationHold || blocked.Result != nil {
		t.Fatal("superseded operation retained host effects", err)
	}
	// Check real Docker, not only stored status: the exact previously serving B
	// remains the sole container for its image; no new candidate was created.
	containers, err := exec.CommandContext(f.ctx, "/usr/local/bin/docker", "ps", "-aq", "--no-trunc", "--filter", "ancestor="+b.Artifact.ImageID).Output()
	if err != nil || strings.TrimSpace(string(containers)) != deployment.Result.ContainerID {
		t.Fatal("superseded apply changed actual B containers", err)
	}
	f.ProbeHTTPS("push fixture B\n")
	t.Logf("actual first-mutation denial: operation=%s HTTP409 no apply admission, no hold or Docker candidate; original B and unrelated HTTPS preserved", blockedID)
	mode, err := os.Stat(f.credential)
	if err != nil || mode.Mode().Perm() != 0600 {
		t.Fatal("owned credential mode changed", err)
	}
	t.Logf("actual API/signed fixed provider push/compiled Linux agent/trusted fixture-CA HTTPS passed: native=%s B=%s artifact=%s switch=%s observed_loaded_generation=%d; failed C and 503 D preserve B and unrelated route; lost committed response replay and restart preserve exact IDs", runtime.GOARCH, f.Commits[1], b.Artifact.ImageID, status.SwitchOperationID, status.ObservedLoadedGeneration)
}

const automaticFixturePebbleImage = "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"

func automaticFixtureDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("owned fixture Docker %s failed: %v", args[0], err)
	}
	return output
}
func automaticFixtureDNS(t *testing.T) {
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

func automaticFixtureTrust(t *testing.T, root string) (*x509.CertPool, []byte) {
	t.Helper()
	automaticFixtureDNS(t)
	pebble := strings.TrimSpace(string(automaticFixtureDocker(t, "run", "-d", "--name", "fixture-pebble", "--label", "io.nerocd.auto-deploy.inner=fixture", "--network", "host", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "--mount", "type=bind,src=/workspace/acceptance/runtime-proxy-control/pebble.json,dst=/pebble-config.json,readonly", automaticFixturePebbleImage, "-config", "/pebble-config.json", "-dnsserver", "127.0.0.1:8053")))
	t.Logf("owned inner Pebble id=%s label=io.nerocd.auto-deploy.inner=fixture", pebble)
	t.Cleanup(func() {
		identity := strings.TrimSpace(string(automaticFixtureDocker(t, "inspect", "--format", `{{.Id}} {{index .Config.Labels "io.nerocd.auto-deploy.inner"}}`, pebble)))
		if identity != pebble+" fixture" {
			t.Error("owned Pebble identity changed")
			return
		}
		automaticFixtureDocker(t, "rm", "--force", "--volumes", pebble)
	})
	automaticFixtureDocker(t, "cp", "fixture-pebble:/test/certs/pebble.minica.pem", filepath.Join(root, "minica.pem"))
	publicRoot, err := os.ReadFile(filepath.Join(root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(publicRoot) {
		t.Fatal("invalid fixture root")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
