//go:build linux && nerocd_proxy_fixture

package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

const wordpressFixtureVersion = "coolify-b12d1386.wordpress-7.1.3-mysql-8.4.13.v1"
const wordpressFixtureImage = "wordpress:7.1.3-php8.3-apache@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834"

func TestHostingCatalogWordPressCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_WORDPRESS_RUNTIME") != "true" || os.Getenv("NEROCD_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires approved WordPress catalog profile")
	}
	f := newStorageRuntimeFixture(t, false)
	b := f.catalogBrowser
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	resource := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+resourceID, nil, 200)
	installed := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if original == nil || retried == nil || !reflect.DeepEqual(*original, installed) || !reflect.DeepEqual(*retried, installed) || installed.Request.TemplateID != "wordpress-with-mysql" || installed.Request.ImmutableVersion != wordpressFixtureVersion || installed.Binding.DestinationID != f.destinationID || installed.Request.PublicURL != "https://catalog-runtime.example.test:25443" || resource.Source.Compose == nil || len(installed.Credentials) != 3 {
		t.Fatal("WordPress install identity/retry changed")
	}
	definition, err := domain.ParseHostingStack(*resource.Source.Compose)
	if err != nil || definition.PublicComponent != "wordpress" || definition.ContainerPort != 80 {
		t.Fatal("WordPress public stack topology changed")
	}
	runtime := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	if runtime.Configuration.RuntimeGeneration != 1 || len(runtime.Configuration.Entries) != 4 {
		t.Fatal("WordPress runtime references missing")
	}
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	verify := func(op domain.HostingOperation) {
		t.Helper()
		if op.Result == nil || op.Result.Stack == nil || !op.Result.Ready || op.Result.Uncertain || op.MutationHold || len(op.Result.Stack.Components) != 2 || len(op.Result.Stack.Volumes) != 2 || op.Release == nil {
			t.Fatal("WordPress stack success missing")
		}
		for _, component := range op.Result.Stack.Components {
			if !component.Running || component.Health != "healthy" {
				t.Fatal("WordPress/MySQL health missing")
			}
			if component.Component == "mysql" {
				raw := bytes.TrimSpace(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{json .HostConfig.PortBindings}}"))
				if string(raw) != "null" && string(raw) != "{}" {
					t.Fatal("MySQL published a host port")
				}
			} else if component.Component != "wordpress" {
				t.Fatal("unexpected WordPress component")
			}
		}
	}
	verify(first)
	b.set("healthy", resourceID)
	b.await(t, "healthy")
	catalogRuntimeRoute(t, f, resourceID, first)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	var proof string
	secretValues := map[string]string{}
	for _, credential := range installed.Credentials {
		if credential.Secret == nil {
			t.Fatal("WordPress credential reference missing")
		}
		revealed := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+credential.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: credential.Secret.Version}, 200)
		if revealed.Value == "" {
			t.Fatal("WordPress generated secret empty")
		}
		secretValues[credential.Name] = revealed.Value
	}
	assertApplication := func() {
		t.Helper()
		parts := strings.SplitN(proof, "|", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "/wp-content/uploads/") {
			t.Fatal("WordPress browser identity missing")
		}
		for path, marker := range map[string][]byte{"/?p=" + parts[0]: []byte("NeroCD persistent WordPress post"), parts[1]: wordpressFixturePNG()} {
			r, e := client.Get("https://catalog-runtime.example.test" + path)
			if e != nil {
				t.Fatal("WordPress trusted HTTPS failed")
			}
			body, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			_ = r.Body.Close()
			matches := bytes.Contains(body, marker)
			if strings.HasPrefix(path, "/wp-content/uploads/") {
				matches = bytes.Equal(body, marker)
			}
			if e != nil || r.StatusCode != 200 || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || !matches {
				t.Fatal("WordPress trusted post/upload identity changed")
			}
		}
	}
	b.set("app_create", resourceID)
	proof = b.await(t, "app_created")
	assertApplication()
	f.stop()
	f.start()
	b.set("redeploy", resourceID)
	second := f.await(b.await(t, "redeployed"), "succeeded")
	verify(second)
	if !reflect.DeepEqual(first.Result.Stack.Volumes, second.Result.Stack.Volumes) || first.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration {
		t.Fatal("WordPress replacement changed data/runtime identities")
	}
	composeRuntimeWriterBarrier(t, first, second)
	f.awaitContinuity(resourceID, second.ID)
	assertApplication()
	b.set("app_recheck", resourceID)
	if b.await(t, "app_checked") != proof {
		t.Fatal("fresh WordPress login lost post/media")
	}
	b.set("stop", resourceID)
	stopped := f.await(b.await(t, "stopped"), "succeeded")
	if stopped.Result == nil || !stopped.Result.StackStopped || stopped.MutationHold || stopped.StackStopPlan == nil || stopped.StackStopPlan.Target.OperationID != second.ID {
		t.Fatal("WordPress Stop authority missing")
	}
	for _, c := range second.Result.Stack.Components {
		if len(bytes.TrimSpace(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+c.ContainerID))) != 0 {
			t.Fatal("WordPress stopped writer remains")
		}
	}
	b.set("start", resourceID)
	started := f.await(b.await(t, "started"), "succeeded")
	verify(started)
	if started.Snapshot.StackStoppedOperationID != stopped.ID || !reflect.DeepEqual(second.Result.Stack.Volumes, started.Result.Stack.Volumes) {
		t.Fatal("WordPress Start changed volumes/stopped authority")
	}
	f.awaitContinuity(resourceID, started.ID)
	assertApplication()
	b.set("app_start_recheck", resourceID)
	if b.await(t, "app_start_checked") != proof {
		t.Fatal("WordPress Start lost post/media")
	}
	// An unavailable immutable image must fail before replacing either writer.
	source := resource.Source
	changed := *source.Compose
	changed.Original = strings.Replace(changed.Original, wordpressFixtureImage, "127.0.0.1:1/wordpress@sha256:"+strings.Repeat("a", 64), 1)
	if changed.Original == source.Compose.Original {
		t.Fatal("WordPress image preparation fixture did not change exact pin")
	}
	source.Compose = &changed
	resource = storageRuntimeJSON[domain.Resource](f, "PATCH", "/api/v1/resources/"+resourceID, app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source}, 200)
	deploy := func(key, path string) domain.HostingOperation {
		options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
		if options.StorageRouteContinuity == nil {
			t.Fatal("WordPress replacement continuity missing")
		}
		input := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: key, ContainerPort: 80, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: path, TimeoutSeconds: 3}, StorageRouteContinuity: options.StorageRouteContinuity}
		return storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", input, 202)
	}
	failed := f.await(deploy("wordpress-image-prepare-failure", "/").ID, "failed")
	if failed.MutationHold || failed.Result != nil && failed.Result.Uncertain {
		t.Fatal("WordPress image preparation did not settle safely")
	}
	for _, c := range started.Result.Stack.Components {
		if string(bytes.TrimSpace(storageRuntimeDocker(t, "inspect", c.ContainerID, "--format", "{{.State.Running}}"))) != "true" {
			t.Fatal("failed image preparation replaced writer")
		}
	}
	assertApplication()
	b.set("app_failure_recheck", resourceID)
	if b.await(t, "app_failure_checked") != proof {
		t.Fatal("WordPress preparation failure lost data")
	}
	source.Compose = installed.Resource.Source.Compose
	resource = storageRuntimeJSON[domain.Resource](f, "PATCH", "/api/v1/resources/"+resourceID, app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source}, 200)
	held := f.await(deploy("wordpress-post-mutation-failure", "/nerocd-required-missing-readiness").ID, "reconciliation_required")
	if !held.MutationHold || held.Result == nil || !held.Result.Uncertain || held.Result.Ready || held.Result.Stack == nil || !held.Result.Stack.ObservationComplete {
		t.Fatal("WordPress post-mutation failure did not retain truthful hold")
	}
	if !reflect.DeepEqual(held.Result.Stack.Volumes, started.Result.Stack.Volumes) {
		t.Fatal("WordPress held attempt changed retained volumes")
	}
	composeRuntimeWriterBarrier(t, started, held)
	// The deliberately failing readiness path must not erase real records or
	// files. Inspect only the exact observed candidate, without clearing its hold.
	var wordpressContainer string
	currentIDs := map[string]bool{}
	for _, component := range held.Result.Stack.Components {
		currentIDs[component.ContainerID] = true
		if component.Component == "wordpress" {
			wordpressContainer = component.ContainerID
		}
	}
	if wordpressContainer == "" {
		t.Fatal("held WordPress candidate missing")
	}
	ids := strings.Fields(string(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project="+held.Snapshot.RuntimeNamespace)))
	if len(ids) != len(currentIDs) {
		t.Fatal("held WordPress stack has unexpected writer count")
	}
	for _, id := range ids {
		if !currentIDs[id] {
			t.Fatal("held WordPress stack has competing writer")
		}
	}
	php := `mysqli_report(MYSQLI_REPORT_OFF); $d=new mysqli(getenv("WORDPRESS_DB_HOST"),getenv("WORDPRESS_DB_USER"),getenv("WORDPRESS_DB_PASSWORD"),getenv("WORDPRESS_DB_NAME")); if($d->connect_errno)exit(41); $r=$d->query("SELECT COUNT(*) AS n FROM wp_posts WHERE post_title='NeroCD persistent WordPress post' AND post_status='publish'"); if(!$r || (int)$r->fetch_assoc()["n"]!==1)exit(42); echo "wordpress-record-retained\n";`
	if string(storageRuntimeDocker(t, "exec", wordpressContainer, "php", "-r", php)) != "wordpress-record-retained\n" {
		t.Fatal("held WordPress database record missing")
	}
	mediaPath := strings.SplitN(proof, "|", 2)[1]
	archive := tar.NewReader(bytes.NewReader(storageRuntimeDocker(t, "cp", wordpressContainer+":/var/www/html"+mediaPath, "-")))
	if _, err := archive.Next(); err != nil {
		t.Fatal("held WordPress upload missing")
	}
	file, err := io.ReadAll(io.LimitReader(archive, 1<<20))
	if err != nil || !bytes.Equal(file, wordpressFixturePNG()) {
		t.Fatal("held WordPress upload bytes changed")
	}
	options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
	competing := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: "wordpress-competing-while-held", ContainerPort: 80, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}, StorageRouteContinuity: options.StorageRouteContinuity}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", competing, 409)
	observed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/hosting-operations/"+held.ID+"/reconcile", map[string]any{}, 202)
	if observed.ID != held.ID || !observed.MutationHold || observed.RunID != held.RunID || !reflect.DeepEqual(observed.Snapshot, held.Snapshot) {
		t.Fatal("WordPress reconciliation changed held authority")
	}
	// Read-only reconciliation is not restoration or permission to mutate.
	b.set("complete", held.ID)
	if b.await(t, "done") != resourceID {
		t.Fatal("WordPress completion resource mismatch")
	}
	f.stop()
	journal, e := os.ReadFile(filepath.Join(f.root, "journal", "journal.json"))
	if e != nil {
		t.Fatal(e)
	}
	ordinary, _ := json.Marshal([]any{installed, first, second, stopped, started, failed, held})
	for _, value := range secretValues {
		if bytes.Contains(journal, []byte(value)) || bytes.Contains(ordinary, []byte(value)) || strings.Contains(f.logs.String(), value) {
			t.Fatal("WordPress managed credential entered public operation/journal/log")
		}
	}
	clear(secretValues)
	clear(journal)
	clear(ordinary)
	t.Log("WORDPRESS_CATALOG_RUNTIME_PASS")
}

func wordpressFixturePNG() []byte {
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aYaQAAAAASUVORK5CYII=")
	if err != nil {
		panic("invalid fixed PNG fixture")
	}
	return raw
}
