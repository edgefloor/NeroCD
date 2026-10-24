package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type connectionHTTPRepository struct {
	*store.MemoryStore
	calls                               []string
	actor, resource, runner, credential string
	authority                           domain.HostingAuthority
	err                                 error
	options                             []domain.HostingDatabaseConnectionOption
}

func (r *connectionHTTPRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == "connection_app" || id == "connection_database" {
		kind := "application"
		if id == "connection_database" {
			kind = "database"
		}
		return domain.Resource{ID: id, ProjectID: "proj_platform", Kind: kind, Source: domain.ResourceSource{Kind: "image"}}, nil
	}
	return r.MemoryStore.GetResource(ctx, id)
}

func (r *connectionHTTPRepository) operation(method, id, resource, actor string) (domain.HostingOperation, error) {
	r.calls = append(r.calls, method)
	r.actor, r.resource = actor, resource
	return domain.HostingOperation{ID: id, ResourceID: resource, CreatedBy: actor, State: "queued"}, r.err
}

func (r *connectionHTTPRepository) ConnectHostingApplicationDatabase(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseConnectionInput, _ domain.AuditEvent) (domain.HostingDatabaseConnection, error) {
	r.calls = append(r.calls, "connect")
	r.actor, r.resource = actor, resource
	return domain.HostingDatabaseConnection{ID: id, ApplicationResourceID: resource, State: "waiting_capture", RecoveryLimit: "observe_only"}, r.err
}

func (r *connectionHTTPRepository) RequestHostingApplicationDatabaseDeployment(_ context.Context, id, resource, actor string, _ domain.HostingDeploymentInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	return r.operation("deploy", id, resource, actor)
}

func (r *connectionHTTPRepository) RequestHostingDatabaseMaintenance(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseMaintenanceInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	return r.operation("maintenance", id, resource, actor)
}

func (r *connectionHTTPRepository) RequestHostingDatabaseConnectionRecovery(_ context.Context, id, resource, actor string, in domain.HostingDatabaseConnectionRecoveryInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.authority.OperationID = in.OperationID
	return r.operation("recovery", id, resource, actor)
}

func (r *connectionHTTPRepository) GetHostingDatabaseConnection(_ context.Context, actor, resource string) (domain.HostingDatabaseConnection, error) {
	r.calls = append(r.calls, "status")
	r.actor, r.resource = actor, resource
	return domain.HostingDatabaseConnection{ID: "hcn_status", ApplicationResourceID: resource, State: "unresolved", RecoveryLimit: "observe_only"}, r.err
}

func (r *connectionHTTPRepository) ListEligibleHostingDatabases(_ context.Context, actor, resource string) ([]domain.HostingDatabaseConnectionOption, error) {
	r.calls = append(r.calls, "options")
	r.actor, r.resource = actor, resource
	return r.options, r.err
}

func (r *connectionHTTPRepository) recordRunner(method, runner, credential string, a domain.HostingAuthority) {
	r.calls = append(r.calls, method)
	r.runner, r.credential, r.authority = runner, credential, a
}

func (r *connectionHTTPRepository) HostingApplicationDatabaseStage(_ context.Context, runner, credential string, in domain.HostingApplicationDatabaseStageRequest) (domain.HostingOperation, error) {
	r.recordRunner("stage", runner, credential, in.HostingAuthority)
	return domain.HostingOperation{ID: in.OperationID}, r.err
}

func (r *connectionHTTPRepository) HostingApplicationDatabaseRetirementPlan(_ context.Context, runner, credential string, in domain.HostingAuthority) (domain.HostingDatabaseBindingEndpointPlan, error) {
	r.recordRunner("retirement-plan", runner, credential, in)
	return domain.HostingDatabaseBindingEndpointPlan{OperationID: in.OperationID}, r.err
}

func (r *connectionHTTPRepository) HostingApplicationDatabaseRetirement(_ context.Context, runner, credential string, in domain.HostingApplicationDatabaseRetirementRequest) (domain.HostingOperation, error) {
	r.recordRunner("retirement", runner, credential, in.HostingAuthority)
	return domain.HostingOperation{ID: in.OperationID}, r.err
}

