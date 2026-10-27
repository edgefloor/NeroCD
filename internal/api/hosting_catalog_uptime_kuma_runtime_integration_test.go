//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
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
	f := newStorageRuntimeFixture(t, true)
	b := f.catalogBrowser
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	installed := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if installed.Template.ID != "uptime-kuma" || installed.Template.Version != domain.HostingCatalogUptimeKumaVersion || installed.Credentials == nil || len(installed.Credentials) != 0 || installed.RuntimeConfiguration.Generation != 1 || original == nil || retried == nil || domain.HostingDocumentHash(*original) != domain.HostingDocumentHash(installed) || domain.HostingDocumentHash(*retried) != domain.HostingDocumentHash(installed) {
		t.Fatal("credential-free catalog installation or identical retry mismatch")
	}
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	if first.Plan == nil || first.Plan.Storage == nil || first.Release == nil || first.Result == nil || first.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || first.Snapshot.RuntimeConfiguration != installed.RuntimeConfiguration {
		t.Fatal("empty runtime generation or owned storage deployment missing")
	}
	runtime := storageRuntimeJSON[domain.HostingRuntimeConfiguration](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	if runtime.RuntimeGeneration != 1 || runtime.Entries == nil || len(runtime.Entries) != 0 {
		t.Fatal("saved empty runtime generation missing")
	}
	current := first
	t.Cleanup(func() { catalogRuntimeState(t, current) })
	b.set("healthy", resourceID)
	b.await(t, "healthy")
	catalogRuntimeRoute(t, f, resourceID, first)
	catalogUptimeKumaHTTPS(t, f)
	b.set("app_create", resourceID)
	monitorID := b.await(t, "app_created")
	deploy := func(key, path string, seconds int) domain.HostingOperation {
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: installed.Resource.Generation, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: key, ContainerPort: 3001, HostPort: 20101, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: seconds}, StorageRouteContinuity: storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200).StorageRouteContinuity}, 202)
	}
	second := f.await(deploy("kuma_redeploy", "/", 120).ID, "succeeded")
	current = second
	f.awaitContinuity(resourceID, second.ID)
	if second.Plan == nil || second.Plan.Storage == nil || *first.Plan.Storage != *second.Plan.Storage || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration || first.Snapshot.Storage != second.Snapshot.Storage {
		t.Fatal("redeployment changed pinned volume or runtime configuration")
	}
	catalogUptimeKumaHTTPS(t, f)
	b.set("app_recheck", resourceID)
	if b.await(t, "app_checked") != monitorID {
		t.Fatal("redeploy changed monitor identity")
	}
	failed := f.await(deploy("kuma_failed_candidate", "/api/status-page/nerocd-required-missing", 3).ID, "failed")
	if failed.Result == nil || !failed.Result.Restored || failed.Result.Uncertain {
		t.Fatal("failed replacement did not retain proven restoration")
	}
	catalogRuntimeJoinFailedReplacement(t, f)
	catalogUptimeKumaHTTPS(t, f)
	b.set("app_start_recheck", resourceID)
	if b.await(t, "app_start_checked") != monitorID {
		t.Fatal("failed replacement changed monitor identity")
	}
	copy := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	if domain.HostingDocumentHash(copy) != domain.HostingDocumentHash(installed) {
		t.Fatal("installation changed during lifecycle")
	}
	t.Log("Kuma SQLite/admin setup, monitor/settings, fresh login/redeploy/new write, failed replacement recovery and immutable empty credential references verified")
	b.set("complete", resourceID)
	b.await(t, "done")
}

func catalogUptimeKumaHTTPS(t *testing.T, f *storageRuntimeFixture) {
	t.Helper()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("https://catalog-runtime.example.test/")
	if err != nil {
		t.Fatal("Kuma managed HTTPS request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || response.StatusCode != 200 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || !bytes.Contains(body, []byte("Uptime Kuma")) {
		t.Fatal("Kuma managed HTTPS trust/readiness missing")
	}
}
