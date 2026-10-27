//go:build linux && nerocd_proxy_fixture

package api

import (
	"bytes"
	"context"
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
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func registryRuntimeConfigurationInput(resource domain.Resource) domain.HostingRuntimeConfigurationInput {
	value := "registry-runtime-present"
	return domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: resource.Generation, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("7", 32), Entries: []domain.HostingRuntimeEntry{{Name: "REGISTRY_FIXTURE", Kind: "literal", Value: &value}}}
}

func TestRegistryRuntimeRequestContracts(t *testing.T) {
	resource := domain.Resource{ID: "res_93243519f9444c36ad33c36fbe7801b7", Generation: 1}
	decode := func(t *testing.T, body, target any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/api/v1/fixture-contract", bytes.NewReader(raw))
		if !decodeBody(httptest.NewRecorder(), request, target) {
			t.Fatal("fixture real decoder rejected body")
		}
	}
	t.Run("runtime", func(t *testing.T) {
		body := registryRuntimeConfigurationInput(resource)
		var decoded domain.HostingRuntimeConfigurationInput
		decode(t, body, &decoded)
		if _, _, err := decoded.Validate(); err != nil {
			t.Fatal("runtime actual validator", err)
		}
		decoded.RequestID = "registry-runtime-" + resource.ID
		if _, _, err := decoded.Validate(); err == nil {
			t.Fatal("original invalid request identity accepted")
		}
	})
	t.Run("storage", func(t *testing.T) {
		body := domain.HostingStorageInput{RequestID: strings.Repeat("6", 32), ExpectedResourceGeneration: 1, MountPath: "/data/uploads"}
		var decoded domain.HostingStorageInput
		decode(t, body, &decoded)
		if err := decoded.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("deployment", func(t *testing.T) {
		body := registryRuntimeDeploymentInput(resource, domain.HostingRegistryCredentialRef{SecretID: "msec_fixture", Version: 1}, nil)
		var decoded domain.HostingDeploymentInput
		decode(t, body, &decoded)
		if err := domain.ValidateHostingDeployment(decoded); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("secret", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]string{"registry": "127.0.0.1:25001", "username": "fixture", "password": "fixture-password"})
		body := app.ManagedSecretInput{ProjectID: "proj_platform", Name: "private registry correct", Value: string(raw)}
		var decoded app.ManagedSecretInput
		decode(t, body, &decoded)
		if err := domain.ValidateHostingName(decoded.Name); err != nil {
			t.Fatal(err)
		}
		credential, err := domain.DecodeHostingRegistryCredential([]byte(decoded.Value))
		if err != nil || credential.Registry != "127.0.0.1:25001" {
			t.Fatal("actual strict secret contract")
		}
		credential.Close()
	})
	t.Run("resource", func(t *testing.T) {
		body := app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: "env_fixture", Name: "Private registry uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "127.0.0.1:25001/" + storageRuntimeOwner + ":a"}}}
		var decoded app.ResourceInput
		decode(t, body, &decoded)
		if domain.ValidateHostingName(decoded.Name) != nil || domain.ValidateResourceSource(decoded.Source) != nil {
			t.Fatal("actual resource validators")
		}
	})
	t.Run("binding", func(t *testing.T) {
		var decoded app.ResourceBindingInput
		decode(t, app.ResourceBindingInput{DestinationID: "dest_fixture"}, &decoded)
		if decoded.DestinationID != "dest_fixture" {
			t.Fatal("binding decoder")
		}
	})
	t.Run("revoke", func(t *testing.T) {
		var decoded app.ManagedSecretRevokeInput
		decode(t, app.ManagedSecretRevokeInput{ExpectedVersion: 1}, &decoded)
		if decoded.ExpectedVersion != 1 {
			t.Fatal("revoke decoder")
		}
	})
}

func registryRuntimeDeploymentInput(resource domain.Resource, credential domain.HostingRegistryCredentialRef, continuity *domain.HostingStorageRouteContinuityInput) domain.HostingDeploymentInput {
	return domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "registry-identical-request", ContainerPort: 8080, HostPort: 20102, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 3}, StorageRouteContinuity: continuity, RegistryCredential: &credential}
}