func (r *connectionHTTPRepository) AuthorizeHostingApplicationDatabaseEffect(_ context.Context, runner, credential string, in domain.HostingApplicationDatabaseEffectRequest) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
	r.recordRunner("authorize", runner, credential, in.HostingAuthority)
	return domain.HostingApplicationDatabaseEffectAuthorization{OperationID: in.OperationID}, r.err
}

func (r *connectionHTTPRepository) GetHostingApplicationDatabaseState(_ context.Context, runner, credential string, in domain.HostingAuthority) (domain.HostingApplicationDatabaseExecutionState, error) {
	r.recordRunner("state", runner, credential, in)
	return domain.HostingApplicationDatabaseExecutionState{OperationID: in.OperationID}, r.err
}

func connectionHTTPFixture(t *testing.T, compatible bool) (*Server, *app.Service, *connectionHTTPRepository, string) {
	t.Helper()
	mem := newSeededTestStore()
	repo := &connectionHTTPRepository{MemoryStore: mem}
	var runs store.RunRepository = repo
	if !compatible {
		runs = mem
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: runs, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	return server, service, repo, createTestSession(t, server)
}

func connectionCall(t *testing.T, s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable connection response", w.Code)
	}
	return w
}

func connectionPublicBodies() map[string]string {
	deployment := `{"expected_generation":1,"idempotency_key":"connection_deploy","container_port":8080,"host_port":0,"readiness":{"path":"/","timeout_seconds":5}}`
	return map[string]string{
		"/api/v1/resources/connection_app/database-connections":            `{"binding":{"database_resource_id":"connection_database","expected_resource_generation":1,"expected_runtime_generation":0,"expected_database_generation":1,"request_id":"` + strings.Repeat("a", 32) + `"},"deployment":` + deployment + `}`,
		"/api/v1/resources/connection_app/database-connection/deployments": deployment,
		"/api/v1/resources/connection_app/database-connection/recovery":    `{"action":"observe","operation_id":"original_operation","stage_hash":"","candidate_hash":"","expected_membership_revision":1,"idempotency_key":"observe"}`,
		"/api/v1/resources/connection_database/database/maintenance":       `{"expected_generation":1,"expected_membership_revision":2,"idempotency_key":"maintain"}`,
	}
}

func TestDatabaseConnectionHTTPPublicAuthorizationAndRouting(t *testing.T) {
	s, service, repo, session := connectionHTTPFixture(t, true)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	tokens := map[string]string{"session": session}
	actors := map[string]string{"session": "usr_bootstrap"}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, err := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "connection " + role, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = account.Token
		actors[role] = account.ServiceAccount.ID
	}
	foreign, err := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_security", Name: "foreign connection", Role: domain.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	tokens["foreign"] = foreign.Token
	for path, body := range connectionPublicBodies() {
		before := len(repo.calls)
		if w := connectionCall(t, s, "POST", path, "", body); w.Code != 401 {
			t.Fatal("anonymous mutation", w.Code)
		}
		for role, token := range tokens {
			count := len(repo.calls)
			w := connectionCall(t, s, "POST", path, token, body)
			want := 403
			if role == domain.RoleOwner || role == "session" {
				want = 202
			}
			if w.Code != want {
				t.Fatalf("%s %s status=%d body=%s", role, path, w.Code, w.Body.String())
			}
			if want == 403 && len(repo.calls) != count {
				t.Fatal("unauthorized request reached repository")
			}
			if want == 202 {
				var out map[string]any
				if json.Unmarshal(w.Body.Bytes(), &out) != nil {
					t.Fatal("invalid operation response")
				}
				id, ok := out["id"].(string)
				expectedResource := "connection_app"
				if strings.HasSuffix(path, "/maintenance") {
					expectedResource = "connection_database"
				}
				if !ok || id == "" || repo.actor != actors[role] || repo.resource != expectedResource {
					t.Fatal("lost operation/actor identity")
				}
				if strings.HasSuffix(path, "/recovery") && repo.authority.OperationID != "original_operation" {
					t.Fatal("recovery relabeled original")
				}
			}
		}
		if len(repo.calls) != before+2 {
			t.Fatal("wrong owner dispatch count")
		}
	}
	for _, path := range []string{"/api/v1/resources/connection_app/database-connection", "/api/v1/resources/connection_app/database-connections/options"} {
		for _, role := range []string{domain.RoleViewer, domain.RoleOwner, "foreign"} {
			count := len(repo.calls)
			w := connectionCall(t, s, "GET", path, tokens[role], "")
			want := 200
			if role == "foreign" {
				want = 403
			}
			if w.Code != want {
				t.Fatal("read authorization", role, w.Code)
			}
			if want == 403 && len(repo.calls) != count {
				t.Fatal("cross-project read reached repo")
			}
			if want == 200 {
				for _, secret := range []string{`"value"`, `"password"`, `"ciphertext"`, `"fence"`} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("private status field")
					}
				}
				if strings.HasSuffix(path, "/options") && strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
					t.Fatal("nil options not normalized")
				}
			}
		}
		for _, suffix := range []string{"?", "?limit=1", "?unexpected=1"} {
			if w := connectionCall(t, s, "GET", path+suffix, session, ""); w.Code != 400 {
				t.Fatal("read query accepted")
			}
		}
		if w := connectionCall(t, s, "GET", path, session, "{}"); w.Code != 400 {
			t.Fatal("read body accepted")
		}
	}
}

