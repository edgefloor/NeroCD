//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/domain"
)

// TestHostingCatalogUptimeKumaCompiledRunnerHTTPS reuses the catalog install,
// runner, route and browser handshake. SQLite setup and application writes use
// Uptime Kuma's real UI; no startup credentials or database bootstrap are injected.
func TestHostingCatalogUptimeKumaCompiledRunnerHTTPS(t *testing.T) {
	if testing.Short() || os.Getenv("NEROCD_CATALOG_RUNTIME") != "disposable-dind" || os.Getenv("NEROCD_CATALOG_APPLICATION") != "uptime-kuma" {
		t.Skip("requires approved isolated Uptime Kuma catalog runtime gate")
	}
	catalogRuntimePersistentApplication(t, catalogRuntimeApplicationProfile{ID: "uptime-kuma", Version: domain.HostingCatalogUptimeKumaVersion, RequestPrefix: "kuma", Port: 3001, ReadinessPath: "/", FailurePath: "/api/status-page/nerocd-required-missing", HealthContent: "Uptime Kuma", Entries: []domain.HostingRuntimeEntry{}})
}

type catalogRuntimeApplicationProfile struct {
	ID, Version, RequestPrefix, ReadinessPath, FailurePath, HealthContent string
	Port                                                                  int
	Entries                                                               []domain.HostingRuntimeEntry
}

func catalogRuntimePersistentApplication(t *testing.T, profile catalogRuntimeApplicationProfile) {
	t.Helper()
	f := newStorageRuntimeFixture(t, true)
	b := f.catalogBrowser
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	installed := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if installed.Template.ID != profile.ID || installed.Template.Version != profile.Version || installed.Credentials == nil || len(installed.Credentials) != 0 || installed.RuntimeConfiguration.Generation != 1 || original == nil || retried == nil || domain.HostingDocumentHash(*original) != domain.HostingDocumentHash(installed) || domain.HostingDocumentHash(*retried) != domain.HostingDocumentHash(installed) {
		t.Fatal("credential-free catalog installation or identical retry mismatch")
	}
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	if first.Plan == nil || first.Plan.Storage == nil || first.Release == nil || first.Result == nil || first.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || first.Snapshot.RuntimeConfiguration != installed.RuntimeConfiguration {
		t.Fatal("empty runtime generation or owned storage deployment missing")
	}
	runtimeResponse := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	runtime := runtimeResponse.Configuration
	if runtime.RuntimeGeneration != 1 || runtime.Entries == nil || !reflect.DeepEqual(runtime.Entries, profile.Entries) {
		t.Fatal("saved canonical runtime generation missing")
	}
	current := first
	t.Cleanup(func() { catalogRuntimeState(t, current) })
	b.set("healthy", resourceID)
	b.await(t, "healthy")
	catalogRuntimeRoute(t, f, resourceID, first)
	catalogRuntimeApplicationHTTPS(t, f, profile)
	b.set("app_create", resourceID)
	monitorID := b.await(t, "app_created")
	deploy := func(key, path string, seconds int) domain.HostingOperation {
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: installed.Resource.Generation, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: key, ContainerPort: profile.Port, HostPort: 20101, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: seconds}, StorageRouteContinuity: storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200).StorageRouteContinuity}, 202)
	}
	second := f.await(deploy(profile.RequestPrefix+"_redeploy", profile.ReadinessPath, 120).ID, "succeeded")
	current = second
	f.awaitContinuity(resourceID, second.ID)
	if second.Plan == nil || second.Plan.Storage == nil || *first.Plan.Storage != *second.Plan.Storage || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration || first.Snapshot.Storage != second.Snapshot.Storage {
		t.Fatal("redeployment changed pinned volume or runtime configuration")
	}
	catalogRuntimeApplicationHTTPS(t, f, profile)
	b.set("app_recheck", resourceID)
	if b.await(t, "app_checked") != monitorID {
		t.Fatal("redeploy changed monitor identity")
	}
	failed := f.await(deploy(profile.RequestPrefix+"_failed_candidate", profile.FailurePath, 3).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed replacement did not retain proven restoration")
	}
	catalogRuntimeJoinFailedReplacement(t, f)
	catalogRuntimeApplicationHTTPS(t, f, profile)
	b.set("app_start_recheck", resourceID)
	if b.await(t, "app_start_checked") != monitorID {
		t.Fatal("failed replacement changed monitor identity")
	}
	copy := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	if domain.HostingDocumentHash(copy) != domain.HostingDocumentHash(installed) {
		t.Fatal("installation changed during lifecycle")
	}
	t.Logf("catalog application=%s real setup, fresh login/redeploy/new write, failed replacement recovery, stable volume/runtime and immutable empty credential references verified", profile.ID)
	b.set("complete", resourceID)
	b.await(t, "done")
}

func catalogRuntimeApplicationHTTPS(t *testing.T, f *storageRuntimeFixture, profile catalogRuntimeApplicationProfile) {
	t.Helper()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("https://catalog-runtime.example.test" + profile.ReadinessPath)
	if err != nil {
		t.Fatal("catalog managed HTTPS request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || response.StatusCode != 200 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || !bytes.Contains(body, []byte(profile.HealthContent)) {
		t.Fatal("catalog managed HTTPS trust/readiness missing")
	}
}

// TestCatalogKumaRuntimeResponseEnvelope reproduces the fixture's incorrect
// top-level decode using the production serializer, without starting Docker.
func TestCatalogKumaRuntimeResponseEnvelope(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, map[string]any{"runtime_configuration": domain.HostingRuntimeConfiguration{ResourceID: "resource_fixture", RuntimeGeneration: 1, Entries: []domain.HostingRuntimeEntry{}}})
	var incorrect domain.HostingRuntimeConfiguration
	if json.Unmarshal(recorder.Body.Bytes(), &incorrect) != nil || incorrect.RuntimeGeneration != 0 || incorrect.Entries != nil {
		t.Fatal("original wrong-level decode was not reproduced")
	}
	var response struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Configuration.RuntimeGeneration != 1 || response.Configuration.Entries == nil || len(response.Configuration.Entries) != 0 {
		t.Fatal("production envelope lost persisted empty generation")
	}
	for _, raw := range []string{`{}`, `{"runtime_configuration":{"runtime_generation":1,"entries":null}}`, `{"runtime_configuration":{"runtime_generation":0,"entries":[]}}`} {
		var missing struct {
			Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
		}
		if json.Unmarshal([]byte(raw), &missing) != nil {
			t.Fatal("negative decoder fixture malformed")
		}
		if missing.Configuration.RuntimeGeneration == 1 && missing.Configuration.Entries != nil && len(missing.Configuration.Entries) == 0 {
			t.Fatal("missing generation or entries accepted")
		}
	}
}
