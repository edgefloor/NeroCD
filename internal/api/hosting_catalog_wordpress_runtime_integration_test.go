//go:build linux && nerocd_proxy_fixture

package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
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
	wordpressRuntimeDatabaseBackupImport(t, f, resource, installed, started, proof, assertApplication)
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
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==")
	if err != nil {
		panic("invalid fixed PNG fixture")
	}
	return raw
}

// This cheap configuration probe uses the real Linux CLI and a real Unix socket.
// It never connects to a daemon or controller and cannot execute a workload.
func TestStorageRuntimeComposeRunnerConfiguration(t *testing.T) {
	binary := os.Getenv("NEROCD_RUNNER_CONFIGURATION_PROBE_BINARY")
	if binary == "" {
		t.Skip("requires explicitly selected actual CLI configuration probe")
	}
	root, err := os.MkdirTemp("/tmp", "ncd-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(root, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	caps := []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingOwnedStorageRuntimeCapability, domain.HostingRegistryPullCapability, domain.HostingStackCapability, domain.HostingStackLifecycleCapability, domain.HostingRuntimeLogsCapability, domain.HostingStackRuntimeLogsCapability}
	base := []string{"runner", "--server", "https://127.0.0.1:1", "--poll-interval", "0s", "--execute", "--hosting-runtime-env", "--hosting-registry-pull", "--hosting-runtime-logs", "--capabilities", strings.Join(caps, ","), "--proxy-state-root", filepath.Join(root, "proxy"), "--proxy-docker-binary", "/usr/local/bin/docker", "--proxy-docker-socket", socket, "--proxy-protocol", domain.HostingProxyCompilerVersion, "--proxy-verified-switch", "--image-docker-endpoint", "unix://" + socket}
	for _, tc := range []struct {
		name     string
		extra    []string
		expected string
	}{
		{"old_wordpress", []string{"--hosting-storage-backup-download"}, "archive downloads require qualified storage archive configuration"},
		{"wordpress_mysql_backup", []string{"--hosting-database", "--hosting-database-backups", "--database-state-root", filepath.Join(root, "database-state"), "--database-secret-root", filepath.Join(root, "database-secrets")}, "runner --poll-interval must be greater than zero"},
		{"git_compose_basic", nil, "runner --poll-interval must be greater than zero"},
		{"managed_archive_requires_config", []string{"--hosting-storage-backup-download"}, "archive downloads require qualified storage archive configuration"},
		{"native_capabilities_require_explicit_config", []string{"--capabilities", strings.Join(append(append([]string{}, caps...), domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability), ",")}, "database capabilities require --hosting-database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, append(append([]string{}, base...), tc.extra...)...)
			command.Env = append(os.Environ(), "NEROCD_TOKEN=", "NEROCD_RUNNER_CREDENTIAL_FILE=")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.expected) {
				t.Fatalf("actual CLI validation category mismatch: %s", strings.TrimSpace(string(output)))
			}
			t.Logf("actual_cli profile=%s category=%s", tc.name, tc.expected)
		})
	}
}

