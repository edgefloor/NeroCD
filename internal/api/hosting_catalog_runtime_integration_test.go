//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

func catalogRuntimeServer(t *testing.T, pg *store.PostgresStore) *Server {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("fixture key generation")
	}
	ring, err := secretcrypto.New("catalog-fixture", map[string][]byte{"catalog-fixture": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("fixture keyring")
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal("fixture service")
	}
	origin := os.Getenv("NEROCD_CATALOG_BROWSER_ORIGIN")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatal("exact loopback catalog browser origin required")
	}
	if _, err := service.BootstrapAdmin(t.Context(), app.BootstrapAdminInput{Email: "catalog-bootstrap@example.local", Name: "Catalog bootstrap", Password: hostingStorageRemoteRuntimeRandom(t)}); err != nil {
		t.Fatal("catalog bootstrap")
	}
	if _, err := pg.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_catalog_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal("catalog viewer membership")
	}
	return NewServerWithConfig(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static(), ServerConfig{PublicOrigin: origin})
}

// catalogSession starts with an empty cookie jar and submits Linkding's real
// login form. No old browser session can prove retained startup credentials.
func catalogSession(t *testing.T, base, user, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := catalogSessionClient(t, jar)
	request, err := http.NewRequest("GET", base+"/login/", nil)
	if err != nil {
		t.Fatal(catalogHTTPDiagnostic("login_page", "construct", 0, nil, err, nil))
	}
	resp, body := catalogHTTPRead(t, "login_page", client, request, 1<<20)
	if resp.StatusCode != 200 {
		t.Fatalf("catalog_http operation=login_page class=http_status status=%d expected=200", catalogHTTPStatus(resp))
	}
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindSubmatch(body)
	if len(match) != 2 {
		t.Fatal("catalog_http operation=login_page class=missing_csrf_token")
	}
	form := url.Values{"username": {user}, "password": {password}, "csrfmiddlewaretoken": {string(match[1])}}
	req, err := http.NewRequest("POST", base+"/login/", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(catalogHTTPDiagnostic("login_submit", "construct", 0, nil, err, nil))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", base+"/login/")
	resp, _ = catalogHTTPRead(t, "login_submit", client, req, 1<<20)
	if resp.StatusCode != 200 || !strings.Contains(resp.Request.URL.Path, "/bookmarks") {
		t.Fatalf("catalog_http operation=login_submit class=unexpected_login_result status=%d expected=200", catalogHTTPStatus(resp))
	}
	return client
}

func catalogBookmark(t *testing.T, client *http.Client, base string, create bool) {
	t.Helper()
	operation := "bookmark_list"
	status := 200
	if create {
		operation = "bookmark_create"
		status = 201
	}
	req, err := catalogBookmarkRequest(client, base, create)
	if err != nil {
		t.Fatal(catalogHTTPDiagnostic(operation, "construct", 0, nil, err, nil))
	}
	resp, raw := catalogHTTPRead(t, operation, client, req, 1<<20)
	if resp.StatusCode != status {
		t.Fatalf("catalog_http operation=%s class=http_status status=%d expected=%d", operation, catalogHTTPStatus(resp), status)
	}
	if !bytes.Contains(raw, []byte("NeroCD retained catalog bookmark")) {
		t.Fatalf("catalog_http operation=%s class=missing_saved_bookmark status=%d", operation, catalogHTTPStatus(resp))
	}
}

func TestHostingCatalogLinkdingCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_CATALOG_RUNTIME") != "disposable-dind" {
		t.Skip("requires approved isolated catalog runtime gate")
	}
	f := newStorageRuntimeFixture(t, true)
	b := f.catalogBrowser
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	v := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if original == nil || retried == nil || domain.HostingDocumentHash(original) != domain.HostingDocumentHash(v) || domain.HostingDocumentHash(retried) != domain.HostingDocumentHash(v) {
		t.Fatal("browser response-loss retry changed saved installation")
	}
	values := map[string]string{}
	for _, ref := range v.Credentials {
		secret := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+ref.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: ref.Secret.Version}, 200)
		values[ref.Name] = secret.Value
	}
	deploy := func(key, path string, seconds int) domain.HostingOperation {
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+v.Resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: key, ContainerPort: 9090, HostPort: 20101, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: seconds}, StorageRouteContinuity: storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+v.Resource.ID+"/storage-deployment-options", nil, 200).StorageRouteContinuity}, 202)
	}
	b.set("deploy", v.Resource.ID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	if first.Plan == nil || first.Release == nil || first.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || first.Result == nil {
		t.Fatal("wrong runtime family")
	}
	currentDiagnosticOperation := first
	catalogRuntimeState(t, currentDiagnosticOperation)
	t.Cleanup(func() { catalogRuntimeState(t, currentDiagnosticOperation) })
	b.set("healthy", v.Resource.ID)
	b.await(t, "healthy")
	// Reuse the ordinary managed proxy and hostname grant. API TLS above and
	// application HTTPS below prove distinct trust boundaries.
	f.stop()
	proxy := storageRuntimeJSON[domain.HostingProxy](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "catalog-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	certDir := filepath.Join(f.root, "proxy", proxy.ID, "certificates")
	if err := os.MkdirAll(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	publicRoot, err := os.ReadFile(filepath.Join(f.root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "root.pem"), publicRoot, 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.awaitLoadedProxy(proxy.ID, first.Release.DaemonID)
	storageRuntimeJSON[domain.HostingHostnameGrant](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "catalog-runtime.example.test"}, 201)
	b.set("route", v.Resource.ID)
	routeOperation := b.await(t, "routed")
	f.proxyAwait(routeOperation)
	verifyHTTPS := func() {
		transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
		}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequest("GET", "https://catalog-runtime.example.test/health", nil)
		if err != nil {
			t.Fatal(catalogHTTPDiagnostic("managed_health", "construct", 0, nil, err, nil))
		}
		response, body := catalogHTTPRead(t, "managed_health", client, req, 2<<20)
		if response.StatusCode != 200 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || !bytes.Contains(body, []byte("1.47.0")) || !bytes.Contains(body, []byte("healthy")) {
			t.Fatalf("catalog_http operation=managed_health class=invalid_trusted_health status=%d", catalogHTTPStatus(response))
		}
	}
	verifyHTTPS()
	base := "http://127.0.0.1:20101"
	request, err := http.NewRequest("GET", base+"/health", nil)
	if err != nil {
		t.Fatal(catalogHTTPDiagnostic("health", "construct", 0, nil, err, nil))
	}
	health, healthBody := catalogHTTPRead(t, "health", http.DefaultClient, request, 4096)
	if health.StatusCode != 200 || !bytes.Contains(healthBody, []byte("1.47.0")) || !bytes.Contains(healthBody, []byte("healthy")) {
		t.Fatalf("catalog_http operation=health class=invalid_version_or_database_health status=%d", catalogHTTPStatus(health))
	}

	client := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, client, base, true)
	f.stop()
	f.start()
	second := f.await(deploy("catalog_redeploy", "/health", 120).ID, "succeeded")
	currentDiagnosticOperation = second
	catalogRuntimeState(t, currentDiagnosticOperation)
	f.awaitContinuity(v.Resource.ID, second.ID)
	verifyHTTPS()
	fresh := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, fresh, base, false)
	failed := f.await(deploy("catalog_failed_candidate", "/catalog-required-missing", 3).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed candidate did not prove recovery")
	}
	// The runner command returns a failed workload after committing restoration.
	// This fixture starts it without the owned service's restart supervisor.
	// Prove the expected process exit before joining and restarting for new work.
	select {
	case processErr := <-f.process.done:
		f.process.done <- processErr // Keep the existing stop helper's join receipt.
		var exitErr *exec.ExitError
		if !errors.As(processErr, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(f.logs.String(), "storage workload failed; committed restoration retained") {
			t.Fatal("deliberately failed candidate did not produce the expected runner exit")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runner did not exit after the deliberately failed candidate")
	}
	f.stop()
	f.start()
	verifyHTTPS()
	recovery := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, recovery, base, false)
	if *first.Plan.Storage != *second.Plan.Storage || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration || first.Snapshot.Storage != second.Snapshot.Storage {
		t.Fatal("redeploy lost immutable storage/runtime identity")
	}
	catalogRuntimeArchiveRoundtrip(t, f, v, first, second, values, base)
	copy := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+v.Resource.ID+"/catalog-installation", nil, 200)
	if domain.HostingDocumentHash(copy) != domain.HostingDocumentHash(v) {
		t.Fatal("installation refreshed in place")
	}
	for _, ref := range copy.Credentials {
		again := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+ref.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: ref.Secret.Version}, 200)
		if again.Value != values[ref.Name] {
			t.Fatal("saved startup credential changed")
		}
	}
	f.stop()
	journal, err := os.ReadFile(filepath.Join(f.root, "journal", "journal.json"))
	if err != nil {
		t.Fatal("journal read")
	}
	serialized, _ := json.Marshal([]any{copy, first, second, failed})
	for _, value := range values {
		if bytes.Contains(journal, []byte(value)) || bytes.Contains(serialized, []byte(value)) || strings.Contains(f.logs.String(), value) {
			t.Fatal("plaintext credential escaped protected delivery")
		}
	}

	t.Log("catalog login/bookmark; fresh login after runner restart and redeploy; failed candidate recovery; stable references; secret scan passed")
	b.set("complete", v.Resource.ID)
	b.await(t, "done")

}

