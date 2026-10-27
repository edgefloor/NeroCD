package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

const gitComposeFixtureBase = "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576"

func gitComposeFixtureFiles(raw, base, marker string, fail bool) map[string]string {
	compose := strings.Replace(raw, "    image: "+base, "    build: ../app\n    image: nerocd-vikunja:prepared", 1)
	compose = strings.Replace(compose, "    depends_on:\n", "    depends_on:\n      version:\n        condition: service_completed_successfully\n", 1)
	compose = strings.Replace(compose, "\n  postgresql:\n", "\n  version:\n    image: nerocd-vikunja:prepared\n    command: [version]\n\n  postgresql:\n", 1)
	if marker == "missing-registry" {
		compose = strings.Replace(compose, "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab", "postgres:nerocd-required-missing-20261027", 1)
	}
	dockerfile := "FROM " + base + "\nCOPY --chmod=0644 marker /nerocd-version\n"
	if fail {
		dockerfile += "RUN exit 9\n"
	}
	return map[string]string{"project/deploy/compose.yaml": compose, "project/app/Dockerfile": dockerfile, "project/app/marker": marker + "\n"}
}

type gitComposeFixtureSource struct {
	Kind string                           `json:"kind"`
	Git  *domain.GitResourceConfiguration `json:"git"`
}

type gitComposeFixtureCreateRequest struct {
	ProjectID            string                  `json:"project_id"`
	ProjectEnvironmentID string                  `json:"project_environment_id"`
	Name                 string                  `json:"name"`
	Kind                 string                  `json:"kind"`
	Source               gitComposeFixtureSource `json:"source"`
}

