//go:build linux && nerocd_proxy_fixture

package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	const publicImage = "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576"
	registryPassword := hostingStorageRemoteRuntimeRandom(t)
	storageRuntimeDocker(t, "pull", publicImage)
	pinnedImageID := strings.TrimSpace(string(storageRuntimeDocker(t, "image", "inspect", publicImage, "--format", "{{.Id}}")))
	protectedRepository := registryRuntimeProtectedImages(t, f, registryPassword, publicImage)
	manifestRequest, _ := http.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1:25001/v2/"+storageRuntimeOwner+"/manifests/a", nil)
	manifestRequest.SetBasicAuth("fixture", registryPassword)
	manifestRequest.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	manifest, manifestErr := f.client.Do(manifestRequest)
	if manifestErr != nil {
		t.Fatal("protected manifest lookup")
	}
	manifestBytes, readErr := io.ReadAll(io.LimitReader(manifest.Body, (1<<20)+1))
	_ = manifest.Body.Close()
	if readErr != nil || len(manifestBytes) > 1<<20 {
		t.Fatal("protected manifest payload")
	}
	digest := manifest.Header.Get("Docker-Content-Digest")
	if manifest.StatusCode != 200 || len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") || fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBytes)) != digest {
		t.Fatal("protected manifest identity")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
		t.Fatal("protected manifest digest")
	}
	protectedImage := protectedRepository + ":a@" + digest
	// Registry2 retains raw manifest bytes. JSON whitespace changes only the
	// immutable manifest digest, preserving every config/layer descriptor.
	manifestB := append(append([]byte(nil), manifestBytes...), '\n')
	sumB := sha256.Sum256(manifestB)
	digestB := fmt.Sprintf("sha256:%x", sumB)
	if digestB == digest {
		t.Fatal("replacement manifest identity unchanged")
	}
	putManifest, _ := http.NewRequestWithContext(t.Context(), "PUT", "http://127.0.0.1:25001/v2/"+storageRuntimeOwner+"/manifests/b", bytes.NewReader(manifestB))
	putManifest.SetBasicAuth("fixture", registryPassword)
	putManifest.Header.Set("Content-Type", manifest.Header.Get("Content-Type"))
	published, publishErr := f.client.Do(putManifest)
	if publishErr != nil {
		t.Fatal("replacement manifest publish")
	}
	_ = published.Body.Close()
	if published.StatusCode != 201 || published.Header.Get("Docker-Content-Digest") != digestB {
		t.Fatal("replacement manifest content identity")
	}
	getManifest, _ := http.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1:25001/v2/"+storageRuntimeOwner+"/manifests/b", nil)
	getManifest.SetBasicAuth("fixture", registryPassword)
	getManifest.Header.Set("Accept", manifest.Header.Get("Content-Type"))
	storedManifest, storedErr := f.client.Do(getManifest)
	if storedErr != nil {
		t.Fatal("replacement manifest read")
	}
	storedBytes, storedReadErr := io.ReadAll(io.LimitReader(storedManifest.Body, (1<<20)+1))
	_ = storedManifest.Body.Close()
	if storedReadErr != nil || storedManifest.StatusCode != 200 || storedManifest.Header.Get("Docker-Content-Digest") != digestB || !bytes.Equal(storedBytes, manifestB) {
		t.Fatal("replacement manifest representation changed")
	}
	replacementImage := protectedRepository + ":b@" + digestB
	registryValue := func(value string) string {
		raw, _ := json.Marshal(map[string]string{"registry": "127.0.0.1:25001", "username": "fixture", "password": value})
		return string(raw)
	}
	registrySecrets := map[string]domain.ManagedSecret{}
	for name, value := range map[string]string{"correct": registryPassword, "wrong": "incorrect", "revoked": registryPassword} {
		registrySecrets[name] = storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: "proj_platform", Name: "Compose registry " + name, Value: registryValue(value)}, 201)
	}
	assertReferenceAbsent := func(reference string) {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "image", "inspect", reference).CombinedOutput()
		if err == nil || !strings.Contains(strings.ToLower(string(output)), "no such image") && !strings.Contains(strings.ToLower(string(output)), "no such object") {
			t.Fatal("protected Compose reference is present")
		}
	}
	assertReferenceAbsent(protectedImage)
	if registrySecrets["correct"].ID == "" || registrySecrets["wrong"].ID == "" || registrySecrets["correct"].ID == registrySecrets["wrong"].ID || registrySecrets["revoked"].ID == "" || registrySecrets["revoked"].ID == registrySecrets["correct"].ID || registrySecrets["revoked"].ID == registrySecrets["wrong"].ID {
		t.Fatal("distinct registry secret identities required")
	}
	registryRef := domain.HostingRegistryCredentialRef{SecretID: registrySecrets["correct"].ID, Version: 1}
	b.set("install", f.destinationID)
	resourceID := b.await(t, "installed")
	resource := storageRuntimeJSON[domain.Resource](f, "GET", "/api/v1/resources/"+resourceID, nil, 200)
	installation := storageRuntimeJSON[domain.HostingCatalogInstallation](f, "GET", "/api/v1/resources/"+resourceID+"/catalog-installation", nil, 200)
	b.mu.Lock()
	original, retried := b.original, b.retried
	b.mu.Unlock()
	if original == nil || retried == nil || domain.HostingDocumentHash(original) != domain.HostingDocumentHash(installation) || domain.HostingDocumentHash(retried) != domain.HostingDocumentHash(installation) {
		t.Fatal("catalog response-loss retry changed installation")
	}
	if resource.Generation != 1 || resource.Source.Kind != "compose" || resource.Source.Compose == nil || installation.Request.TemplateID != "vikunja-with-postgresql" || installation.Request.ImmutableVersion != domain.HostingCatalogVikunjaVersion || installation.Request.PublicURL != "https://catalog-runtime.example.test:25443" || installation.Resource.ID != resourceID || installation.Binding.DestinationID != f.destinationID || installation.StorageRef != (domain.HostingStorageRef{}) || installation.Template.Definition.Compose == nil || !reflect.DeepEqual(resource.Source.Compose, installation.Template.Definition.Compose) {
		t.Fatal("catalog Compose installation identity or source changed")
	}
	definition, err := domain.ParseHostingStack(*resource.Source.Compose)
	if err != nil || definition.OriginalHash != "sha256:2ed2a1cc22b4c020f26e214d4e96a419eb56445a9d14f87f2df703a7ee9de767" || definition.PublicComponent != "vikunja" || definition.ContainerPort != 3456 || strings.Count(resource.Source.Compose.Original, publicImage) != 1 {
		t.Fatal("catalog original Compose source changed before first deployment")
	}
	runtime := storageRuntimeJSON[struct {
		Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
	}](f, "GET", "/api/v1/resources/"+resourceID+"/runtime-configuration", nil, 200)
	document, _, canonicalErr := domain.CanonicalHostingRuntimeDocument(runtime.Configuration.Entries)
	if canonicalErr != nil || runtime.Configuration.RuntimeGeneration != 1 || len(runtime.Configuration.Entries) != 5 || installation.RuntimeConfiguration.Generation != 1 || installation.RuntimeConfiguration.Hash != domain.HostingDocumentHash(document) || len(installation.Credentials) != 2 {
		t.Fatal("catalog canonical runtime references missing")
	}
	values := map[string]string{}
	for _, entry := range runtime.Configuration.Entries {
		switch entry.Name {
		case "SERVICE_URL_VIKUNJA", "SERVICE_USER_POSTGRESQL", "POSTGRESQL_DATABASE":
			want := "vikunja"
			if entry.Name == "SERVICE_URL_VIKUNJA" {
				want = installation.Request.PublicURL
			}
			if entry.Kind != "literal" || entry.Value == nil || *entry.Value != want || entry.Secret != nil {
				t.Fatal("catalog literal runtime value changed")
			}
		case "SERVICE_PASSWORD_POSTGRESQL", "SERVICE_PASSWORD_JWTSECRET":
			if entry.Kind != "managed_secret" || entry.Secret == nil || entry.Secret.Version != 1 || entry.Secret.SecretID == "" || entry.Value != nil {
				t.Fatal("catalog generated runtime secret reference missing")
			}
			found := false
			for _, credential := range installation.Credentials {
				found = found || reflect.DeepEqual(entry, credential)
			}
			if !found {
				t.Fatal("catalog runtime credential reference differs")
			}
			secret := storageRuntimeJSON[app.ManagedSecretReveal](f, "POST", "/api/v1/managed-secrets/"+entry.Secret.SecretID+"/reveal", app.ManagedSecretRevealInput{Version: 1}, 200)
			if secret.Value == "" {
				t.Fatal("catalog generated runtime secret empty")
			}
			values[entry.Name] = secret.Value
		default:
			t.Fatal("unexpected catalog runtime variable")
		}
	}
	password, jwt := values["SERVICE_PASSWORD_POSTGRESQL"], values["SERVICE_PASSWORD_JWTSECRET"]
	if password == "" || jwt == "" || password == jwt {
		t.Fatal("catalog generated secret identities missing")
	}
	clear(values)
	b.set("deploy", resourceID)
	first := f.await(b.await(t, "deployed"), "succeeded")
	if first.Plan == nil || first.Plan.Stack == nil || first.Result == nil || first.Result.Stack == nil || first.Release == nil || first.Plan.CompilerVersion != domain.HostingStackCompilerVersion || len(first.Result.Stack.Components) != 3 || len(first.Result.Stack.Volumes) != 2 || first.Result.Uncertain || first.MutationHold {
		t.Fatal("whole-stack initial success missing")
	}
	verify := func(op domain.HostingOperation, expectedRegistry domain.HostingRegistryCredentialRef) {
		t.Helper()
		if op.Snapshot.RegistryCredential != expectedRegistry {
			t.Fatal("Compose registry snapshot reference changed")
		}
		for _, component := range op.Result.Stack.Components {
			var ports map[string]json.RawMessage
			if json.Unmarshal(storageRuntimeDocker(t, "container", "inspect", component.ContainerID, "--format", "{{json .HostConfig.PortBindings}}"), &ports) != nil {
				t.Fatal("component port inspection")
			}
			if component.Component == "vikunja" {
				actual := bytes.TrimSpace(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.Image}}"))
				if string(actual) != pinnedImageID {
					t.Fatal("application image differs from pinned Vikunja content")
				}
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
	if first.Snapshot.ResourceGeneration != 1 || first.Snapshot.Stack.Generation != 1 || first.Snapshot.Stack.Hash != domain.HostingDocumentHash(definition) || first.Snapshot.RuntimeConfiguration != installation.RuntimeConfiguration {
		t.Fatal("initial catalog deployment lost immutable source or runtime reference")
	}
	verify(first, domain.HostingRegistryCredentialRef{})
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
	containersBefore := map[string][]byte{}
	for _, component := range first.Result.Stack.Components {
		containersBefore[component.ContainerID] = storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.Id}} {{json .Config}} {{json .Mounts}} {{.State.Running}} {{.State.StartedAt}}")
	}
	routesBefore := storageRuntimeJSON[[]domain.HostingRoute](f, "GET", "/api/v1/resources/"+resourceID+"/routes", nil, 200)
	proxyBefore := storageRuntimeJSON[domain.HostingProxy](f, "GET", "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
	changedCompose := *resource.Source.Compose
	changedCompose.Original = strings.Replace(changedCompose.Original, publicImage, replacementImage, 1)
	changedCompose.Original = strings.Replace(changedCompose.Original, "        if [", "        printf 'nerocd-init-log-fixture\\n'\n        if [", 1)
	changedSource := resource.Source
	changedSource.Compose = &changedCompose
	oldGeneration := resource.Generation
	resource = storageRuntimeJSON[domain.Resource](f, "PATCH", "/api/v1/resources/"+resourceID, app.ResourcePatch{ExpectedGeneration: oldGeneration, Source: &changedSource}, 200)
	updatedDefinition, updatedErr := domain.ParseHostingStack(*resource.Source.Compose)
	if updatedErr != nil || resource.Generation != oldGeneration+1 || resource.Source.Compose.Original != changedCompose.Original || updatedDefinition.OriginalHash == definition.OriginalHash {
		t.Fatal("immutable replacement source update")
	}
	protectedImage = replacementImage
	assertReferenceAbsent(protectedImage)
	originalOperation := storageRuntimeJSON[domain.HostingOperation](f, "GET", "/api/v1/hosting-operations/"+first.ID, nil, 200)
	if !reflect.DeepEqual(originalOperation.Snapshot, first.Snapshot) || originalOperation.StackSource == nil || originalOperation.StackSource.OriginalHash != definition.OriginalHash {
		t.Fatal("source update changed deployed immutable A")
	}
	denialOptions := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+resourceID+"/storage-deployment-options", nil, 200)
	if denialOptions.StackRef == nil || denialOptions.StackRef.Generation != resource.Generation || denialOptions.StackRef.Hash != domain.HostingDocumentHash(updatedDefinition) {
		t.Fatal("saved replacement Stack identity")
	}
	deniedBody := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, IdempotencyKey: "compose-registry-wrong", ContainerPort: 3456, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}, StorageRouteContinuity: denialOptions.StorageRouteContinuity, RegistryCredential: &domain.HostingRegistryCredentialRef{SecretID: registrySecrets["wrong"].ID, Version: 1}}
	assertReferenceAbsent(protectedImage)
	denied := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", deniedBody, 202)
	if denied.Snapshot.RegistryCredential != *deniedBody.RegistryCredential || denied.Snapshot.ResourceGeneration != resource.Generation || denied.Snapshot.Stack != *denialOptions.StackRef {
		t.Fatal("admitted wrong credential/source snapshot differs from request")
	}
	if replay := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", deniedBody, 202); replay.ID != denied.ID {
		t.Fatal("Compose registry identical request changed operation")
	}
	denied = f.await(denied.ID, "failed")
	if denied.MutationHold || denied.Result != nil && denied.Result.Uncertain {
		t.Fatal("denied pull acquired mutation hold")
	}
	deniedBody.RegistryCredential = &registryRef
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", deniedBody, 409)
	revoked := registrySecrets["revoked"]
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/managed-secrets/"+revoked.ID+"/revoke", app.ManagedSecretRevokeInput{ExpectedVersion: 1}, 200)
	deniedBody.IdempotencyKey = "compose-registry-revoked"
	deniedBody.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: revoked.ID, Version: 1}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+resourceID+"/deployments", deniedBody, 409)
	for id, before := range containersBefore {
		if !bytes.Equal(before, storageRuntimeDocker(t, "inspect", id, "--format", "{{.Id}} {{json .Config}} {{json .Mounts}} {{.State.Running}} {{.State.StartedAt}}")) {
			t.Fatal("denied Compose credentials mutated previous component")
		}
	}
	if !reflect.DeepEqual(routesBefore, storageRuntimeJSON[[]domain.HostingRoute](f, "GET", "/api/v1/resources/"+resourceID+"/routes", nil, 200)) || !reflect.DeepEqual(proxyBefore, storageRuntimeJSON[domain.HostingProxy](f, "GET", "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)) {
		t.Fatal("denied Compose credentials changed loaded route or proxy holds")
	}
	verifyHTTPS()
	assertReferenceAbsent(protectedImage)
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
	verify(second, registryRef)
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
	expectedRuntime := runtime.Configuration
	expectedRuntime.ResourceGeneration = resource.Generation
	if !reflect.DeepEqual(expectedRuntime, runtimeAgain.Configuration) {
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
	storageRuntimeDocker(t, "image", "rm", "--force", pinnedImageID)
	assertReferenceAbsent(pinnedImageID)
	assertReferenceAbsent(protectedImage)
	b.set("start", resourceID)
	started := f.await(b.await(t, "started"), "succeeded")
	if started.Plan == nil || started.Plan.PreviousStopped == nil || started.Plan.PreviousStopped.StopOperationID != stopped.ID || started.Snapshot.StackStoppedOperationID != stopped.ID || started.Result == nil || started.Result.Stack == nil || !reflect.DeepEqual(started.Result.Stack.Volumes, second.Result.Stack.Volumes) || started.Result.Stack.NetworkID != second.Result.Stack.NetworkID || started.Snapshot.RuntimeConfiguration != second.Snapshot.RuntimeConfiguration {
		t.Fatal("Start changed retained stack identity")
	}
	verify(started, registryRef)
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
	composeRuntimeRemoteVolumeArchive(t, f, resource, started)
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
	targetDeploy := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+target.ID+"/deployments", domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: target.Generation, ExpectedRuntimeConfigurationGeneration: 1, RegistryCredential: &registryRef, IdempotencyKey: "compose-import-target", ContainerPort: 3456, HostPort: first.Snapshot.HostPort + 1, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}}, 202)
	targetDeploy = f.await(targetDeploy.ID, "succeeded")
	verify(targetDeploy, registryRef)
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
	bad := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: runtime.Configuration.RuntimeGeneration, RegistryCredential: &registryRef, IdempotencyKey: "compose-controlled-failure", ContainerPort: 3456, HostPort: first.Snapshot.HostPort, Readiness: domain.HostingReadiness{Path: "/api/v1/required-missing-compose-readiness", TimeoutSeconds: 2}, StorageRouteContinuity: options.StorageRouteContinuity}
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
	ordinary, _ := json.Marshal([]any{installation, resource, runtime, first, second, stopped, started, held, denied})
	for _, value := range []string{password, jwt} {
		if bytes.Contains(journal, []byte(value)) || bytes.Contains(ordinary, []byte(value)) || strings.Contains(f.logs.String(), value) {
			t.Fatal("managed credential entered ordinary operation/journal/log output")
		}
	}
	registryRuntimeArtifactSecrecy(t, f, ordinary, registryPassword)
	clear(journal)
	clear(ordinary)
	t.Log("catalog Compose: original public source and generated runtime refs deployed; private Compose registry: protected replacement and uncached Start, anonymous PostgreSQL/init, selected immutable refs, wrong/revoked pre-mutation route/container preservation, later valid redeploy/Start and artifact secrecy verified")
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
	remote := strings.TrimSuffix(update, "}") + `,"remote_target":{"target_id":"target_fixture","revision":1}}`
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, remote, "application/json", 200)
	if repo.input.RemoteTarget == nil || repo.input.RemoteTarget.TargetID != "target_fixture" || repo.input.RemoteTarget.Revision != 1 || !repo.input.Enabled || repo.input.Volume != "vikunja-data" {
		t.Fatal("production schedule decoder lost Compose remote fixture selection")
	}
	for _, invalid := range []string{
		strings.Replace(body, `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`, `fixture-invalid-request`, 1),
		strings.Replace(body, `"volume":"vikunja-data"`, `"volume":"../vikunja-data"`, 1),
		strings.Replace(body, `"acknowledge_interruption":true`, `"acknowledge_interruption":null`, 1),
	} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
}