// This private handshake sequences the browser; production handlers alone
// authorize catalog, reveal and deployment requests. No credentials enter it.
type catalogRuntimeBrowser struct {
	mu                sync.Mutex
	key, phase, id    string
	reports           chan hostingStorageRemoteRuntimeReport
	original, retried *domain.HostingCatalogInstallation
}

func newCatalogRuntimeBrowser(t *testing.T) *catalogRuntimeBrowser {
	t.Helper()
	key := os.Getenv("NEROCD_CATALOG_BROWSER_KEY")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(key) {
		t.Fatal("private catalog browser key required")
	}
	return &catalogRuntimeBrowser{key: key, reports: make(chan hostingStorageRemoteRuntimeReport, 1)}
}
func (b *catalogRuntimeBrowser) set(phase, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phase, b.id = phase, id
}
func (b *catalogRuntimeBrowser) await(t *testing.T, action string) string {
	t.Helper()
	select {
	case v := <-b.reports:
		if v.Action != action || v.ID == "" {
			t.Fatal("catalog browser phase mismatch")
		}
		return v.ID
	case <-time.After(90 * time.Second):
		t.Fatalf("catalog browser did not complete %s", action)
	}
	return ""
}
func (b *catalogRuntimeBrowser) serve(w http.ResponseWriter, r *http.Request, server http.Handler) bool {
	if strings.HasPrefix(r.URL.Path, "/__catalog_fixture/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-NeroCD-Fixture-Key")), []byte(b.key)) != 1 {
			w.WriteHeader(403)
			return true
		}
		w.Header().Set("Cache-Control", "no-store")
		b.mu.Lock()
		phase, id := b.phase, b.id
		b.mu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/__catalog_fixture/state" {
			writeJSON(w, 200, map[string]string{"phase": phase, "id": id})
			return true
		}
		if r.Method == "POST" && r.URL.Path == "/__catalog_fixture/report" {
			var v hostingStorageRemoteRuntimeReport
			decoder := json.NewDecoder(io.LimitReader(r.Body, 1025))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&v) != nil || decoder.Decode(new(any)) != io.EOF || len(v.ID) > 128 || v.ID == "" {
				w.WriteHeader(400)
				return true
			}
			expected := map[string]string{"install": "installed", "deploy": "deployed", "healthy": "healthy", "route": "routed", "complete": "done", "backup": "backed_up", "restore": "restored", "restore_deploy": "restored_deployed"}[phase]
			if v.Action != expected {
				w.WriteHeader(409)
				return true
			}
			select {
			case b.reports <- v:
				w.WriteHeader(204)
			default:
				w.WriteHeader(409)
			}
			return true
		}
		w.WriteHeader(404)
		return true
	}
	if r.Method == "POST" && r.URL.Path == "/api/v1/hosting-catalog/installations" {
		// Commit the real first request, then simulate its lost response. The
		// browser must retry the unchanged request through current authorization.
		receipt := httptest.NewRecorder()
		server.ServeHTTP(receipt, r)
		if receipt.Code == 201 {
			var v domain.HostingCatalogInstallation
			if json.Unmarshal(receipt.Body.Bytes(), &v) != nil {
				w.WriteHeader(500)
				return true
			}
			b.mu.Lock()
			first := b.original == nil
			if first {
				b.original = &v
			} else {
				b.retried = &v
			}
			b.mu.Unlock()
			if first {
				writeJSON(w, 503, map[string]string{"code": "unavailable"})
				return true
			}
		}
		for name, values := range receipt.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(receipt.Code)
		_, _ = w.Write(receipt.Body.Bytes())
		return true
	}
	return false
}

