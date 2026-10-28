package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

const gitComposeRejectProxyQueue = `CREATE FUNCTION reject_recovery_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN RAISE EXCEPTION 'fixture queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovery_queue BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_recovery_queue()`
const gitComposeAllowProxyQueue = `DROP TRIGGER IF EXISTS reject_recovery_queue ON audit_events; DROP FUNCTION IF EXISTS reject_recovery_queue()`
const gitComposeRecoveryWriterRows = `SELECT jsonb_build_array((SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_apply_admissions),(SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT jsonb_agg(to_jsonb(v) ORDER BY binding_id,volume_name) FROM hosting_compose_volumes v))::text`

const gitComposeFixturePostgres = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

const gitComposeFixtureMultiline = "first line\nsecond line"

const gitComposeFixtureBase = "vikunja/vikunja:2.7.1@sha256:f7dc8428d10aef17f3075886d3c1c022f566ec456f88985f02c137f4fc534576"

// Rotation changes the only accepted password, so version2 cannot succeed
// using stale version1 material even when the image receipt is otherwise valid.
func gitComposeFixtureBuildRegistryAuth(next http.Handler, password string) (http.Handler, func(string)) {
	var active atomic.Value
	active.Store(password)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "build-fixture" || subtle.ConstantTimeCompare([]byte(password), []byte(active.Load().(string))) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NeroCD build registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
	return handler, func(password string) { active.Store(password) }
}

func TestGitComposeBuildRegistryCredentialRotation(t *testing.T) {
	handler, rotate := gitComposeFixtureBuildRegistryAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), "first-fixture-password")
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	check := func(password string, want int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v2/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("build-fixture", password)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("registry rotation status=%d want=%d", response.StatusCode, want)
		}
	}
	check("first-fixture-password", http.StatusOK)
	check("second-fixture-password", http.StatusUnauthorized)
	rotate("second-fixture-password")
	check("first-fixture-password", http.StatusUnauthorized)
	check("second-fixture-password", http.StatusOK)
}

func gitComposeFixtureFiles(raw, base, buildBase, marker string, fail bool, registryImage string) map[string]string {
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
	dockerfile := "FROM " + buildBase + "\nCOPY --chmod=0644 marker /nerocd-version\n" +
		"ARG MULTILINE\nARG CONFIGURED\nARG COMPOSE_MARKER\nARG INTERPOLATED\nARG DEFAULTED\nARG ALTERNATE\nARG ESCAPED\nARG EMPTY=not-empty\nARG OMITTED=docker-default\nARG BARE=docker-default\nARG NUMBER\nARG FLAG\nARG SERVICE_URL_VIKUNJA=runtime-not-imported\n" +
		"LABEL nerocd.fixture.multiline=\"${MULTILINE}\" nerocd.fixture.configured=\"${CONFIGURED}\" nerocd.fixture.marker=\"${COMPOSE_MARKER}\" nerocd.fixture.interpolated=\"${INTERPOLATED}\" nerocd.fixture.defaulted=\"${DEFAULTED}\" nerocd.fixture.alternate=\"${ALTERNATE}\" nerocd.fixture.escaped=\"${ESCAPED}\" nerocd.fixture.empty=\"${EMPTY}\" nerocd.fixture.omitted=\"${OMITTED}\" nerocd.fixture.bare=\"${BARE}\" nerocd.fixture.number=\"${NUMBER}\" nerocd.fixture.flag=\"${FLAG}\" nerocd.fixture.runtime=\"${SERVICE_URL_VIKUNJA}\"\n"
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
	settings := domain.HostingBuildSettings{Strategy: "compose", ContextDir: "project", ComposeFile: "deploy/compose.yaml", PublicComponent: "vikunja", ContainerPort: 3456, BuildVariables: []domain.HostingBuildVariable{{Name: "CONFIGURED", Value: "operator-value"}, {Name: "MULTILINE", Value: gitComposeFixtureMultiline}}}
	settings.BuildRegistryCredential = &domain.HostingRegistryCredentialRef{SecretID: "msec_build_registry", Version: 1}
	privateBase := "172.17.0.1:25002/private/base@sha256:" + strings.Repeat("b", 64)
	privateFiles := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, privateBase, "A", false, "")
	if !strings.HasPrefix(privateFiles["project/app/Dockerfile"], "FROM "+privateBase+"\n") || strings.Contains(privateFiles["project/deploy/compose.yaml"], privateBase) {
		t.Fatal("private build base leaked into deployment registry services or alias")
	}
	for _, fixture := range []struct{ marker, registryImage string }{{"A", ""}, {"B", ""}, {"missing-registry", ""}, {"B", "127.0.0.1:25001/nerocd-fixture:b@sha256:" + strings.Repeat("a", 64)}} {
		marker := fixture.marker
		files := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, gitComposeFixtureBase, marker, false, fixture.registryImage)
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
				want := map[string]string{"MULTILINE": gitComposeFixtureMultiline, "CONFIGURED": "operator-value", "COMPOSE_MARKER": marker, "INTERPOLATED": "operator-value", "DEFAULTED": "fallback", "ALTERNATE": "present", "ESCAPED": "$literal", "EMPTY": "", "NUMBER": "20", "FLAG": "true"}
				if !reflect.DeepEqual(service.Build.Args, want) {
					t.Fatal("generated Compose argument resolution changed", marker)
				}
			}
			if service.Name == "vikunja" && (service.Build == nil || service.Build.Context != "project/app" || service.DependsOn["version"] != "service_completed_successfully") {
				t.Fatal("relative build path or completed alias contract changed")
			}
		}
	}
	missing := gitComposeFixtureFiles(string(raw), gitComposeFixtureBase, gitComposeFixtureBase, "missing-argument", false, "")
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
		{"build_registry_delivery", domain.HostingBuildRegistryCredentialRequest{Authority: domain.HostingBuildAuthority{BuildID: "build_fixture", RunID: "run_fixture", LeaseID: "lease_fixture", Attempt: 1, Fence: "fence_fixture"}, DeliveryID: "delivery_fixture"}, new(domain.HostingBuildRegistryCredentialRequest)},
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

