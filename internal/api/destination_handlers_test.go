package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestDestinationAPIEnforcesRolesScopesAndImmutableBinding(t *testing.T) {
	server, mem := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	passwordHash, err := auth.HashPassword("runner-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = mem.ProvisionOIDCUser(t.Context(), domain.User{ID: "usr_destination_runner_admin", Email: "runner-admin@example.local", Name: "Destination Runner Administrator", Status: domain.UserActive, GlobalRole: domain.RoleRunnerAdmin, PasswordHash: passwordHash, CreatedAt: now}, domain.OIDCExternalIdentity{Issuer: "https://identity.example.invalid", Subject: "destination-admin", UserID: "usr_destination_runner_admin", CreatedAt: now}, domain.AuditEvent{ID: "aud_destination_runner_admin", ActorID: "usr_bootstrap", Action: "identity.provision", TargetID: "usr_destination_runner_admin", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	humanRunnerAdmin := createTestSessionFor(t, server, "runner-admin@example.local", "runner-admin-password")
	legacy, err := server.app.CreateAPIToken(ctx, app.APITokenInput{Name: "legacy runner administrator", Roles: []string{domain.RoleRunnerAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	runnerAdmin := legacy.Token
	account, err := server.app.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: "proj_security", Name: "placement bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	runnerID := "runner_api_destination"
	enrollment, err := server.app.CreateRunnerEnrollment(ctx, app.RunnerEnrollmentInput{RunnerID: runnerID, RunnerName: "Destination Runner", Capabilities: []string{"compose_deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(runnerID))
	hash := hex.EncodeToString(sum[:])
	if _, err = server.app.ConsumeRunnerEnrollment(t.Context(), enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + hash[:32], CredentialHash: hash}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s returned%d want%d body=%s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	createBody := fmt.Sprintf(`{"name":"Docker host","runner_id":%q}`, runnerID)
	request("GET", "/api/v1/servers", "", "", 401)
	request("POST", "/api/v1/servers", createBody, viewer, 403)
	var detail app.ServerDetail
	if err = json.Unmarshal(request("POST", "/api/v1/servers", createBody, runnerAdmin, 201), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Destination.Name != "default" || detail.Destination.ServerID != detail.Server.ID || detail.Destination.RuntimeKind != "docker_compose" {
		t.Fatalf("detail=%+v", detail)
	}
	request("POST", "/api/v1/servers", createBody, admin, 409)
	request("GET", "/api/v1/servers/"+detail.Server.ID, "", humanRunnerAdmin, 200)
	request("GET", "/api/v1/servers/"+detail.Server.ID, "", viewer, 403)
	request("GET", "/api/v1/servers/"+detail.Server.ID, "", account.Token, 403)
	request("GET", "/api/v1/servers/bad.id", "", admin, 400)
	request("GET", "/api/v1/servers/missing", "", admin, 404)
	request("POST", "/api/v1/servers", `{"name":"","runner_id":"runner_api_destination"}`, admin, 400)
	request("POST", "/api/v1/servers", `{"name":"bad","runner_id":"bad/id"}`, admin, 400)
	request("POST", "/api/v1/servers", `{"name":"missing","runner_id":"runner_missing"}`, admin, 404)
	request("POST", "/api/v1/servers", `{"name":"custom","runner_id":"runner_api_destination","destination_name":"override"}`, admin, 400)
	var page struct {
		Items                       []json.RawMessage `json:"items"`
		Limit, Offset, Count, Total int
	}
	if err = json.Unmarshal(request("GET", "/api/v1/servers?limit=0&offset=0", "", admin, 200), &page); err != nil || page.Total != 1 || page.Count != 0 || len(page.Items) != 0 {
		t.Fatalf("server pagination=%+v %v", page, err)
	}
	for _, path := range []string{"/api/v1/destinations", "/api/v1/destinations?project_id=proj_security&server_id=" + detail.Server.ID, "/api/v1/destinations?project_id=&server_id=" + detail.Server.ID, "/api/v1/destinations?project_id=", "/api/v1/destinations?server_id=bad.id", "/api/v1/destinations?project_id=proj_security&project_id=proj_platform"} {
		request("GET", path, "", admin, 400)
	}
	request("GET", "/api/v1/destinations?project_id=missing", "", admin, 404)
	request("GET", "/api/v1/destinations?server_id=missing", "", admin, 404)
	request("GET", "/api/v1/destinations?server_id="+detail.Server.ID, "", viewer, 403)
	request("GET", "/api/v1/destinations?server_id="+detail.Server.ID, "", admin, 200)
	grantPath := "/api/v1/destinations/" + detail.Destination.ID + "/project-grants"
	for _, token := range []string{viewer, account.Token} {
		request("GET", grantPath, "", token, 403)
		request("POST", grantPath, `{"project_id":"proj_security"}`, token, 403)
		request("POST", grantPath+"/revoke", `{"project_id":"proj_security"}`, token, 403)
	}
	request("POST", grantPath, `{"project_id":"missing"}`, admin, 404)
	request("POST", grantPath, `{"project_id":"bad/id"}`, admin, 400)
	request("POST", grantPath, `{"project_id":null}`, admin, 400)
	request("POST", "/api/v1/destinations/missing/project-grants", `{"project_id":"proj_security"}`, admin, 404)
	request("POST", grantPath, `{"project_id":"proj_security"}`, admin, 201)
	request("POST", grantPath, `{"project_id":"proj_security"}`, admin, 409)
	request("POST", grantPath, `{"project_id":"proj_platform"}`, admin, 201)
	request("GET", grantPath, "", admin, 200)
	if err = json.Unmarshal(request("GET", "/api/v1/destinations?project_id=proj_security", "", viewer, 200), &page); err != nil || page.Total != 1 || page.Count != 1 {
		t.Fatalf("project pagination=%+v %v", page, err)
	}
	request("GET", "/api/v1/destinations?project_id=proj_platform", "", account.Token, 403)
	createResource := func(project, name string) domain.Resource {
		t.Helper()
		environment, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: project, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		resource, err := server.app.CreateResource(ctx, app.ResourceInput{ProjectID: project, ProjectEnvironmentID: environment.ID, Name: name, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
		if err != nil {
			t.Fatal(err)
		}
		return resource
	}
	resource := createResource("proj_security", "security-web")
	cross := createResource("proj_platform", "platform-web")
	pending := createResource("proj_security", "pending-web")
	bindingPath := "/api/v1/resources/" + resource.ID + "/binding"
	bindingBody := fmt.Sprintf(`{"destination_id":%q}`, detail.Destination.ID)
	request("GET", bindingPath, "", viewer, 404)
	request("POST", bindingPath, bindingBody, viewer, 403)
	request("POST", "/api/v1/resources/"+cross.ID+"/binding", bindingBody, account.Token, 403)
	request("GET", "/api/v1/resources/"+cross.ID+"/binding", "", account.Token, 403)
	request("POST", bindingPath, fmt.Sprintf(`{"destination_id":%q,"project_id":"proj_platform"}`, detail.Destination.ID), account.Token, 400)
	request("POST", bindingPath, fmt.Sprintf(`{"destination_id":%q,"runtime_namespace":"attacker"}`, detail.Destination.ID), account.Token, 400)
	request("POST", bindingPath, `{"destination_id":"bad/id"}`, account.Token, 400)
	request("POST", bindingPath, `{"destination_id":"missing"}`, account.Token, 404)
	request("GET", "/api/v1/resources/bad.id/binding", "", admin, 400)
	request("GET", "/api/v1/resources/missing/binding", "", admin, 404)
	var binding domain.ResourceBinding
	if err = json.Unmarshal(request("POST", bindingPath, bindingBody, account.Token, 201), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.ResourceID != resource.ID || binding.DestinationID != detail.Destination.ID || binding.CreatedBy != account.ServiceAccount.ID || !strings.HasPrefix(binding.RuntimeNamespace, "ncd_") {
		t.Fatalf("binding=%+v", binding)
	}
	request("POST", bindingPath, bindingBody, account.Token, 409)
	request("GET", bindingPath, "", viewer, 200)
	request("POST", grantPath+"/revoke", `{"project_id":"proj_security"}`, admin, 200)
	request("POST", grantPath+"/revoke", `{"project_id":"proj_security"}`, admin, 409)
	if err = json.Unmarshal(request("GET", "/api/v1/destinations?project_id=proj_security", "", viewer, 200), &page); err != nil || page.Total != 0 || page.Count != 0 {
		t.Fatalf("revoked pagination=%+v %v", page, err)
	}
	var retained domain.ResourceBinding
	if err = json.Unmarshal(request("GET", bindingPath, "", viewer, 200), &retained); err != nil || retained != binding {
		t.Fatalf("revocation changed binding=%+v %v", retained, err)
	}
	request("POST", "/api/v1/resources/"+pending.ID+"/binding", bindingBody, account.Token, 403)
	request("POST", grantPath, `{"project_id":"proj_security"}`, admin, 201)
	request("POST", "/api/v1/resources/"+pending.ID+"/binding", bindingBody, account.Token, 201)
}