// Direct target reads retain the saved source Host context. This proves database
// import through WordPress, not URL migration or HTTPS on another hostname.
func wordpressRuntimeDatabaseBackupImport(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, proof string, assertSource func()) {
	t.Helper()
	target, deployed := wordpressRuntimeImportTarget(t, f, resource, installed, source, "b", 1)
	b := f.catalogBrowser
	b.set("database_backup", target.Resource.ID)
	backup := composeRuntimeAwaitDatabaseBackup(t, f, b.await(t, "database_backed_up"))
	if backup.Version != "mysql.compose-logical-local.v1" || backup.SourceResourceID != resource.ID || backup.SourceComponent != "mysql" || backup.SourceDeployedOperationID != source.ID || backup.Blob == nil || backup.Blob.Format != "mysql_sql" {
		t.Fatal("WordPress MySQL backup identity changed")
	}
	b.set("database_mutate", resource.ID)
	later := b.await(t, "database_source_mutated")
	if _, err := strconv.ParseInt(later, 10, 64); err != nil {
		t.Fatal("WordPress later source record identity invalid")
	}
	b.set("database_import", target.Resource.ID)
	imported := composeRuntimeAwaitDatabaseBackup(t, f, b.await(t, "database_imported"))
	if imported.BackupID != backup.ID || imported.TargetResourceID != target.Resource.ID || imported.TargetComponent != "mysql" || imported.TargetDeployedOperationID != deployed.ID || imported.Blob == nil || *imported.Blob != *backup.Blob {
		t.Fatal("WordPress import changed exact target or SQL archive")
	}
	parts := strings.SplitN(proof, "|", 2)
	if len(parts) != 2 {
		t.Fatal("WordPress source post proof missing")
	}
	restored := wordpressRuntimePost(t, deployed, "read", parts[0], "")
	if !strings.Contains(restored.Title, "NeroCD persistent WordPress post") || !strings.Contains(restored.Body, "NeroCD persistent WordPress body") {
		t.Fatal("WordPress snapshot post missing after import")
	}
	if wordpressRuntimePost(t, deployed, "count", "Target row before MySQL import", "").Count != 0 || wordpressRuntimePost(t, deployed, "count", "New write after compose_backup", "").Count != 0 {
		t.Fatal("WordPress import retained prior target or later source record")
	}
	written := wordpressRuntimePost(t, deployed, "create", "Independent target MySQL import write", "Only restored WordPress database contains this write")
	if written.ID < 1 || wordpressRuntimePost(t, source, "count", written.Title, "").Count != 0 {
		t.Fatal("WordPress target write reached source")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/?p=%d", deployed.Snapshot.HostPort, written.ID)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(t.Context(), "GET", endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "catalog-runtime.example.test:25443"
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("exact target HTTP unavailable")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	closed := response.Body.Close()
	if readErr != nil || closed != nil || len(body) > 2<<20 || response.StatusCode != 200 || !bytes.Contains(body, []byte(written.Title)) || !bytes.Contains(body, []byte(written.Body)) {
		t.Fatal("WordPress target HTTP did not serve independent record")
	}
	assertSource()
	b.set("database_check", target.Resource.ID)
	if b.await(t, "database_checked") != parts[0] {
		t.Fatal("WordPress source browser preservation proof changed")
	}
	wordpressRuntimeImportFailures(t, f, resource, installed, source, backup, assertSource)
	t.Log("COMPOSE_MYSQL_DATABASE_IMPORT_PASS snapshot target write exact HTTP source Host; original HTTPS/uploads preserved; no filesystem or URL migration")
}

func wordpressRuntimeImportTarget(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, key string, offset int) (domain.HostingCatalogInstallation, domain.HostingOperation) {
	t.Helper()
	target := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "POST", "/api/v1/hosting-catalog/installations", domain.HostingCatalogInstallInput{RequestID: strings.Repeat(key, 32), ProjectID: resource.ProjectID, ProjectEnvironmentID: resource.ProjectEnvironmentID, DestinationID: f.destinationID, Name: "WordPress database import target " + key, TemplateID: "wordpress-with-mysql", ImmutableVersion: wordpressFixtureVersion, PublicURL: installed.Request.PublicURL}, 201)
	if target.Resource.ID == resource.ID || target.Binding.ID == installed.Binding.ID || target.Request.ImmutableVersion != wordpressFixtureVersion {
		t.Fatal("WordPress import target is not distinct")
	}
	for _, a := range installed.Credentials {
		for _, b := range target.Credentials {
			if a.Secret != nil && b.Secret != nil && a.Secret.SecretID == b.Secret.SecretID {
				t.Fatal("WordPress target reused source credentials")
			}
		}
	}
	deployed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+target.Resource.ID+"/deployments", domain.HostingDeploymentInput{ExpectedGeneration: target.Resource.Generation, ExpectedRuntimeConfigurationGeneration: 1, ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, IdempotencyKey: "wordpress-component-import-target-" + key, ContainerPort: 80, HostPort: source.Snapshot.HostPort + offset, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}}, 202)
	deployed = f.await(deployed.ID, "succeeded")
	if deployed.Result == nil || deployed.Result.Stack == nil || !deployed.Result.Ready || deployed.MutationHold {
		t.Fatal("WordPress target deployment unavailable")
	}
	for _, a := range source.Result.Stack.Volumes {
		for _, b := range deployed.Result.Stack.Volumes {
			if a.VolumeName == b.VolumeName {
				t.Fatal("WordPress target shares source volume")
			}
		}
	}
	wordpressRuntimePost(t, deployed, "seed", "", "")
	return target, deployed
}