// Capture only the settled, exactly owned container before older cleanup
// callbacks stop the runner or remove its guest. No raw inspect or logs escape.
func catalogRuntimeState(t *testing.T, op domain.HostingOperation) {
	t.Helper()
	if op.Result == nil || op.Plan == nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(op.Result.ContainerID) {
		t.Log("catalog_container class=target_unavailable")
		return
	}
	id := op.Result.ContainerID
	output := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/local/bin/docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...).Output()
	}
	format := `{"ID":{{json .Id}},"Binding":{{json (index .Config.Labels "io.nerocd.binding")}},"Operation":{{json (index .Config.Labels "io.nerocd.operation")}},"Plan":{{json (index .Config.Labels "io.nerocd.plan")}},"Running":{{.State.Running}},"Restarting":{{.State.Restarting}},"OOMKilled":{{.State.OOMKilled}},"Dead":{{.State.Dead}},"ExitCode":{{.State.ExitCode}},"RestartCount":{{.RestartCount}},"Memory":{{.HostConfig.Memory}},"MemorySwap":{{.HostConfig.MemorySwap}},"NanoCPUs":{{.HostConfig.NanoCpus}},"PidsLimit":{{if .HostConfig.PidsLimit}}{{.HostConfig.PidsLimit}}{{else}}0{{end}}}`
	raw, err := output("inspect", "--format", format, id)
	if err != nil {
		t.Logf("catalog_container class=%s", catalogHTTPFailureClass(err))
		return
	}
	diagnostic, owned := catalogContainerDiagnostic(raw, id, op.BindingID, op.ID, op.Plan.Hash)
	clear(raw)
	t.Log(diagnostic)
	if !owned {
		return
	}
	if workers, err := output("top", id, "-eo", "pid,comm"); err == nil {
		t.Log(catalogWorkerDiagnostic(workers))
	} else {
		t.Log("catalog_workers class=unavailable")
	}
	if counters, err := output("exec", id, "cat", "/sys/fs/cgroup/memory.events"); err == nil {
		t.Log(catalogOOMDiagnostic(counters))
	} else {
		t.Log("catalog_cgroup class=unavailable")
	}
}

