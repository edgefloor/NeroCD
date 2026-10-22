package api

import (
	"encoding/json"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostingRuntimeHTTPStrictBoundary(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	envRaw := call("POST", "/api/v1/project-environments", `{"project_id":"proj_platform","name":"runtime"}`, 201)
	var env domain.ProjectEnvironment
	if err := json.Unmarshal(envRaw.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	resourceRaw := call("POST", "/api/v1/resources", `{"project_id":"proj_platform","project_environment_id":"`+env.ID+`","name":"runtime","kind":"application","source":{"kind":"image","image":{"reference":"nginx:1.27"}}}`, 201)
	var resource domain.Resource
	if err := json.Unmarshal(resourceRaw.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/resources/" + resource.ID + "/runtime-configuration"
	valid := `{"expected_resource_generation":1,"expected_runtime_generation":0,"request_id":"0123456789abcdef0123456789abcdef","entries":[{"name":"APP","kind":"literal","value":""}]}`
	for _, method := range []string{"GET", "PUT"} {
		body := ""
		if method == "PUT" {
			body = valid
		}
		rec := call(method, path, body, 503)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache policy missing")
		}
	}
	for _, body := range []string{strings.Replace(valid, `"name":"APP"`, `"name":"APP","NAME":"HIDDEN"`, 1), strings.Replace(valid, `"expected_runtime_generation":0`, `"expected_runtime_generation":0,"Expected_Runtime_Generation":2`, 1), valid + ` {}`, strings.Replace(valid, `"value":""`, `"value":"\ud800"`, 1), strings.Replace(valid, `"entries":`, `"unknown":true,"entries":`, 1)} {
		rec := call("PUT", path, body, 400)
		if strings.Contains(rec.Body.String(), "HIDDEN") {
			t.Fatal("input reflected")
		}
	}
	call("PUT", path, strings.Repeat(" ", domain.HostingRuntimeRequestLimit)+valid, 413)
}

func TestHostingRuntimeHTTPPostgresRoundTrip(t *testing.T) {
	server, _, db := newHostingBuildAPIFixture(t)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(ctx, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "runtime", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, server)
	call := func(method, body string, want int) domain.HostingRuntimeConfiguration {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/resources/"+resource.ID+"/runtime-configuration", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s code=%d want=%d body=%s", method, rec.Code, want, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache header missing")
		}
		var got domain.HostingRuntimeConfiguration
		if want == 200 {
			var envelope struct {
				RuntimeConfiguration *domain.HostingRuntimeConfiguration `json:"runtime_configuration"`
			}
			if e := json.Unmarshal(rec.Body.Bytes(), &envelope); e != nil {
				t.Fatal(e)
			}
			if envelope.RuntimeConfiguration == nil {
				t.Fatal("missing runtime_configuration envelope")
			}
			got = *envelope.RuntimeConfiguration
		}
		return got
	}
	if zero := call("GET", "", 200); zero.RuntimeGeneration != 0 || zero.Entries == nil || zero.CreatedAt != nil {
		t.Fatal(zero)
	}
	input := `{"expected_resource_generation":1,"expected_runtime_generation":0,"request_id":"0123456789abcdef0123456789abcdef","entries":[{"name":"VISIBLE","kind":"literal","value":"  visible  "}]}`
	var beforeRuns int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM task_runs`).Scan(&beforeRuns); err != nil {
		t.Fatal(err)
	}
	saved := call("PUT", input, 200)
	if saved.RuntimeGeneration != 1 || *saved.Entries[0].Value != "  visible  " {
		t.Fatal(saved)
	}
	replay := call("PUT", input, 200)
	if !saved.CreatedAt.Equal(*replay.CreatedAt) {
		t.Fatal("replay changed immutable version")
	}
	call("PUT", strings.Replace(input, "visible  ", "changed  ", 1), 409)
	if current := call("GET", "", 200); current.RuntimeGeneration != 1 {
		t.Fatal(current)
	}
	var runs int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM task_runs`).Scan(&runs); err != nil || runs != beforeRuns {
		t.Fatalf("save scheduled work %d %v", runs, err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'`); err != nil {
		t.Fatal(err)
	}
	// Bootstrap project membership may still grant owner; remove it to verify current authority.
	if _, err = db.Exec(t.Context(), `DELETE FROM project_members WHERE user_id='usr_bootstrap' AND project_id='proj_platform'`); err != nil {
		t.Fatal(err)
	}
	call("PUT", input, 403)
}
