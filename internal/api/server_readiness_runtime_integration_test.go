package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

type readinessFixtureAgent struct {
	command *exec.Cmd
	done    chan error
	once    sync.Once
}

func (a *readinessFixtureAgent) Stop(t *testing.T) {
	t.Helper()
	a.once.Do(func() {
		_ = a.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-a.done:
		case <-time.After(4 * time.Second):
			_ = a.command.Process.Kill()
			<-a.done
			t.Error("readiness agent did not join after SIGTERM")
		}
	})
}

func TestServerReadinessCompiledAgent(t *testing.T) {
	if os.Getenv("NEROCD_READINESS_RUNTIME") != "disposable-dind" {
		t.Skip("run scripts/ci/server-readiness-runtime-gate.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("requires native Linux fixture")
	}
	server, _, database := newHostingBuildAPIFixture(t)
	server.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	caps := []string{domain.RunTypeComposeDeploy, domain.ServerReadinessCapability}
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_readiness_runtime", RunnerName: "readiness", Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	token := "ncr_readiness_fixture_credential"
	sum := sha256.Sum256([]byte(token))
	enrolled, err := server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.app.CreateServer(admin, app.ServerInput{Name: "readiness runtime", RunnerID: enrolled.Runner.ID})
	if err != nil {
		t.Fatal(err)
	}
	var reportMu sync.Mutex
	var reports [][]byte
	var challenges atomic.Int32
	var lost atomic.Bool
	control := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runner/server-readiness/challenge" {
			challenges.Add(1)
		}
		if r.URL.Path != "/api/v1/runner/server-readiness/report" {
			server.ServeHTTP(w, r)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
		if e != nil {
			t.Error(e)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		reportMu.Lock()
		reports = append(reports, append([]byte(nil), raw...))
		reportMu.Unlock()
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, r)
		if rec.Code == 200 && lost.CompareAndSwap(false, true) {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_ = conn.Close()
			return
		}
		for key, values := range rec.Header() {
			for _, v := range values {
				w.Header().Add(key, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	defer control.Close()
	root, err := os.MkdirTemp("/tmp", "nerocd-readiness-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	certificate := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "credential")
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	logs := &runtimeFixtureLog{}
	command := func(extra ...string) *exec.Cmd {
		args := []string{"runner", "--hosting-readiness", "--execute", "--server", control.URL, "--credential-file", credential, "--work-dir", root, "--poll-interval", "200ms", "--capabilities", strings.Join(caps, ",")}
		args = append(args, extra...)
		c := exec.CommandContext(ctx, "/fixture/nerocd", args...)
		c.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+certificate, "PATH=/fixture/readiness-bin:/usr/local/bin:/usr/bin:/bin")
		c.Stdout, c.Stderr = logs, logs
		return c
	}
	status := func() domain.ServerReadiness {
		t.Helper()
		value, e := server.app.ServerReadiness(admin, target.Server.ID)
		if e != nil {
			t.Fatal(e)
		}
		return value
	}
	wait := func(name string, limit time.Duration, predicate func(domain.ServerReadiness) bool) domain.ServerReadiness {
		t.Helper()
		until := time.Now().Add(limit)
		for {
			value := status()
			if predicate(value) {
				t.Logf("%s status=%s reason=%s", name, value.Status, value.Reason)
				return value
			}
			if time.Now().After(until) {
				t.Fatalf("%s timed out: %+v", name, value)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	start := func(extra ...string) *readinessFixtureAgent {
		t.Helper()
		a := &readinessFixtureAgent{command: command(extra...), done: make(chan error, 1)}
		if e := a.command.Start(); e != nil {
			t.Fatal(e)
		}
		go func() { a.done <- a.command.Wait() }()
		t.Cleanup(func() { a.Stop(t) })
		return a
	}
	initial := status()
	if initial.Status != "unknown" || initial.Reason != "no_sample" {
		t.Fatal(initial)
	}
	badJournal := filepath.Join(root, "unsafe-journal")
	if err = os.Mkdir(badJournal, 0755); err != nil {
		t.Fatal(err)
	}
	if err = command("--journal-dir", badJournal).Run(); err == nil {
		t.Fatal("unsafe journal startup succeeded")
	}
	if challenges.Load() != 0 || status().Reason != "no_sample" {
		t.Fatal("startup failure collected readiness")
	}
	var runsBefore int64
	if err = database.QueryRow(ctx, `SELECT count(*) FROM task_runs`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	agent := start()
	ready := wait("actual collector ready", 8*time.Second, func(v domain.ServerReadiness) bool { return v.Status == "ready" })
	if ready.Report == nil || ready.Report.Architecture != runtime.GOARCH || ready.Report.DaemonArchitecture != runtime.GOARCH || ready.Report.DockerAPIVersion != "1.54" || ready.Report.CLI != "available" || ready.Report.JournalRoot != "accessible" {
		t.Fatal(ready)
	}
	reportMu.Lock()
	if len(reports) < 2 || !bytes.Equal(reports[0], reports[1]) {
		t.Error("lost-response retry was not identical")
	}
	reportMu.Unlock()
	agent.Stop(t)
	journals, err := filepath.Glob(filepath.Join(root, ".nerocd-journal-*"))
	if err != nil || len(journals) != 1 {
		t.Fatal("default resolved journal missing", journals, err)
	}
	journal, err := runner.OpenAttemptJournal(journals[0])
	if err != nil {
		t.Fatal(err)
	}
	if journal.Depth() != 0 {
		t.Error("collector journaled diagnostic")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	stale := wait("real clock stale", 35*time.Second, func(v domain.ServerReadiness) bool { return v.Status == "unknown" && v.Reason == "stale" })
	if stale.ObservedAt == nil || ready.ObservedAt == nil || !stale.ObservedAt.Equal(*ready.ObservedAt) {
		t.Fatal("staleness extended original receipt")
	}
	agent = start()
	recovered := wait("collector recovery", 8*time.Second, func(v domain.ServerReadiness) bool { return v.Status == "ready" && v.SampleID != ready.SampleID })
	agent.Stop(t)
	missing := start("--image-docker-endpoint", "unix://"+filepath.Join(root, "missing.sock"))
	wait("daemon unreachable", 8*time.Second, func(v domain.ServerReadiness) bool {
		return v.Status == "blocked" && v.Report != nil && v.Report.Docker == "unreachable" && v.SampleID != recovered.SampleID
	})
	missing.Stop(t)
	socket := filepath.Join(root, "unsupported.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	unsupportedHTTP := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/version" {
			t.Error("unsupported daemon received non-readonly request")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.53", "MinAPIVersion": "1.44"})
	})}
	served := make(chan error, 1)
	go func() { served <- unsupportedHTTP.Serve(listener) }()
	defer func() { _ = unsupportedHTTP.Close(); <-served }()
	unsupported := start("--image-docker-endpoint", "unix://"+socket)
	wait("unsupported Docker API", 8*time.Second, func(v domain.ServerReadiness) bool {
		return v.Status == "blocked" && v.Report != nil && v.Report.Docker == "unsupported_api"
	})
	unsupported.Stop(t)
	agent = start()
	wait("recovery before root change", 8*time.Second, func(v domain.ServerReadiness) bool { return v.Status == "ready" })
	if err = os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	wait("unsafe work root", 13*time.Second, func(v domain.ServerReadiness) bool {
		return v.Status == "blocked" && v.Report != nil && v.Report.WorkRoot == "unsafe"
	})
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	wait("work root recovery", 13*time.Second, func(v domain.ServerReadiness) bool { return v.Status == "ready" })
	beforeRevoke := challenges.Load()
	if _, err = database.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id=$1`, enrolled.Runner.ID); err != nil {
		t.Fatal(err)
	}
	wait("current enrollment revoked", time.Second, func(v domain.ServerReadiness) bool {
		return v.Status == "unknown" && v.Reason == "authority_unavailable"
	})
	until := time.Now().Add(13 * time.Second)
	for challenges.Load() == beforeRevoke && time.Now().Before(until) {
		time.Sleep(100 * time.Millisecond)
	}
	if challenges.Load() == beforeRevoke {
		t.Fatal("revoked collector did not attempt next bounded cycle")
	}
	agent.Stop(t)
	var runsAfter int64
	if err = database.QueryRow(ctx, `SELECT count(*) FROM task_runs`).Scan(&runsAfter); err != nil || runsAfter != runsBefore {
		t.Fatal("collector created jobs", runsBefore, runsAfter, err)
	}
	if _, err = os.Stat("/fixture/readiness-cli-executed"); !os.IsNotExist(err) {
		t.Fatal("collector executed CLI")
	}
	if logs.contains(token) || logs.contains("never-readiness-raw-canary") {
		t.Fatal("collector exposed private/raw data")
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	for _, raw := range reports {
		if len(raw) > 8192 || bytes.Contains(raw, []byte(root)) || bytes.Contains(raw, []byte(token)) {
			t.Fatal("report leaked local data")
		}
	}
	t.Logf("compiled native agent completed no_sample->ready->real-clock stale->recovered; Docker=%s arch=%s, lost-response retry, root failure/recovery, unsupported/unreachable Docker, revocation and SIGTERM join; jobs unchanged", ready.Report.DockerAPIVersion, ready.Report.Architecture)
}