// Extend the existing Linkding fixture through public owner routes, ordinary
// combined deployment, and fresh application sessions on a separate volume.
func catalogRuntimeArchiveRoundtrip(t *testing.T, f *storageRuntimeFixture, installed domain.HostingCatalogInstallation, old, current domain.HostingOperation, values map[string]string, sourceBase string) {
	t.Helper()
	source := storageRuntimeJSON[domain.HostingStorage](f, "GET", "/api/v1/resources/"+installed.Resource.ID+"/storage", nil, 200)
	if storageArchiveWriterRunning(t, old.Result.ContainerID) || !storageArchiveWriterRunning(t, current.Result.ContainerID) {
		t.Fatal("catalog source writer inventory changed")
	}
	sourceDirectory := catalogArchiveVolumeDirectory(t, source.VolumeName)
	probe := filepath.Join(sourceDirectory, "nerocd-recovery-probe")
	if err := os.Mkdir(probe, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(probe, "saved"), []byte("archived ownership probe"), 0640); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{probe, filepath.Join(probe, "saved")} {
		if err := os.Chown(path, 12345, 12346); err != nil {
			t.Fatal(err)
		}
	}
	metadata := catalogArchiveProbeEvidence(t, sourceDirectory)
	since := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	fault := f.arm("/api/v1/runners/hosting/storage-archive/complete")
	f.catalogBrowser.set("backup", installed.Resource.ID)
	backupID := f.catalogBrowser.await(t, "backed_up")
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	backupOperation := storageRuntimeJSON[domain.HostingOperation](f, "GET", "/api/v1/hosting-operations/"+backup.OperationID, nil, 200)
	f.lastOperation.Store(&backupOperation)
	t.Logf("catalog archive backup operation=%s state=%s", backupOperation.ID, backupOperation.State)
	entry := storageArchiveRuntimeLose(f, fault, backup.OperationID)
	if entry.Complete.Result.ArchiveOutcome != "succeeded" || entry.Complete.Result.ResumeOutcome != "resumed" || entry.Complete.Result.Uncertain {
		t.Fatal("catalog backup did not resume original writer")
	}
	for _, h := range entry.Plan.Writers {
		if h.ContainerID == current.Result.ContainerID && h.Snapshot.RuntimeConfiguration != current.Snapshot.RuntimeConfiguration {
			t.Fatal("backup lost exact healthy runtime")
		}
	}
	storageArchiveRuntimeReplay(f, backup.OperationID, f.archiveCompletions.Load())
	if storageArchiveHelperStarts(t, backup.OperationID, since) != 1 {
		t.Fatal("catalog backup replay ran helper twice")
	}
	if storageArchiveWriterRunning(t, old.Result.ContainerID) || !storageArchiveWriterRunning(t, current.Result.ContainerID) {
		t.Fatal("catalog backup recreated or started wrong writer")
	}
	catalogArchiveAwaitReady(t, sourceBase, current)
	client := catalogSession(t, sourceBase, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogArchiveBookmark(t, client, sourceBase, "NeroCD source after backup", true, true)
	// Change desired metadata after the backup without changing the running
	// application. Restore must copy the source snapshot, not this latest input.
	runtime := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+installed.Resource.ID+"/runtime-configuration", nil, 200).Configuration
	changed := append([]domain.HostingRuntimeEntry(nil), runtime.Entries...)
	literal := "after_backup"
	changed = append(changed, domain.HostingRuntimeEntry{Name: "CATALOG_BACKUP_PROBE", Kind: "literal", Value: &literal})
	storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "PUT", "/api/v1/resources/"+installed.Resource.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: runtime.RuntimeGeneration, RequestID: strings.Repeat("d", 32), Entries: changed}, 200)
	restoreFault := f.arm("/api/v1/runners/hosting/storage-archive/complete")
	f.catalogBrowser.set("restore", backupID)
	restoreID := f.catalogBrowser.await(t, "restored")
	restored := storageRuntimeJSON[domain.HostingStorageRestore](f, "GET", "/api/v1/storage-restores/"+restoreID, nil, 200)
	if restored.Resource.ID == source.ResourceID || restored.Storage.VolumeName == source.VolumeName || len(restored.Operation.StorageArchive.Plan.Writers) != 0 {
		t.Fatal("catalog restore reused source target or writer authority")
	}
	f.lastOperation.Store(&restored.Operation)
	restoredEntry := storageArchiveRuntimeLose(f, restoreFault, restoreID)
	if !restoredEntry.Complete.Result.RestoreVerified || restoredEntry.Complete.Result.Uncertain {
		t.Fatal("catalog restoration not verified")
	}
	targetDirectory := catalogArchiveVolumeDirectory(t, restored.Storage.VolumeName)
	if catalogArchiveProbeEvidence(t, targetDirectory) != metadata {
		t.Fatal("restored ownership/modes/bytes changed")
	}
	created := strings.TrimSpace(string(storageRuntimeDocker(t, "volume", "inspect", "--format", "{{.CreatedAt}}", restored.Storage.VolumeName)))
	storageArchiveRuntimeReplay(f, restoreID, f.archiveCompletions.Load())
	if storageArchiveHelperStarts(t, restoreID, since) != 1 || strings.TrimSpace(string(storageRuntimeDocker(t, "volume", "inspect", "--format", "{{.CreatedAt}}", restored.Storage.VolumeName))) != created {
		t.Fatal("catalog restore replay recreated volume or repeated extraction")
	}
	pinned := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+restored.Resource.ID+"/runtime-configuration", nil, 200).Configuration
	if pinned.RuntimeGeneration != 1 || domain.HostingDocumentHash(domain.HostingRuntimeDocument{Entries: pinned.Entries}) != current.Snapshot.RuntimeConfiguration.Hash {
		t.Fatal("target runtime differs from pinned backup source")
	}
	storageRuntimeJSON[json.RawMessage](f, "GET", "/api/v1/resources/"+restored.Resource.ID+"/catalog-installation", nil, 404)
	f.catalogBrowser.set("restore_deploy", restored.Resource.ID)
	deployed := f.await(f.catalogBrowser.await(t, "restored_deployed"), "succeeded")
	if deployed.Plan == nil || deployed.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || deployed.Snapshot.RuntimeConfiguration.Hash != current.Snapshot.RuntimeConfiguration.Hash {
		t.Fatal("restored ordinary deploy lost runtime identity")
	}
	targetBase := "http://127.0.0.1:20104"
	fresh := catalogSession(t, targetBase, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, fresh, targetBase, false)
	catalogArchiveBookmark(t, fresh, targetBase, "NeroCD source after backup", false, false)
	catalogArchiveBookmark(t, fresh, targetBase, "NeroCD restored independent write", true, true)
	catalogArchiveBookmark(t, client, sourceBase, "NeroCD restored independent write", false, false)
	catalogArchiveBookmark(t, client, sourceBase, "NeroCD source after backup", false, true)
	catalogRuntimeResumeFailure(t, f, installed.Resource, current, old)
	catalogArchiveAwaitReady(t, sourceBase, current)
	recoveredSession := catalogSession(t, sourceBase, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, recoveredSession, sourceBase, false)
	catalogArchiveBookmark(t, recoveredSession, sourceBase, "NeroCD source after backup", false, true)
	raw, _ := json.Marshal([]any{entry.Plan, restoredEntry.Plan, pinned, deployed})
	for _, value := range values {
		if bytes.Contains(raw, []byte(value)) || strings.Contains(f.logs.String(), value) {
			t.Fatal("plaintext escaped catalog archive metadata")
		}
	}
	t.Log("catalog archive: pinned runtime, exact writer reuse, separate restored volume, fresh login/read/write, ownership/modes, replay and owner recovery verified")
}