// A removed exact predecessor cannot still write. Require its destruction before
// every replacement starts; retained die events must also agree with that order.
// Completed one-shot containers may have no die event in Docker's finite history.
func composeRuntimeWriterOrder(raw []byte, previous, current []string) error {
	if len(previous) == 0 || len(current) == 0 {
		return fmt.Errorf("writer component evidence missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	started, destroyed, died := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for {
		var event struct {
			Action   string
			TimeNano int64
			Actor    struct{ ID string }
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("Docker writer event evidence malformed: %w", err)
		}
		switch event.Action {
		case "start", "destroy", "die":
			if event.Actor.ID == "" || event.TimeNano <= 0 {
				return fmt.Errorf("Docker writer event identity or timestamp missing")
			}
		default:
			continue
		}
		switch event.Action {
		case "start":
			if prior := started[event.Actor.ID]; prior == 0 || event.TimeNano < prior {
				started[event.Actor.ID] = event.TimeNano
			}
		case "destroy":
			if event.TimeNano > destroyed[event.Actor.ID] {
				destroyed[event.Actor.ID] = event.TimeNano
			}
		case "die":
			if event.TimeNano > died[event.Actor.ID] {
				died[event.Actor.ID] = event.TimeNano
			}
		}
	}
	var earliest int64
	for _, id := range current {
		stamp := started[id]
		if id == "" || stamp == 0 {
			return fmt.Errorf("new component start evidence missing")
		}
		if earliest == 0 || stamp < earliest {
			earliest = stamp
		}
	}
	for _, id := range previous {
		if id == "" || destroyed[id] == 0 {
			return fmt.Errorf("predecessor destruction evidence missing")
		}
		if destroyed[id] >= earliest || died[id] >= earliest {
			return fmt.Errorf("new stack writer started before every predecessor was removed")
		}
	}
	return nil
}

func TestGitComposeWriterOrderCapturedEvents(t *testing.T) {
	// Relevant events captured at 2b83265c. The init and version die events
	// were absent; all four exact predecessors were destroyed before new start.
	previous := []string{"f8e1883efcffa9b3b6c322a4462218e44bad07addb9099deb27aec6b8fa14ac0", "03ef0d1a0df8276ccd10a8ac32e8f5dc287f2e8cf3d66ea1aca42294e8cf1e7b", "6f98150d8e51313b304bac5f1baf7aa60b2eabe64dfcff51e16a6af293aa0d4c", "7acc61a177291a3401aefc2669aaa68a13015a9861c05be8bbea1e9ef5393ffa"}
	current := []string{"db1544c7753f549f9e85fee315d3521a87ef917d9bd3d9b427663e1749a10b29", "7df431daa517d632cbc4ef94394b13ad61ea74c39c0c5beeb116dcd6e903f2b9", "bc1fe54907967fe328aa52c549a49cb5828fba319135b285cdfe3e66613d86ec", "3e5ae056abe33efe8bbd41d8241ce2fec7af584f4e9e942c62724f0625aadd03"}
	raw := []byte(`{"Action":"die","timeNano":1793153716203224139,"Actor":{"ID":"03ef0d1a0df8276ccd10a8ac32e8f5dc287f2e8cf3d66ea1aca42294e8cf1e7b"}}
{"Action":"die","timeNano":1793153716286903693,"Actor":{"ID":"7acc61a177291a3401aefc2669aaa68a13015a9861c05be8bbea1e9ef5393ffa"}}
{"Action":"destroy","timeNano":1793153716336524391,"Actor":{"ID":"f8e1883efcffa9b3b6c322a4462218e44bad07addb9099deb27aec6b8fa14ac0"}}
{"Action":"destroy","timeNano":1793153716337543603,"Actor":{"ID":"03ef0d1a0df8276ccd10a8ac32e8f5dc287f2e8cf3d66ea1aca42294e8cf1e7b"}}
{"Action":"destroy","timeNano":1793153716338037354,"Actor":{"ID":"6f98150d8e51313b304bac5f1baf7aa60b2eabe64dfcff51e16a6af293aa0d4c"}}
{"Action":"destroy","timeNano":1793153716338572356,"Actor":{"ID":"7acc61a177291a3401aefc2669aaa68a13015a9861c05be8bbea1e9ef5393ffa"}}
{"Action":"start","timeNano":1793153716626598137,"Actor":{"ID":"db1544c7753f549f9e85fee315d3521a87ef917d9bd3d9b427663e1749a10b29"}}
{"Action":"start","timeNano":1793153716637413671,"Actor":{"ID":"7df431daa517d632cbc4ef94394b13ad61ea74c39c0c5beeb116dcd6e903f2b9"}}
{"Action":"start","timeNano":1793153716641149808,"Actor":{"ID":"bc1fe54907967fe328aa52c549a49cb5828fba319135b285cdfe3e66613d86ec"}}
{"Action":"die","timeNano":1793153716779147742,"Actor":{"ID":"db1544c7753f549f9e85fee315d3521a87ef917d9bd3d9b427663e1749a10b29"}}
{"Action":"die","timeNano":1793153716803829278,"Actor":{"ID":"bc1fe54907967fe328aa52c549a49cb5828fba319135b285cdfe3e66613d86ec"}}
{"Action":"start","timeNano":1793153722268736474,"Actor":{"ID":"3e5ae056abe33efe8bbd41d8241ce2fec7af584f4e9e942c62724f0625aadd03"}}`)
	if err := composeRuntimeWriterOrder(raw, previous, current); err != nil {
		t.Fatal(err)
	}
	for i, id := range previous {
		t.Run(fmt.Sprintf("missing predecessor %d", i), func(t *testing.T) {
			var kept []string
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, `"Action":"destroy"`) && strings.Contains(line, id) {
					continue
				}
				kept = append(kept, line)
			}
			if err := composeRuntimeWriterOrder([]byte(strings.Join(kept, "\n")), previous, current); err == nil {
				t.Fatal("accepted missing exact predecessor destruction")
			}
		})
	}
}

