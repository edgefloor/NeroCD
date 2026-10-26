//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/artifactfs"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/notificationtransport"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

type hostingStorageRemoteRuntimeFixture struct {
	operatorMutations atomic.Int64
	retentionWire     atomic.Pointer[hostingStorageRetentionRuntimeWire]
	pg                *store.PostgresStore
	notifications     *backupNotificationReceivers
	scheduleACK       atomic.Bool
	stopJobs          func(context.Context) error
	uploadChecks      atomic.Int64
	uploadViolations  atomic.Int64
	*storageRuntimeFixture
	wire    atomic.Pointer[hostingStorageRemoteRuntimeWire]
	browser *hostingStorageRemoteRuntimeBrowser
}

func newHostingStorageRemoteRuntimeFixture(t *testing.T) *hostingStorageRemoteRuntimeFixture {
	t.Helper()
	if os.Getenv("NEROCD_STORAGE_RUNTIME") != "disposable-dind" {
		t.Skip("requires owned storage runtime gate")
	}
	root, err := os.MkdirTemp("/var/lib", storageRuntimeOwner+"-")
	if err != nil {
		t.Fatal(err)
	}
	var f *storageRuntimeFixture
	t.Cleanup(func() {
		if f != nil && f.preserveRoot {
			t.Log("fixture root preserved after uncertain proxy cleanup")
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	roots, proxyRoots := storageRuntimeTrust(t, root)
	_, pg, database := newHostingBuildAPIFixture(t)
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("fixture key generation")
	}
	ring, err := secretcrypto.New("storage-remote-fixture", map[string][]byte{"storage-remote-fixture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("fixture keyring")
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal("fixture service")
	}
	origin := os.Getenv("NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN")
	parsedOrigin, parseErr := url.Parse(origin)
	if parseErr != nil || parsedOrigin.Scheme != "https" || parsedOrigin.Hostname() != "127.0.0.1" || parsedOrigin.Port() == "" || parsedOrigin.Path != "" {
		t.Fatal("exact loopback browser origin required")
	}
	if _, err := service.BootstrapAdmin(t.Context(), app.BootstrapAdminInput{Email: "storage-bootstrap@example.local", Name: "Storage bootstrap", Password: hostingStorageRemoteRuntimeRandom(t)}); err != nil {
		t.Fatal("fixture browser bootstrap")
	}
	server := NewServerWithConfig(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: origin})
	f = &storageRuntimeFixture{t: t, root: root, roots: roots, logs: &runtimeFixtureLog{}}
	keyString := os.Getenv("NEROCD_STORAGE_REMOTE_BROWSER_KEY")
	if len(keyString) != 64 {
		t.Fatal("private browser handshake key required")
	}
	remote := &hostingStorageRemoteRuntimeFixture{pg: pg, storageRuntimeFixture: f, browser: &hostingStorageRemoteRuntimeBrowser{key: keyString, phase: "starting", reports: make(chan hostingStorageRemoteRuntimeReport, 8)}}
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if remote.browser.serve(w, r) {
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/storage-backup-schedules") && remote.scheduleACK.CompareAndSwap(true, false) {
			captured := httptest.NewRecorder()
			server.ServeHTTP(captured, r)
			if captured.Code != 200 {
				t.Errorf("schedule ACK fault did not commit: status=%d code=%s", captured.Code, storageScheduleResponseCode(captured.Body.Bytes()))
			}
			checks := make(chan error, 2)
			for range 2 {
				go func() { checks <- remote.pg.ReconcileStorageBackupSchedules(r.Context()) }()
			}
			for range 2 {
				if err := <-checks; err != nil {
					t.Error("concurrent scheduled discovery", err)
				}
			}
			http.Error(w, "fixture lost schedule response", 503)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && (strings.HasPrefix(r.URL.Path, "/api/v1/resources/") || strings.HasPrefix(r.URL.Path, "/api/v1/storage-backups/") || strings.HasPrefix(r.URL.Path, "/api/v1/hosting-operations/")) {
			remote.operatorMutations.Add(1)
		}
		if wire := remote.retentionWire.Load(); wire != nil && wire.serve(w, r, server) {
			return
		}
		if wire := remote.wire.Load(); wire != nil && wire.serve(w, r, server) {
			return
		}
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			f.completions.Add(1)
		}
		if r.URL.Path == "/api/v1/runners/hosting/storage-archive/complete" {
			f.archiveCompletions.Add(1)
		}
		fault := f.fault.Load()
		if fault == nil || r.URL.Path != fault.path || !f.fault.CompareAndSwap(fault, nil) {
			server.ServeHTTP(w, r)
			return
		}
		defer close(fault.finished)
		receipt := httptest.NewRecorder()
		server.ServeHTTP(receipt, r)
		fault.response = bytes.Clone(receipt.Body.Bytes())
		if receipt.Code != 200 {
			t.Errorf("fault request did not commit: path=%s status=%d body=%s", fault.path, receipt.Code, fault.response)
		}
		close(fault.entered)
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			t.Error("response-loss process did not cancel request")
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:18190")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	control.Listener = listener
	control.StartTLS()
	control.URL = "https://127.0.0.1:18190"
	f.control, f.client = control, control.Client()
	f.client.Timeout = 10 * time.Second
	t.Cleanup(control.Close)
	if err := os.WriteFile(filepath.Join(root, "api.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proxy-roots.pem"), proxyRoots, 0600); err != nil {
		t.Fatal(err)
	}
	login := storageRuntimeAuthorizedJSON[struct {
		Token string `json:"token"`
	}](f, "", http.MethodPost, "/api/v1/sessions", map[string]string{"email": "admin@example.local", "password": "admin"}, 201)
	f.token = login.Token
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingOwnedStorageCapability}
	{
		storageRuntimeConfigureArchive(t, f, pg)
		caps = append(caps, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability, "storage_backup_transfer", "hosting_storage_remote_backup_v1")
		raw, err := os.ReadFile(f.archiveConfig)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if json.Unmarshal(raw, &cfg) != nil {
			t.Fatal("archive configuration")
		}
		if os.Getenv("NEROCD_STORAGE_RETENTION_RUNTIME") == "disposable-dind" {
			cfg["backup_retention"] = true
			caps = append(caps, domain.RunTypeStorageBackupRetention, domain.HostingStorageBackupRetentionCapability)
		}
		cfg["remote_backups"] = true
		cfg["remote_fixture_origin"] = "http://127.0.0.1:9001"
		raw, err = json.Marshal(cfg)
		if err != nil || os.WriteFile(f.archiveConfig, raw, 0600) != nil {
			t.Fatal("remote archive configuration")
		}
	}
	f.capabilities = append([]string(nil), caps...)
	enrollment := storageRuntimeJSON[app.CreatedRunnerEnrollment](f, http.MethodPost, "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_storage_runtime_fixture", RunnerName: "owned storage fixture", Capabilities: caps}, 201)
	credential := "ncr_storage_runtime_fixture_only"
	digest := sha256.Sum256([]byte(credential))
	enrolled := storageRuntimeAuthorizedJSON[app.ConsumedRunnerEnrollment](f, enrollment.Token, http.MethodPost, "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("e", 32), CredentialHash: hex.EncodeToString(digest[:])}, 200)
	if err := os.WriteFile(filepath.Join(root, "credential"), []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	target := storageRuntimeJSON[app.ServerDetail](f, http.MethodPost, "/api/v1/servers", app.ServerInput{Name: "storage runtime fixture", RunnerID: enrolled.Runner.ID}, 201)
	f.serverID, f.destinationID = target.Server.ID, target.Destination.ID
	storageRuntimeJSON[json.RawMessage](f, http.MethodPost, "/api/v1/destinations/"+f.destinationID+"/project-grants", app.DestinationProjectInput{ProjectID: "proj_platform"}, 201)
	environment := storageRuntimeJSON[domain.ProjectEnvironment](f, http.MethodPost, "/api/v1/project-environments", app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage runtime fixture"}, 201)
	f.environmentID = environment.ID
	// Only queue infrastructure is initialized directly. Every hosting authority
	// and desired input below is obtained through an authenticated public API.
	for {
		tx, err := database.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := storageRuntimeRollback(tx); err != nil {
				t.Errorf("queue migration rollback: %v", err)
			}
		})
		changed, err := hostingjobs.MigrateNextTx(t.Context(), tx)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	remote.notifications = startBackupNotificationReceivers(t)
	sender, notificationErr := notificationtransport.New(notificationtransport.Policy{FixtureTLSOrigins: []string{remote.notifications.webhookOrigin, remote.notifications.smtpOrigin}, RootCAs: remote.notifications.roots})
	if notificationErr != nil {
		t.Fatal("storage notification sender")
	}
	if err = pg.ConfigureBackupNotifications(ring, sender, origin); err != nil {
		t.Fatal("storage notification configuration")
	}
	stopJobs, err := pg.StartHostingExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	remote.stopJobs = stopJobs
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := remote.stopJobs(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { f.stop(); t.Logf("compiled runner logs:\n%s", f.logs.String()) })
	f.start(caps)
	return remote
}

type hostingStorageRemoteRuntimeSilo struct {
	t                                 *testing.T
	id, owner, volume, access, secret string
}

func hostingStorageRemoteRuntimeRandom(t *testing.T) string {
	t.Helper()
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal("fixture random")
	}
	return hex.EncodeToString(b[:])
}
func (s *hostingStorageRemoteRuntimeSilo) command(script string) []byte {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "exec", "--interactive", s.id, "sh", "-s")
	cmd.Stdin = strings.NewReader("set -eu\n" + script)
	raw, err := cmd.Output()
	if err != nil {
		s.t.Fatal("owned Silo administration failed")
	}
	return raw
}
func hostingStorageRemoteRuntimeStartSilo(f *hostingStorageRemoteRuntimeFixture) *hostingStorageRemoteRuntimeSilo {
	t := f.t
	t.Helper()
	s := &hostingStorageRemoteRuntimeSilo{t: t, owner: hostingStorageRemoteRuntimeRandom(t), access: hostingStorageRemoteRuntimeRandom(t)[:20], secret: hostingStorageRemoteRuntimeRandom(t)}
	s.volume = "nerocd-storage-remote-silo-" + s.owner
	var image []struct{ ID, Architecture, OS string }
	if json.Unmarshal(storageRuntimeDocker(t, "image", "inspect", databaseRemoteSiloImage), &image) != nil || len(image) != 1 || image[0].Architecture != "arm64" || image[0].OS != "linux" {
		t.Fatal("pinned native Silo required")
	}
	storageRuntimeDocker(t, "volume", "create", "--label", "io.nerocd.storage-remote.owner="+s.owner, s.volume)
	t.Cleanup(func() {
		if s.id != "" {
			var owned []struct {
				ID     string `json:"Id"`
				Image  string
				Config struct{ Labels map[string]string }
				Mounts []struct{ Type, Name, Destination string }
			}
			if json.Unmarshal(storageRuntimeDocker(t, "inspect", s.id), &owned) != nil || len(owned) != 1 || owned[0].ID != s.id || owned[0].Image != image[0].ID || owned[0].Config.Labels["io.nerocd.storage-remote.owner"] != s.owner || len(owned[0].Mounts) != 1 || owned[0].Mounts[0].Type != "volume" || owned[0].Mounts[0].Name != s.volume || owned[0].Mounts[0].Destination != "/data" {
				t.Error("Silo ownership uncertain; preserving")
				return
			}
			storageRuntimeDocker(t, "rm", "--force", s.id)
		}
		var v []struct {
			Name   string
			Labels map[string]string
		}
		if json.Unmarshal(storageRuntimeDocker(t, "volume", "inspect", s.volume), &v) != nil || len(v) != 1 || v[0].Name != s.volume || len(v[0].Labels) != 1 || v[0].Labels["io.nerocd.storage-remote.owner"] != s.owner || strings.TrimSpace(string(storageRuntimeDocker(t, "ps", "-aq", "--filter", "volume="+s.volume))) != "" {
			t.Error("Silo volume ownership/users uncertain; preserving")
			return
		}
		storageRuntimeDocker(t, "volume", "rm", s.volume)
	})
	env := []byte("MINIO_ROOT_USER=" + hostingStorageRemoteRuntimeRandom(t)[:20] + "\nMINIO_ROOT_PASSWORD=" + hostingStorageRemoteRuntimeRandom(t) + "\nFIXTURE_ACCESS=" + s.access + "\nFIXTURE_SECRET=" + s.secret + "\nMINIO_BROWSER=off\n")
	path := filepath.Join(f.root, "storage-remote-silo.env")
	if os.WriteFile(path, env, 0600) != nil {
		t.Fatal("private Silo environment")
	}
	clear(env)
	s.id = strings.TrimSpace(string(storageRuntimeDocker(t, "run", "--detach", "--name", s.volume, "--label", "io.nerocd.storage-remote.owner="+s.owner, "--network", "host", "--cpus", "1", "--memory", "512m", "--pids-limit", "128", "--mount", "type=volume,src="+s.volume+",dst=/data", "--env-file", path, databaseRemoteSiloImage, "server", "/data", "--address", "127.0.0.1:9000")))
	if len(s.id) != 64 || strings.Trim(s.id, "0123456789abcdef") != "" {
		t.Fatal("Silo identity")
	}
	s.command(`for n in $(seq 1 30); do
 if mc alias set fixture http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 && mc ready fixture >/dev/null 2>&1; then break; fi
 sleep 1
done
mc ready fixture >/dev/null
mc mb fixture/nerocd-storage-runtime >/dev/null
mc admin user add fixture "$FIXTURE_ACCESS" "$FIXTURE_SECRET" >/dev/null
`)
	s.policy(false, true)
	return s
}
func (s *hostingStorageRemoteRuntimeSilo) policy(put, attach bool) {
	actions := []string{"s3:GetObject"}
	if os.Getenv("NEROCD_STORAGE_RETENTION_RUNTIME") == "disposable-dind" {
		actions = append(actions, "s3:DeleteObject", "s3:DeleteObjectVersion")
	}
	if put {
		actions = append(actions, "s3:PutObject")
	}
	policy := map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"s3:ListBucket"}, "Resource": []string{"arn:aws:s3:::nerocd-storage-runtime"}}, map[string]any{"Effect": "Allow", "Action": actions, "Resource": []string{"arn:aws:s3:::nerocd-storage-runtime/nerocd/*"}}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		s.t.Fatal(err)
	}
	script := "cat > /tmp/storage-policy.json <<'POLICY'\n" + string(raw) + "\nPOLICY\nmc admin policy create fixture fixture-storage /tmp/storage-policy.json >/dev/null\n"
	if attach {
		script += "mc admin policy attach fixture fixture-storage --user \"$FIXTURE_ACCESS\" >/dev/null\n"
	}
	s.command(script)
}
func hostingStorageRemoteRuntimeProxy(t *testing.T, o *hostingStorageRemoteRuntimeObservation, guard func() bool) {
	t.Helper()
	backend, err := url.Parse("http://127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.ModifyResponse = o.modify
	proxy.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
	listener, err := net.Listen("tcp", "127.0.0.1:9001")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.observe(r)
		if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/nerocd-storage-runtime/nerocd/storage/") && !guard() {
			http.Error(w, "writer resume unverified", 409)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
}

func hostingStorageRemoteRuntimeAwait(f *hostingStorageRemoteRuntimeFixture, backup, mode, state string) hostingStorageRemoteRuntimeTransfer {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			f.t.Fatalf("storage transfer did not reach %s/%s", mode, state)
		case <-tick.C:
			values := storageRuntimeJSON[[]hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup+"/transfers", nil, 200)
			for _, v := range values {
				if v.Mode == mode && v.State == state {
					return v
				}
			}
		}
	}
}
func hostingStorageRemoteRuntimeRoute(f *hostingStorageRemoteRuntimeFixture, resource, host, operation string) {
	storageRuntimeJSON[domain.HostingHostnameGrant](f.storageRuntimeFixture, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: host}, 201)
	route := storageRuntimeJSON[domain.HostingRoute](f.storageRuntimeFixture, "POST", "/api/v1/resources/"+resource+"/routes", domain.HostingRouteInput{IdempotencyKey: "route-" + resource, Hostname: host, PathPrefix: "/", ExpectedHealthyOperationID: operation}, 202)
	f.proxyAwait(route.OperationID)
}
func hostingStorageRemoteRuntimeProxySetup(f *hostingStorageRemoteRuntimeFixture, daemon string) {
	f.stop()
	proxy := storageRuntimeJSON[domain.HostingProxy](f.storageRuntimeFixture, "POST", "/api/v1/servers/"+f.serverID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "remote-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	certDir := filepath.Join(f.root, "proxy", proxy.ID, "certificates")
	if os.MkdirAll(certDir, 0700) != nil {
		f.t.Fatal("proxy cert directory")
	}
	cert, err := os.ReadFile(filepath.Join(f.root, "minica.pem"))
	if err != nil || os.WriteFile(filepath.Join(certDir, "root.pem"), cert, 0600) != nil {
		f.t.Fatal("proxy CA")
	}
	f.start()
	f.awaitLoadedProxy(proxy.ID, daemon)
}

func TestHostingStorageRemoteCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_STORAGE_REMOTE_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact committed candidate and owned remote runtime GO")
	}
	f := newHostingStorageRemoteRuntimeFixture(t)
	silo := hostingStorageRemoteRuntimeStartSilo(f)
	observation := &hostingStorageRemoteRuntimeObservation{}
	resource := f.resource("remote uploaded application A", "a")
	source := f.storage(resource, strings.Repeat("1", 32))
	a := f.await(f.deploy(resource, "remote-storage-a", "/", storageRemoteRuntimeApplicationPort, nil).ID, "succeeded")
	resource = storageRuntimeJSON[domain.Resource](f.storageRuntimeFixture, "PATCH", "/api/v1/resources/"+resource.ID, map[string]any{"expected_generation": resource.Generation, "name": "remote uploaded application B", "source": map[string]any{"kind": "image", "image": domain.ImageSource{Reference: storageRuntimeImage + "b"}}}, 200)
	b := f.await(f.deploy(resource, "remote-storage-b", "/", storageRemoteRuntimeApplicationPort, nil).ID, "succeeded")
	if a.Result == nil || b.Result == nil || b.Release == nil || storageArchiveWriterRunning(t, a.Result.ContainerID) || !storageArchiveWriterRunning(t, b.Result.ContainerID) {
		t.Fatal("exact initial stopped/running writer inventory")
	}
	hostingStorageRemoteRuntimeProxy(t, observation, func() bool {
		f.uploadChecks.Add(1)
		valid := hostingStorageRemoteRuntimeWriterState(a.Result.ContainerID, false) && hostingStorageRemoteRuntimeWriterState(b.Result.ContainerID, true)
		if !valid {
			f.uploadViolations.Add(1)
		}
		return valid
	})
	hostingStorageRemoteRuntimeProxySetup(f, b.Release.DaemonID)
	hostingStorageRemoteRuntimeRoute(f, resource.ID, "storage-remote.example.test", b.ID)
	upload := make([]byte, 65537)
	if _, err := rand.Read(upload); err != nil {
		t.Fatal(err)
	}
	f.https("storage-remote.example.test", "PUT", "/upload", upload, 201)
	f.https("storage-remote.example.test", "POST", "/rename", nil, 204)
	f.https("storage-remote.example.test", "POST", "/metadata", nil, 204)
	metadata := storageArchiveFileEvidence(t, source.VolumeName)
	if !strings.Contains(metadata, "saved:12345:12346:0640:") || !strings.Contains(metadata, "nested:12345:12346:0750:") {
		t.Fatal("nontrivial source permissions missing")
	}
	f.browser.targetSetup(resource.ID, silo.access, silo.secret)
	targetID := f.browser.await(t, "target")
	targets := storageRuntimeJSON[[]struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}](f.storageRuntimeFixture, "GET", "/api/v1/projects/proj_platform/database-backup-targets", nil, 200)
	var target struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	for _, value := range targets {
		if value.ID == targetID {
			target = value
		}
	}
	if target.ID == "" || target.Revision != 1 {
		t.Fatal("browser target creation did not persist exact new target")
	}
	f.browser.set("validation", resource.ID, target.ID, "")
	validationID := f.browser.await(t, "validation")
	hostingStorageRemoteRuntimeAwaitID(f, validationID, "succeeded")
	// Stop and join the complete controller/consumer lifetime, then restart it
	// before owner UI admission. No overlapping scheduler lifetime is retained.
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 10*time.Second)
	if err := f.stopJobs(stopCtx); err != nil {
		stopCancel()
		t.Fatal("storage scheduler join", err)
	}
	stopCancel()
	var restartErr error
	f.stopJobs, restartErr = f.pg.StartHostingExecution(t.Context())
	if restartErr != nil {
		t.Fatal("storage scheduler restart", restartErr)
	}
	hostingStorageRemoteRuntimeChannels(f)
	f.scheduleACK.Store(true)
	since := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	f.browser.set("backup", resource.ID, target.ID, "")
	backupID := f.browser.await(t, "backup")
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	local := f.await(backup.OperationID, "succeeded")
	failed := hostingStorageRemoteRuntimeAwait(f, backup.ID, "upload", "failed")
	uploadRecoveryURL := hostingStorageRemoteRuntimeAlerts(f, resource, backup.ID, failed.ID, "storage_transfer", "upload_failed", true)
	backup = storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup.ID, nil, 200)
	if local.StorageArchive == nil || local.StorageArchive.Result == nil || local.StorageArchive.Result.ResumeOutcome != "resumed" || backup.Blob == nil || failed.Blob == nil || *failed.Blob != *backup.Blob || failed.FailureCode != "access_denied" || failed.TargetID != target.ID || failed.TargetRevision != target.Revision {
		t.Fatal("denied PUT must retain exact successful local archive and resumed writer")
	}
	phases := map[string]bool{}
	for _, p := range local.StorageArchive.Progress {
		phases[p.Phase] = true
	}
	if !phases["stopped"] || !phases["resumed"] {
		t.Fatal("local writer quiescence/resume absent")
	}
	if storageArchiveWriterRunning(t, a.Result.ContainerID) || !storageArchiveWriterRunning(t, b.Result.ContainerID) {
		t.Fatal("remote denial changed writer inventory")
	}
	if !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), upload) {
		t.Fatal("source unreadable after failed PUT")
	}
	archive := hostingStorageRemoteRuntimeArchive(f, *backup.Blob)
	evidence := os.Getenv("NEROCD_STORAGE_REMOTE_EVIDENCE")
	if evidence != "/fixture/evidence" {
		t.Fatal("exact durable evidence export directory required")
	}
	if err := os.WriteFile(filepath.Join(evidence, "owned-upload.tar"), archive, 0600); err != nil {
		t.Fatal("durable fixture archive export")
	}
	observation.set("/nerocd-storage-runtime/nerocd/storage/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.SHA256, "sha256:")+".tar", "")
	// Reload reads use the same public APIs after an actual runner restart.
	f.stop()
	f.start()
	survived := storageRuntimeJSON[domain.HostingStorageBackup](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup.ID, nil, 200)
	if survived.Blob == nil || *survived.Blob != *backup.Blob || !bytes.Equal(hostingStorageRemoteRuntimeArchive(f, *backup.Blob), archive) {
		t.Fatal("restart changed retained local archive")
	}
	beforeRecoveryMutations := f.operatorMutations.Load()
	f.browser.setRecovery("denied", resource.ID, target.ID, backup.ID, f.environmentID, failed.ID, uploadRecoveryURL)
	if f.browser.await(t, "warning") != backup.ID {
		t.Fatal("browser reloaded wrong backup")
	}
	if f.operatorMutations.Load() != beforeRecoveryMutations {
		t.Fatal("opening delivered upload recovery link caused an operator mutation")
	}
	silo.policy(true, false)
	wire := &hostingStorageRemoteRuntimeWire{backup: backup.ID, entered: make(chan struct{}), release: make(chan struct{})}
	f.wire.Store(wire)
	f.browser.set("retry", resource.ID, target.ID, backup.ID)
	retryID := f.browser.await(t, "retry")
	retry := storageRuntimeJSON[hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "GET", "/api/v1/storage-backup-transfers/"+retryID, nil, 200)
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("real successful upload receipt loss not reached")
	}
	f.stop()
	close(wire.release)
	f.start()
	deadline := time.Now().Add(30 * time.Second)
	for {
		wire.mu.Lock()
		replayed := wire.replays > 0
		wire.mu.Unlock()
		if replayed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("byte-exact committed transfer receipt not replayed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	uploaded := hostingStorageRemoteRuntimeAwaitID(f, retry.ID, "succeeded")
	f.wire.Store(nil)
	if uploaded.Blob == nil || *uploaded.Blob != *backup.Blob || storageArchiveHelperStarts(t, backup.OperationID, since) != 1 {
		t.Fatal("upload retry rearchived or changed archive identity")
	}
	_, puts := observation.counts()
	if f.uploadChecks.Load() < 2 || f.uploadViolations.Load() != 0 {
		t.Fatal("remote PUT preceded verified exact writer resume")
	}
	if puts != 1 {
		t.Fatalf("response loss repeated remote PUT: %d", puts)
	}
	// The CAS remains present throughout both bad downloads. Success may not be
	// reported from those cached bytes, and extraction may not start.
	var retryable hostingStorageRemoteRuntimeTransfer
	var heldRestore domain.HostingStorageRestore
	for _, fault := range []string{"corrupt", "truncated"} {
		observation.set("/nerocd-storage-runtime/nerocd/storage/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.SHA256, "sha256:")+".tar", fault)
		before, _ := observation.counts()
		restored := hostingStorageRemoteRuntimeRestore(f, backup.ID, uploaded, target.Revision, "blocked-"+fault)
		failedRestore := f.await(restored.Operation.ID, "failed")
		download := hostingStorageRemoteRuntimeAwaitDownload(f, backup.ID, restored.Operation.ID, "failed")
		if failedRestore.RunID != "" || !failedRestore.RestoreOwnershipHeld || download.FailureCode != "object_mismatch" {
			t.Fatal("failed remote download must retain retryable new-volume hold without extraction run")
		}
		for _, name := range strings.Fields(string(storageRuntimeDocker(t, "volume", "ls", "--format", "{{.Name}}"))) {
			if name == restored.Storage.VolumeName {
				t.Fatal("failed download created target volume")
			}
		}
		retryable, heldRestore = download, restored
		after, _ := observation.counts()
		if after <= before || storageArchiveHelperStarts(t, restored.Operation.ID, since) != 0 {
			t.Fatal("bad remote object bypassed GET or reached extraction")
		}
		if !bytes.Equal(hostingStorageRemoteRuntimeArchive(f, *backup.Blob), archive) {
			t.Fatal("bad GET corrupted retained CAS")
		}
	}
	observation.set("/nerocd-storage-runtime/nerocd/storage/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.SHA256, "sha256:")+".tar", "")
	retryRequest := storageRemoteRuntimeRetryRequest(t, retryable.ID)
	recovered := storageRuntimeJSON[hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "POST", "/api/v1/storage-backups/"+backup.ID+"/transfers/retry", retryRequest, 202)
	sameRecovered := storageRuntimeJSON[hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "POST", "/api/v1/storage-backups/"+backup.ID+"/transfers/retry", retryRequest, 202)
	if recovered.ID != sameRecovered.ID || recovered.RestoreOperationID != heldRestore.Operation.ID {
		t.Fatal("download retry did not preserve held restore or deduplicate")
	}
	hostingStorageRemoteRuntimeAwaitID(f, recovered.ID, "succeeded")
	f.await(heldRestore.Operation.ID, "succeeded")
	if storageArchiveHelperStarts(t, heldRestore.Operation.ID, since) != 1 {
		t.Fatal("download retry did not extract exactly once")
	}
	before, _ := observation.counts()
	f.browser.set("restore", resource.ID, target.ID, backup.ID)
	restoredID := f.browser.await(t, "restore")
	restored := storageRuntimeJSON[domain.HostingStorageRestore](f.storageRuntimeFixture, "GET", "/api/v1/storage-restores/"+restoredID, nil, 200)
	f.await(restored.Operation.ID, "succeeded")
	after, _ := observation.counts()
	if after <= before || restored.Storage.ID == source.ID || restored.Storage.VolumeName == source.VolumeName || restored.Resource.ID == resource.ID || restored.Storage.DestinationID != source.DestinationID || storageArchiveFileEvidence(t, restored.Storage.VolumeName) != metadata {
		t.Fatal("fresh remote restore GET/identity/permissions contract")
	}
	deployed := f.await(f.deploy(restored.Resource, "remote-restored-deploy", "/", storageRemoteRuntimeRestorePort, nil).ID, "succeeded")
	hostingStorageRemoteRuntimeRoute(f, restored.Resource.ID, "storage-remote-restored.example.test", deployed.ID)
	if !bytes.Equal(f.https("storage-remote-restored.example.test", "GET", "/download", nil, 200), upload) {
		t.Fatal("new application did not read restored bytes")
	}
	replacement := make([]byte, 65539)
	if _, err := rand.Read(replacement); err != nil {
		t.Fatal(err)
	}
	f.https("storage-remote-restored.example.test", "PUT", "/upload", replacement, 201)
	f.https("storage-remote-restored.example.test", "POST", "/rename", nil, 204)
	if !bytes.Equal(f.https("storage-remote-restored.example.test", "GET", "/download", nil, 200), replacement) || !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), upload) || storageArchiveFileEvidence(t, source.VolumeName) != metadata {
		t.Fatal("new-volume writes changed source or failed read/write")
	}
	hostingStorageRemoteRuntimeAuthorization(f, resource, backup, failed, target.ID, target.Revision)
	// The failed archive is also admitted through the durable schedule path.
	f.https("storage-remote.example.test", "POST", "/oversize", nil, 201)
	failedSchedule := storageRuntimeJSON[domain.HostingStorageBackupSchedule](f.storageRuntimeFixture, "POST", "/api/v1/resources/"+resource.ID+"/storage-backup-schedules", domain.HostingStorageBackupScheduleInput{RequestID: hostingStorageScheduleRuntimeID(t), ExpectedResourceGeneration: resource.Generation, Frequency: fmt.Sprintf("%d %d * * *", time.Now().UTC().Minute(), time.Now().UTC().Hour()), Timezone: "UTC", Enabled: true}, 200)
	failedOccurrence := hostingStorageRemoteRuntimeOccurrence(f, resource.ID, failedSchedule.ID)
	failedArchive := f.await(failedOccurrence.OperationID, "failed")
	if failedArchive.StorageArchive == nil || failedArchive.StorageArchive.Result == nil || failedArchive.StorageArchive.Result.ResumeOutcome != "resumed" || failedArchive.MutationHold {
		t.Fatal("scheduled archive failure did not compensate writers")
	}
	storageRuntimeJSON[domain.HostingStorageBackupSchedule](f.storageRuntimeFixture, "PUT", "/api/v1/resources/"+resource.ID+"/storage-backup-schedules/"+failedSchedule.ID, domain.HostingStorageBackupScheduleInput{RequestID: hostingStorageScheduleRuntimeID(t), ExpectedResourceGeneration: resource.Generation, ExpectedVersion: failedSchedule.Revision, Frequency: failedSchedule.Frequency, Timezone: failedSchedule.Timezone, Enabled: false}, 200)
	archiveRecoveryURL := hostingStorageRemoteRuntimeAlerts(f, resource, failedOccurrence.BackupID, failedOccurrence.OperationID, "storage_backup", "backup_failed", false)
	beforeArchiveRecoveryMutations := f.operatorMutations.Load()
	f.browser.setRecovery("archive_alert", resource.ID, target.ID, failedOccurrence.BackupID, f.environmentID, failedOccurrence.OperationID, archiveRecoveryURL)
	if f.browser.await(t, "archive_warning") != failedOccurrence.BackupID {
		t.Fatal("browser selected wrong failed archive from delivered link")
	}
	if f.operatorMutations.Load() != beforeArchiveRecoveryMutations {
		t.Fatal("opening delivered archive recovery link caused an operator mutation")
	}
	f.https("storage-remote.example.test", "DELETE", "/oversize", nil, 204)
	if !bytes.Equal(f.https("storage-remote.example.test", "GET", "/download", nil, 200), upload) {
		t.Fatal("scheduled archive failure altered source")
	}
	if os.Getenv("NEROCD_STORAGE_RETENTION_RUNTIME") == "disposable-dind" {
		hostingStorageRetentionRuntimeWorkflow(f, silo, observation, restored.Resource, target.ID, target.Revision, replacement, upload)
	}
	f.browser.set("complete", resource.ID, target.ID, backup.ID)
	f.browser.await(t, "done")
	markers := map[string]any{"backup_id": backup.ID, "upload_transfer_id": uploaded.ID, "restore_operation_id": restored.Operation.ID, "source_volume": source.VolumeName, "restored_volume": restored.Storage.VolumeName, "archive_sha256": backup.Blob.SHA256, "archive_bytes": len(archive), "source_upload_sha256": fmt.Sprintf("%x", sha256.Sum256(upload)), "new_upload_sha256": fmt.Sprintf("%x", sha256.Sum256(replacement)), "source_metadata": metadata, "object_gets": after, "first_scheduled_archive_upload_puts": puts, "receipt_replayed": true, "put_resume_checks": f.uploadChecks.Load(), "put_resume_violations": f.uploadViolations.Load(), "first_scheduled_archive_helper_starts": 1, "source_preserved": true, "scheduled_archive": true, "scheduled_backup_failure_alerts": true, "scheduled_upload_failure_alerts": true, "scheduler_join_restart": true, "delivered_upload_recovery_link_verified": true, "delivered_archive_recovery_link_verified": true, "delivered_upload_recovery_url_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(uploadRecoveryURL))), "delivered_archive_recovery_url_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(archiveRecoveryURL)))}
	raw, err := json.MarshalIndent(markers, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(evidence, "remote-markers.json"), append(raw, '\n'), 0600) != nil {
		t.Fatal("safe marker export")
	}
	for _, secret := range []string{silo.access, silo.secret} {
		if strings.Contains(f.logs.String(), secret) || bytes.Contains(raw, []byte(secret)) {
			t.Fatal("fixture evidence contains protected credential")
		}
	}
	t.Logf("remote owned-upload workflow backup=%s transfer=%s restored=%s GET=%d first_scheduled_archive_PUT=%d first_scheduled_archive_helper_starts=1 source_preserved=true", backup.ID, uploaded.ID, restored.Operation.ID, after, puts)
}