func connectionRunnerBodies(t *testing.T) map[string]string {
	t.Helper()
	a := domain.HostingAuthority{OperationID: "hop_connection", RunID: "run_connection", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	hash := "sha256:" + strings.Repeat("b", 64)
	candidate := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: a.OperationID, PlanHash: hash, CreateNonce: strings.Repeat("a", 32), ContainerID: strings.Repeat("c", 64), ImageID: hash, DaemonID: "daemon", DefaultNetworkID: strings.Repeat("d", 64)}
	candidate.Hash = candidate.DocumentHash()
	target := domain.HostingDatabaseBindingEndpointTarget{ContainerID: candidate.ContainerID, ContainerOperationID: a.OperationID, ImageID: hash, ReleaseHash: hash, ContainerPlanHash: hash, NetworkID: strings.Repeat("e", 64), NetworkName: "private_network", DaemonID: "daemon"}
	plan := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: a.OperationID, BindingHash: hash, MembershipRevision: 1, Action: "attach", Target: target}
	plan.Hash = plan.DocumentHash()
	stage := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, ReceiptID: "created_receipt", OperationID: a.OperationID, PlanHash: hash, Phase: "candidate_created", Candidate: candidate, AttachPlan: plan}
	stage.Hash = stage.DocumentHash()
	receipt := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "retirement_receipt", OperationID: a.OperationID, PlanHash: hash, BindingHash: hash, MembershipRevision: 1, Phase: "retire_intent", Intent: &domain.HostingDatabaseBindingEndpoint{Target: target, EndpointID: strings.Repeat("f", 64)}}
	receipt.Hash = receipt.DocumentHash()
	values := map[string]any{"stage": domain.HostingApplicationDatabaseStageRequest{HostingAuthority: a, Stage: stage}, "retirement-plan": a, "state": a, "retirement": domain.HostingApplicationDatabaseRetirementRequest{HostingAuthority: a, PlanHash: hash, Receipt: receipt}, "authorize": domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("a", 32), Action: "create"}}
	out := map[string]string{}
	for method, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out[method] = string(raw)
	}
	return out
}

