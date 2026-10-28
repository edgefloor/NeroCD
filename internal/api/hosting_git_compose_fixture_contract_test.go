package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

const gitComposeRejectProxyQueue = `CREATE FUNCTION reject_recovery_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN RAISE EXCEPTION 'fixture queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovery_queue BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_recovery_queue()`
const gitComposeAllowProxyQueue = `DROP TRIGGER IF EXISTS reject_recovery_queue ON audit_events; DROP FUNCTION IF EXISTS reject_recovery_queue()`
const gitComposeRecoveryWriterRows = `SELECT jsonb_build_array((SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_apply_admissions),(SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT jsonb_agg(to_jsonb(v) ORDER BY binding_id,volume_name) FROM hosting_compose_volumes v))::text`

const gitComposeFixturePostgres = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

const gitComposeFixtureBase = "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576"

func gitComposeFixtureFiles(raw, base, marker string, fail bool, registryImage string) map[string]string {
	compose := strings.Replace(raw, "    image: "+base, "    build:\n      context: ../app\n"+gitComposeFixtureArgs(marker)+"    image: nerocd-vikunja:prepared", 1)
	compose = strings.Replace(compose, "    depends_on:\n", "    depends_on:\n      version:\n        condition: service_completed_successfully\n", 1)
	compose = strings.Replace(compose, "\n  postgresql:\n", "\n  version:\n    image: nerocd-vikunja:prepared\n    command: [version]\n\n  postgresql:\n", 1)
	compose = strings.Replace(compose, "        if [", "        printf 'nerocd-init-log-fixture\\n'\n        if [", 1)
	if registryImage != "" {
		compose = strings.Replace(compose, gitComposeFixturePostgres, registryImage, 1)
	}
	if marker == "missing-registry" {
		compose = strings.Replace(compose, "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab", "postgres:nerocd-required-missing-20261027", 1)
	}
	dockerfile := "FROM " + base + "\nCOPY --chmod=0644 marker /nerocd-version\n" +
		"ARG CONFIGURED\nARG COMPOSE_MARKER\nARG INTERPOLATED\nARG DEFAULTED\nARG ALTERNATE\nARG ESCAPED\nARG EMPTY=not-empty\nARG OMITTED=docker-default\nARG BARE=docker-default\nARG NUMBER\nARG FLAG\nARG SERVICE_URL_VIKUNJA=runtime-not-imported\n" +
		"LABEL nerocd.fixture.configured=\"${CONFIGURED}\" nerocd.fixture.marker=\"${COMPOSE_MARKER}\" nerocd.fixture.interpolated=\"${INTERPOLATED}\" nerocd.fixture.defaulted=\"${DEFAULTED}\" nerocd.fixture.alternate=\"${ALTERNATE}\" nerocd.fixture.escaped=\"${ESCAPED}\" nerocd.fixture.empty=\"${EMPTY}\" nerocd.fixture.omitted=\"${OMITTED}\" nerocd.fixture.bare=\"${BARE}\" nerocd.fixture.number=\"${NUMBER}\" nerocd.fixture.flag=\"${FLAG}\" nerocd.fixture.runtime=\"${SERVICE_URL_VIKUNJA}\"\n"
	if fail {
		dockerfile += "RUN exit 9\n"
	}
	return map[string]string{"project/deploy/compose.yaml": compose, "project/app/Dockerfile": dockerfile, "project/app/marker": marker + "\n"}
}