func TestComposeRemoteVolumeFixtureRequestContract(t *testing.T) {
	server, repo, owner, _, _ := archiveHTTPFixture(t)
	path := "/api/v1/resources/" + repo.resource.ID + "/storage-backups"
	body := `{"request_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","expected_resource_generation":1,"volume":"vikunja-data","expected_deployed_operation_id":"hop_fixture","acknowledge_interruption":true,"remote_target":{"target_id":"target_fixture","revision":1}}`
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 202)
	if repo.backupInput.Volume != "vikunja-data" || repo.backupInput.ExpectedDeployedOperationID != "hop_fixture" || !repo.backupInput.AcknowledgeInterruption || repo.backupInput.RemoteTarget == nil || repo.backupInput.RemoteTarget.TargetID != "target_fixture" || repo.backupInput.RemoteTarget.Revision != 1 {
		t.Fatal("production archive decoder lost Compose remote fixture selection")
	}
	for _, tc := range []struct{ name, body string }{
		{"zero target revision", strings.Replace(body, `"revision":1`, `"revision":0`, 1)},
		{"empty opaque target ID", strings.Replace(body, `"target_id":"target_fixture"`, `"target_id":""`, 1)},
		{"interruption not acknowledged", strings.Replace(body, `"acknowledge_interruption":true`, `"acknowledge_interruption":false`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repo.calls
			archiveHTTPCall(t, server, "POST", path, owner, tc.body, "application/json", 400)
			if repo.calls != before {
				t.Fatal("invalid Compose remote archive fixture reached persistence")
			}
		})
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
	backupsBeforeDownload := storageRuntimeJSON[[]domain.HostingStorageBackup](f, "GET", "/api/v1/resources/"+resource.ID+"/storage-backups", nil, 200)
	writersBeforeDownload := map[string][]byte{}
	for _, component := range deployed.Result.Stack.Components {
		writersBeforeDownload[component.ContainerID] = storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.Id}} {{.State.Running}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}")
	}
	downloadSince := time.Now().Format(time.RFC3339Nano)
	f.catalogBrowser.set("volume_recheck", failedBackup.ID)
	if f.catalogBrowser.await(t, "volume_checked") != taskID {
		t.Fatal("application task/upload/new writes lost after volume backup compensation")
	}
	if !reflect.DeepEqual(backupsBeforeDownload, storageRuntimeJSON[[]domain.HostingStorageBackup](f, "GET", "/api/v1/resources/"+resource.ID+"/storage-backups", nil, 200)) {
		t.Fatal("archive download changed backup history or created another archive")
	}
	for id, beforeDownload := range writersBeforeDownload {
		if !bytes.Equal(beforeDownload, storageRuntimeDocker(t, "inspect", id, "--format", "{{.Id}} {{.State.Running}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}")) {
			t.Fatal("archive download changed writer identity or state")
		}
		for _, action := range []string{"start", "die", "stop"} {
			if len(bytes.TrimSpace(storageRuntimeDocker(t, "events", "--since", downloadSince, "--until", time.Now().Format(time.RFC3339Nano), "--filter", "container="+id, "--filter", "event="+action, "--format", "{{.Action}}"))) != 0 {
				t.Fatal("archive download interrupted a component")
			}
		}
	}
	t.Log("Compose archive native download: exact selected blob; no new archive or writer lifecycle events")
	t.Logf("Compose local archive scheduled_backup=%s failed_backup=%s stable named volume preserved after redeploy and Start", backupID, failedBackup.ID)
}