func hostingStorageRemoteRuntimeAwaitID(f *hostingStorageRemoteRuntimeFixture, id, state string) hostingStorageRemoteRuntimeTransfer {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			f.t.Fatal("storage transfer deadline")
		case <-tick.C:
			v := storageRuntimeJSON[hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "GET", "/api/v1/storage-backup-transfers/"+id, nil, 200)
			if v.ID != id {
				f.t.Fatal("transfer identity changed")
			}
			if v.State == state {
				return v
			}
			if v.State == "failed" || v.State == "uncertain" || v.State == "succeeded" {
				f.t.Fatalf("transfer settled %s, expected %s, code=%s", v.State, state, v.FailureCode)
			}
		}
	}
}
func hostingStorageRemoteRuntimeArchive(f *hostingStorageRemoteRuntimeFixture, blob domain.HostingStorageArchiveBlob) []byte {
	f.t.Helper()
	if blob.Validate() != nil {
		f.t.Fatal("invalid archive metadata")
	}
	store, err := artifactfs.OpenStore(filepath.Join(f.root, "archive-artifacts"), domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		f.t.Fatal("archive CAS unavailable")
	}
	defer func() {
		if store.Close() != nil {
			f.t.Error("archive CAS close")
		}
	}()
	handle, metadata, err := store.Open(f.t.Context(), strings.TrimPrefix(blob.SHA256, "sha256:"))
	if err != nil {
		f.t.Fatal("retained local archive unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(handle, blob.SizeBytes+1))
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) != blob.SizeBytes || metadata.Size != blob.SizeBytes || "sha256:"+fmt.Sprintf("%x", sha256.Sum256(raw)) != blob.SHA256 {
		f.t.Fatal("local archive size/digest changed")
	}
	return raw
}
func hostingStorageRemoteRuntimeRestore(f *hostingStorageRemoteRuntimeFixture, backup string, uploaded hostingStorageRemoteRuntimeTransfer, revision int64, name string) domain.HostingStorageRestore {
	return storageRuntimeJSON[domain.HostingStorageRestore](f.storageRuntimeFixture, "POST", "/api/v1/storage-backups/"+backup+"/restores", storageRemoteRuntimeRestoreRequest(f.t, uploaded.ID, revision, name), 202)
}
func hostingStorageRemoteRuntimeAuthorization(f *hostingStorageRemoteRuntimeFixture, resource domain.Resource, backup domain.HostingStorageBackup, failed hostingStorageRemoteRuntimeTransfer, target string, revision int64) {
	f.t.Helper()
	login := storageRuntimeAuthorizedJSON[struct {
		Token string `json:"token"`
	}](f.storageRuntimeFixture, "", "POST", "/api/v1/sessions", map[string]string{"email": "viewer@example.local", "password": "viewer"}, 201)
	paths := []string{"/api/v1/storage-backups/" + backup.ID + "/transfers", "/api/v1/storage-backup-transfers/" + failed.ID}
	for _, path := range paths {
		storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, "", "GET", path, nil, 401)
		storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "GET", path, nil, 403)
	}
	storageRuntimeJSON[json.RawMessage](f.storageRuntimeFixture, "POST", "/api/v1/project-members", app.ProjectMemberInput{ProjectID: "proj_platform", Email: "viewer@example.local", Role: "viewer"}, 200)
	for _, path := range paths {
		storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "GET", path, nil, 200)
	}
	schedulePath := "/api/v1/resources/" + resource.ID + "/storage-backup-schedules"
	storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "GET", schedulePath, nil, 200)
	storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "POST", schedulePath, domain.HostingStorageBackupScheduleInput{RequestID: hostingStorageScheduleRuntimeID(f.t), ExpectedResourceGeneration: resource.Generation, Frequency: "daily", Timezone: "UTC", Enabled: true}, 403)

	storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", storageRemoteRuntimeBackupRequest(f.t, resource.Generation, target, revision), 403)
	storageRuntimeAuthorizedJSON[json.RawMessage](f.storageRuntimeFixture, login.Token, "POST", "/api/v1/storage-backups/"+backup.ID+"/transfers/retry", storageRemoteRuntimeRetryRequest(f.t, failed.ID), 403)
}

