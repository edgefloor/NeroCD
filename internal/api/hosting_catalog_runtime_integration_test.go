//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := client.Get(base + "/login/")
	if err != nil {
		t.Fatal("login page unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("login page invalid")
	}
	match := regexp.MustCompile(`name="csrfmiddlewaretoken" value="([^"]+)"`).FindSubmatch(body)
	if len(match) != 2 {
		t.Fatal("login CSRF token missing")
	}
	form := url.Values{"username": {user}, "password": {password}, "csrfmiddlewaretoken": {string(match[1])}}
	req, err := http.NewRequest("POST", base+"/login/", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", base+"/login/")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal("login request failed")
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Request.URL.Path, "/bookmarks") {
		t.Fatal("retained credentials failed fresh login")
	}
	return client
}

func catalogBookmark(t *testing.T, client *http.Client, base string, create bool) {
	t.Helper()
	req, err := catalogBookmarkRequest(client, base, create)
	if err != nil {
		t.Fatalf("bookmark request construction failed: class=%s", catalogHTTPFailureClass(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("bookmark request failed: class=%s status=%d", catalogHTTPFailureClass(err), catalogHTTPStatus(resp))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	closeErr := resp.Body.Close()
	status := 200
	if create {
		status = 201
	}
	if err != nil {
		t.Fatalf("bookmark response read failed: class=%s status=%d", catalogHTTPFailureClass(err), catalogHTTPStatus(resp))
	}
	if closeErr != nil {
		t.Fatalf("bookmark response close failed: class=%s status=%d", catalogHTTPFailureClass(closeErr), catalogHTTPStatus(resp))
	}
	if resp.StatusCode != status {
		t.Fatalf("bookmark persistence assertion failed: class=http_status status=%d expected=%d", catalogHTTPStatus(resp), status)
	}
	if !bytes.Contains(raw, []byte("NeroCD retained catalog bookmark")) {
		t.Fatalf("bookmark persistence assertion failed: class=missing_saved_bookmark status=%d", catalogHTTPStatus(resp))
	}
}

func TestHostingCatalogLinkdingCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_CATALOG_RUNTIME") != "disposable-dind" {
		t.Skip("requires approved isolated catalog runtime gate")
	}
	f := newStorageRuntimeFixture(t)
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
		if body := f.https("catalog-runtime.example.test", "GET", "/health", nil, 200); !bytes.Contains(body, []byte("1.47.0")) || !bytes.Contains(body, []byte("healthy")) {
			t.Fatal("trusted application HTTPS/version/database health missing")
		}
	}
	verifyHTTPS()
	base := "http://127.0.0.1:20101"
	health, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal("Linkding health unavailable")
	}
	healthBody, err := io.ReadAll(io.LimitReader(health.Body, 4096))
	health.Body.Close()
	if err != nil || health.StatusCode != 200 || !bytes.Contains(healthBody, []byte("1.47.0")) || !bytes.Contains(healthBody, []byte("healthy")) {
		t.Fatal("Linkding version/database health not proven")
	}

	client := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, client, base, true)
	f.stop()
	f.start()
	second := f.await(deploy("catalog_redeploy", "/health", 120).ID, "succeeded")
	f.awaitContinuity(v.Resource.ID, second.ID)
	verifyHTTPS()
	fresh := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, fresh, base, false)
	failed := f.await(deploy("catalog_failed_candidate", "/catalog-required-missing", 3).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed candidate did not prove recovery")
	}
	verifyHTTPS()
	recovery := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, recovery, base, false)
	if *first.Plan.Storage != *second.Plan.Storage || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration || first.Snapshot.Storage != second.Snapshot.Storage {
		t.Fatal("redeploy lost immutable storage/runtime identity")
	}
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
			expected := map[string]string{"install": "installed", "deploy": "deployed", "healthy": "healthy", "route": "routed", "complete": "done"}[phase]
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