// Reuse the existing Silo, upload guard and transfer machinery. A denied PUT
// follows a successful local archive, and retry must upload that same artifact.
func composeRuntimeRemoteVolumeArchive(t *testing.T, f *storageRuntimeFixture, resource domain.Resource, deployed domain.HostingOperation) {
	t.Helper()
	for _, address := range []string{"127.0.0.1:9000", "127.0.0.1:9001"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal("Compose remote fixture loopback port unavailable")
		}
		if listener.Close() != nil {
			t.Fatal("Compose remote fixture loopback probe cleanup failed")
		}
	}
	remote := &hostingStorageRemoteRuntimeFixture{storageRuntimeFixture: f}
	silo := hostingStorageRemoteRuntimeStartSilo(remote)
	observation := &hostingStorageRemoteRuntimeObservation{}
	checks, violations := 0, 0
	var guardMu sync.Mutex
	hostingStorageRemoteRuntimeProxy(t, observation, func() bool {
		guardMu.Lock()
		defer guardMu.Unlock()
		checks++
		valid := true
		for _, component := range deployed.Result.Stack.Components {
			valid = valid && hostingStorageRemoteRuntimeWriterState(component.ContainerID, component.Running)
		}
		if !valid {
			violations++
		}
		return valid
	})
	target := storageRuntimeJSON[domain.HostingDatabaseBackupTarget](f, "POST", "/api/v1/projects/"+resource.ProjectID+"/database-backup-targets", map[string]any{"request_id": strings.Repeat("a", 31) + "1", "expected_revision": 0, "name": "Compose selected S3", "endpoint": "http://127.0.0.1:9001", "bucket": "nerocd-storage-runtime", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": silo.access, "secret_key": silo.secret}}, 200)
	validation := storageRuntimeJSON[hostingStorageRemoteRuntimeTransfer](f, "POST", "/api/v1/projects/"+resource.ProjectID+"/database-backup-targets/"+target.ID+"/storage-validations", domain.HostingDatabaseBackupTargetValidationInput{RequestID: "compose-remote-target-validation", ExpectedRevision: target.Revision, DestinationID: f.destinationID}, 202)
	validated := hostingStorageRemoteRuntimeAwaitID(remote, validation.ID, "succeeded")
	if validated.Mode != "validate" || validated.TargetID != target.ID || validated.TargetRevision != target.Revision {
		t.Fatal("Compose remote validation changed target identity")
	}
	var volumeRoot string
	for _, volume := range deployed.Result.Stack.Volumes {
		if volume.Name == "vikunja-data" {
			volumeRoot = filepath.Join("/var/lib/docker/volumes", volume.VolumeName, "_data")
		}
	}
	if volumeRoot == "" {
		t.Fatal("Compose remote upload volume missing")
	}
	beforeFiles := composeRuntimeUploadSnapshot(t, volumeRoot)
	unselectedStates := map[string]string{}
	for _, component := range deployed.Result.Stack.Components {
		if component.Component != "vikunja" {
			unselectedStates[component.ContainerID] = string(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.State.Running}} {{.State.ExitCode}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}"))
		}
	}
	since := time.Now().Format(time.RFC3339Nano)
	f.catalogBrowser.set("volume_remote_backup", target.ID)
	backupID := f.catalogBrowser.await(t, "volume_remote_backed_up")
	backup := storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	operation := f.await(backup.OperationID, "succeeded")
	failed := hostingStorageRemoteRuntimeAwait(remote, backupID, "upload", "failed")
	backup = storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	if operation.StorageArchive == nil || operation.StorageArchive.Plan.CompilerVersion != "managed-compose.remote-volume-archive.v1" || operation.StorageArchive.Plan.PolicyVersion != "managed-compose.remote-volume-archive.v1" || operation.StorageArchive.Plan.RemoteTarget == nil || operation.StorageArchive.Plan.RemoteTarget.TargetID != target.ID || operation.StorageArchive.Plan.RemoteTarget.Revision != target.Revision || operation.StorageArchive.Result == nil || operation.StorageArchive.Result.ResumeOutcome != "resumed" || operation.StorageArchive.Result.Uncertain || operation.MutationHold || backup.State != "succeeded" || backup.Volume != "vikunja-data" || backup.SourceDeployedOperationID != deployed.ID || backup.Blob == nil || failed.Blob == nil || *failed.Blob != *backup.Blob || failed.FailureCode != "access_denied" || failed.TargetID != target.ID || failed.TargetRevision != target.Revision {
		t.Fatal("Compose denied upload did not preserve successful local archive and resumed authority")
	}
	schedules := storageRuntimeJSON[[]domain.HostingStorageBackupSchedule](f, "GET", "/api/v1/resources/"+resource.ID+"/storage-backup-schedules", nil, 200)
	var remoteSchedule *domain.HostingStorageBackupSchedule
	for i := range schedules {
		candidate := &schedules[i]
		if candidate.RemoteTarget != nil && candidate.RemoteTarget.TargetID == target.ID {
			if remoteSchedule != nil {
				t.Fatal("Compose remote fixture created multiple selected schedules")
			}
			remoteSchedule = candidate
		}
	}
	if remoteSchedule == nil || remoteSchedule.Version != domain.HostingComposeStorageBackupScheduleVersion || remoteSchedule.ResourceID != resource.ID || remoteSchedule.Volume != "vikunja-data" || !remoteSchedule.AcknowledgeInterruption || remoteSchedule.Enabled || remoteSchedule.Revision != 2 || remoteSchedule.RemoteTarget.Revision != target.Revision {
		t.Fatal("Compose remote schedule lost selected volume, consent or pinned target")
	}
	occurrences := storageRuntimeJSON[[]domain.HostingStorageBackupOccurrence](f, "GET", "/api/v1/resources/"+resource.ID+"/storage-backup-schedules/"+remoteSchedule.ID+"/occurrences", nil, 200)
	var occurrence *domain.HostingStorageBackupOccurrence
	for i := range occurrences {
		if occurrences[i].BackupID == backupID {
			occurrence = &occurrences[i]
		}
	}
	if occurrence == nil || occurrence.ScheduleID != remoteSchedule.ID || occurrence.ScheduleRevision != 1 || occurrence.ResourceID != resource.ID || occurrence.Volume != "vikunja-data" || occurrence.OperationID != operation.ID || occurrence.OwnershipHeld {
		t.Fatal("Compose remote occurrence lost immutable schedule-to-archive identity")
	}
	if storageArchiveHelperStarts(t, operation.ID, since) != 1 {
		t.Fatal("Compose remote archive helper did not run exactly once")
	}
	archive := hostingStorageRemoteRuntimeArchive(remote, *backup.Blob)
	postArchiveStates := map[string]string{}
	for _, component := range deployed.Result.Stack.Components {
		postArchiveStates[component.ContainerID] = string(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.State.Running}} {{.State.ExitCode}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}"))
		if component.Component != "vikunja" && postArchiveStates[component.ContainerID] != unselectedStates[component.ContainerID] {
			t.Fatal("Compose remote archive touched PostgreSQL or completed init")
		}
	}
	silo.policy(true, false)
	retrySince := time.Now().Format(time.RFC3339Nano)
	f.catalogBrowser.set("volume_remote_retry", backupID)
	retryID := f.catalogBrowser.await(t, "volume_remote_retried")
	uploaded := hostingStorageRemoteRuntimeAwaitID(remote, retryID, "succeeded")
	if uploaded.ID == failed.ID || uploaded.Mode != "upload" || uploaded.BackupID != backupID || uploaded.TargetID != target.ID || uploaded.TargetRevision != target.Revision || uploaded.Blob == nil || *uploaded.Blob != *backup.Blob || storageArchiveHelperStarts(t, operation.ID, since) != 1 {
		t.Fatal("Compose upload retry changed archive identity or rearchived")
	}
	survived := storageRuntimeJSON[domain.HostingStorageBackup](f, "GET", "/api/v1/storage-backups/"+backupID, nil, 200)
	if survived.OperationID != backup.OperationID || survived.State != "succeeded" || survived.Blob == nil || *survived.Blob != *backup.Blob || !bytes.Equal(archive, hostingStorageRemoteRuntimeArchive(remote, *survived.Blob)) {
		t.Fatal("Compose retry changed retained local archive")
	}
	key := domain.StorageRemoteObjectKey(backupID, *backup.Blob)
	object := silo.command("mc cat fixture/nerocd-storage-runtime/" + key + "\n")
	if !bytes.Equal(object, archive) || "sha256:"+fmt.Sprintf("%x", sha256.Sum256(object)) != backup.Blob.SHA256 {
		t.Fatal("Compose remote object differs from retained local archive")
	}
	clear(object)
	clear(archive)
	for _, component := range deployed.Result.Stack.Components {
		if postArchiveStates[component.ContainerID] != string(storageRuntimeDocker(t, "inspect", component.ContainerID, "--format", "{{.State.Running}} {{.State.ExitCode}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}")) {
			t.Fatal("Compose upload retry changed writer or init state")
		}
		for _, action := range []string{"start", "stop", "die"} {
			if len(bytes.TrimSpace(storageRuntimeDocker(t, "events", "--since", retrySince, "--until", time.Now().Format(time.RFC3339Nano), "--filter", "container="+component.ContainerID, "--filter", "event="+action, "--format", "{{.Action}}"))) != 0 {
				t.Fatal("Compose upload retry interrupted a component")
			}
		}
	}
	if !reflect.DeepEqual(beforeFiles, composeRuntimeUploadSnapshot(t, volumeRoot)) {
		t.Fatal("Compose remote archive or retry changed uploads or numeric metadata")
	}
	guardMu.Lock()
	observedChecks, observedViolations := checks, violations
	guardMu.Unlock()
	if observedChecks < 2 || observedViolations != 0 {
		t.Fatal("Compose upload began before exact writers were resumed")
	}
	for _, secret := range []string{silo.access, silo.secret} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("Compose remote credentials appeared in runner logs")
		}
	}
	t.Logf("Compose remote scheduled_backup=%s schedule=%s occurrence=%s failed_transfer=%s retry=%s local_bytes=%d exact_remote_blob=true helper_starts=1 retry_component_events=0 resume_checks=%d", backupID, remoteSchedule.ID, occurrence.ID, failed.ID, uploaded.ID, backup.Blob.SizeBytes, observedChecks)
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