// The existing guest registry seeds these scratch images. The second registry
// requires real Basic authentication; Engine pushes and pulls never use login.
func registryRuntimeProtectedImages(t *testing.T, f *storageRuntimeFixture, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal("registry fixture password hash")
	}
	authPath := filepath.Join(f.root, "registry-htpasswd")
	if os.WriteFile(authPath, append(append([]byte("fixture:"), hash...), '\n'), 0600) != nil {
		t.Fatal("registry fixture auth file")
	}
	name := storageRuntimeOwner + "-private-registry"
	if strings.TrimSpace(string(storageRuntimeDocker(t, "ps", "-aq", "--filter", "name=^/"+name+"$"))) != "" {
		t.Fatal("private registry name exists")
	}
	const image = "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	id := strings.TrimSpace(string(storageRuntimeDocker(t, "run", "--detach", "--pull=never", "--name", name, "--label", storageRuntimeLabel+"="+storageRuntimeOwner, "--network", "host", "--tmpfs", "/var/lib/registry:rw,size=64m", "--mount", "type=bind,src="+authPath+",dst=/auth/htpasswd,readonly", "--env", "REGISTRY_HTTP_ADDR=127.0.0.1:25001", "--env", "REGISTRY_AUTH=htpasswd", "--env", "REGISTRY_AUTH_HTPASSWD_REALM=NeroCD acceptance", "--env", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", image)))
	t.Cleanup(func() {
		identity := strings.TrimSpace(string(storageRuntimeDocker(t, "inspect", "--format", `{{.Id}} {{index .Config.Labels "io.nerocd.storage-runtime.owner"}}`, id)))
		if identity != id+" "+storageRuntimeOwner {
			t.Error("private registry ownership changed")
			return
		}
		storageRuntimeDocker(t, "rm", "--force", "--volumes", id)
	})
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for deadline := time.Now().Add(10 * time.Second); ; {
		response, e := client.Get("http://127.0.0.1:25001/v2/")
		if e == nil {
			_ = response.Body.Close()
			if response.StatusCode != 401 || !strings.HasPrefix(response.Header.Get("WWW-Authenticate"), "Basic realm=") {
				t.Fatal("private registry challenge differs")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private registry readiness")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, tc := range []struct {
		password string
		status   int
	}{{"incorrect", 401}, {password, 200}} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:25001/v2/", nil)
		req.SetBasicAuth("fixture", tc.password)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal("private registry challenge request")
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatal("private registry credential contract")
		}
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	defer transport.CloseIdleConnections()
	engine := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	credentials, _ := json.Marshal(map[string]string{"username": "fixture", "password": password, "serveraddress": "127.0.0.1:25001"})
	target := "127.0.0.1:25001/" + storageRuntimeOwner
	for _, version := range []string{"a", "b"} {
		storageRuntimeDocker(t, "tag", storageRuntimeImage+version, target+":"+version)
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://engine/images/"+target+"/push?tag="+version, nil)
		request.Header.Set("X-Registry-Auth", base64.URLEncoding.EncodeToString(credentials))
		response, e := engine.Do(request)
		if e != nil {
			t.Fatal("private registry seed push")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || bytes.Contains(data, []byte(`"error"`)) {
			t.Fatal("private registry seed push failed")
		}
		storageRuntimeDocker(t, "image", "rm", "--force", target+":"+version)
	}
	return target
}

func TestHostingPrivateRegistryCompiledRunnerHTTPS(t *testing.T) {
	if os.Getenv("NEROCD_CATALOG_APPLICATION") != "private-registry" {
		t.Skip("requires approved isolated private registry runtime gate")
	}
	f := newStorageRuntimeFixture(t)
	password := hostingStorageRemoteRuntimeRandom(t)
	image := registryRuntimeProtectedImages(t, f, password)
	secretValue := func(value string) string {
		raw, _ := json.Marshal(map[string]string{"registry": "127.0.0.1:25001", "username": "fixture", "password": value})
		return string(raw)
	}
	correct := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: "proj_platform", Name: "private registry correct", Value: secretValue(password)}, 201)
	wrong := storageRuntimeJSON[domain.ManagedSecret](f, "POST", "/api/v1/managed-secrets", app.ManagedSecretInput{ProjectID: "proj_platform", Name: "private registry wrong", Value: secretValue("incorrect")}, 201)
	create := func(name string) domain.Resource {
		r := storageRuntimeJSON[domain.Resource](f, "POST", "/api/v1/resources", map[string]any{"project_id": "proj_platform", "project_environment_id": f.environmentID, "name": name, "kind": "application", "source": map[string]any{"kind": "image", "image": domain.ImageSource{Reference: image + ":a"}}}, 201)
		storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+r.ID+"/binding", app.ResourceBindingInput{DestinationID: f.destinationID}, 201)
		saved := storageRuntimeJSON[struct {
			Configuration domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
		}](f, "PUT", "/api/v1/resources/"+r.ID+"/runtime-configuration", registryRuntimeConfigurationInput(r), 200)
		if saved.Configuration.RuntimeGeneration != 1 {
			t.Fatal("runtime configuration generation")
		}
		return r
	}
	stateless := create("Private registry stateless")
	stored := create("Private registry uploads")
	storage := f.storage(stored, strings.Repeat("6", 32))
	f.catalogBrowser.set("install", stateless.ID)
	if f.catalogBrowser.await(t, "installed") != stateless.ID {
		t.Fatal("browser resource identity")
	}
	var states []domain.HostingOperation
	for i, r := range []domain.Resource{stateless, stored} {
		// Every selected deployment must miss its registry reference before pre-pull.
		output, e := exec.CommandContext(t.Context(), "/usr/local/bin/docker", "--host", "unix:///var/run/docker.sock", "image", "inspect", image+":a").CombinedOutput()
		if e == nil || !strings.Contains(strings.ToLower(string(output)), "no such image") && !strings.Contains(strings.ToLower(string(output)), "no such object") {
			t.Fatal("private image reference not demonstrably uncached")
		}
		phase, action := "deploy", "deployed"
		if i == 1 {
			phase, action = "restore_deploy", "restored_deployed"
		}
		f.catalogBrowser.set(phase, r.ID)
		id := f.catalogBrowser.await(t, action)
		op := f.await(id, "succeeded")
		raw, _ := json.Marshal(op.Snapshot)
		if !bytes.Contains(raw, []byte(correct.ID)) || op.Snapshot.RuntimeConfiguration.Generation != 1 || op.Result == nil || !op.Result.Ready {
			t.Fatal("selected credential/runtime snapshot absent")
		}
		environment := storageRuntimeDocker(t, "inspect", "--format", "{{json .Config.Env}}", op.Result.ContainerID)
		if !bytes.Contains(environment, []byte("REGISTRY_FIXTURE=registry-runtime-present")) || bytes.Contains(environment, []byte(password)) {
			t.Fatal("runtime literal missing or registry secret injected")
		}
		states = append(states, op)
		storageRuntimeDocker(t, "image", "rm", "--force", image+":a")
	}
	get := func(method, path string, body []byte, status int) []byte {
		req, _ := http.NewRequestWithContext(t.Context(), method, "http://127.0.0.1:20102"+path, bytes.NewReader(body))
		response, e := f.client.Do(req)
		if e != nil {
			t.Fatal("protected application traffic")
		}
		defer func() { _ = response.Body.Close() }()
		data, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if e != nil || response.StatusCode != status {
			t.Fatal("protected application response")
		}
		return data
	}
	if string(get("GET", "/", nil, 200)) != "A\n" {
		t.Fatal("protected image traffic")
	}
	payload := []byte("private registry retained uploaded data\n")
	get("PUT", "/upload", payload, 201)
	get("POST", "/rename", nil, 204)
	statelessResponse, statelessErr := f.client.Get("http://127.0.0.1:20101/")
	if statelessErr != nil {
		t.Fatal("stateless private image traffic")
	}
	statelessData, statelessReadErr := io.ReadAll(io.LimitReader(statelessResponse.Body, 1024))
	_ = statelessResponse.Body.Close()
	if statelessReadErr != nil || statelessResponse.StatusCode != 200 || string(statelessData) != "A\n" {
		t.Fatal("stateless private image response")
	}
	current := states[1]
	catalogRuntimeRoute(t, f, stored.ID, current)
	if !bytes.Equal(f.https("catalog-runtime.example.test", "GET", "/download", nil, 200), payload) {
		t.Fatal("trusted routed uploads absent")
	}
	replacementInput := registryRuntimeDeploymentInput(stored, domain.HostingRegistryCredentialRef{SecretID: correct.ID, Version: 1}, f.continuity(stored, current))
	replacementInput.IdempotencyKey = "registry-routed-success"
	current = f.await(storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", replacementInput, 202).ID, "succeeded")
	f.awaitContinuity(stored.ID, current.ID)
	if current.Snapshot.Storage != states[1].Snapshot.Storage || current.Snapshot.RuntimeConfiguration != states[1].Snapshot.RuntimeConfiguration || !bytes.Equal(f.https("catalog-runtime.example.test", "GET", "/download", nil, 200), payload) {
		t.Fatal("private routed replacement lost immutable refs or uploads")
	}
	states = append(states, current)
	routesBefore := storageRuntimeJSON[[]domain.HostingRoute](f, "GET", "/api/v1/resources/"+stored.ID+"/routes", nil, 200)
	proxyBefore := storageRuntimeJSON[domain.HostingProxy](f, "GET", "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
	before := storageRuntimeDocker(t, "inspect", current.Result.ContainerID)
	options := storageRuntimeJSON[domain.HostingStorageDeploymentOptions](f, "GET", "/api/v1/resources/"+stored.ID+"/storage-deployment-options", nil, 200)
	if !options.Available || options.StorageRouteContinuity == nil {
		t.Fatal("routed private replacement options unavailable")
	}
	body := registryRuntimeDeploymentInput(stored, domain.HostingRegistryCredentialRef{SecretID: wrong.ID, Version: 1}, options.StorageRouteContinuity)
	failed := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", body, 202)
	replay := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", body, 202)
	if replay.ID != failed.ID {
		t.Fatal("identical request changed operation")
	}
	failed = f.await(failed.ID, "failed")
	body.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: correct.ID, Version: 1}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", body, 409)
	if !bytes.Equal(before, storageRuntimeDocker(t, "inspect", current.Result.ContainerID)) {
		t.Fatal("denied pull mutated previous workload")
	}
	if !bytes.Equal(get("GET", "/download", nil, 200), payload) {
		t.Fatal("denied pull lost uploads")
	}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/managed-secrets/"+wrong.ID+"/revoke", app.ManagedSecretRevokeInput{ExpectedVersion: 1}, 200)
	body.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: wrong.ID, Version: 1}
	historical := storageRuntimeJSON[domain.HostingOperation](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", body, 202)
	if historical.ID != failed.ID {
		t.Fatal("revocation lost immutable historical request receipt")
	}
	body.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: correct.ID, Version: 1}
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/managed-secrets/"+correct.ID+"/revoke", app.ManagedSecretRevokeInput{ExpectedVersion: 1}, 200)
	body.IdempotencyKey = "registry-revoked"
	storageRuntimeJSON[json.RawMessage](f, "POST", "/api/v1/resources/"+stored.ID+"/deployments", body, 409)
	if !bytes.Equal(before, storageRuntimeDocker(t, "inspect", current.Result.ContainerID)) || !bytes.Equal(get("GET", "/download", nil, 200), payload) {
		t.Fatal("revoked credential mutated workload or data")
	}
	routesAfter := storageRuntimeJSON[[]domain.HostingRoute](f, "GET", "/api/v1/resources/"+stored.ID+"/routes", nil, 200)
	proxyAfter := storageRuntimeJSON[domain.HostingProxy](f, "GET", "/api/v1/servers/"+f.serverID+"/proxy", nil, 200)
	if !reflect.DeepEqual(routesBefore, routesAfter) || !reflect.DeepEqual(proxyBefore, proxyAfter) || proxyAfter.ActiveOperationID != "" || proxyAfter.HoldOperationID != "" || !bytes.Equal(f.https("catalog-runtime.example.test", "GET", "/download", nil, 200), payload) {
		t.Fatal("denied credential changed loaded route or trusted upload response")
	}
	volume := storageRuntimeDocker(t, "volume", "inspect", storage.VolumeName)
	if !bytes.Contains(volume, []byte(storage.VolumeName)) {
		t.Fatal("retained upload volume absent")
	}
	f.stop()
	serialized, _ := json.Marshal([]any{states, failed})
	engineAuth, _ := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Server   string `json:"serveraddress"`
	}{"fixture", password, "127.0.0.1:25001"})
	markers := [][]byte{[]byte("X-Registry-Auth"), []byte("x-registry-auth"), []byte(base64.URLEncoding.EncodeToString(engineAuth)), []byte(base64.StdEncoding.EncodeToString([]byte("fixture:" + password))), []byte(password), []byte(secretValue(password)), []byte(base64.URLEncoding.EncodeToString([]byte(secretValue(password))))}
	for _, marker := range markers {
		if bytes.Contains(serialized, marker) || strings.Contains(f.logs.String(), string(marker)) {
			t.Fatal("registry credential escaped into operation/log")
		}
	}
	for _, directory := range []string{"journal", "runner"} {
		if e := filepath.WalkDir(filepath.Join(f.root, directory), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			for _, marker := range markers {
				if bytes.Contains(data, marker) {
					return fmt.Errorf("registry credential persisted")
				}
			}
			return nil
		}); e != nil {
			t.Fatal("runner artifact secrecy check", e)
		}
	}
	// The runner must not produce Docker login configuration at any default root.
	for _, path := range []string{"/root/.docker/config.json", filepath.Join(f.root, "runner", ".docker", "config.json")} {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("durable Docker login configuration present")
		}
	}
	if reflect.DeepEqual(states[0].Snapshot.Storage, states[1].Snapshot.Storage) {
		t.Fatal("stateless/storage paths not distinct")
	}
	t.Log("private registry: uncached authenticated pulls, both UI forms, pinned runtime/ref, replay/conflict, wrong/revoked pre-mutation denial, retained uploads and artifact secrecy verified")
	f.catalogBrowser.set("complete", stored.ID)
	f.catalogBrowser.await(t, "done")
}
