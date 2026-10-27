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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

// This is the single real application gate. Setup and component inspection share
// the existing disposable DinD fixture; all desired changes use authorized APIs
// or the candidate UI, and the application's account/task/file use its real UI.
func TestHostingComposeVikunjaCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact approved Compose runtime profile")
	}
	f := newStorageRuntimeFixture(t)
	b := f.catalogBrowser
	password := hostingStorageRemoteRuntimeRandom(t) + "$cash$$${NEVER_EXPAND}"
	jwt := hostingStorageRemoteRuntimeRandom(t) + "$jwt$$${NEVER_EXPAND}"
	for name, value := range map[string]string{"Compose PostgreSQL": password, "Compose JWT": jwt} {
		storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: "proj_platform", Name: name, Value: value}, 201)
	}
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	resource := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+resourceID, nil, 200)
	if resource.Source.Kind != "compose" || resource.Source.Compose == nil {
		t.Fatal("browser did not save Compose source")
	}
	definition, err := domain.ParseHostingStack(*resource.Source.Compose)
	if err != nil || definition.OriginalHash != "sha256:c46d52024d31bad0a70342051c26dd4401ad80d29ec8867c22cca0e60e9a6925" || definition.PublicComponent != "vikunja" || definition.ContainerPort != 3456 {
		t.Fatal("original reference-bearing source or public component changed")
	}
	runtime := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	if runtime.Configuration.RuntimeGeneration != 1 || len(runtime.Configuration.Entries) != 5 {
		t.Fatal("browser runtime variables not saved")
	}
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	if first.Plan == nil || first.Plan.Stack == nil || first.Result == nil || first.Result.Stack == nil || first.Release == nil || first.Plan.CompilerVersion != domain.HostingStackCompilerVersion || len(first.Result.Stack.Components) != 3 || len(first.Result.Stack.Volumes) != 2 || first.Result.Uncertain || first.MutationHold {
		t.Fatal("whole-stack initial success missing")
	}
	verify := func(op domain.HostingOperation) {
		t.Helper()
		for _, component := range op.Result.Stack.Components {
			var ports map[string]json.RawMessage
			if json.Unmarshal(storageRuntimeDocker(t, "container", "inspect", component.ContainerID, "--format", "{{json .HostConfig.PortBindings}}"), &ports) != nil {
				t.Fatal("component port inspection")
			}
			if component.Component == "postgresql" && len(ports) != 0 {
				t.Fatal("PostgreSQL published a host port")
			}
			if component.Component == "init" {
				if component.Running || component.ExitCode != 0 || len(ports) != 0 {
					t.Fatal("upload initialization did not complete privately")
				}
				continue
			}
			if !component.Running || component.Health != "healthy" {
				t.Fatal("required component is not healthy")
			}
			// Inspect only this exact component's protected execution material. Compare
			// in memory; never print or persist environment bytes in evidence artifacts.
			raw := storageRuntimeDocker(t, "container", "inspect", component.ContainerID, "--format", "{{json .Config.Env}}")
			var environment []string
			if json.Unmarshal(raw, &environment) != nil {
				t.Fatal("component environment inspection")
			}
			clear(raw)
			values := map[string]string{}
			for _, value := range environment {
				name, v, ok := strings.Cut(value, "=")
				if ok {
					values[name] = v
				}
			}
			if component.Component == "postgresql" && values["POSTGRES_PASSWORD"] != password || component.Component == "vikunja" && (values["VIKUNJA_DATABASE_PASSWORD"] != password || values["VIKUNJA_SERVICE_SECRET"] != jwt) {
				t.Fatal("protected values were reinterpolated or changed")
			}
			clear(values)
			clear(environment)
		}
	}
	verify(first)
	uploadOwnership := composeRuntimeUploadOwnership(t, first)
	b.set("healthy", resourceID)
	b.await(t, "healthy")
	// Existing managed route/TLS authority, including its certificate trust root.
	f.stop()
	proxy := storageRuntimeJSON[domain.HostingProxy](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy", domain.HostingProxyInput{ControlProjectID: "proj_platform", IdempotencyKey: "compose-proxy", Configuration: domain.HostingProxyConfiguration{Email: "operator@example.test", CA: "letsencrypt-staging", ManagementPort: 19080}}, 202)
	certDir := filepath.Join(f.root, "proxy", proxy.ID, "certificates")
	if err = os.MkdirAll(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(f.root, "minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(certDir, "root.pem"), root, 0600); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.awaitLoadedProxy(proxy.ID, first.Release.DaemonID)
	storageRuntimeJSON[domain.HostingHostnameGrant](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: "proj_platform", Hostname: "catalog-runtime.example.test"}, 201)
	b.set("route", resourceID)
	f.proxyAwait(b.await(t, "routed"))
	verifyHTTPS := func(available ...bool) {
		t.Helper()
		transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:443")
		}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, err := client.Get("https://catalog-runtime.example.test/api/v1/info")
		if err != nil {
			t.Fatal("managed application HTTPS request failed")
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		wantHealthy := len(available) == 0 || available[0]
		if err != nil || wantHealthy && response.StatusCode != 200 || !wantHealthy && (response.StatusCode < 500 || response.StatusCode > 599) || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
			t.Fatal("application HTTPS trust/readiness missing")
		}
	}
	verifyHTTPS()
	b.set("app_create", resourceID)
	taskID := b.await(t, "app_created")
	logSession := composeRuntimeFollow(t, f, resourceID, first, "vikunja", false)
	b.set("redeploy", resourceID)
	second := f.await(b.await(t, "redeployed"), "succeeded")
	if second.Plan == nil || second.Result == nil || second.Result.Stack == nil || second.Snapshot.HostPort != first.Snapshot.HostPort || second.Snapshot.RuntimeConfiguration != first.Snapshot.RuntimeConfiguration || !reflect.DeepEqual(second.Result.Stack.Volumes, first.Result.Stack.Volumes) || second.Result.Stack.NetworkID != first.Result.Stack.NetworkID {
		t.Fatal("redeploy changed port, runtime references, volume origins or private network")
	}
	logPage := storageRuntimeJSON[domain.HostingRuntimeLogPage](f, "GET", "/api/v1/runtime-log-sessions/"+logSession.ID+"/logs", nil, 200)
	if logPage.Session.State != "target_changed" || logPage.Session.Target != logSession.Target {
		t.Fatal("component follow silently retargeted on redeploy")
	}
	composeRuntimeFollow(t, f, resourceID, second, "postgresql", true)
	composeRuntimeFollow(t, f, resourceID, second, "init", true)
	verify(second)
	if composeRuntimeUploadOwnership(t, second) != uploadOwnership {
		t.Fatal("redeploy changed retained upload directory ownership or mode")
	}
	f.awaitContinuity(resourceID, second.ID)
	verifyHTTPS()
	composeRuntimeWriterBarrier(t, first, second)
	b.set("app_recheck", resourceID)
	if b.await(t, "app_checked") != taskID {
		t.Fatal("fresh application login did not read original task")
	}
	runtimeAgain := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	if !reflect.DeepEqual(runtime.Configuration, runtimeAgain.Configuration) {
		t.Fatal("saved credential references changed on redeploy")
	}
	stopFollow := composeRuntimeFollow(t, f, resourceID, second, "vikunja", false)
	b.set("stop", resourceID)
	stopped := f.await(b.await(t, "stopped"), "succeeded")
	if stopped.StackStopPlan == nil || stopped.Result == nil || !stopped.Result.StackStopped || stopped.MutationHold || stopped.Result.Uncertain || stopped.StackStopPlan.Target.OperationID != second.ID {
		t.Fatal("UI stop did not confirm exact whole-stack removal")
	}
	for _, component := range second.Result.Stack.Components {
		if len(bytes.TrimSpace(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+component.ContainerID))) != 0 {
			t.Fatal("stopped writer remains present")
		}
	}
	if len(bytes.TrimSpace(storageRuntimeDocker(t, "container", "ls", "--all", "--quiet", "--filter", "label=com.docker.compose.project="+second.Snapshot.RuntimeNamespace))) != 0 {
		t.Fatal("stopped Compose project retains containers")
	}
	verifyHTTPS(false)
	stoppedLogs := storageRuntimeJSON[domain.HostingRuntimeLogPage](f, "GET", "/api/v1/runtime-log-sessions/"+stopFollow.ID+"/logs", nil, 200)
	if stoppedLogs.Session.State != "target_changed" || stoppedLogs.Session.Target != stopFollow.Target {
		t.Fatal("Stop did not end exact old log target")
	}
	lifecycle := storageRuntimeJSON[domain.HostingStackLifecycle](f, "GET", "/api/v1/resources/"+resourceID+"/stack-lifecycle", nil, 200)
	if lifecycle.State != "stopped" || lifecycle.StopOperationID != stopped.ID {
		t.Fatal("missing explicit stopped state")
	}
	b.set("start", resourceID)
	started := f.await(b.await(t, "started"), "succeeded")
	if started.Plan == nil || started.Plan.PreviousStopped == nil || started.Plan.PreviousStopped.StopOperationID != stopped.ID || started.Snapshot.StackStoppedOperationID != stopped.ID || started.Result == nil || started.Result.Stack == nil || !reflect.DeepEqual(started.Result.Stack.Volumes, second.Result.Stack.Volumes) || started.Result.Stack.NetworkID != second.Result.Stack.NetworkID || started.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration {
		t.Fatal("Start changed retained stack identity")
	}
	verify(started)
	if composeRuntimeUploadOwnership(t, started) != uploadOwnership {
		t.Fatal("Start changed upload ownership")
	}
	f.awaitContinuity(resourceID, started.ID)
	verifyHTTPS()
	composeRuntimeWriterBarrier(t, second, started)
	b.set("app_start_recheck", resourceID)
	if b.await(t, "app_start_checked") != taskID {
		t.Fatal("fresh login after Start lost application data")
	}
	// Provision a distinct already-deployed application through ordinary setup
	// APIs. Backup and destructive import themselves are requested through UI.
	target := storageRuntimeJSON[domain.Resource](f, "POST", "/api/v1/resources", app.ResourceInput{RequestID: strings.Repeat("d", 32), ProjectID: resource.ProjectID, ProjectEnvironmentID: resource.ProjectEnvironmentID, Name: "Compose database restore target", Kind: "application", Source: resource.Source}, 201)
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+target.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destinationID}, 201)
	targetEntries := append([]domain.HostingRuntimeEntry(nil), runtime.Configuration.Entries...)
	for i := range targetEntries {
		if targetEntries[i].Name == "SERVICE_URL_VIKUNJA" {
			value := "https://restore.catalog-runtime.example.test:25443"
			targetEntries[i].Value = &value
		}
	}
	storageRuntimeJSON[json.RawMessage](f, "PUT", "/api/v1/resources/"+target.ID+"/runtime-configuration", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: target.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("e", 32), Entries: targetEntries}, 200)
	targetDeploy := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+target.ID+"/deployments", domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: target.Generation, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "compose-import-target", ContainerPort: 3456, HostPort: first.Snapshot.HostPort + 1, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}}, 202)
	targetDeploy = f.await(targetDeploy.ID, "succeeded")
	verify(targetDeploy)
	for _, a := range started.Result.Stack.Volumes {
		for _, b := range targetDeploy.Result.Stack.Volumes {
			if a.VolumeName == b.VolumeName {
				t.Fatal("import target shares source storage")
			}
		}
	}
	storageRuntimeJSON[domain.HostingHostnameGrant](f, "POST", "/api/v1/servers/"+f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: resource.ProjectID, Hostname: "restore.catalog-runtime.example.test"}, 201)
	targetRoute := storageRuntimeJSON[domain.HostingRoute](f, "POST", "/api/v1/resources/"+target.ID+"/routes", domain.HostingRouteInput{IdempotencyKey: "compose-import-target-route", Hostname: "restore.catalog-runtime.example.test", PathPrefix: "/", ExpectedHealthyOperationID: targetDeploy.ID}, 202)
	f.proxyAwait(targetRoute.OperationID)
	b.set("database_backup", target.ID)
	backupID := b.await(t, "database_backed_up")
	awaitBackup := func(id string) domain.HostingDatabaseBackupOperation {
		t.Helper()
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
			operation := storageRuntimeJSON[domain.HostingDatabaseBackupOperation](f, "GET", "/api/v1/database-backups/"+id, nil, 200)
			if operation.State == "succeeded" && !operation.OwnershipHeld {
				return operation
			}
			if operation.State == "failed" || operation.State == "uncertain" {
				t.Fatalf("component database operation settled %s code=%s held=%t", operation.State, operation.FailureCode, operation.OwnershipHeld)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("component database operation deadline")
		return domain.HostingDatabaseBackupOperation{}
	}
	schedules := storageRuntimeJSON[[]domain.HostingDatabaseBackupSchedule](f, "GET", "/api/v1/resources/"+resourceID+"/database-backup-schedules", nil, 200)
	if len(schedules) != 1 || schedules[0].Enabled || schedules[0].Component != "postgresql" || schedules[0].Version != domain.HostingDatabaseBackupComponentScheduleVersion {
		t.Fatal("browser component schedule not disabled with immutable identity")
	}
	occurrences := storageRuntimeJSON[[]domain.HostingDatabaseBackupOccurrence](f, "GET", "/api/v1/resources/"+resourceID+"/database-backup-schedules/"+schedules[0].ID+"/occurrences", nil, 200)
	if len(occurrences) != 1 || occurrences[0].OperationID != backupID || occurrences[0].Component != "postgresql" || occurrences[0].ScheduleRevision != 1 {
		t.Fatal("browser scheduled source dump lost historical identity")
	}
	backup := awaitBackup(backupID)
	if backup.SourceComponent != "postgresql" || backup.SourceDeployedOperationID != started.ID || backup.Version != domain.HostingDatabaseBackupComponentVersion || backup.Blob == nil {
		t.Fatal("wrong logical component backup")
	}
	b.set("database_mutate", resourceID)
	b.await(t, "database_source_mutated")
	b.set("database_import", target.ID)
	imported := awaitBackup(b.await(t, "database_imported"))
	if imported.BackupID != backupID || imported.TargetResourceID != target.ID || imported.TargetComponent != "postgresql" || imported.TargetDeployedOperationID != targetDeploy.ID || imported.Blob == nil || *imported.Blob != *backup.Blob {
		t.Fatal("wrong immutable logical import target or archive")
	}
	for _, component := range targetDeploy.Result.Stack.Components {
		var actual struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if json.Unmarshal(storageRuntimeDocker(t, "container", "inspect", component.ContainerID, "--format", "{{json .State}}"), &actual) != nil || actual.Running != component.Running || !component.Running && actual.ExitCode != component.ExitCode {
			t.Fatal("import failed to preserve target writer/init states")
		}
	}
	b.set("database_check", target.ID)
	if b.await(t, "database_checked") != taskID {
		t.Fatal("restored application records missing")
	}
	options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
	bad := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: "compose-controlled-failure", ContainerPort: 3456, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: "/api/v1/required-missing-compose-readiness", TimeoutSeconds: 2}, StorageRouteContinuity: options.StorageRouteContinuity}
	failed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", bad, 202)
	held := f.await(failed.ID, "reconciliation_required")
	if !held.MutationHold || held.Result == nil || !held.Result.Uncertain || held.Result.Ready || held.Result.Stack == nil || !held.Result.Stack.ObservationComplete || len(held.Result.Stack.Components) != 3 {
		t.Fatal("failed replacement omitted actual component state or released uncertainty")
	}
	bad.IdempotencyKey = "compose-forbidden-competing"
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", bad, 409)
	f.stop()
	journal, err := os.ReadFile(filepath.Join(f.root, "journal", "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _ := json.Marshal([]any{resource, runtime, first, second, stopped, started, held})
	for _, value := range []string{password, jwt} {
		if bytes.Contains(journal, []byte(value)) || bytes.Contains(ordinary, []byte(value)) || strings.Contains(f.logs.String(), value) {
			t.Fatal("managed credential entered ordinary operation/journal/log output")
		}
	}
	clear(journal)
	clear(ordinary)
	t.Log("real Vikunja/PostgreSQL: shipped Compose example, successful init job, UI credentials/destination/deploy, trusted HTTPS, fresh login/task/upload/new writes after redeploy, stable refs/volumes/upload ownership, private DB, no overlapping writers, UI Stop removes all writers, UI Start retains records/uploads/credentials/HTTPS, logical DB backup/UI distinct-target transactional replacement/source later-write exclusion/target independent writes (uploads excluded), controlled failed replacement held")
	b.set("complete", resourceID)
	b.await(t, "done")
}

func composeRuntimeUploadOwnership(t *testing.T, operation domain.HostingOperation) string {
	t.Helper()
	for _, volume := range operation.Result.Stack.Volumes {
		if volume.Name != "vikunja-data" {
			continue
		}
		info, err := os.Stat(filepath.Join("/var/lib/docker/volumes", volume.VolumeName, "_data"))
		if err != nil {
			t.Fatal("upload directory ownership unavailable")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 1000 || owner.Gid != 1000 || info.Mode().Perm() != 0755 {
			t.Fatal("upload directory ownership or mode changed")
		}
		return volume.VolumeName + ":1000:1000:755"
	}
	t.Fatal("owned upload volume missing")
	return ""
}

func composeRuntimeWriterBarrier(t *testing.T, previous, current domain.HostingOperation) {
	t.Helper()
	raw := storageRuntimeDocker(t, "events", "--since", previous.CreatedAt.Format(time.RFC3339Nano), "--until", time.Now().Format(time.RFC3339Nano), "--filter", "type=container", "--filter", "label=io.nerocd.binding="+current.BindingID, "--format", "{{json .}}")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	defer clear(raw)
	stopped := map[string]int64{}
	started := map[string]int64{}
	for decoder.More() {
		var event struct {
			Action   string
			TimeNano int64
			Actor    struct{ ID string }
		}
		if decoder.Decode(&event) != nil {
			t.Fatal("Docker writer event evidence malformed")
		}
		if event.Action == "die" {
			stopped[event.Actor.ID] = event.TimeNano
		}
		if event.Action == "start" {
			started[event.Actor.ID] = event.TimeNano
		}
	}
	earliest := int64(0)
	for _, component := range current.Result.Stack.Components {
		stamp := started[component.ContainerID]
		if stamp == 0 {
			t.Fatal("new component start evidence missing")
		}
		if earliest == 0 || stamp < earliest {
			earliest = stamp
		}
	}
	for _, component := range previous.Result.Stack.Components {
		if stopped[component.ContainerID] == 0 || stopped[component.ContainerID] > earliest {
			t.Fatal("new stack writer started before every predecessor stopped")
		}
	}
}

// The explicit acceptance init command has finite output; EOF is disconnected.
func composeRuntimeFollow(t *testing.T, f *storageRuntimeFixture, resourceID string, operation domain.HostingOperation, component string, closeSession bool) domain.HostingRuntimeLogSession {
	t.Helper()
	requestID := hostingStorageRemoteRuntimeRandom(t)[:32]
	input := domain.HostingRuntimeLogOpen{RequestID: requestID, RequestExpiresAt: time.Now().UTC().Add(25 * time.Second), Component: component, ExpectedOperationID: operation.ID}
	session := storageRuntimeJSON[domain.HostingRuntimeLogSession](f, "POST", "/api/v1/resources/"+resourceID+"/runtime-log-sessions", input, 200)
	deadline := time.Now().Add(10 * time.Second)
	for {
		page := storageRuntimeJSON[domain.HostingRuntimeLogPage](f, "GET", "/api/v1/runtime-log-sessions/"+session.ID+"/logs", nil, 200)
		if page.Session.Target.Component != component || page.Session.Target.OperationID != operation.ID {
			t.Fatal("component log target changed")
		}
		for _, event := range page.Items {
			if event.Component != component {
				t.Fatal("cross-component log output")
			}
		}
		if component == "init" {
			found := false
			for _, event := range page.Items {
				found = found || strings.Contains(event.Message, "nerocd-init-log-fixture")
			}
			if found && (page.Session.State == "following" || page.Session.State == "disconnected") {
				break
			}
		} else if page.Session.State == "following" && len(page.Items) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("component collector did not follow/read", component, page.Session.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if closeSession {
		storageRuntimeJSON[domain.HostingRuntimeLogSession](f, "DELETE", "/api/v1/runtime-log-sessions/"+session.ID, nil, 200)
	}
	return session
}