func TestComposeRegistryRuntimeRequestContracts(t *testing.T) {
	for _, mode := range []string{"initial", "replacement", "start"} {
		t.Run(mode, func(t *testing.T) {
			input := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "compose-registry-" + mode, ContainerPort: 3456, HostPort: 18080, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}, RegistryCredential: &domain.HostingRegistryCredentialRef{SecretID: "msec_fixture", Version: 1}}
			if mode != "initial" {
				input.StorageRouteContinuity = &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "hop_proxy", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_fixture", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_previous"}}}
			}
			if mode == "start" {
				input.ManagedStack.ExpectedStoppedOperationID = "hop_stopped"
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			var decoded domain.HostingDeploymentInput
			if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_fixture/deployments", bytes.NewReader(raw)), &decoded) || domain.ValidateHostingDeployment(decoded) != nil || !reflect.DeepEqual(input, decoded) {
				t.Fatal("actual Compose deployment decoder/validator changed fixture body")
			}
			for _, invalid := range []domain.HostingDeploymentInput{func() domain.HostingDeploymentInput {
				v := input
				v.ManagedStack = &domain.HostingStackDeploymentInput{}
				return v
			}(), func() domain.HostingDeploymentInput { v := input; v.Readiness.TimeoutSeconds = 0; return v }(), func() domain.HostingDeploymentInput {
				v := input
				v.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "msec_fixture", Version: 0}
				return v
			}()} {
				badRaw, err := json.Marshal(invalid)
				if err != nil {
					t.Fatal(err)
				}
				var badDecoded domain.HostingDeploymentInput
				if decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_fixture/deployments", bytes.NewReader(badRaw)), &badDecoded) && domain.ValidateHostingDeployment(badDecoded) == nil {
					t.Fatal("invalid acknowledgement/readiness/credential accepted")
				}
			}
		})
	}
}

