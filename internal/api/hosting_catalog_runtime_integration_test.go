//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	return NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
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
	var body io.Reader
	method := "GET"
	path := "/api/bookmarks/"
	if create {
		method = "POST"
		path += "?disable_scraping&disable_html_snapshot"
		body = strings.NewReader(`{"url":"http://127.0.0.1:9090/health","title":"NeroCD retained catalog bookmark","tag_names":["acceptance"]}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", base+"/bookmarks/")
	u, _ := url.Parse(base)
	for _, cookie := range client.Jar.Cookies(u) {
		if cookie.Name == "csrftoken" {
			req.Header.Set("X-CSRFToken", cookie.Value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("bookmark request failed")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	status := 200
	if create {
		status = 201
	}
	if err != nil || resp.StatusCode != status || !bytes.Contains(raw, []byte("NeroCD retained catalog bookmark")) {
		t.Fatal("bookmark persistence assertion failed")
	}
}

func TestHostingCatalogLinkdingCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_CATALOG_RUNTIME") != "disposable-dind" {
		t.Skip("requires approved isolated catalog runtime gate")
	}
	f := newStorageRuntimeFixture(t)
	templates := storageRuntimeJSON[[]domain.HostingCatalogTemplate](f, "GET", "/api/v1/hosting-catalog?project_id=proj_platform", nil, 200)
	if len(templates) != 1 {
		t.Fatal("expected one qualified template")
	}
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj_platform", ProjectEnvironmentID: f.environmentID, DestinationID: f.destinationID, Name: "Linkding acceptance", TemplateID: "linkding", ImmutableVersion: domain.HostingCatalogLinkdingVersion}
	v := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "POST", "/api/v1/hosting-catalog/installations", in, 201)
	retry := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "POST", "/api/v1/hosting-catalog/installations", in, 201)
	if domain.HostingDocumentHash(retry) != domain.HostingDocumentHash(v) {
		t.Fatal("duplicate install changed saved copy")
	}
	values := map[string]string{}
	for _, ref := range v.Credentials {
		secret := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+ref.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: ref.Secret.Version}, 200)
		values[ref.Name] = secret.Value
	}
	deploy := func(key, path string, seconds int) domain.HostingOperation {
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+v.Resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: key, ContainerPort: 9090, HostPort: 20101, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: seconds}}, 202)
	}
	first := f.await(deploy("catalog_first", "/health", 120).ID, "succeeded")
	if first.Plan == nil || first.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || first.Result == nil {
		t.Fatal("wrong runtime family")
	}
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
	fresh := catalogSession(t, base, values["LD_SUPERUSER_NAME"], values["LD_SUPERUSER_PASSWORD"])
	catalogBookmark(t, fresh, base, false)
	failed := f.await(deploy("catalog_failed_candidate", "/catalog-required-missing", 3).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed candidate did not prove recovery")
	}
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
}