// This handshake sequences only the browser test. It never grants hosting
// authority: all backup, transfer and restore actions use production handlers.
type hostingStorageRemoteRuntimeReport struct {
	Action string `json:"action"`
	ID     string `json:"id"`
}
type hostingStorageRemoteRuntimeBrowser struct {
	mu                                                  sync.Mutex
	key, phase, resource, target, backup                string
	retentionName, retentionBackups, retentionExecution string
	access, secret                                      string
	environment, source, recoveryURL                    string
	reports                                             chan hostingStorageRemoteRuntimeReport
}

func (b *hostingStorageRemoteRuntimeBrowser) serve(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/__storage_remote_fixture/") {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-NeroCD-Fixture-Key")), []byte(b.key)) != 1 {
		w.WriteHeader(403)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" && r.URL.Path == "/__storage_remote_fixture/state" {
		b.mu.Lock()
		defer b.mu.Unlock()
		state := map[string]string{"phase": b.phase, "resource_id": b.resource, "target_id": b.target, "backup_id": b.backup}
		if os.Getenv("NEROCD_STORAGE_RETENTION_RUNTIME") == "disposable-dind" {
			state["retention_mode"] = "true"
		}
		if strings.HasPrefix(b.phase, "retention") {
			state["resource_name"], state["retention_backup_ids"], state["retention_execution_id"] = b.retentionName, b.retentionBackups, b.retentionExecution
		}
		if b.recoveryURL != "" {
			state["recovery_url"], state["environment_id"], state["source_id"] = b.recoveryURL, b.environment, b.source
		}
		if b.phase == "target" {
			state["access_key"], state["secret_key"] = b.access, b.secret
		}
		writeJSON(w, 200, state)
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/__storage_remote_fixture/report" {
		var report hostingStorageRemoteRuntimeReport
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1025))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&report) != nil || len(report.ID) > 128 {
			w.WriteHeader(400)
			return true
		}
		b.mu.Lock()
		phase := b.phase
		b.mu.Unlock()
		expected := map[string]string{"target": "target", "validation": "validation", "backup": "backup", "denied": "warning", "retry": "retry", "restore": "restore", "complete": "done", "archive_alert": "archive_warning", "retention": "retention", "retention_history": "retention_history"}[phase]
		if report.Action != expected {
			w.WriteHeader(409)
			return true
		}
		select {
		case b.reports <- report:
			w.WriteHeader(204)
		default:
			w.WriteHeader(409)
		}
		return true
	}
	w.WriteHeader(404)
	return true
}
func (b *hostingStorageRemoteRuntimeBrowser) set(phase, resource, target, backup string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.resource, b.target, b.backup = phase, resource, target, backup
	b.access, b.secret = "", ""
	b.environment, b.source, b.recoveryURL = "", "", ""
}
func (b *hostingStorageRemoteRuntimeBrowser) await(t *testing.T, action string) string {
	t.Helper()
	select {
	case report := <-b.reports:
		if report.Action != action || report.ID == "" {
			t.Fatal("browser report does not match current phase")
		}
		return report.ID
	case <-time.After(90 * time.Second):
		t.Fatalf("real browser did not complete %s", action)
	}
	return ""
}