func TestComposeRegistrySourceUpdateContract(t *testing.T) {
	server, _ := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	environment, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "compose-registry-patch"})
	if err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, server)
	send := func(method, path string, input any, want int) []byte {
		t.Helper()
		raw, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("actual patch contract status%d want%d", w.Code, want)
		}
		return w.Body.Bytes()
	}
	a := "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576"
	b := "127.0.0.1:25001/fixture:b@sha256:" + strings.Repeat("b", 64)
	original, readErr := os.ReadFile("../../examples/compose/vikunja-postgresql.yaml")
	if readErr != nil {
		t.Fatal(readErr)
	}
	source := domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: string(original), PublicComponent: "vikunja", ContainerPort: 3456}}
	var resource domain.Resource
	if json.Unmarshal(send("POST", "/api/v1/resources", app.ResourceInput{RequestID: strings.Repeat("c", 32), ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "Compose registry patch", Kind: "application", Source: source}, 201), &resource) != nil {
		t.Fatal("resource receipt")
	}
	changed := *source.Compose
	changed.Original = strings.Replace(changed.Original, a, b, 1)
	changed.Original = strings.Replace(changed.Original, "        if [", "        printf 'nerocd-init-log-fixture\\n'\n        if [", 1)
	source.Compose = &changed
	patch := app.ResourcePatch{ExpectedGeneration: resource.Generation, Source: &source}
	var updated domain.Resource
	if json.Unmarshal(send("PATCH", "/api/v1/resources/"+resource.ID, patch, 200), &updated) != nil || updated.Generation != resource.Generation+1 || !reflect.DeepEqual(updated.Source, source) {
		t.Fatal("A to B source CAS receipt")
	}
	send("PATCH", "/api/v1/resources/"+resource.ID, patch, 409)
	patch.ExpectedGeneration = 0
	send("PATCH", "/api/v1/resources/"+resource.ID, patch, 400)
	send("PATCH", "/api/v1/resources/"+resource.ID, map[string]any{"expected_generation": updated.Generation, "source": nil}, 400)
}