func catalogArchiveBookmark(t *testing.T, client *http.Client, base, title string, create, want bool) {
	t.Helper()
	req, err := catalogBookmarkRequest(client, base, create)
	if err != nil {
		t.Fatal("catalog archive bookmark request")
	}
	if create {
		payload, _ := json.Marshal(map[string]any{"url": "http://127.0.0.1:9090/health?probe=" + url.QueryEscape(title), "title": title, "tag_names": []string{"recovery"}})
		req.Body = io.NopCloser(bytes.NewReader(payload))
		req.ContentLength = int64(len(payload))
	}
	response, raw := catalogHTTPRead(t, "archive_bookmark", client, req, 1<<20)
	expected := 200
	if create {
		expected = 201
	}
	if response.StatusCode != expected || bytes.Contains(raw, []byte(title)) != want {
		t.Fatal("catalog archive bookmark isolation/read/write mismatch")
	}
}

func catalogArchiveVolumeDirectory(t *testing.T, name string) string {
	t.Helper()
	var rows []struct{ Name, Mountpoint string }
	if json.Unmarshal(storageRuntimeDocker(t, "volume", "inspect", name), &rows) != nil || len(rows) != 1 || rows[0].Name != name || rows[0].Mountpoint != "/var/lib/docker/volumes/"+name+"/_data" {
		t.Fatal("exact owned volume directory unavailable")
	}
	return rows[0].Mountpoint
}
func catalogArchiveProbeEvidence(t *testing.T, directory string) string {
	t.Helper()
	var out strings.Builder
	for _, relative := range []string{"nerocd-recovery-probe", "nerocd-recovery-probe/saved"} {
		path := filepath.Join(directory, relative)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("numeric ownership unavailable")
		}
		fmt.Fprintf(&out, "%s:%d:%d:%04o;", relative, stat.Uid, stat.Gid, info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(directory, "nerocd-recovery-probe/saved"))
	if err != nil {
		t.Fatal(err)
	}
	return out.String() + string(raw)
}