type wordpressRuntimePostResult struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Count int    `json:"count"`
}

func wordpressRuntimePost(t *testing.T, operation domain.HostingOperation, action, value, body string) wordpressRuntimePostResult {
	t.Helper()
	container := wordpressRuntimeComponent(t, operation, "wordpress", true).ContainerID
	const script = `$_SERVER['HTTP_HOST']='catalog-runtime.example.test:25443';$_SERVER['HTTPS']='on';$_SERVER['REQUEST_URI']='/';$action=$argv[1];if($action==='seed'){define('WP_INSTALLING',true);}require '/var/www/html/wp-load.php';if($action==='seed'){if(is_blog_installed()){exit(2);}require_once ABSPATH.'wp-admin/includes/upgrade.php';add_filter('pre_wp_mail','__return_true');wp_install('Import target','targetbeforeimport','target@example.test',true,'',wp_generate_password(48));$action='create';$argv[2]='Target row before MySQL import';$argv[3]='This target-only row must be replaced';}if($action==='create'){$id=wp_insert_post(['post_title'=>$argv[2],'post_content'=>$argv[3],'post_status'=>'publish','post_author'=>1],true);if(is_wp_error($id)||!$id){exit(3);}$post=get_post($id);echo json_encode(['id'=>$post->ID,'title'=>$post->post_title,'body'=>$post->post_content]);}elseif($action==='read'){$post=get_post((int)$argv[2]);if(!$post){exit(4);}echo json_encode(['id'=>$post->ID,'title'=>$post->post_title,'body'=>$post->post_content]);}elseif($action==='count'){$posts=get_posts(['post_type'=>'post','post_status'=>'any','title'=>$argv[2],'numberposts'=>2]);echo json_encode(['count'=>count($posts)]);}else{exit(5);}`
	raw := storageRuntimeDocker(t, "exec", "--user", "www-data", container, "php", "-r", script, "--", action, value, body)
	var result wordpressRuntimePostResult
	if len(raw) > 8192 || json.Unmarshal(raw, &result) != nil {
		t.Fatal("WordPress core API result unavailable")
	}
	return result
}