func TestGitComposeFixtureSourceAndRequestContracts(t *testing.T) {
	// Match resourcesHandler's request-only source decoder. It intentionally
	// does not marshal a persisted Git source identity back into a request.
	type createDecoded struct {
		RequestID            string                     `json:"request_id,omitempty"`
		ProjectID            string                     `json:"project_id"`
		ProjectEnvironmentID string                     `json:"project_environment_id"`
		Name                 string                     `json:"name"`
		Kind                 string                     `json:"kind"`
		Source               domain.ResourceSourceInput `json:"source"`
	}
	raw, err := os.ReadFile("../../examples/compose/vikunja-postgresql.yaml")
	if err != nil {
		t.Fatal(err)
	}
	settings := domain.HostingBuildSettings{Strategy: "compose", ContextDir: "project", ComposeFile: "deploy/compose.yaml", PublicComponent: "vikunja", ContainerPort: 3456}
	for _, marker := range []string{"A", "B", "missing-registry"} {
		files := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, marker, false)
		definition, err := domain.ParseHostingGitCompose(domain.HostingStackSource{Original: files["project/deploy/compose.yaml"], PublicComponent: "vikunja", ContainerPort: 3456}, settings)
		if err != nil {
			t.Fatal("actual Git Compose fixture parser", err)
		}
		if len(definition.Services) != 4 || len(definition.Volumes) != 2 {
			t.Fatal("fixture component/storage coverage changed")
		}
		owner, err := domain.HostingStackBuildOwner(definition, "version")
		if err != nil || owner != "vikunja" {
			t.Fatal("version alias lost built owner")
		}
		for _, service := range definition.Services {
			if service.Name == "vikunja" && (service.Build == nil || service.Build.Context != "project/app" || service.DependsOn["version"] != "service_completed_successfully") {
				t.Fatal("relative build path or completed alias contract changed")
			}
		}
	}
	source := gitComposeFixtureSource{Kind: "git", Git: &domain.GitResourceConfiguration{RepositoryURL: "https://example.com:443/repo.git", RequestedRef: "main", CredentialKind: "https_basic", Settings: settings}}
	bodies := []struct {
		name    string
		in, out any
	}{
		{"create", gitComposeFixtureCreateRequest{ProjectID: "proj_platform", ProjectEnvironmentID: "penv_fixture", Name: "Compose Vikunja acceptance", Kind: "application", Source: source}, new(createDecoded)},
		{"binding", app.ResourceBindingInput{DestinationID: "dst_fixture"}, new(app.ResourceBindingInput)},
		{"source_secret", app.ManagedSecretInput{ProjectID: "proj_platform", Name: "Git Compose HTTPS source", Value: `{"username":"fixture-user","password":"fixture-value"}`}, new(app.ManagedSecretInput)},
		{"grant", domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, SecretID: "secret_fixture", SecretVersion: 1, RequestID: strings.Repeat("b", 32)}, new(domain.HostingSourceGrantRequest)},
		{"build", domain.HostingBuildRequest{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, GrantID: "source-grant-fixture", IdempotencyKey: "git-compose-build"}, new(domain.HostingBuildRequest)},
		{"runtime", domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("c", 32), Entries: []domain.HostingRuntimeEntry{}}, new(domain.HostingRuntimeConfigurationInput)},
		{"from_build", domain.HostingBuildDeploymentInput{BuildID: "build_fixture", ExpectedGeneration: 1, ExpectedSourceGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "git-compose-deploy", ContainerPort: 3456, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}, ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}}, new(domain.HostingBuildDeploymentInput)},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
			w := httptest.NewRecorder()
			if !decodeBody(w, r, tc.out) {
				t.Fatal("fixture request rejected by production decoder", w.Body.String())
			}
			if tc.name == "create" {
				decoded := tc.out.(*createDecoded)
				input := tc.in.(gitComposeFixtureCreateRequest)
				if decoded.RequestID != "" || decoded.ProjectID != input.ProjectID || decoded.ProjectEnvironmentID != input.ProjectEnvironmentID || decoded.Name != input.Name || decoded.Kind != input.Kind || decoded.Source.Kind != input.Source.Kind || decoded.Source.Git == nil || *decoded.Source.Git != *input.Source.Git {
					t.Fatal("production resource source decoder changed Git configuration")
				}
				return
			}
			again, _ := json.Marshal(tc.out)
			if string(raw) != string(again) {
				t.Fatal("production decoder changed fixture request")
			}
		})
	}
	server, _ := newTestServer()
	for _, route := range []struct{ method, path, pattern string }{
		{"POST", "/api/v1/resources", "POST /api/v1/resources"},
		{"POST", "/api/v1/resources/resource_fixture/binding", "POST /api/v1/resources/{id}/binding"},
		{"POST", "/api/v1/managed-secrets", "POST /api/v1/managed-secrets"},
		{"POST", "/api/v1/resources/resource_fixture/source-grants", "POST /api/v1/resources/{id}/source-grants"},
		{"POST", "/api/v1/resources/resource_fixture/builds", "POST /api/v1/resources/{id}/builds"},
		{"GET", "/api/v1/hosting-builds/build_fixture", "GET /api/v1/hosting-builds/{id}"},
		{"PUT", "/api/v1/resources/resource_fixture/runtime-configuration", "PUT /api/v1/resources/{id}/runtime-configuration"},
		{"POST", "/api/v1/resources/resource_fixture/deployments/from-build", "POST /api/v1/resources/{id}/deployments/from-build"},
		{"GET", "/api/v1/resources/resource_fixture/stack-lifecycle", "GET /api/v1/resources/{id}/stack-lifecycle"},
		{"POST", "/api/v1/resources/resource_fixture/stack-stop", "POST /api/v1/resources/{id}/stack-stop"},
	} {
		_, pattern := server.mux.Handler(httptest.NewRequest(route.method, route.path, nil))
		if pattern != route.pattern {
			t.Fatalf("fixture route missing: %s %s resolved %s", route.method, route.path, pattern)
		}
	}
	deployment := bodies[len(bodies)-1].in.(domain.HostingBuildDeploymentInput)
	if domain.ValidateHostingBuildDeployment(deployment) != nil {
		t.Fatal("fixture deployment failed semantic validation")
	}
}