// The private Docker wrapper fails only the exact source writer's start. All
// other commands use the real daemon; owner recovery removes this fault first.
func catalogRuntimeResumeFailure(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, current, old domain.HostingOperation) {
	t.Helper()
	f.stop()
	marker := filepath.Join(f.root, "archive-resume-fault")
	wrapper := filepath.Join(f.root, "archive-docker")
	script := "#!/bin/sh\ncase \" $* \" in *\" container start " + current.Result.ContainerID + " \"*) [ ! -f '" + marker + "' ] || exit 47;; esac\nexec /usr/local/bin/docker \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("fail exact owned resume"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.archiveConfig)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		t.Fatal("archive fixture config")
	}
	config["docker_binary"], _ = json.Marshal(wrapper)
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.archiveConfig, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	since := time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", domain.HostingStorageBackupInput{RequestID: strings.Repeat("e", 32), ExpectedResourceGeneration: 1}, 202)
	failed := f.await(backup.OperationID, "reconciliation_required")
	if failed.StorageArchive == nil || failed.StorageArchive.Result == nil || failed.StorageArchive.Result.ResumeOutcome != "uncertain" || !failed.StorageArchive.Result.Uncertain || !failed.MutationHold {
		t.Fatal("resume failure lost truthful unresolved hold")
	}
	stopped := false
	for _, p := range failed.StorageArchive.Progress {
		stopped = stopped || p.Phase == "stopped"
	}
	if !stopped || storageArchiveWriterRunning(t, current.Result.ContainerID) || storageArchiveWriterRunning(t, old.Result.ContainerID) {
		t.Fatal("resume fault did not occur after exact writer quiescence")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	recovery := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/hosting-operations/"+failed.ID+"/storage-archive-recovery", domain.HostingStorageArchiveRecoveryInput{RequestID: strings.Repeat("f", 32), ExpectedPlanHash: failed.StorageArchive.Plan.Hash}, 202)
	evidence := recovery.StorageArchive.Plan.RecoveryEvidence
	if evidence == nil || len(evidence.ResumeRequiredContainerIDs) != 1 || evidence.ResumeRequiredContainerIDs[0] != current.Result.ContainerID {
		t.Fatal("owner recovery widened originally-running writer obligation")
	}
	recovered := f.await(recovery.ID, "succeeded")
	if recovered.MutationHold || !storageArchiveWriterRunning(t, current.Result.ContainerID) || storageArchiveWriterRunning(t, old.Result.ContainerID) {
		t.Fatal("owner recovery failed exact writer resume")
	}
	if storageArchiveHelperStarts(t, recovery.ID, since) != 0 {
		t.Fatal("owner recovery repeated archive helper execution")
	}
}