func TestGitComposeWriterOrderEvidence(t *testing.T) {
	event := func(action, id string, stamp int64) string {
		return fmt.Sprintf(`{"Action":%q,"Actor":{"ID":%q},"timeNano":%d}`+"\n", action, id, stamp)
	}
	good := event("die", "old", 8) + event("destroy", "old", 9) + event("start", "new", 10)
	for _, tc := range []struct {
		name, raw string
		wantError bool
	}{
		{"ordered", good, false},
		{"completed one shot", event("destroy", "old", 9) + event("start", "new", 10), false},
		{"missing destruction", event("die", "old", 8) + event("start", "new", 10), true},
		{"late destruction", event("destroy", "old", 11) + event("start", "new", 10), true},
		{"same time destruction", event("destroy", "old", 10) + event("start", "new", 10), true},
		{"late die", good + event("die", "old", 11), true},
		{"same time die", good + event("die", "old", 10), true},
		{"missing start", event("destroy", "old", 9), true},
		{"restart cannot hide overlap", event("start", "new", 5) + event("die", "old", 10) + event("destroy", "old", 11) + event("start", "new", 15), true},
		{"out of order restart evidence", event("start", "new", 15) + event("die", "old", 10) + event("destroy", "old", 11) + event("start", "new", 5), true},
		{"wrong predecessor", event("destroy", "other", 9) + event("start", "new", 10), true},
		{"zero timestamp", event("destroy", "old", 0) + event("start", "new", 10), true},
		{"malformed evidence", good + "{", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := composeRuntimeWriterOrder([]byte(tc.raw), []string{"old"}, []string{"new"}); (err != nil) != tc.wantError {
				t.Fatalf("writer evidence error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
	if err := composeRuntimeWriterOrder([]byte(good), []string{"old", "missing"}, []string{"new"}); err == nil {
		t.Fatal("accepted missing component destruction")
	}
	if err := composeRuntimeWriterOrder([]byte(good), []string{"old"}, []string{"new", "missing"}); err == nil {
		t.Fatal("accepted missing component start")
	}
}
