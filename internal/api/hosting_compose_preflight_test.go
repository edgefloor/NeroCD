package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

// Validate the representative gate's request family through production handlers
// without a database, Docker daemon, browser, or physical setup/retry cycle.
func TestHostingComposeFixtureRequestPreflight(t *testing.T) {
	server, _ := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	environment, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "compose-preflight"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("../../examples/compose/vikunja-postgresql.yaml")
	if err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, server)
	send := func(method, path string, input any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("preflight %s %s status%d expected%d", method, path, response.Code, want)
		}
		return response.Body.Bytes()
	}
	input := app.ResourceInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "Compose Vikunja acceptance", Kind: "application", Source: domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{Original: string(original), PublicComponent: "vikunja", ContainerPort: 3456}}}
	var resource domain.Resource
	if json.Unmarshal(send("POST", "/api/v1/resources", input, http.StatusCreated), &resource) != nil || resource.ID == "" || resource.Source.Compose.Original != string(original) {
		t.Fatal("representative source did not pass real decoder and normalization")
	}
	value := "https://catalog-runtime.example.test:25443"
	config := domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("b", 32), Entries: []domain.HostingRuntimeEntry{{Name: "SERVICE_URL_VIKUNJA", Kind: "literal", Value: &value}, {Name: "SERVICE_PASSWORD_POSTGRESQL", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: "msec_preflight_postgresql", Version: 1}}}}
	send("PUT", "/api/v1/resources/"+resource.ID+"/runtime-configuration", config, http.StatusServiceUnavailable)
	deploy := domain.HostingDeploymentInput{ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "compose-preflight", ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ContainerPort: 3456, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}}
	send("POST", "/api/v1/resources/"+resource.ID+"/deployments", deploy, http.StatusServiceUnavailable)
	deploy.ManagedStack = nil
	send("POST", "/api/v1/resources/"+resource.ID+"/deployments", deploy, http.StatusBadRequest)
	registered := false
	for _, route := range publicRoutes {
		if route.Method == http.MethodPost && route.Path == "/api/v1/runners/hosting/stack-volumes" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("stack provenance runner route is unregistered")
	}
}