// Docker start proves writer resumption, not application readiness. Probe only
// the exact known local health endpoint within the deployment's readiness budget.
// Login and bookmark requests remain strict single application operations.
func catalogArchiveAwaitReady(t *testing.T, base string, op domain.HostingOperation) {
	t.Helper()
	if op.Result == nil || op.Plan == nil || op.Release == nil || base != "http://127.0.0.1:"+strconv.Itoa(op.Snapshot.HostPort) || op.Snapshot.Readiness.Path != "/health" || op.Snapshot.Readiness.TimeoutSeconds < 1 || op.Snapshot.Readiness.TimeoutSeconds > 120 {
		t.Fatal("catalog readiness requires the exact settled local application")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(op.Snapshot.Readiness.TimeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	attempts := 0
	for {
		if !storageArchiveWriterRunning(t, op.Result.ContainerID) {
			t.Fatal("resumed catalog writer stopped during readiness")
		}
		attempts++
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			t.Fatal("catalog readiness request construction")
		}
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
			closeErr := response.Body.Close()
			if readErr == nil && closeErr == nil && len(body) <= 4096 && response.StatusCode == 200 && bytes.Contains(body, []byte("1.47.0")) && bytes.Contains(body, []byte("healthy")) {
				t.Logf("catalog resumed readiness operation=%s attempts=%d", op.ID, attempts)
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("resumed catalog application did not become healthy within its readiness deadline")
		case <-ticker.C:
		}
	}
}