// Failure targets are separate stacks. A held import is never reused or released
// by this fixture, and source application files are never part of the SQL dump.
func wordpressRuntimeImportFailures(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, installed domain.HostingCatalogInstallation, source domain.HostingOperation, backup domain.HostingDatabaseBackupOperation, assertSource func()) {
	t.Helper()
	restore := func(target domain.HostingCatalogInstallation, deployed domain.HostingOperation, key string) domain.HostingDatabaseBackupOperation {
		current := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+target.Resource.ID, nil, 200)
		in := domain.HostingDatabaseBackupRestoreInput{RequestID: strings.Repeat(key, 32), ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: deployed.ID, ExpectedResourceGeneration: current.Generation, AcknowledgeReplacement: true}}
		op := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", in, 202)
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
			op = storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "GET", "/api/v1/database-backups/"+op.ID, nil, 200)
			if op.State == "uncertain" || op.State == "failed" {
				if !op.OwnershipHeld || op.BackupID != backup.ID || op.TargetResourceID != target.Resource.ID || op.TargetComponent != "mysql" || op.TargetDeployedOperationID != deployed.ID || op.Blob == nil || *op.Blob != *backup.Blob {
					t.Fatal("failed MySQL import lost exact held target/archive")
				}
				in.RequestID = strings.Repeat(key, 31) + "0"
				storageRuntimeJSON[map[string]any](f, "POST", "/api/v1/database-backups/"+backup.ID+"/restores", in, 409)
				return op
			}
			if op.State == "succeeded" {
				t.Fatal("injected MySQL import unexpectedly succeeded")
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("failed MySQL import deadline")
		return op
	}
	corruptTarget, corruptDeployment := wordpressRuntimeImportTarget(t, f, resource, installed, source, "c", 2)
	cas := filepath.Join(f.root, "database-state", "logical-backups", strings.TrimPrefix(backup.Blob.Digest, "sha256:"))
	original, err := os.ReadFile(cas)
	if err != nil || int64(len(original)) != backup.Blob.Bytes || "sha256:"+fmt.Sprintf("%x", sha256.Sum256(original)) != backup.Blob.Digest {
		t.Fatal("original component CAS unavailable")
	}
	restored := false
	restoreCAS := func() {
		if !restored {
			if err := os.WriteFile(cas, original, 0600); err != nil {
				t.Error("component CAS restoration failed")
			} else {
				restored = true
			}
		}
	}
	t.Cleanup(restoreCAS)
	corrupted := bytes.Clone(original)
	corrupted[len(corrupted)-1] ^= 1
	if err := os.WriteFile(cas, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	restore(corruptTarget, corruptDeployment, "d")
	restoreCAS()
	if !restored {
		t.Fatal("component CAS restoration failed")
	}
	// No SQL reached the target, so its pre-import row must still exist.
	if wordpressRuntimePost(t, corruptDeployment, "count", "Target row before MySQL import", "").Count != 1 {
		t.Fatal("corrupt archive changed target SQL")
	}
	assertSource()
	t.Log("COMPOSE_MYSQL_CORRUPT_ARCHIVE_PASS exact digest rejection before SQL; target held and source preserved")
	partialTarget, partialDeployment := wordpressRuntimeImportTarget(t, f, resource, installed, source, "e", 3)
	database := wordpressRuntimeComponent(t, partialDeployment, "mysql", true)
	// These two static names are confined to this exact newly installed target.
	// The recovery account is necessary because root cannot regrant a privilege
	// that it no longer owns. Passwords stay in the container environment/files.
	const setup = `set -eu
case "$MYSQL_ROOT_PASSWORD" in ''|*[!A-Za-z0-9_-]*) exit 64;; esac
umask 077
mkdir /tmp/nerocd-import-fault
printf '[client]\nuser=root\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_ROOT_PASSWORD" >/tmp/nerocd-import-fault/root.cnf
printf '[client]\nuser=nerocd_fixture_recovery\npassword=%s\nsocket=/var/run/mysqld/mysqld.sock\n' "$MYSQL_ROOT_PASSWORD" >/tmp/nerocd-import-fault/recovery.cnf
chmod 0400 /tmp/nerocd-import-fault/*.cnf
/usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM mysql.user WHERE User='nerocd_fixture_recovery'" | /usr/bin/grep -qx 0
/usr/bin/touch /tmp/nerocd-import-fault/owned
/usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf <<SQL
CREATE USER 'nerocd_fixture_recovery'@'localhost' IDENTIFIED BY '$MYSQL_ROOT_PASSWORD';
GRANT ALL PRIVILEGES ON *.* TO 'nerocd_fixture_recovery'@'localhost' WITH GRANT OPTION;
GRANT SYSTEM_USER ON *.* TO 'nerocd_fixture_recovery'@'localhost';
REVOKE CREATE ON *.* FROM 'root'@'localhost';
SQL`
	const cleanup = `set -eu
if test -f /tmp/nerocd-import-fault/owned; then
 /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf -e "GRANT CREATE ON *.* TO 'root'@'localhost'" 2>/dev/null || /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/recovery.cnf -e "GRANT CREATE ON *.* TO 'root'@'localhost'"
 /usr/bin/mysql --defaults-extra-file=/tmp/nerocd-import-fault/root.cnf -e "DROP USER IF EXISTS 'nerocd_fixture_recovery'@'localhost'"
 /usr/bin/rm -- /tmp/nerocd-import-fault/root.cnf /tmp/nerocd-import-fault/recovery.cnf /tmp/nerocd-import-fault/owned
 /usr/bin/rmdir /tmp/nerocd-import-fault
fi`
	cleaned := false
	clean := func() {
		if !cleaned {
			storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "sh", "-ec", cleanup)
			cleaned = true
		}
	}
	t.Cleanup(clean)
	storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "sh", "-ec", setup)
	restore(partialTarget, partialDeployment, "f")
	wordpressRuntimeComponent(t, partialDeployment, "wordpress", false)
	count := storageRuntimeDocker(t, "exec", "--user", "mysql", database.ContainerID, "/usr/bin/mysql", "--defaults-extra-file=/tmp/nerocd-import-fault/root.cnf", "--batch", "--skip-column-names", "-e", "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='wordpress' AND table_name='wp_commentmeta'")
	if strings.TrimSpace(string(count)) != "0" {
		t.Fatal("partial MySQL import did not demonstrate DROP before denied CREATE")
	}
	clean()
	// Repairing fixture privileges does not release the operation or start writers.
	wordpressRuntimeComponent(t, partialDeployment, "wordpress", false)
	assertSource()
	t.Log("COMPOSE_MYSQL_PARTIAL_IMPORT_HELD_PASS actual DROP then denied CREATE; exact target writers stopped and ownership held; source preserved")
}