func TestDatabaseConnectionHTTPRunnerCurrentBearerAndRouting(t *testing.T) {
	s, service, repo, session := connectionHTTPFixture(t, true)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	registered, err := service.RegisterRunner(admin, app.RunnerInput{ID: "runner_connection", Name: "connection", Capabilities: []string{domain.HostingApplicationDatabaseCapability}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := createBrowserSession(t, s)
	sum := sha256.Sum256([]byte(registered.Token))
	for method, body := range connectionRunnerBodies(t) {
		path := "/api/v1/runners/hosting/database-binding/" + method
		for _, token := range []string{"", session, "invalid_bearer"} {
			count := len(repo.calls)
			w := connectionCall(t, s, "POST", path, token, body)
			if w.Code != 401 || len(repo.calls) != count {
				t.Fatal("runner accepted session/missing/wrong bearer", method, w.Code)
			}
		}
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("runner accepted browser cookie")
		}
		count := len(repo.calls)
		w = connectionCall(t, s, "POST", path, registered.Token, body)
		if w.Code != 200 || len(repo.calls) != count+1 || repo.calls[count] != method || repo.runner != registered.Runner.ID || repo.credential != hex.EncodeToString(sum[:]) || repo.authority.OperationID != "hop_connection" || repo.authority.Fence != "fence" || repo.authority.Attempt != 1 {
			t.Fatalf("runner routing/identity %s status=%d body=%s", method, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), registered.Token) {
			t.Fatal("runner bearer reflected")
		}
	}
	if _, err = repo.UpdateRunnerToken(t.Context(), registered.Runner.ID, hex.EncodeToString(sum[:]), domain.RunnerRevoked, time.Now()); err != nil {
		t.Fatal(err)
	}
	body := connectionRunnerBodies(t)["state"]
	count := len(repo.calls)
	if w := connectionCall(t, s, "POST", "/api/v1/runners/hosting/database-binding/state", registered.Token, body); w.Code != 401 || len(repo.calls) != count {
		t.Fatal("revoked runner accepted", w.Code)
	}
}

func TestDatabaseConnectionHTTPStrictBodiesAndUnavailable(t *testing.T) {
	s, service, repo, session := connectionHTTPFixture(t, true)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	registered, err := service.RegisterRunner(admin, app.RunnerInput{ID: "runner_connection", Name: "connection", Capabilities: []string{domain.HostingApplicationDatabaseCapability}})
	if err != nil {
		t.Fatal(err)
	}
	bodies := connectionPublicBodies()
	for phase, body := range connectionRunnerBodies(t) {
		bodies["/api/v1/runners/hosting/database-binding/"+phase] = body
	}
	for path, valid := range bodies {
		token := session
		if databaseConnectionRunnerPath(path) {
			token = registered.Token
		}
		badBodies := []string{"", `null`, `[]`, `{}`, valid + `{}`, strings.TrimSuffix(valid, "}") + `,"unknown":"private_canary"}`, strings.TrimSuffix(valid, "}") + `,"unknown":null}`, strings.TrimSuffix(valid, "}") + `,"unknown":1,"unknown":2}`, strings.Replace(valid, `"operation_id"`, `"Operation_id"`, 1), strings.Replace(valid, `"expected_generation":1`, `"expected_generation":null`, 1)}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(valid), &fields) != nil {
			t.Fatal("fixture JSON")
		}
		for field, value := range fields {
			badBodies = append(badBodies, `{"`+field+`":`+string(value)+`,`+strings.TrimPrefix(valid, "{"), strings.Replace(valid, `"`+field+`"`, `"`+strings.ToUpper(field[:1])+field[1:]+`"`, 1), strings.Replace(valid, `"`+field+`":`+string(value), `"`+field+`":null`, 1))
			break
		}
		for _, bad := range badBodies {
			if bad == valid {
				continue
			}
			count := len(repo.calls)
			w := connectionCall(t, s, "POST", path, token, bad)
			if w.Code != 400 || len(repo.calls) != count || strings.Contains(w.Body.String(), "private_canary") {
				t.Fatal("open/ambiguous body", path, w.Code)
			}
		}
		request := httptest.NewRequest("POST", path, strings.NewReader(valid))
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Authorization", "Bearer "+token)
		unsupported := httptest.NewRecorder()
		s.ServeHTTP(unsupported, request)
		if unsupported.Code != 415 || unsupported.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("media type/cache boundary")
		}
		if w := connectionCall(t, s, "POST", path+"?authority=foreign", token, valid); w.Code != 400 {
			t.Fatal("query accepted")
		}
		if w := connectionCall(t, s, "POST", path, token, strings.Repeat(" ", domain.HostingDatabaseConnectionJSONLimit+1)+valid); w.Code != 413 {
			t.Fatal("oversized body accepted", w.Code)
		}
		repo.err = errors.New("private_canary upstream failure")
		w := connectionCall(t, s, "POST", path, token, valid)
		if w.Code != 500 || strings.Contains(w.Body.String(), "private_canary") {
			t.Fatal("upstream error disclosed", w.Code)
		}
		repo.err = store.ErrHostingDatabaseConnectionUnavailable
		w = connectionCall(t, s, "POST", path, token, valid)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "hosting_database_connection_unavailable") {
			t.Fatal("unavailable backend not explicit", w.Code)
		}
		repo.err = nil
	}
	count := len(repo.calls)
	recoveryPath := "/api/v1/resources/connection_app/database-connection/recovery"
	body := strings.Replace(connectionPublicBodies()[recoveryPath], `"observe"`, `"resume"`, 1)
	if w := connectionCall(t, s, "POST", recoveryPath, session, body); w.Code != 400 || len(repo.calls) != count {
		t.Fatal("effectful recovery admitted")
	}
	// A backend which implements neither new repository must also fail safely.
	unavailable, _, _, user := connectionHTTPFixture(t, false)
	path := "/api/v1/resources/connection_app/database-connections"
	if w := connectionCall(t, unavailable, "POST", path, user, connectionPublicBodies()[path]); w.Code != 503 {
		t.Fatal("unsupported backend manufactured success", w.Code)
	}
}

