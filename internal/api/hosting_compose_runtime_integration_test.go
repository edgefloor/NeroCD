//go:build linux && nerocd_proxy_fixture

package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	"nerocd/internal/artifactfs"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

// This is the single real application gate. Setup and component inspection share
// the existing disposable DinD fixture; all desired changes use authorized APIs
// or the candidate UI, and the application's account/task/file use its real UI.
func TestHostingComposeVikunjaCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_COMPOSE_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact approved Compose runtime profile")
	}
	f := newStorageRuntimeFixture(t, true)
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
	volumeEligibility := storageRuntimeJSON[domain.HostingStorageBackupVolumes](f, "GET", "/api/v1/resources/"+resourceID+"/storage-backup-volumes", nil, 200)
	selectedEligible := false
	selectedReason := "missing_volume"
	for _, volume := range volumeEligibility.Volumes {
		if volume.Volume == "vikunja-data" {
			selectedEligible, selectedReason = volume.Eligible, volume.Reason
		}
	}
	safeEligibilityReason := func(reason string) string {
		switch reason {
		case "", "missing_volume", "unavailable", "unsupported_source", "archive_unavailable", "deployment_changed", "runner_unavailable", "unused_volume", "volume_unavailable", "requires_runtime_ownership_verification", "no_eligible_volumes":
			return reason
		default:
			return "unknown"
		}
	}
	resourceMatches := volumeEligibility.ResourceID == resourceID
	generationMatches := volumeEligibility.ResourceGeneration == resource.Generation
	deploymentMatches := volumeEligibility.OperationID == first.ID
	t.Logf("Compose schedule prerequisite available=%t resource_matches=%t generation_matches=%t deployment_matches=%t selected_eligible=%t reason=%s selected_reason=%s", volumeEligibility.Available, resourceMatches, generationMatches, deploymentMatches, selectedEligible, safeEligibilityReason(volumeEligibility.Reason), safeEligibilityReason(selectedReason))
	if !volumeEligibility.Available || !resourceMatches || !generationMatches || !deploymentMatches || !selectedEligible {
		t.Fatal("Compose schedule prerequisite eligibility failed")
	}
	b.set("volume_schedule", resourceID)
	volumeScheduleID := b.await(t, "volume_scheduled")
	volumeSchedule := composeRuntimeVolumeSchedule(t, f, resourceID, volumeScheduleID, false, 1)
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
	if !reflect.DeepEqual(volumeSchedule, composeRuntimeVolumeSchedule(t, f, resourceID, volumeScheduleID, false, 1)) {
		t.Fatal("redeploy changed saved named-volume schedule identity")
	}
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
	if !reflect.DeepEqual(volumeSchedule, composeRuntimeVolumeSchedule(t, f, resourceID, volumeScheduleID, false, 1)) {
		t.Fatal("Start changed saved named-volume schedule identity")
	}
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
	composeRuntimeVolumeArchive(t, f, resource, started, taskID, volumeScheduleID)
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

func TestComposeVolumeScheduleFixtureRequestContract(t *testing.T) {
	server, original, owner, _, _ := archiveHTTPFixture(t)
	original.resource.Kind = "application"
	repo := &storageScheduleHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/resources/" + original.resource.ID + "/storage-backup-schedules"
	body := `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"expected_version":0,"frequency":"every_minute","timezone":"UTC","enabled":false,"volume":"vikunja-data","expected_deployed_operation_id":"hop_fixture","acknowledge_interruption":true}`
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 200)
	if repo.input.Volume != "vikunja-data" || repo.input.ExpectedDeployedOperationID != "hop_fixture" || !repo.input.AcknowledgeInterruption || repo.input.Enabled || repo.input.ExpectedVersion != 0 {
		t.Fatal("production schedule decoder lost fixture create contract")
	}
	update := strings.Replace(strings.Replace(body, `"expected_version":0`, `"expected_version":1`, 1), `"enabled":false`, `"enabled":true`, 1)
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, update, "application/json", 200)
	if !repo.input.Enabled || repo.input.ExpectedVersion != 1 || repo.input.Volume != "vikunja-data" || !repo.input.AcknowledgeInterruption {
		t.Fatal("production schedule decoder lost fixture enable contract")
	}
	disable := strings.Replace(strings.Replace(body, `"expected_version":0`, `"expected_version":2`, 1), `,"expected_deployed_operation_id":"hop_fixture","acknowledge_interruption":true`, "", 1)
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, disable, "application/json", 200)
	if repo.input.Enabled || repo.input.ExpectedVersion != 2 || repo.input.Volume != "vikunja-data" {
		t.Fatal("production schedule decoder lost fixture disable contract")
	}
	for _, invalid := range []string{
		strings.Replace(body, `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`, `fixture-invalid-request`, 1),
		strings.Replace(body, `"volume":"vikunja-data"`, `"volume":"../vikunja-data"`, 1),
		strings.Replace(body, `"acknowledge_interruption":true`, `"acknowledge_interruption":null`, 1),
	} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
}