func wordpressRuntimeComponent(t *testing.T, operation domain.HostingOperation, name string, running bool) domain.HostingStackComponent {
	t.Helper()
	if operation.Result == nil || operation.Result.Stack == nil || operation.Release == nil {
		t.Fatal("component operation identity absent")
	}
	for _, component := range operation.Result.Stack.Components {
		if component.Component != name {
			continue
		}
		var observed []struct {
			ID              string `json:"Id"`
			Image           string
			Config          struct{ Labels map[string]string }
			State           struct{ Running bool }
			NetworkSettings struct {
				Ports map[string][]struct{ HostIP, HostPort string }
			}
		}
		raw := storageRuntimeDocker(t, "inspect", component.ContainerID)
		if json.Unmarshal(raw, &observed) != nil || len(observed) != 1 || observed[0].ID != component.ContainerID || observed[0].Image != component.ImageID || observed[0].State.Running != running || observed[0].Config.Labels["io.nerocd.binding"] != operation.Snapshot.BindingID || observed[0].Config.Labels["io.nerocd.operation"] != component.OperationID || observed[0].Config.Labels["io.nerocd.stack.component"] != name {
			t.Fatal("exact component identity/running state changed")
		}
		if name == "wordpress" && running {
			ports := observed[0].NetworkSettings.Ports["80/tcp"]
			if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(operation.Snapshot.HostPort) {
				t.Fatal("WordPress exact target endpoint changed")
			}
		}
		return component
	}
	t.Fatal("expected component absent")
	return domain.HostingStackComponent{}
}

func TestWordPressComponentBackupEnrollment(t *testing.T) {
	_, pg, _ := newHostingBuildAPIFixture(t)
	server := catalogRuntimeServer(t, pg)
	control := httptest.NewTLSServer(server)
	t.Cleanup(control.Close)
	f := &storageRuntimeFixture{t: t, control: control, client: control.Client()}
	f.token = createTestSession(t, server)
	caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingDatabaseMySQLCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseMySQLBackupCapability, domain.HostingDatabaseBackupComponentCapability, "hosting_compose_mysql_backup_v1", domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.RunTypeComposeDeploy}
	created := storageRuntimeJSON[app.CreatedRunnerEnrollment](f, "POST", "/api/v1/runner-enrollments", app.RunnerEnrollmentInput{RunnerID: "runner_wordpress_backup", RunnerName: "WordPress component backup", Capabilities: caps}, 201)
	credential := "ncr_wordpress_component_backup_fixture"
	enrolled := storageRuntimeAuthorizedJSON[app.ConsumedRunnerEnrollment](f, created.Token, "POST", "/api/v1/runner-enrollments/consume", app.RunnerEnrollmentConsumeInput{RequestID: "consume_wordpress_backup", CredentialHash: fmt.Sprintf("%x", sha256.Sum256([]byte(credential)))}, 200)
	client := runnerclient.New(control.URL, credential)
	client.HTTP = control.Client()
	id, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: true, RuntimeEnvironment: true})
	if err != nil || id != enrolled.Runner.ID {
		t.Fatal("actual WordPress backup enrollment/profile rejected")
	}
	if _, err := client.RuntimeExecutionIdentityWithOptions(t.Context(), runnerclient.RuntimeExecutionOptions{}); err == nil {
		t.Fatal("component backup capabilities accepted without configured execution")
	}
}