// Keep A's mapping and B's list forms semantically equivalent except for the
// version marker. Docker defaults distinguish omitted args from explicit empty.
func gitComposeFixtureArgs(marker string) string {
	if marker == "missing-argument" {
		return "      args:\n        COMPOSE_MARKER: ${NEROCD_REQUIRED_FIXTURE:?required fixture argument}\n"
	}
	if marker == "B" {
		return "      args:\n        - CONFIGURED=compose-value\n        - COMPOSE_MARKER=B\n        - INTERPOLATED=${CONFIGURED}\n        - DEFAULTED=${NEROCD_UNSET_FIXTURE:-fallback}\n        - ALTERNATE=${CONFIGURED:+present}\n        - ESCAPED=$$literal\n        - EMPTY=\n        - OMITTED\n        - BARE\n        - NUMBER=20\n        - FLAG=true\n        - SERVICE_URL_VIKUNJA\n"
	}
	return "      args:\n        CONFIGURED: compose-value\n        COMPOSE_MARKER: " + marker + "\n        INTERPOLATED: ${CONFIGURED}\n        DEFAULTED: ${NEROCD_UNSET_FIXTURE:-fallback}\n        ALTERNATE: ${CONFIGURED:+present}\n        ESCAPED: $$literal\n        EMPTY: ''\n        OMITTED: null\n        BARE:\n        NUMBER: 20\n        FLAG: true\n        SERVICE_URL_VIKUNJA: null\n"
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
	settings := domain.HostingBuildSettings{Strategy: "compose", ContextDir: "project", ComposeFile: "deploy/compose.yaml", PublicComponent: "vikunja", ContainerPort: 3456, BuildVariables: []domain.HostingBuildVariable{{Name: "CONFIGURED", Value: "operator-value"}}}
	for _, fixture := range []struct{ marker, registryImage string }{{"A", ""}, {"B", ""}, {"missing-registry", ""}, {"B", "127.0.0.1:25001/nerocd-fixture:b@sha256:" + strings.Repeat("a", 64)}} {
		marker := fixture.marker
		files := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, marker, false, fixture.registryImage)
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
			owner, ownerErr := domain.HostingStackBuildOwner(definition, service.Name)
			if ownerErr != nil || (service.Name == "vikunja" || service.Name == "version") && owner != "vikunja" || (service.Name == "postgresql" || service.Name == "init") && owner != "" {
				t.Fatal("registry-only services and local owner/alias coverage changed")
			}
			if service.Name == "postgresql" {
				expectedImage := gitComposeFixturePostgres
				if marker == "missing-registry" {
					expectedImage = "postgres:nerocd-required-missing-20261027"
				}
				if fixture.registryImage != "" {
					expectedImage = fixture.registryImage
				}
				expectedImage, normalizeErr := domain.NormalizeHostingImage(expectedImage)
				if normalizeErr != nil || service.Image != expectedImage || service.Build != nil {
					t.Fatal("registry fixture did not isolate the expected PostgreSQL image")
				}
			}
			if service.Name == "version" && (service.Build != nil || len(service.Command) != 1 || service.Command[0] != "version") {
				t.Fatal("local image alias changed its verified version command")
			}
			if service.Name == "init" && !strings.Contains(strings.Join(service.Command, "\n"), "nerocd-init-log-fixture") {
				t.Fatal("generated Git init command omitted required component log marker", marker)
			}
			if service.Name == "vikunja" && service.Build != nil {
				want := map[string]string{"CONFIGURED": "operator-value", "COMPOSE_MARKER": marker, "INTERPOLATED": "operator-value", "DEFAULTED": "fallback", "ALTERNATE": "present", "ESCAPED": "$literal", "EMPTY": "", "NUMBER": "20", "FLAG": "true"}
				if !reflect.DeepEqual(service.Build.Args, want) {
					t.Fatal("generated Compose argument resolution changed", marker)
				}
			}
			if service.Name == "vikunja" && (service.Build == nil || service.Build.Context != "project/app" || service.DependsOn["version"] != "service_completed_successfully") {
				t.Fatal("relative build path or completed alias contract changed")
			}
		}
	}
	missing := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, "missing-argument", false, "")
	if _, err := domain.ParseHostingGitCompose(domain.HostingStackSource{Original: missing["project/deploy/compose.yaml"], PublicComponent: "vikunja", ContainerPort: 3456}, settings); err == nil {
		t.Fatal("missing required argument must fail before engine execution")
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
		{"from_build_replacement", domain.HostingBuildDeploymentInput{BuildID: "build_fixture", ExpectedGeneration: 1, ExpectedSourceGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "git-compose-replace", ContainerPort: 3456, HostPort: 21001, Readiness: domain.HostingReadiness{Path: "/health", TimeoutSeconds: 120}, ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, StorageRouteContinuity: &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_loaded", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_fixture", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_previous"}}}}, new(domain.HostingBuildDeploymentInput)},
	}
	private := bodies[len(bodies)-1].in.(domain.HostingBuildDeploymentInput)
	private.IdempotencyKey = "git-compose-private-replace"
	private.RegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "msec_registry", Version: 1}
	bodies = append(bodies, struct {
		name    string
		in, out any
	}{"from_build_private_replacement", private, new(domain.HostingBuildDeploymentInput)})
	start := private
	start.IdempotencyKey = "git-compose-private-start"
	start.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true, ExpectedStoppedOperationID: "hop_stopped"}
	bodies = append(bodies, struct {
		name    string
		in, out any
	}{"from_build_private_start", start, new(domain.HostingBuildDeploymentInput)})
	bodies = append(bodies, struct {
		name    string
		in, out any
	}{"route_recovery", domain.HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: "sha256:" + strings.Repeat("a", 64), ExpectedCompleteReceiptID: "receipt_fixture", ExpectedCompleteReceiptHash: "sha256:" + strings.Repeat("b", 64)}, new(domain.HostingStorageRouteRecoveryInput)})
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
				if decoded.RequestID != "" || decoded.ProjectID != input.ProjectID || decoded.ProjectEnvironmentID != input.ProjectEnvironmentID || decoded.Name != input.Name || decoded.Kind != input.Kind || decoded.Source.Kind != input.Source.Kind || decoded.Source.Git == nil || !reflect.DeepEqual(decoded.Source.Git, input.Source.Git) {
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
		{"GET", "/api/v1/managed-secrets", "GET /api/v1/managed-secrets"},
		{"GET", "/api/v1/managed-secrets/msec_registry/versions", "GET /api/v1/managed-secrets/{id}/versions"},
		{"POST", "/api/v1/managed-secrets/msec_registry/revoke", "POST /api/v1/managed-secrets/{id}/revoke"},
		{"POST", "/api/v1/resources/resource_fixture/source-grants", "POST /api/v1/resources/{id}/source-grants"},
		{"POST", "/api/v1/resources/resource_fixture/builds", "POST /api/v1/resources/{id}/builds"},
		{"GET", "/api/v1/hosting-builds/build_fixture", "GET /api/v1/hosting-builds/{id}"},
		{"PUT", "/api/v1/resources/resource_fixture/runtime-configuration", "PUT /api/v1/resources/{id}/runtime-configuration"},
		{"POST", "/api/v1/resources/resource_fixture/deployments/from-build", "POST /api/v1/resources/{id}/deployments/from-build"},
		{"GET", "/api/v1/resources/resource_fixture/storage-deployment-options", "GET /api/v1/resources/{id}/storage-deployment-options"},
		{"GET", "/api/v1/resources/resource_fixture/storage-route-continuity/hop_fixture", "GET /api/v1/resources/{id}/storage-route-continuity/{operation_id}"},
		{"POST", "/api/v1/resources/resource_fixture/storage-route-continuity/hop_fixture/recover", "POST /api/v1/resources/{id}/storage-route-continuity/{operation_id}/recover"},
		{"GET", "/api/v1/resources/resource_fixture/stack-lifecycle", "GET /api/v1/resources/{id}/stack-lifecycle"},
		{"POST", "/api/v1/resources/resource_fixture/stack-stop", "POST /api/v1/resources/{id}/stack-stop"},
	} {
		_, pattern := server.mux.Handler(httptest.NewRequest(route.method, route.path, nil))
		if pattern != route.pattern {
			t.Fatalf("fixture route missing: %s %s resolved %s", route.method, route.path, pattern)
		}
	}
	deployment := start
	if domain.ValidateHostingBuildDeployment(deployment) != nil {
		t.Fatal("fixture deployment failed semantic validation")
	}
}