// A disabled schedule must survive replacement and Stop/Start unchanged. Its
// occurrence resolves current deployment authority when the owner enables it.
func composeRuntimeVolumeSchedule(t *testing.T, f *storageRuntimeFixture, resourceID, scheduleID string, enabled bool, revision int64) domain.HostingStorageBackupSchedule {
	t.Helper()
	schedules := storageRuntimeJSON[[]domain.HostingStorageBackupSchedule](f, "GET", "/api/v1/resources/"+resourceID+"/storage-backup-schedules", nil, 200)
	if len(schedules) != 1 || schedules[0].ID != scheduleID || schedules[0].ResourceID != resourceID || schedules[0].Version != "managed-compose.local-volume-archive.schedule.v1" || schedules[0].Volume != "vikunja-data" || !schedules[0].AcknowledgeInterruption || schedules[0].Enabled != enabled || schedules[0].Revision != revision || schedules[0].RemoteTarget != nil {
		t.Fatal("named-volume schedule lost immutable identity, consent, revision or local scope")
	}
	return schedules[0]
}

// Read the real local CAS after UI admission, then exercise helper failure on
// the same selected volume without replacing the deployed stack.
func composeRuntimeVolumeArchive(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, deployed domain.HostingOperation, taskID, scheduleID string) {
	t.Helper()
	volume := ""
	for _, v := range deployed.Result.Stack.Volumes {
		if v.Name == "vikunja-data" {
			volume = v.VolumeName
		}
	}
	if volume == "" {
		t.Fatal("selected upload volume missing")
	}
	root := filepath.Join("/var/lib/docker/volumes", volume, "_data")
	var inspected []struct {
		Name, Mountpoint string
		Labels           map[string]string
	}
	if json.Unmarshal(storageRuntimeDocker(t, "volume", "inspect", volume), &inspected) != nil || len(inspected) != 1 || inspected[0].Name != volume || inspected[0].Mountpoint != root || inspected[0].Labels["io.nerocd.binding"] != deployed.BindingID {
		t.Fatal("selected upload volume ownership/path mismatch")
	}
	before := composeRuntimeUploadSnapshot(t, root)
	if len(before) < 2 {
		t.Fatal("upload archive fixture contains no application file")
	}
	unchanged := map[string]string{}
	for _, component := range deployed.Result.Stack.Components {
		if component.Component != "vikunja" {
			unchanged[component.ContainerID] = string(storageRuntimeDocker(t, "container", "inspect", component.ContainerID, "--format", "{{.State.Running}} {{.State.ExitCode}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}"))
		}
	}
	assertWriters := func(since string) {
		t.Helper()
		for _, c := range deployed.Result.Stack.Components {
			if c.Component == "vikunja" {
				if !storageArchiveWriterRunning(t, c.ContainerID) {
					t.Fatal("archive did not resume exact application writer")
				}
				continue
			}
			if string(storageRuntimeDocker(t, "container", "inspect", c.ContainerID, "--format", "{{.State.Running}} {{.State.ExitCode}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}")) != unchanged[c.ContainerID] {
				t.Fatal("archive touched PostgreSQL or completed init state")
			}
			for _, action := range []string{"start", "die", "stop"} {
				if len(bytes.TrimSpace(storageRuntimeDocker(t, "events", "--since", since, "--until", time.Now().Format(time.RFC3339Nano), "--filter", "container="+c.ContainerID, "--filter", "event="+action, "--format", "{{.Action}}"))) != 0 {
					t.Fatal("archive interrupted unselected component")
				}
			}
		}
		if !reflect.DeepEqual(before, composeRuntimeUploadSnapshot(t, root)) {
			t.Fatal("archive changed upload bytes or numeric metadata")
		}
	}
	since := time.Now().Format(time.RFC3339Nano)
	f.catalogBrowser.set("volume_schedule_run", scheduleID)
	backupID := f.catalogBrowser.await(t, "volume_schedule_backed_up")
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	op := f.await(backup.OperationID, "succeeded")
	if op.StorageArchive == nil || op.StorageArchive.Result == nil || op.StorageArchive.Result.ArchiveOutcome != "succeeded" || op.StorageArchive.Result.ResumeOutcome != "resumed" || op.StorageArchive.Result.Uncertain || op.MutationHold {
		t.Fatal("Compose volume backup did not settle with resumed writers")
	}
	backup = storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	if backup.State != "succeeded" || backup.Blob == nil {
		t.Fatal("Compose volume archive metadata missing")
	}
	metadata := storageRuntimeJSON[struct {
		Volume                    string
		SourceDeployedOperationID string `json:"source_deployed_operation_id"`
	}](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	if metadata.Volume != "vikunja-data" || metadata.SourceDeployedOperationID != deployed.ID || op.StorageArchive.Plan.CompilerVersion != "managed-compose.local-volume-archive.v1" || op.StorageArchive.Plan.PolicyVersion != "managed-compose.local-volume-archive.v1" {
		t.Fatal("Compose backup lost selected volume/deployment family")
	}
	pool, err := artifactfs.OpenStore(filepath.Join(f.root, "archive-artifacts"), domain.HostingStorageArchiveMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	file, blob, err := pool.Open(t.Context(), strings.TrimPrefix(backup.Blob.SHA256, "sha256:"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if blob.Size != backup.Blob.SizeBytes {
		t.Fatal("archive CAS size mismatch")
	}
	actual := map[string]string{}
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, reader); err != nil {
			t.Fatal(err)
		}
		actual[header.Name] = fmt.Sprintf("%d:%d:%o:%d:%x", header.Uid, header.Gid, header.Mode, header.Typeflag, hash.Sum(nil))
	}
	if !reflect.DeepEqual(before, actual) {
		t.Fatal("local CAS archive differs from real upload bytes/UID/GID/modes")
	}
	t.Logf("Compose local archive backup=%s volume=vikunja-data CAS_bytes=%d members=%d exact writer resumed; PostgreSQL/init untouched", backupID, blob.Size, len(actual))
	composeRuntimeVolumeSchedule(t, f, resource.ID, scheduleID, false, 3)
	occurrences := storageRuntimeJSON[[]domain.HostingStorageBackupOccurrence](f, "GET", "/api/v1/resources/"+resource.ID+"/storage-backup-schedules/"+scheduleID+"/occurrences", nil, 200)
	if len(occurrences) != 1 || occurrences[0].BackupID != backupID || occurrences[0].ScheduleRevision != 2 || occurrences[0].Volume != "vikunja-data" || occurrences[0].OwnershipHeld {
		t.Fatal("scheduled volume archive lost occurrence identity or retained ownership")
	}
	assertWriters(since)
	// O_EXCL-equivalent creation rejects a preexisting fixture path. The codec
	// rejects this FIFO via no-follow metadata before opening it.
	fifo := filepath.Join(root, "nerocd-compose-archive-fixture.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(fifo); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	since = time.Now().Format(time.RFC3339Nano)
	failedBackup := storageRuntimeJSON[domain.HostingStorageBackup](f, "POST", "/api/v1/resources/"+resource.ID+"/storage-backups", map[string]any{"request_id": strings.Repeat("f", 32), "expected_resource_generation": resource.Generation, "volume": "vikunja-data", "expected_deployed_operation_id": deployed.ID, "acknowledge_interruption": true}, 202)
	failed := f.await(failedBackup.OperationID, "failed")
	if failed.StorageArchive == nil || failed.StorageArchive.Result == nil || failed.StorageArchive.Result.ArchiveOutcome != "failed" || failed.StorageArchive.Result.ResumeOutcome != "resumed" || failed.StorageArchive.Result.Uncertain || failed.MutationHold {
		t.Fatal("failed Compose archive did not settle with compensated writers")
	}
	phases := map[string]bool{}
	for _, progress := range failed.StorageArchive.Progress {
		phases[progress.Phase] = true
	}
	if !phases["stopped"] || !phases["helper_identified"] || !phases["resumed"] || phases["archive_published"] {
		t.Fatal("Compose archive failure missed quiescence/helper/resume or published an archive")
	}
	failedBackup = storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+failedBackup.ID, nil, 200)
	if failedBackup.State != "failed" || failedBackup.Blob != nil {
		t.Fatal("failed Compose archive published backup metadata")
	}
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	assertWriters(since)
	f.catalogBrowser.set("volume_recheck", failedBackup.ID)
	if f.catalogBrowser.await(t, "volume_checked") != taskID {
		t.Fatal("application task/upload/new writes lost after volume backup compensation")
	}
	t.Logf("Compose local archive scheduled_backup=%s failed_backup=%s stable named volume preserved after redeploy and Start", backupID, failedBackup.ID)
}

func composeRuntimeUploadSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unexpected upload fixture file type")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("upload numeric metadata missing")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		kind := byte(tar.TypeDir)
		hash := sha256.New()
		if info.Mode().IsRegular() {
			kind = tar.TypeReg
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, readErr := io.Copy(hash, file)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		result[name] = fmt.Sprintf("%d:%d:%o:%d:%x", owner.Uid, owner.Gid, info.Mode().Perm(), kind, hash.Sum(nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
