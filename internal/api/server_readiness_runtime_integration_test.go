package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	"nerocd/web"
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
			t.Logf("owned agent pid=%d joined", a.command.Process.Pid)
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

const readinessBrowserCaps = "compose-deploy,image_deploy,image_release_ports_v1,hosting_runtime_env_v1,hosting_observation_v1,hosting_runtime_logs_v1,hosting_readiness_v1"

// The browser handoff is test-only and accepts the exact generated grammar,
// never executable shell text. Only the four dedicated fixture paths change.
func readinessBrowserArgv(command, origin, root string) ([]string, error) {
	expected := "nerocd runner --server '" + origin + "' --enrollment-file /secure/nerocd-runner-enrollment.token --credential-file /secure/nerocd-runner.credential --work-dir /var/lib/nerocd/work --journal-dir /var/lib/nerocd/journal --capabilities '" + readinessBrowserCaps + "' --compose-image-policy pull --hosting-readiness --hosting-runtime-env --hosting-observation --hosting-runtime-logs --execute"
	if command != expected {
		return nil, errors.New("unexpected generated command")
	}
	args := strings.Fields(command)[1:]
	replacements := map[string]string{"--enrollment-file": "enrollment", "--credential-file": "credential", "--work-dir": "work", "--journal-dir": "journal"}
	for i := 0; i < len(args); i++ {
		args[i] = strings.Trim(args[i], "'")
		if name, ok := replacements[args[i]]; ok {
			i++
			args[i] = filepath.Join(root, name)
		}
	}
	return append(args, "--poll-interval", "200ms"), nil
}

func readinessBrowserRestartArgv(initial []string) []string {
	result := make([]string, 0, len(initial)-2)
	for i := 0; i < len(initial); i++ {
		if initial[i] == "--enrollment-file" {
			i++
			continue
		}
		result = append(result, initial[i])
	}
	return result
}

func TestReadinessBrowserRestartUsesCredential(t *testing.T) {
	initial := []string{"runner", "--enrollment-file", "/owned/enrollment", "--credential-file", "/owned/credential", "--capabilities", readinessBrowserCaps, "--hosting-readiness", "--execute"}
	original := append([]string(nil), initial...)
	got := readinessBrowserRestartArgv(initial)
	want := []string{"runner", "--credential-file", "/owned/credential", "--capabilities", readinessBrowserCaps, "--hosting-readiness", "--execute"}
	if !slices.Equal(got, want) || !slices.Equal(initial, original) {
		t.Fatal("restart changed credential/capability flags or initial argv")
	}
}

func readinessBrowserControlAllowed(principal auth.Principal, actorID, origin string, r *http.Request) bool {
	admin := false
	for _, role := range principal.Roles {
		admin = admin || role == domain.RoleSystemAdmin
	}
	return admin && principal.ID == actorID && r.Method == http.MethodPost && r.Header.Get("Origin") == origin && r.Header.Get("X-NeroCD-CSRF") == "1" && r.URL.RawQuery == ""
}

type readinessBrowserSession struct {
	mu     sync.Mutex
	hashes map[string][32]byte
}

func (s *readinessBrowserSession) allow(actor string, r *http.Request) bool {
	cookie, err := r.Cookie("nerocd_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes == nil {
		s.hashes = make(map[string][32]byte)
	}
	original, ok := s.hashes[actor]
	if !ok {
		s.hashes[actor] = hash
		return true
	}
	return original == hash
}

func TestReadinessBrowserHelperSecurity(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/fixture", nil)
	sessions := &readinessBrowserSession{}
	request.AddCookie(&http.Cookie{Name: "nerocd_session", Value: "fixture-session-a"})
	if !sessions.allow("actor", request) {
		t.Fatal("first session rejected")
	}
	if !sessions.allow("actor", request) {
		t.Fatal("same session rejected")
	}
	other := request.Clone(t.Context())
	other.Header = request.Header.Clone()
	other.Header.Set("Cookie", "nerocd_session=fixture-session-b")
	if sessions.allow("actor", other) {
		t.Fatal("second session accepted")
	}
	request.Header.Set("Origin", "http://127.0.0.1:32966")
	request.Header.Set("X-NeroCD-CSRF", "1")
	principal := auth.Principal{ID: "actor", Roles: []string{domain.RoleSystemAdmin}}
	if !readinessBrowserControlAllowed(principal, "actor", "http://127.0.0.1:32966", request) {
		t.Fatal("valid control rejected")
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Method = http.MethodGet }, func(r *http.Request) { r.Header.Del("Origin") }, func(r *http.Request) { r.Header.Del("X-NeroCD-CSRF") }, func(r *http.Request) { r.URL.RawQuery = "x=1" }} {
		r := request.Clone(t.Context())
		r.Header = request.Header.Clone()
		change(r)
		if readinessBrowserControlAllowed(principal, "actor", "http://127.0.0.1:32966", r) {
			t.Fatal("unsafe control allowed")
		}
	}
	if readinessBrowserControlAllowed(auth.Principal{ID: "actor"}, "actor", "http://127.0.0.1:32966", request) || readinessBrowserControlAllowed(principal, "other", "http://127.0.0.1:32966", request) {
		t.Fatal("authority boundary lost")
	}
	for _, command := range []string{"", "nerocd runner; touch /tmp/escape", "$(id)"} {
		if _, err := readinessBrowserArgv(command, "https://127.0.0.1:1", "/tmp/owned"); err == nil {
			t.Fatal("arbitrary command accepted")
		}
	}
}