func TestComposeCatalogFixtureRequestContract(t *testing.T) {
	input := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("c", 32), ProjectID: "proj_platform", ProjectEnvironmentID: "env_fixture", DestinationID: "dest_fixture", Name: "Compose Vikunja acceptance", TemplateID: "vikunja-with-postgresql", ImmutableVersion: domain.HostingCatalogVikunjaVersion, PublicURL: "https://catalog-runtime.example.test:25443"}
	for _, name := range []string{"valid", "missing_url", "invalid_url", "other_template_url", "invalid_request"} {
		t.Run(name, func(t *testing.T) {
			value := input
			switch name {
			case "missing_url":
				value.PublicURL = ""
			case "invalid_url":
				value.PublicURL = "not a URL"
			case "other_template_url":
				value.TemplateID = "linkding"
				value.ImmutableVersion = domain.HostingCatalogLinkdingVersion
			case "invalid_request":
				value.RequestID = "catalog-fixture"
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded domain.HostingCatalogInstallInput
			accepted := decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/hosting-catalog/installations", bytes.NewReader(raw)), &decoded) && decoded.Validate() == nil
			if accepted != (name == "valid") || accepted && !reflect.DeepEqual(decoded, input) {
				t.Fatal("actual catalog decoder/validator contract changed")
			}
		})
	}
	original, err := os.ReadFile("../../examples/compose/vikunja-postgresql.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(original)) != "sha256:2ed2a1cc22b4c020f26e214d4e96a419eb56445a9d14f87f2df703a7ee9de767" {
		t.Fatal("catalog example source changed")
	}
	changed := strings.Replace(string(original), "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576", "127.0.0.1:25001/fixture:b@sha256:"+strings.Repeat("b", 64), 1)
	changed = strings.Replace(changed, "        if [", "        printf 'nerocd-init-log-fixture\\n'\n        if [", 1)
	definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: changed, PublicComponent: "vikunja", ContainerPort: 3456})
	if err != nil || len(definition.Services) != 3 {
		t.Fatal("catalog public to protected source contract invalid")
	}
}