func hostingStorageRemoteRuntimeWriterState(id string, running bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "inspect", id).Output()
	var values []struct {
		ID    string `json:"Id"`
		State struct{ Running bool }
	}
	return err == nil && json.Unmarshal(raw, &values) == nil && len(values) == 1 && values[0].ID == id && values[0].State.Running == running
}

func hostingStorageRemoteRuntimeAwaitDownload(f *hostingStorageRemoteRuntimeFixture, backup, restore, state string) hostingStorageRemoteRuntimeTransfer {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		values := storageRuntimeJSON[[]hostingStorageRemoteRuntimeTransfer](f.storageRuntimeFixture, "GET", "/api/v1/storage-backups/"+backup+"/transfers", nil, 200)
		for _, value := range values {
			if value.Mode == "download" && value.RestoreOperationID == restore && value.State == state {
				return value
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("exact restore download did not settle")
	return hostingStorageRemoteRuntimeTransfer{}
}

func (b *hostingStorageRemoteRuntimeBrowser) targetSetup(resource, access, secret string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.resource, b.access, b.secret = "target", resource, access, secret
}

func hostingStorageScheduleRuntimeID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal("schedule request identity")
	}
	return hex.EncodeToString(b[:])
}
func hostingStorageRemoteRuntimeOccurrence(f *hostingStorageRemoteRuntimeFixture, resource, schedule string) domain.HostingStorageBackupOccurrence {
	f.t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		rows := storageRuntimeJSON[[]domain.HostingStorageBackupOccurrence](f.storageRuntimeFixture, "GET", "/api/v1/resources/"+resource+"/storage-backup-schedules/"+schedule+"/occurrences", nil, 200)
		if len(rows) > 0 {
			if len(rows) != 1 || rows[0].OperationID == "" || rows[0].BackupID == "" {
				f.t.Fatal("scheduled admission identity")
			}
			return rows[0]
		}
		select {
		case <-deadline.C:
			f.t.Fatal("scheduled occurrence deadline")
		case <-tick.C:
		}
	}
}
func hostingStorageRemoteRuntimeChannels(f *hostingStorageRemoteRuntimeFixture) {
	f.t.Helper()
	receiver := f.notifications
	parsed, err := url.Parse(receiver.smtpOrigin)
	if err != nil {
		f.t.Fatal("SMTP origin")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		f.t.Fatal("SMTP port")
	}
	actor := storageRuntimeJSON[auth.Principal](f.storageRuntimeFixture, "GET", "/api/v1/me", nil, 200)
	for _, kind := range []string{"webhook", "email"} {
		configuration := map[string]any{"webhook_url": receiver.webhookOrigin + "/backup-events"}
		if kind == "email" {
			configuration = map[string]any{"smtp_host": "127.0.0.1", "smtp_port": port, "smtp_security": "tls", "smtp_username": receiver.username, "smtp_password": receiver.password, "from_address": "fixture@example.local", "recipient_user_ids": []string{actor.ID}}
		}
		storageRuntimeJSON[domain.NotificationChannel](f.storageRuntimeFixture, "POST", "/api/v1/projects/proj_platform/notification-channels", map[string]any{"request_id": hostingStorageScheduleRuntimeID(f.t), "expected_generation": 0, "name": "Storage " + kind, "type": kind, "enabled": true, "events": []string{"backup_failed", "upload_failed"}, "configuration": configuration}, 200)
	}
}
func hostingStorageRemoteRuntimeAlerts(f *hostingStorageRemoteRuntimeFixture, resource domain.Resource, backup, source, kind, event string, local bool) string {
	f.t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		rows := storageRuntimeJSON[[]domain.NotificationDelivery](f.storageRuntimeFixture, "GET", "/api/v1/projects/proj_platform/notification-deliveries", nil, 200)
		found := []domain.NotificationDelivery{}
		for _, v := range rows {
			if v.SourceID == source {
				if v.SourceKind != kind || v.Event != event || v.SourceState != "failed" || v.LocalCopyAvailable != local {
					f.t.Fatal("storage failure delivery source mismatch")
				}
				if v.State == "delivered" {
					found = append(found, v)
				}
			}
		}
		if len(found) == 2 {
			receipts := f.notifications.snapshot()
			actualURL := ""
			transports := map[string]bool{}
			for _, d := range found {
				matches := 0
				for _, receipt := range receipts {
					if receipt.Accepted && bytes.Contains(receipt.Body, []byte(d.EventID)) && bytes.Contains(receipt.Body, []byte(d.ID)) {
						matches++
						if transports[receipt.Transport] {
							f.t.Fatal("duplicate storage notification transport")
						}
						transports[receipt.Transport] = true
						delivered, err := hostingStorageRemoteRuntimeReceiptURL(receipt, d)
						if err != nil {
							f.t.Fatal("delivered storage alert URL unavailable", err)
						}
						if err = hostingStorageRemoteRuntimeRecoveryURL(delivered, os.Getenv("NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN"), resource, backup, source, kind); err != nil {
							f.t.Fatal("delivered storage alert URL association", err)
						}
						if delivered != os.Getenv("NEROCD_STORAGE_REMOTE_BROWSER_ORIGIN")+d.RecoveryURL {
							f.t.Fatal("delivered URL differs from persisted event")
						}
						if actualURL != "" && actualURL != delivered {
							f.t.Fatal("SMTP and webhook recovery URLs differ")
						}
						actualURL = delivered
					}
				}
				if matches != 1 {
					f.t.Fatal("storage alert stable IDs did not reach exactly one real transport", matches)
				}
			}
			if !transports["email"] || !transports["webhook"] || actualURL == "" {
				f.t.Fatal("accepted SMTP and webhook recovery URLs required")
			}
			return actualURL
		}
		select {
		case <-deadline.C:
			f.t.Fatal("storage SMTP/webhook failure notification deadline")
		case <-tick.C:
		}
	}
}