func TestServerReadinessEnrollmentBrowser(t *testing.T) {
	if os.Getenv("NEROCD_READINESS_BROWSER") != "disposable-dind" {
		t.Skip("requires actual browser gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Linux fixture required")
	}
	origin := os.Getenv("NEROCD_READINESS_BROWSER_ORIGIN")
	if origin != "http://127.0.0.1:32966" {
		t.Fatal("exact owned browser origin required")
	}
	base, pg, database := newHostingBuildAPIFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	user, err := base.app.BootstrapAdmin(ctx, app.BootstrapAdminInput{Email: "readiness-admin@example.local", Name: "Readiness administrator", Password: "readiness-browser-fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary seeded viewer and fixed recovery administrator are local test identities.
	password, err := auth.HashPassword("readiness-browser-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"usr_bootstrap", "usr_viewer"} {
		if _, err = database.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, password); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "readiness_browser_project_owner", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	api := NewServerWithConfig(base.app, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: origin, AllowInsecureCookies: true})
	var consumes atomic.Int32
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runner-enrollments/consume" {
			consumes.Add(1)
		}
		api.ServeHTTP(w, r)
	}))
	defer tlsServer.Close()
	root, err := os.MkdirTemp("/tmp", "nerocd-readiness-browser-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	for _, name := range []string{"work", "journal"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	stepPath := "/api/v1/fixture/readiness/" + hex.EncodeToString(nonce) + "/"
	type action struct {
		name       string
		token      string
		command    string
		enrollment string
		result     chan int
	}
	actions := make(chan action)
	var agent *readinessFixtureAgent
	defer func() {
		if agent != nil {
			agent.Stop(t)
		}
	}()
	var argv []string
	logs := &runtimeFixtureLog{}
	start := func(args []string) error {
		c := exec.CommandContext(ctx, "/fixture/nerocd", args...)
		c.Env = append(os.Environ(), "NEROCD_MODE=development", "NEROCD_TOKEN=", "SSL_CERT_FILE="+ca, "PATH=/fixture/readiness-bin:/usr/local/bin:/usr/bin:/bin")
		c.Stdout, c.Stderr = logs, logs
		if e := c.Start(); e != nil {
			return e
		}
		agent = &readinessFixtureAgent{command: c, done: make(chan error, 1)}
		t.Logf("owned browser agent pid=%d started", c.Process.Pid)
		go func(a *readinessFixtureAgent) { a.done <- a.command.Wait() }(agent)
		return nil
	}
	sessions := &readinessBrowserSession{}
	handler := api.authenticateBrowser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, e := base.app.CurrentPrincipal(r.Context())
		name := strings.TrimPrefix(r.URL.Path, stepPath)
		actor := user.ID
		if name == "complete" {
			actor = "usr_bootstrap"
		}
		if e != nil || !readinessBrowserControlAllowed(principal, actor, origin, r) || !sessions.allow(actor, r) {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		switch name {
		case "start", "stop", "restart", "unsafe-root", "safe-root", "revoke", "demote", "complete":
		default:
			http.Error(w, "unknown fixture action", 400)
			return
		}
		var input struct {
			Token      string `json:"token"`
			Command    string `json:"command"`
			Enrollment string `json:"enrollment"`
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
		defer clear(raw)
		if e != nil || len(raw) > 16384 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		var extra any
		if !errors.Is(decoder.Decode(&extra), io.EOF) {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if name != "start" && (input.Token != "" || input.Command != "" || input.Enrollment != "") {
			http.Error(w, "empty action required", 400)
			return
		}
		item := action{name: name, token: input.Token, command: input.Command, enrollment: input.Enrollment, result: make(chan int, 1)}
		select {
		case actions <- item:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		select {
		case code := <-item.result:
			w.WriteHeader(code)
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	listener, err := net.Listen("tcp4", "0.0.0.0:18080")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, stepPath) {
			handler.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = httpServer.Shutdown(cleanup)
		if e := <-serveDone; e != nil && !errors.Is(e, http.ErrServerClosed) {
			t.Error(e)
		}
	}()
	metadata, _ := json.Marshal(map[string]string{"base_url": origin, "runner_origin": tlsServer.URL, "step_path": stepPath, "runner_id": "runner_readiness_browser"})
	fmt.Printf("READINESS_BROWSER_READY %s\n", metadata)
	enrollmentToken := ""
	demoted := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("bounded browser fixture did not complete")
		case item := <-actions:
			code := 204
			switch item.name {
			case "start":
				if len(argv) != 0 || !strings.HasPrefix(item.token, "nce_") || len(item.token) > 256 || !strings.HasPrefix(item.enrollment, "enroll_") || len(item.enrollment) > 128 {
					code = 400
					break
				}
				argv, err = readinessBrowserArgv(item.command, tlsServer.URL, root)
				if err != nil {
					code = 400
					break
				}
				enrollmentToken = item.token
				if err = os.WriteFile(filepath.Join(root, "enrollment"), []byte(item.token), 0600); err == nil {
					err = start(argv)
				}
			case "stop":
				if agent == nil {
					code = 409
				} else {
					agent.Stop(t)
					agent = nil
				}
			case "restart":
				if agent != nil || len(argv) == 0 {
					code = 409
				} else {
					if _, e := os.Stat(filepath.Join(root, "enrollment")); !os.IsNotExist(e) {
						t.Error("initial enrollment file remained")
					}
					info, e := os.Stat(filepath.Join(root, "credential"))
					if e != nil || info.Mode().Perm() != 0600 {
						t.Error("initial credential mode", e)
					}
					if consumes.Load() != 1 {
						t.Error("initial enrollment consume count", consumes.Load())
					}
					err = start(readinessBrowserRestartArgv(argv))
				}
			case "unsafe-root":
				err = os.Chmod(filepath.Join(root, "work"), 0777)
			case "safe-root":
				err = os.Chmod(filepath.Join(root, "work"), 0700)
			case "revoke":
				recovery := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
				_, err = base.app.RevokeRunnerToken(recovery, app.RunnerTokenInput{RunnerID: "runner_readiness_browser"})
			case "demote":
				_, err = database.Exec(ctx, `UPDATE users SET global_role='user' WHERE id=$1`, user.ID)
				demoted = err == nil
			case "complete":
				if !demoted {
					code = 409
					break
				}
				item.result <- 204
				if agent != nil {
					agent.Stop(t)
					agent = nil
				}
				info, e := os.Stat(filepath.Join(root, "credential"))
				if e != nil || info.Mode().Perm() != 0600 {
					t.Error("credential mode", e)
				}
				if _, e = os.Stat(filepath.Join(root, "enrollment")); !os.IsNotExist(e) {
					t.Error("enrollment not consumed")
				}
				enrolled, e := pg.GetRunnerByID(ctx, "runner_readiness_browser")
				if e != nil || strings.Join(enrolled.Capabilities, ",") != readinessBrowserCaps {
					t.Error("persisted capability parity", e)
				}
				if consumes.Load() != 1 {
					t.Error("restart consumed enrollment again", consumes.Load())
				}
				if _, e := os.Stat("/fixture/readiness-cli-executed"); !os.IsNotExist(e) {
					t.Error("metadata fixture executed CLI")
				}
				credentialBytes, e := os.ReadFile(filepath.Join(root, "credential"))
				if e != nil {
					t.Error(e)
				} else {
					if logs.contains(string(credentialBytes)) {
						t.Error("persisted credential logged")
					}
					clear(credentialBytes)
				}
				if logs.contains(enrollmentToken) || logs.contains("ncr_") {
					t.Error("credential disclosed in agent logs")
				}
				t.Log("actual UI enrollment consumed by compiled TLS agent; role loss, persisted capabilities and credential0600 checked")
				return
			}
			if err != nil {
				t.Error("fixture action failed", item.name, err)
				err = nil
				code = 500
			}
			item.result <- code
		}
	}
}