func TestDatabaseConnectionHTTPOpenAPIRoutes(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range PublicRoutes() {
		if !databaseConnectionPath(route.Path) {
			continue
		}
		item := doc.Paths.Find(route.Path)
		if item == nil || item.GetOperation(route.Method) == nil {
			t.Fatal("new route missing OpenAPI", route.Path)
		}
	}
}

var _ store.HostingDatabaseConnectionRepository = (*connectionHTTPRepository)(nil)
var _ store.HostingApplicationDatabaseRunnerRepository = (*connectionHTTPRepository)(nil)

func TestDatabaseConnectionHTTPNamedSchemaParity(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	values := []any{domain.HostingDatabaseConnectionInput{}, domain.HostingDatabaseBindingInput{}, domain.HostingDatabaseConnection{}, domain.HostingDatabaseConnectionOption{}, domain.HostingDatabaseMaintenanceInput{}, domain.HostingDatabaseConnectionRecoveryInput{}, domain.HostingApplicationDatabaseStageRequest{}, domain.HostingApplicationDatabaseStage{}, domain.HostingApplicationDatabaseCandidate{}, domain.HostingDatabaseBinding{}, domain.HostingDatabaseBindingEndpointTarget{}, domain.HostingDatabaseBindingEndpointPlan{}, domain.HostingDatabaseBindingEndpoint{}, domain.HostingDatabaseBindingEndpointObservation{}, domain.HostingDatabaseBindingEndpointReceipt{}, domain.HostingApplicationDatabaseRetirementRequest{}, domain.HostingApplicationDatabaseEffectRequest{}, domain.HostingApplicationDatabaseEffectAuthorization{}, domain.HostingApplicationDatabaseExecutionState{}, domain.HostingApplicationDatabaseExecutionProof{}, domain.HostingDatabaseNetworkMemberEvidence{}, domain.HostingDatabaseNetworkRetirementEvidence{}, domain.HostingDatabaseMaintenanceProof{}, domain.HostingApplicationDatabaseRef{}, domain.HostingApplicationDatabasePlan{}, domain.HostingDatabaseNetworkMember{}, domain.HostingDatabaseNetworkPlan{}, domain.HostingDatabaseNetworkPredecessor{}}
	var fields func(reflect.Type, func(string, bool))
	fields = func(typ reflect.Type, visit func(string, bool)) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous {
				fields(f.Type, visit)
				continue
			}
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			visit(name, !strings.Contains(tag, ","))
		}
	}
	for _, value := range values {
		typ := reflect.TypeOf(value)
		schema := doc.Components.Schemas[typ.Name()]
		if schema == nil || schema.Value == nil || schema.Value.AdditionalProperties.Has == nil || *schema.Value.AdditionalProperties.Has {
			t.Fatal("missing closed named schema", typ.Name())
		}
		count := 0
		fields(typ, func(name string, required bool) {
			count++
			if schema.Value.Properties[name] == nil || slices.Contains(schema.Value.Required, name) != required {
				t.Fatal("schema differs from domain field", typ.Name(), name)
			}
		})
		if len(schema.Value.Properties) != count {
			t.Fatal("invented schema fields", typ.Name())
		}
	}
	for phase, body := range connectionRunnerBodies(t) {
		name := map[string]string{"stage": "HostingApplicationDatabaseStageRequest", "retirement-plan": "HostingAuthority", "retirement": "HostingApplicationDatabaseRetirementRequest", "authorize": "HostingApplicationDatabaseEffectRequest", "state": "HostingAuthority"}[phase]
		var value any
		if json.Unmarshal([]byte(body), &value) != nil {
			t.Fatal("request fixture")
		}
		if err = doc.Components.Schemas[name].Value.VisitJSON(value); err != nil {
			t.Fatal("typed request differs from schema", name, err)
		}
	}
	for path, body := range connectionPublicBodies() {
		name := "HostingDatabaseConnectionInput"
		switch {
		case strings.HasSuffix(path, "/deployments"):
			name = "HostingDeploymentInput"
		case strings.HasSuffix(path, "/recovery"):
			name = "HostingDatabaseConnectionRecoveryInput"
		case strings.HasSuffix(path, "/maintenance"):
			name = "HostingDatabaseMaintenanceInput"
		}
		var value any
		if json.Unmarshal([]byte(body), &value) != nil {
			t.Fatal("fixture JSON")
		}
		if err = doc.Components.Schemas[name].Value.VisitJSON(value); err != nil {
			t.Fatal("typed public request differs from schema", name, err)
		}
	}
	// Pending and unresolved status contain no secret-bearing expansion and may
	// legitimately have empty IDs/hashes while prerequisite operations settle.
	raw, err := json.Marshal(domain.HostingDatabaseConnection{ID: "hcn_status", ApplicationResourceID: "connection_app", State: "unresolved", RecoveryLimit: "observe_only"})
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		t.Fatal("status JSON")
	}
	if err = doc.Components.Schemas["HostingDatabaseConnection"].Value.VisitJSON(value); err != nil {
		t.Fatal("pending status not represented", err)
	}
	operation := doc.Components.Schemas["HostingOperation"].Value
	for _, field := range []string{"application_database", "database_maintenance"} {
		if operation.Properties[field] == nil || operation.Properties[field].Ref == "" || slices.Contains(operation.Required, field) {
			t.Fatal("sidecar must be optional named proof", field)
		}
	}
}