func (b *hostingStorageRemoteRuntimeBrowser) setRecovery(phase, resource, target, backup, environment, source, delivered string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.resource, b.target, b.backup = phase, resource, target, backup
	b.environment, b.source, b.recoveryURL = environment, source, delivered
	b.access, b.secret = "", ""
}

// Return bytes extracted from the accepted transport receipt. Expected metadata
// validates association; it never supplies a replacement for the delivered URL.
func hostingStorageRemoteRuntimeReceiptURL(receipt backupNotificationReceipt, d domain.NotificationDelivery) (string, error) {
	if receipt.Transport == "webhook" {
		var p domain.NotificationPayload
		if json.Unmarshal(receipt.Body, &p) != nil || p.EventID != d.EventID || p.DeliveryID != d.ID || p.SourceID != d.SourceID || p.SourceKind != d.SourceKind || p.Event != d.Event || p.SourceState != d.SourceState || p.LocalCopyAvailable != d.LocalCopyAvailable {
			return "", fmt.Errorf("webhook source identity mismatch")
		}
		return p.RecoveryURL, nil
	}
	if receipt.Transport != "email" {
		return "", fmt.Errorf("unknown receipt transport")
	}
	message, err := mail.ReadMessage(bytes.NewReader(receipt.Body))
	if err != nil {
		return "", fmt.Errorf("SMTP message invalid")
	}
	if message.Header.Get("Message-ID") != "<"+d.ID+"@nerocd.invalid>" {
		return "", fmt.Errorf("SMTP delivery identity mismatch")
	}
	body, err := io.ReadAll(io.LimitReader(message.Body, backupNotificationMessageLimit+1))
	if err != nil || len(body) > backupNotificationMessageLimit {
		return "", fmt.Errorf("SMTP body invalid")
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, line := range []string{"Event: " + d.Event, "Event ID: " + d.EventID, "Delivery ID: " + d.ID, "Source: " + d.SourceKind + "/" + d.SourceID, "Source state: " + d.SourceState} {
		if strings.Count("\n"+text, "\n"+line+"\n") != 1 {
			return "", fmt.Errorf("SMTP source identity mismatch")
		}
	}
	delivered := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "https://") {
			if delivered != "" {
				return "", fmt.Errorf("multiple SMTP recovery links")
			}
			delivered = line
		}
	}
	if delivered == "" {
		return "", fmt.Errorf("SMTP recovery link missing")
	}
	return delivered, nil
}
func hostingStorageRemoteRuntimeRecoveryURL(delivered, origin string, resource domain.Resource, backup, source, kind string) error {
	u, err := url.Parse(delivered)
	if err != nil || u.Scheme+"://"+u.Host != origin || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.EscapedPath() != "/projects" {
		return fmt.Errorf("recovery origin or path mismatch")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("recovery query invalid")
	}
	expected := map[string]string{"project_id": resource.ProjectID, "environment_id": resource.ProjectEnvironmentID, "resource_id": resource.ID, "backup_id": backup, "source_kind": kind}
	if kind == "storage_transfer" {
		expected["transfer_id"] = source
	} else if kind != "storage_backup" {
		return fmt.Errorf("recovery source family mismatch")
	}
	if len(q) != len(expected) {
		return fmt.Errorf("recovery query fields mismatch")
	}
	for key, value := range expected {
		if len(q[key]) != 1 || q.Get(key) != value {
			return fmt.Errorf("recovery selection mismatch")
		}
	}
	return nil
}
