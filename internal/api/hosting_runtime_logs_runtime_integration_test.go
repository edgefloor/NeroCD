package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
)

type runtimeLogsFixtureTrace struct {
	mu        sync.Mutex
	paths     []string
	opens     map[string]int
	active    atomic.Int32
	mismatch  atomic.Bool
	forbidden atomic.Bool
}

func runtimeLogsUnixProxy(t *testing.T, trace *runtimeLogsFixtureTrace) string {
	t.Helper()
	socket := "/fixture/runtime-log-reader.sock"
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "docker"; r.Host = "docker" }, Transport: transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0)}
	exact := regexp.MustCompile(`^/v1\.54/containers/[a-f0-9]{64}/(json|logs)$`)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := r.Method == "GET" && (r.URL.Path == "/version" || r.URL.Path == "/v1.54/info" || exact.MatchString(r.URL.Path))
		logs := strings.HasSuffix(r.URL.Path, "/logs")
		if logs {
			valid = valid && r.URL.RawQuery == "stdout=1&stderr=1&timestamps=1&tail=200&follow=1"
		} else {
			valid = valid && r.URL.RawQuery == ""
		}
		if !valid {
			trace.forbidden.Store(true)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		trace.mu.Lock()
		if len(trace.paths) < 512 {
			trace.paths = append(trace.paths, r.URL.Path)
		}
		if logs {
			trace.opens[r.URL.Path]++
		}
		trace.mu.Unlock()
		if logs {
			trace.active.Add(1)
			defer trace.active.Add(-1)
		}
		if trace.mismatch.Load() && r.URL.Path == "/v1.54/info" {
			_, _ = io.WriteString(w, `{"ID":"different-daemon"}`)
			return
		}
		proxy.ServeHTTP(w, r)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); transport.CloseIdleConnections(); <-done; _ = os.Remove(socket) })
	return "unix://" + socket
}
func TestHostingRuntimeLogsCompiledRunner(t *testing.T) {
	if os.Getenv("NEROCD_RUNTIME_LOGS_RUNTIME") != "disposable-dind" {
		t.Skip("run acceptance/runtime-hosting-logs/gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("requires Linux fixture")
	}
	server, pg, database := newHostingBuildAPIFixture(t)
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := server.app.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime logs"})
	if err != nil {
		t.Fatal(err)
	}
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability}
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_runtime_logs", RunnerName: "logs", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	const token = "ncr_runtime_logs_fixture_bearer"
	sum := sha256.Sum256([]byte(token))
	enrolled, err := server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := server.app.CreateServer(admin, app.ServerInput{Name: "runtime logs", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.app.GrantDestinationProject(admin, destination.Destination.ID, app.DestinationProjectInput{ProjectID: env.ProjectID}); err != nil {
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
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(c); e != nil {
			t.Error(e)
		}
	})
	trace := &runtimeLogsFixtureTrace{opens: map[string]int{}}
	endpoint := runtimeLogsUnixProxy(t, trace)
	var stallAppend atomic.Bool
	appendBlocked := make(chan struct{}, 1)
	var stalled atomic.Int32
	var lost atomic.Bool
	lost.Store(true)
	var retryVerified, leak atomic.Bool
	var appendMu sync.Mutex
	lastID, lastHash := "", ""
	duplicates := 0
	control := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/runtime-log-sessions/events" {
			if stallAppend.Load() {
				_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, domain.HostingRuntimeLogByteLimit+1))
				_ = r.Body.Close()
				stalled.Add(1)
				defer stalled.Add(-1)
				select {
				case appendBlocked <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}

			raw, e := io.ReadAll(io.LimitReader(r.Body, domain.HostingRuntimeLogByteLimit+1))
			if e != nil {
				w.WriteHeader(500)
				return
			}
			defer clear(raw)
			if bytes.Contains(raw, []byte(token)) {
				leak.Store(true)
			}
			var batch domain.HostingRuntimeLogBatch
			if json.Unmarshal(raw, &batch) != nil {
				w.WriteHeader(400)
				return
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(raw))
			appendMu.Lock()
			if batch.BatchID == lastID {
				duplicates++
				if digest == lastHash {
					retryVerified.Store(true)
				} else {
					leak.Store(true)
				}
			}
			lastID, lastHash = batch.BatchID, digest
			appendMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if lost.Swap(false) {
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Error("lost-response append did not commit")
				}
				connection, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr == nil {
					_ = connection.Close()
				}
				return
			}
		}
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(control.Close)
	actorToken := createTestSession(t, server)
	call := func(method, path string, in, out any, want int) {
		t.Helper()
		var body io.Reader
		if in != nil {
			raw, e := json.Marshal(in)
			if e != nil {
				t.Fatal(e)
			}
			body = bytes.NewReader(raw)
		}
		request, e := http.NewRequestWithContext(ctx, method, control.URL+path, body)
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+actorToken)
		if in != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, e := control.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != want {
			t.Fatalf("registered %s %s status%d want%d", method, path, response.StatusCode, want)
		}
		if out != nil {
			if e = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(out); e != nil {
				t.Fatal(e)
			}
		}
	}
	work := t.TempDir()
	certificate := filepath.Join(work, "ca.pem")
	credential := filepath.Join(work, "credential")
	if err = os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	controlled := &runtimeFixtureLog{}
	command := func(collect bool) *exec.Cmd {
		args := []string{"runner", "--server", control.URL, "--credential-file", credential, "--work-dir", work, "--journal-dir", filepath.Join(work, "journal"), "--poll-interval", "200ms"}
		capabilities := append([]string(nil), caps...)
		if collect {
			args = append(args, "--hosting-runtime-logs", "--image-docker-endpoint", endpoint)
			capabilities = append(capabilities, domain.HostingRuntimeLogsCapability)
		} else {
			args = append(args, "--once")
		}
		args = append(args, "--capabilities", strings.Join(capabilities, ","))
		c := exec.CommandContext(ctx, "/fixture/nerocd", args...)
		c.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+certificate)
		c.Stdout, c.Stderr = controlled, controlled
		return c
	}
	start := func() *observationFixtureAgent {
		t.Helper()
		agent := &observationFixtureAgent{command: command(true), done: make(chan error, 1)}
		if e := agent.command.Start(); e != nil {
			t.Fatal(e)
		}
		go func() { agent.done <- agent.command.Wait() }()
		t.Cleanup(func() { agent.Stop(t) })
		return agent
	}
	docker := func(args ...string) string {
		t.Helper()
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, e := exec.CommandContext(c, "/usr/local/bin/docker", args...).Output()
		if e != nil {
			t.Fatal("owned Docker fixture action failed")
		}
		return strings.TrimSpace(string(out))
	}
	createResource := func(name string) domain.Resource {
		t.Helper()
		resource, e := server.app.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: name, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: os.Getenv("NEROCD_RUNTIME_LOGS_IMAGE")}}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = server.app.BindResourceDestination(admin, resource.ID, app.ResourceBindingInput{DestinationID: destination.Destination.ID}); e != nil {
			t.Fatal(e)
		}
		return resource
	}
	deploy := func(resource domain.Resource, key string) domain.HostingOperation {
		t.Helper()
		var op domain.HostingOperation
		call("POST", "/api/v1/resources/"+resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: key, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 3}}, &op, 202)
		if e := pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		if e := command(false).Run(); e != nil {
			t.Fatal("compiled registered deployment failed")
		}
		op, e := server.app.HostingOperation(admin, op.ID)
		if e != nil || op.State != "succeeded" || op.Result == nil {
			t.Fatal("real deployment did not settle")
		}
		return op
	}
	primary := createResource("primary actual json-file")
	initial := deploy(primary, "logs-primary-actual")
	t.Logf("primary real compiled deployment: operation=%s run=%s exact_container=%s image=%s daemon=%s", initial.ID, initial.RunID, initial.Result.ContainerID, initial.Result.ImageID, initial.Result.DaemonID)
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_runtime_logs_v1') WHERE id=$1`, enrolled.Runner.ID); err != nil {
		t.Fatal(err)
	}
	agent := start()
	wait := func(name string, condition func() bool) {
		t.Helper()
		until := time.Now().Add(18 * time.Second)
		for !condition() {
			if time.Now().After(until) {
				t.Fatal(name + " timed out")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	open := func(resource domain.Resource, suffix string) domain.HostingRuntimeLogSession {
		t.Helper()
		sum := sha256.Sum256([]byte(suffix))
		var session domain.HostingRuntimeLogSession
		call("POST", "/api/v1/resources/"+resource.ID+"/runtime-log-sessions", domain.HostingRuntimeLogOpen{RequestID: hex.EncodeToString(sum[:16]), RequestExpiresAt: time.Now().Add(20 * time.Second)}, &session, 200)
		return session
	}
	page := func(session domain.HostingRuntimeLogSession) domain.HostingRuntimeLogPage {
		t.Helper()
		var got domain.HostingRuntimeLogPage
		call("GET", "/api/v1/runtime-log-sessions/"+session.ID+"/logs?limit=100", nil, &got, 200)
		return got
	}
	trigger := func(port int, path string) {
		t.Helper()
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
		response, e := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if e != nil {
			t.Fatal("owned workload trigger failed")
		}
		_ = response.Body.Close()
	}
	if os.Getenv("NEROCD_RUNTIME_LOGS_ACTUAL_BROWSER") == "1" {
		completed := runHostingRuntimeLogsActualBrowser(ctx, t, server, database, env, primary, initial, func(stage string) {
			switch stage {
			case "emit":
				trigger(initial.Plan.Snapshot.HostPort, "/emit")
			case "burst":
				trigger(initial.Plan.Snapshot.HostPort, "/burst")
			case "disconnect":
				agent.Stop(t)
				wait("browser collector disconnected", func() bool { return trace.active.Load() == 0 })
			case "resume":
				agent = start()
				wait("browser collector reconnected", func() bool { return trace.active.Load() == 1 })
			}
		})
		agent.Stop(t)
		if !completed {
			t.Log("browser fixture released early; journey remains unqualified")
			return
		}
		if !retryVerified.Load() || trace.forbidden.Load() || leak.Load() || controlled.contains(token) {
			t.Fatal("browser collector retry, Docker allowlist or credential redaction failed")
		}
		t.Log("actual browser: registered deployment settled before Docker output; compiled collector, exact lost-append retry, viewer stop/reconnect gap/expiry and authorization clearing qualified")
		return
	}
	session := open(primary, "primary")
	wait("empty following plus lost-response exact retry", func() bool { return retryVerified.Load() })
	if !page(session).Session.DemandExpiresAt.Equal(session.DemandExpiresAt) {
		t.Fatal("collector renewed browser demand")
	}
	trigger(initial.Plan.Snapshot.HostPort, "/emit")
	wait("post-deployment stdout and stderr", func() bool {
		p := page(session)
		stdout, stderr, post, redacted := false, false, false, false
		previous := 0
		for _, item := range p.Items {
			if item.Sequence <= previous {
				t.Error("server ordering regressed")
			}
			previous = item.Sequence
			redacted = redacted || strings.Contains(item.Message, "[REDACTED]") && strings.Contains(item.Message, "canary-safe")
			stdout = stdout || item.Stream == "stdout"
			stderr = stderr || item.Stream == "stderr"
			post = post || item.SourceTimestamp != nil && item.SourceTimestamp.After(initial.UpdatedAt)
			if strings.Contains(item.Message, token) {
				t.Error("bearer reached API cache")
			}
		}
		return stdout && stderr && post && redacted
	})
	// Several renewals retain exactly one reader. The producer burst exercises
	// actual source output through server prefix retention, not synthetic appends.
	trigger(initial.Plan.Snapshot.HostPort, "/burst")
	wait("retention prefix gap", func() bool { p := page(session); return p.DeletedThroughSequence > 0 && p.Gap })
	trace.mu.Lock()
	opens := trace.opens["/v1.54/containers/"+initial.Result.ContainerID+"/logs"]
	trace.mu.Unlock()
	if opens != 1 {
		t.Fatal("renewal duplicated the primary reader")
	}
	// Demand expires in real time; the runner must not keep it alive.
	until := session.DemandExpiresAt.Add(12 * time.Second)
	for trace.active.Load() != 0 || time.Now().Before(session.DemandExpiresAt) {
		if time.Now().After(until) {
			t.Fatal("demand expiry did not close exact reader")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !page(session).Session.DemandExpiresAt.Equal(session.DemandExpiresAt) {
		t.Fatal("demand deadline slid")
	}
	agent.Stop(t)
	t.Log("primary: real registered API and compiled runner deployment completed before new stdout/stderr output; empty batch retry, renewals, retention gap and real demand expiry qualified")
	// Additional modes use fixture-created containers and fixture-reported apply
	// evidence, because production deployment options do not select TTY/driver.
	seed := func(resource domain.Resource, key, driver string, tty bool, port int) domain.HostingOperation {
		t.Helper()
		var op domain.HostingOperation
		call("POST", "/api/v1/resources/"+resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: key, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 3}}, &op, 202)
		if e := pg.PrepareDeployment(ctx, op.ID); e != nil {
			t.Fatal(e)
		}
		op, err = server.app.HostingOperation(admin, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		lease, e := pg.ClaimRun(ctx, enrolled.Runner.ID, time.Now(), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		if lease.Run.ID != op.RunID {
			t.Fatal("fixture claimed unrelated run")
		}
		authority := domain.HostingAuthority{OperationID: op.ID, RunID: lease.Run.ID, LeaseID: lease.Lease.ID, Attempt: lease.Lease.Attempt, Fence: lease.Lease.Fence}
		if _, e = pg.HostingRunnerPlan(ctx, enrolled.Runner.ID, authority); e != nil {
			t.Fatal(e)
		}
		if _, e = pg.HostingPrepared(ctx, enrolled.Runner.ID, domain.HostingPreparedRequest{HostingAuthority: authority, ReceiptID: key + "-prepared", Release: *initial.Release}); e != nil {
			t.Fatal(e)
		}
		admitted, e := pg.HostingApplyBegin(ctx, enrolled.Runner.ID, authority)
		if e != nil {
			t.Fatal(e)
		}
		suffix := strings.TrimPrefix(domain.HostingDocumentHash(op.ID), "sha256:")[:16]
		args := []string{"run", "--detach", "--name", admitted.Plan.Snapshot.RuntimeNamespace + "_" + suffix, "--log-driver", driver, "--label", "io.nerocd.binding=" + op.BindingID, "--label", "io.nerocd.operation=" + op.ID, "--label", "io.nerocd.release=" + initial.Release.Hash, "--label", "io.nerocd.plan=" + admitted.Plan.Hash, "--publish", "127.0.0.1:" + strconv.Itoa(port) + ":8080"}
		if tty {
			args = append(args, "--tty")
		}
		args = append(args, os.Getenv("NEROCD_RUNTIME_LOGS_IMAGE"))
		id := docker(args...)
		if len(id) != 64 {
			t.Fatal("fixture container ID incomplete")
		}
		if _, e = pg.HostingComplete(ctx, enrolled.Runner.ID, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: key + "-complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: id, ImageID: initial.Release.ImageID, DaemonID: initial.Release.DaemonID, PlanHash: admitted.Plan.Hash}}); e != nil {
			t.Fatal(e)
		}
		op, e = server.app.HostingOperation(admin, op.ID)
		if e != nil || op.State != "succeeded" {
			t.Fatal("fixture evidence did not settle")
		}
		t.Logf("additional synthetic apply provenance only: driver=%s tty=%t exact_container=%s operation=%s", driver, tty, id, op.ID)
		return op
	}
	for i, mode := range []struct {
		name, driver string
		tty          bool
	}{{"json tty", "json-file", true}, {"local stdout stderr", "local", false}, {"local tty", "local", true}, {"unsupported none", "none", false}} {
		resource := createResource(mode.name)
		port := 20080 + i
		op := seed(resource, "logs-mode-"+strconv.Itoa(i), mode.driver, mode.tty, port)
		running := start()
		session = open(resource, "mode-"+strconv.Itoa(i))
		if mode.driver == "none" {
			wait("unsupported driver", func() bool { return page(session).Session.State == "unsupported_driver" })
			trace.mu.Lock()
			opened := trace.opens["/v1.54/containers/"+op.Result.ContainerID+"/logs"]
			trace.mu.Unlock()
			if opened != 0 {
				t.Fatal("unsupported driver opened stream")
			}
			running.Stop(t)
			continue
		}
		trigger(port, "/emit")
		wait(mode.name+" output", func() bool {
			p := page(session)
			stdout, stderr := false, false
			for _, item := range p.Items {
				stdout = stdout || item.Stream == "stdout"
				stderr = stderr || item.Stream == "stderr"
				if item.SourceTimestamp == nil {
					t.Error("Docker source timestamp lost")
				}
				if mode.tty && item.Stream != "combined" {
					t.Error("TTY invented separate streams")
				}
				if !mode.tty && item.Stream == "combined" {
					t.Error("non-TTY streams merged")
				}
				if strings.Contains(item.Message, token) {
					t.Error("bearer reached retained API output")
				}
			}
			return len(p.Items) > 1 && (mode.tty || stdout && stderr)
		})
		switch i {
		case 0:
			// A daemon mismatch stops the exact stream. Reacquisition after the
			// conservative lease wait must create a visible epoch gap and new tail.
			trace.mismatch.Store(true)
			wait("daemon mismatch close", func() bool { return trace.active.Load() == 0 })
			trace.mismatch.Store(false)
			wait("new epoch visible source gap", func() bool { p := page(session); return p.Gap && trace.active.Load() == 1 })
			if _, e := database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,'hosting_runtime_logs_v1') WHERE id=$1`, enrolled.Runner.ID); e != nil {
				t.Fatal(e)
			}
			wait("revocation closes stream", func() bool { return trace.active.Load() == 0 })
			if _, e := database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,'hosting_runtime_logs_v1') WHERE id=$1`, enrolled.Runner.ID); e != nil {
				t.Fatal(e)
			}
		case 1:
			replacement := seed(resource, "logs-replacement", "local", false, 20190)
			if replacement.Result.ContainerID == op.Result.ContainerID {
				t.Fatal("fixture replacement identity unchanged")
			}
			wait("replacement closes old exact stream", func() bool { return trace.active.Load() == 0 })
			if docker("inspect", "--format", "{{.State.Running}}", op.Result.ContainerID) != "true" {
				t.Fatal("collector or fixture stopped the old container during replacement proof")
			}
		default:
			call("DELETE", "/api/v1/runtime-log-sessions/"+session.ID, nil, nil, 200)
			wait("explicit viewer stop", func() bool { return trace.active.Load() == 0 })
			running.Stop(t)
			// A real started process must join both a blocked append and its source
			// reader after SIGTERM. Pause only this fixture-owned workload.
			docker("pause", op.Result.ContainerID)
			stalledSession := open(resource, "stalled-append")
			stallAppend.Store(true)
			blocked := start()
			select {
			case <-appendBlocked:
			case <-time.After(8 * time.Second):
				t.Fatal("compiled stalled append handshake missing")
			}
			blocked.Stop(t)
			wait("signal joins stalled append and reader", func() bool { return trace.active.Load() == 0 && stalled.Load() == 0 })
			stallAppend.Store(false)
			call("DELETE", "/api/v1/runtime-log-sessions/"+stalledSession.ID, nil, nil, 200)
			docker("unpause", op.Result.ContainerID)

		}
		running.Stop(t)
	}
	if trace.forbidden.Load() || leak.Load() || controlled.contains(token) {
		t.Fatal("collector exceeded Docker allowlist or exposed bearer in controlled output")
	}
	appendMu.Lock()
	retried := duplicates
	appendMu.Unlock()
	if retried != 1 {
		t.Fatal("lost append response did not produce exactly one identical retry")
	}
	trace.mu.Lock()
	pathCount := len(trace.paths)
	counts := map[string]int{}
	for _, path := range trace.paths {
		switch {
		case path == "/version":
			counts["version"]++
		case path == "/v1.54/info":
			counts["info"]++
		case strings.HasSuffix(path, "/json"):
			counts["inspect"]++
		case strings.HasSuffix(path, "/logs"):
			counts["logs"]++
		}
	}
	trace.mu.Unlock()
	t.Logf("allowlisted Docker GET counts: version=%d info=%d inspect=%d logs=%d forbidden=false", counts["version"], counts["info"], counts["inspect"], counts["logs"])
	t.Logf("compiled Linux runtime-log adapter: Docker API1.54; primary actual deployment; additional TTY/local fixture provenance; source/server order; exact retry; empty batch; retention/demand/daemon/revocation/replacement/stop; %d allowlisted GETs; known bearer absent controlled output", pathCount)
}