func TestDatabaseConnectionHTTPRuntimeReferenceSchemaValues(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"HostingDatabaseMaintenanceInput":               {"idempotency_key"},
		"HostingDatabaseConnectionRecoveryInput":        {"idempotency_key"},
		"HostingApplicationDatabaseStageRequest":        {"lease_id", "fence"},
		"HostingApplicationDatabaseStage":               {"receipt_id"},
		"HostingApplicationDatabaseCandidate":           {"daemon_id"},
		"HostingDatabaseBindingEndpointTarget":          {"daemon_id"},
		"HostingDatabaseBindingEndpointReceipt":         {"receipt_id"},
		"HostingApplicationDatabaseRetirementRequest":   {"lease_id", "fence"},
		"HostingApplicationDatabaseEffectRequest":       {"lease_id", "fence"},
		"HostingApplicationDatabaseEffectAuthorization": {"lease_id", "fence"},
		"HostingDatabaseNetworkMember":                  {"receipt_id"},
	}
	values := []struct{ name, value string }{
		{"interior_space", "maintain members"}, {"interior_repeated_space", "maintain  members"}, {"single", "a"}, {"ascii_limit", strings.Repeat("x", 128)},
		{"unicode_interior", "内 部"}, {"unicode_byte_limit", strings.Repeat("é", 64)}, {"interior_separator", "a\u2028b"},
		{"empty", ""}, {"leading_space", " leading"}, {"trailing_space", "trailing "}, {"leading_unicode_space", "\u00a0leading"}, {"trailing_unicode_space", "trailing\u3000"},
		{"tab_control", "a\tb"}, {"newline_control", "a\nb"}, {"null_control", "a\x00b"}, {"unicode_control", "a\u0085b"}, {"ascii_over_limit", strings.Repeat("x", 129)},
	}
	for name, properties := range fields {
		for _, field := range properties {
			t.Run(name+"/"+field, func(t *testing.T) {
				schema := doc.Components.Schemas[name].Value.Properties[field].Value
				for _, sample := range values {
					// This exported validator exercises the same validRuntimeReferenceID
					// contract as each scalar above without replacing its domain validator.
					valid := (domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: 1, IdempotencyKey: sample.value}).Validate() == nil
					if got := schema.VisitJSONString(sample.value) == nil; got != valid {
						t.Fatalf("scalar parity differs for %s: schema=%t domain=%t", sample.name, got, valid)
					}
				}
			})
		}
	}
	// Deployment idempotency retains its separate whitespace-forbidding
	// domain contract. Its accepted shared schema is outside this repair.
	deployment := domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "maintain members", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}
	if domain.ValidateHostingDeployment(deployment) == nil {
		t.Fatal("shared deployment validator was relaxed")
	}

}