func TestGitComposeProxyQueueFailureInjection(t *testing.T) {
	_, _, database := newHostingBuildAPIFixture(t)
	var rows string
	if err := database.QueryRow(t.Context(), gitComposeRecoveryWriterRows).Scan(&rows); err != nil {
		t.Fatal("runtime recovery snapshot query rejected by fixture schema", err)
	}
	if _, err := database.Exec(t.Context(), gitComposeRejectProxyQueue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := database.Exec(ctx, gitComposeAllowProxyQueue); err != nil {
			t.Error(err)
		}
	})
	insert := func(id, action string) error {
		_, err := database.Exec(t.Context(), `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap',$2,'fixture','{}',clock_timestamp())`, id, action)
		return err
	}
	if err := insert("audit_fixture_normal", "fixture.normal"); err != nil {
		t.Fatal("queue injection rejected unrelated audit", err)
	}
	if err := insert("audit_fixture_rejected", "hosting.proxy_generation_requested"); err == nil || !strings.Contains(err.Error(), "fixture queue rejection") {
		t.Fatal("queue injection did not reject exact production action")
	}
	var count int
	if err := database.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE id='audit_fixture_rejected'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected queue audit persisted", err)
	}
	if _, err := database.Exec(t.Context(), gitComposeAllowProxyQueue); err != nil {
		t.Fatal(err)
	}
	if err := insert("audit_fixture_accepted", "hosting.proxy_generation_requested"); err != nil {
		t.Fatal("queue injection cleanup left queue blocked", err)
	}
}
