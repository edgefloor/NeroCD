package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestHostingAPI(t *testing.T) {
	server, mem := newTestServer()
	if _, err := mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_hosting_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	request := func(method, path, body, credential string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	request("GET", "/api/v1/resources?project_id=proj_platform", "", "", 401)
	request("GET", "/api/v1/resources", "", token, 400)
	request("GET", "/api/v1/project-environments", "", token, 400)
	request("GET", "/api/v1/resources?project_id=missing", "", token, 404)
	body := request("POST", "/api/v1/project-environments", `{"project_id":"proj_platform","name":"production"}`, token, 201)
	var environment domain.ProjectEnvironment
	if err := json.Unmarshal(body, &environment); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/project-environments", `{"project_id":"proj_platform","name":"production"}`, token, 409)
	request("POST", "/api/v1/project-environments", `{"project_id":"proj_platform","name":"forbidden"}`, viewer, 403)
	create := fmt.Sprintf(`{"project_id":"proj_platform","project_environment_id":%q,"name":"web","kind":"application","source":{"kind":"image","image":{"reference":"localhost:5000/acme/web:v1"}}}`, environment.ID)
	var item domain.Resource
	if err := json.Unmarshal(request("POST", "/api/v1/resources", create, token, 201), &item); err != nil {
		t.Fatal(err)
	}
	if item.Generation != 1 {
		t.Fatalf("generation=%d", item.Generation)
	}
	request("POST", "/api/v1/resources", create, token, 409)
	request("POST", "/api/v1/resources", create, viewer, 403)
	request("GET", "/api/v1/resources/"+item.ID, "", viewer, 200)
	request("PATCH", "/api/v1/resources/"+item.ID, `{"name":"renamed","expected_generation":1}`, viewer, 403)
	body = request("PATCH", "/api/v1/resources/"+item.ID, `{"name":"renamed","expected_generation":1}`, token, 200)
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if item.Generation != 2 || item.Name != "renamed" {
		t.Fatalf("updated=%+v", item)
	}
	request("PATCH", "/api/v1/resources/"+item.ID, `{"name":"stale","expected_generation":1}`, token, 409)
	for _, patch := range []string{`{"expected_generation":2,"name":null,"source":{"kind":"image","image":{"reference":"nginx:1.27"}}}`, `{"expected_generation":2,"name":"web","source":null}`, `{"name":"bad"}`, `{"expected_generation":2}`, `{"expected_generation":2,"project_id":"proj_security"}`, `{"expected_generation":2,"generation":99,"name":"x"}`, `{"expected_generation":2,"source":{"kind":"image","image":{"reference":"nginx"}}}`, `{"expected_generation":2,"source":{"kind":"image","image":{"reference":"nginx:v1","password":"secret"}}}`} {
		request("PATCH", "/api/v1/resources/"+item.ID, patch, token, 400)
	}
	body = request("GET", "/api/v1/resources?project_id=proj_platform&project_environment_id="+environment.ID+"&limit=1&offset=0", "", viewer, 200)
	var page struct {
		Items []domain.Resource `json:"items"`
		Total int               `json:"total"`
		Count int               `json:"count"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Count != 1 || len(page.Items) != 1 || page.Items[0].Generation != 2 {
		t.Fatalf("page=%s", body)
	}
	request("GET", "/api/v1/project-environments?project_id=proj_platform&limit=0", "", viewer, 200)
	request("GET", "/api/v1/resources/missing", "", token, 404)
	request("POST", "/api/v1/resources", strings.Replace(create, "localhost:5000/acme/web:v1", "nginx", 1), token, 400)
}

func TestGitResourceAPIStrictInputAndMemoryUnavailable(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	request := func(body string, want int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/resources", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/project-environments", strings.NewReader(`{"project_id":"proj_platform","name":"git"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var env domain.ProjectEnvironment
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != 201 {
		t.Fatal(rec.Code, err)
	}
	for _, source := range []string{`null`, `{"kind":"git","git":null}`, `{"kind":"git","git":{"source_id":"caller"}}`, `{"kind":"git","git":{"repository_policy":{}}}`, `{"kind":"git","git":{"generation":1}}`, `{"kind":"image","image":{"reference":"nginx:1"},"git":{}}`} {
		request(fmt.Sprintf(`{"project_id":"proj_platform","project_environment_id":%q,"name":"git","kind":"application","source":%s}`, env.ID, source), 400)
	}
	request(fmt.Sprintf(`{"project_id":"proj_platform","project_environment_id":%q,"name":"git","kind":"application","source":{"kind":"git","git":{"repository_url":"https://github.com/acme/web","requested_ref":"main","credential_kind":"public","settings":{"strategy":"dockerfile"}}}}`, env.ID), 503)
}
