package main

import (
	"encoding/json"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestRunnerExternalImportRegistrationThroughIdentity(t *testing.T) {
	// Use the same capability constructors invoked by the CLI's parsed flags.
	caps, err := runnerRuntimeEnvironmentCapabilities(true, true, "", false, []string{"shell"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err = runnerDatabaseCapabilities(true, true, caps)
	if err != nil {
		t.Fatal(err)
	}
	caps, err = runnerExternalImportCapabilities(true, caps)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(caps, domain.HostingDatabaseImportUploadCapability) {
		t.Fatal("CLI omitted local upload capability")
	}
	var registered []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/register":
			var input runnerRegisterRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || r.Header.Get("Authorization") != "Bearer registration" {
				w.WriteHeader(400)
				return
			}
			registered = input.Capabilities
			_ = json.NewEncoder(w).Encode(runnerRegisterResponse{Runner: domain.Runner{ID: input.ID, Capabilities: registered}, Token: "dedicated"})
		case "/api/v1/runners/identity":
			if r.Header.Get("Authorization") != "Bearer dedicated" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner_upload", "capabilities": registered})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	var response runnerRegisterResponse
	if err = postAPIInto(server.URL+"/api/v1/runners/register", runnerRegisterRequest{ID: "runner_upload", Capabilities: caps}, "registration", &response); err != nil {
		t.Fatal(err)
	}
	options := runnerclient.RuntimeExecutionOptions{Database: true, RuntimeEnvironment: true, ExternalDatabaseImports: true, DatabaseTrustedLoopbackHTTP: true}
	identity, err := runnerclient.New(server.URL, response.Token).RuntimeExecutionIdentityWithOptions(t.Context(), options)
	if err != nil || identity != "runner_upload" {
		t.Fatal("CLI registration cannot satisfy runtime identity", err)
	}
	mu.Lock()
	registered = slices.DeleteFunc(registered, func(capability string) bool { return capability == domain.HostingDatabaseImportUploadCapability })
	mu.Unlock()
	if _, err = runnerclient.New(server.URL, response.Token).RuntimeExecutionIdentityWithOptions(t.Context(), options); err == nil {
		t.Fatal("old incomplete enrollment accepted")
	}
	for _, capability := range caps {
		if _, err = runnerExternalImportCapabilities(false, []string{capability}); capability == domain.HostingDatabaseImportUploadCapability && err == nil {
			t.Fatal("upload capability bypassed opt-in")
		}
	}
	// The exact production flag spelling is --hosting-database-external-imports;
	// an explicit capability without it must fail before registration.
	if err = runRunner([]string{"--server", server.URL, "--token", "registration", "--capabilities", domain.HostingDatabaseImportUploadCapability, "--once"}); err == nil || !strings.Contains(err.Error(), "external import capabilities require explicit execution opt-in") {
		t.Fatal("CLI accepted upload capability without opt-in")
	}
}