func TestDatabaseConnectionHTTPRuntimeReferenceRequestParity(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, service, repo, user := connectionHTTPFixture(t, true)
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	runner, err := service.RegisterRunner(admin, app.RunnerInput{ID: "runner_reference", Name: "reference", Capabilities: []string{domain.HostingApplicationDatabaseCapability}})
	if err != nil {
		t.Fatal(err)
	}
	bodies := connectionPublicBodies()
	for _, item := range []struct{ path, schema string }{
		{"/api/v1/resources/connection_database/database/maintenance", "HostingDatabaseMaintenanceInput"},
		{"/api/v1/resources/connection_app/database-connection/recovery", "HostingDatabaseConnectionRecoveryInput"},
	} {
		var body map[string]any
		if json.Unmarshal([]byte(bodies[item.path]), &body) != nil {
			t.Fatal("request fixture")
		}
		body["idempotency_key"] = "maintain members"
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Components.Schemas[item.schema].Value.VisitJSON(body) != nil {
			t.Fatal("schema rejects domain-valid owner request", item.schema)
		}
		count := len(repo.calls)
		if w := connectionCall(t, s, "POST", item.path, user, string(raw)); w.Code != 202 || len(repo.calls) != count+1 {
			t.Fatal("owner HTTP rejects valid interior spaces", w.Code)
		}
	}
	for _, phase := range []string{"stage", "retirement", "authorize"} {
		var body map[string]any
		if json.Unmarshal([]byte(connectionRunnerBodies(t)[phase]), &body) != nil {
			t.Fatal("runner fixture")
		}
		body["lease_id"], body["fence"] = "lease interior", "fence interior"
		name := map[string]string{"stage": "HostingApplicationDatabaseStageRequest", "retirement": "HostingApplicationDatabaseRetirementRequest", "authorize": "HostingApplicationDatabaseEffectRequest"}[phase]
		if doc.Components.Schemas[name].Value.VisitJSON(body) != nil {
			t.Fatal("schema rejects domain-valid runner request", name)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		count := len(repo.calls)
		if w := connectionCall(t, s, "POST", "/api/v1/runners/hosting/database-binding/"+phase, runner.Token, string(raw)); w.Code != 200 || len(repo.calls) != count+1 || repo.authority.LeaseID != "lease interior" || repo.authority.Fence != "fence interior" {
			t.Fatal("runner HTTP reference parity", phase, w.Code)
		}
	}
	deploymentPath := "/api/v1/resources/connection_app/database-connection/deployments"
	count := len(repo.calls)
	deploymentBody := strings.Replace(bodies[deploymentPath], `"connection_deploy"`, `"maintain members"`, 1)
	if w := connectionCall(t, s, "POST", deploymentPath, user, deploymentBody); w.Code != 400 || len(repo.calls) != count {
		t.Fatal("deployment idempotency whitespace was admitted")
	}
	// JSON strings require valid Unicode. Test invalid raw UTF-8 at the HTTP
	// boundary, before JSON replacement could erase the invalid input bytes.
	invalid := string([]byte{'b', 0xff, 'd'})
	if (domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: 1, IdempotencyKey: invalid}).Validate() == nil {
		t.Fatal("domain accepted invalid UTF-8")
	}
	path := "/api/v1/resources/connection_database/database/maintenance"
	for _, value := range []string{invalid, strings.Repeat("é", 65)} {
		raw := `{"expected_generation":1,"expected_membership_revision":1,"idempotency_key":"` + value + `"}`
		count := len(repo.calls)
		if w := connectionCall(t, s, "POST", path, user, raw); w.Code != 400 || len(repo.calls) != count {
			t.Fatal("invalid UTF-8 or byte-over-limit request reached repository")
		}
	}
}
