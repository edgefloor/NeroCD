package api

import (
	"encoding/json"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
)

func TestRunnerAdminCanManageRunnersAndViewerIsDenied(t *testing.T) {
	server, _ := newTestServer()
	systemAdmin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	request := func(method string, path string, token string, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	requireStatus := func(rec *httptest.ResponseRecorder, want int, operation string) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("%s = %d, want %d: %s", operation, rec.Code, want, rec.Body.String())
		}
	}

	runnerAdminTokenRequest := request(http.MethodPost, "/api/v1/api-tokens", systemAdmin, `{"name":"runner admin contract","roles":["runner_admin"]}`)
	requireStatus(runnerAdminTokenRequest, http.StatusCreated, "create runner_admin token")
	var runnerAdminToken app.CreatedAPIToken
	if err := json.NewDecoder(runnerAdminTokenRequest.Body).Decode(&runnerAdminToken); err != nil {
		t.Fatal(err)
	}
	if runnerAdminToken.Token == "" {
		t.Fatal("runner_admin API token is empty")
	}

	runnerAdminMe := request(http.MethodGet, "/api/v1/me", runnerAdminToken.Token, "")
	requireStatus(runnerAdminMe, http.StatusOK, "runner_admin current principal")
	var runnerAdminPrincipal auth.Principal
	if err := json.NewDecoder(runnerAdminMe.Body).Decode(&runnerAdminPrincipal); err != nil {
		t.Fatal(err)
	}
	if !containsRole(runnerAdminPrincipal.Roles, "runner_admin") || containsRole(runnerAdminPrincipal.Roles, "system_admin") {
		t.Fatalf("runner_admin principal roles = %#v, want runner_admin without system_admin", runnerAdminPrincipal.Roles)
	}
	viewerMe := request(http.MethodGet, "/api/v1/me", viewer, "")
	requireStatus(viewerMe, http.StatusOK, "viewer current principal")
	var viewerPrincipal auth.Principal
	if err := json.NewDecoder(viewerMe.Body).Decode(&viewerPrincipal); err != nil {
		t.Fatal(err)
	}
	if containsRole(viewerPrincipal.Roles, "runner_admin") || containsRole(viewerPrincipal.Roles, "system_admin") {
		t.Fatalf("viewer principal roles = %#v, want no runner administration role", viewerPrincipal.Roles)
	}

	for _, runnerID := range []string{
		"runner_runner_admin_contract",
		"runner_system_admin_contract",
		"runner_viewer_contract",
	} {
		register := request(http.MethodPost, "/api/v1/runners/register", systemAdmin, `{"id":"`+runnerID+`","name":"Role Contract Runner","capabilities":["shell"]}`)
		requireStatus(register, http.StatusCreated, "system_admin registers runner")
	}

	for _, principal := range []struct {
		name     string
		token    string
		runnerID string
	}{
		{name: "runner_admin", token: runnerAdminToken.Token, runnerID: "runner_runner_admin_contract"},
		{name: "system_admin", token: systemAdmin, runnerID: "runner_system_admin_contract"},
	} {
		t.Run(principal.name, func(t *testing.T) {
			requireStatus(request(http.MethodGet, "/api/v1/runners", principal.token, ""), http.StatusOK, "list runners")
			requireStatus(request(http.MethodGet, "/api/v1/runners/"+principal.runnerID, principal.token, ""), http.StatusOK, "get runner detail")

			enrollment := request(http.MethodPost, "/api/v1/runner-enrollments", principal.token, `{"runner_id":"runner_`+principal.name+`_contract","runner_name":"Role Contract Enrollment","capabilities":["shell"],"ttl_seconds":600}`)
			requireStatus(enrollment, http.StatusCreated, "create runner enrollment")
			var created app.CreatedRunnerEnrollment
			if err := json.NewDecoder(enrollment.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			if created.Enrollment.ID == "" {
				t.Fatal("created runner enrollment ID is empty")
			}

			revoke := request(http.MethodPost, "/api/v1/runner-enrollments/revoke", principal.token, `{"enrollment_id":"`+created.Enrollment.ID+`"}`)
			requireStatus(revoke, http.StatusOK, "revoke runner enrollment")

			revokeToken := request(http.MethodPost, "/api/v1/runners/revoke-token", principal.token, `{"runner_id":"`+principal.runnerID+`"}`)
			requireStatus(revokeToken, http.StatusOK, "revoke runner token")
			if !strings.Contains(revokeToken.Body.String(), `"status":"revoked"`) {
				t.Fatalf("revoked runner response = %s, want revoked status", revokeToken.Body.String())
			}
		})
	}

	for _, operation := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list runners", method: http.MethodGet, path: "/api/v1/runners"},
		{name: "get runner detail", method: http.MethodGet, path: "/api/v1/runners/runner_viewer_contract"},
		{name: "create runner enrollment", method: http.MethodPost, path: "/api/v1/runner-enrollments", body: `{"runner_id":"runner_viewer_contract","runner_name":"Viewer Contract Enrollment","capabilities":["shell"],"ttl_seconds":600}`},
		{name: "revoke runner enrollment", method: http.MethodPost, path: "/api/v1/runner-enrollments/revoke", body: `{"enrollment_id":"enrollment_viewer_contract"}`},
		{name: "revoke runner token", method: http.MethodPost, path: "/api/v1/runners/revoke-token", body: `{"runner_id":"runner_viewer_contract"}`},
	} {
		t.Run("viewer is denied "+operation.name, func(t *testing.T) {
			requireStatus(request(operation.method, operation.path, viewer, operation.body), http.StatusForbidden, operation.name)
		})
	}
}

func containsRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func TestRunnerEnrollmentCapabilityAndTagBounds(t *testing.T) {
	server, _ := newTestServer()
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	values := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s_%02d", prefix, i)
		}
		return out
	}
	cases := []struct {
		name       string
		caps, tags []string
		token      string
		ttl, want  int
	}{
		{name: "32", caps: values("cap", 32), token: admin, want: 201},
		{name: "33", caps: values("cap", 33), token: admin, want: 201},
		{name: "64", caps: values("cap", 64), tags: values("tag", 32), token: admin, want: 201},
		{name: "65", caps: values("cap", 65), token: admin, want: 400},
		{name: "tags33", caps: values("cap", 1), tags: values("tag", 33), token: admin, want: 400},
		{name: "empty", caps: []string{}, token: admin, want: 400},
		{name: "cap_string64", caps: []string{strings.Repeat("a", 64)}, token: admin, want: 201},
		{name: "cap_string65", caps: []string{strings.Repeat("a", 65)}, token: admin, want: 400},
		{name: "tag_string65", caps: values("cap", 1), tags: []string{strings.Repeat("a", 65)}, token: admin, want: 400},
		{name: "generic_shell", caps: []string{domain.RunTypeComposeDeploy, "shell"}, token: admin, want: 400},
		{name: "viewer", caps: values("cap", 64), token: viewer, want: 403},
		{name: "unauthenticated", caps: values("cap", 64), want: 401},
		{name: "ttl_min", caps: values("cap", 1), token: admin, ttl: 60, want: 201},
		{name: "ttl_max", caps: values("cap", 1), token: admin, ttl: 3600, want: 201},
		{name: "ttl_below", caps: values("cap", 1), token: admin, ttl: 59, want: 400},
		{name: "ttl_above", caps: values("cap", 1), token: admin, ttl: 3601, want: 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := app.RunnerEnrollmentInput{RunnerID: "runner_bound_" + c.name, RunnerName: "bounded enrollment", Capabilities: c.caps, Tags: c.tags, TTLSeconds: c.ttl}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runner-enrollments", strings.NewReader(string(raw)))
			request.Header.Set("Content-Type", "application/json")
			if c.token != "" {
				request.Header.Set("Authorization", "Bearer "+c.token)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != c.want {
				t.Fatalf("status%d want%d", response.Code, c.want)
			}
		})
	}
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	input := doc.Components.Schemas["RunnerEnrollmentInput"].Value
	caps, tags := input.Properties["capabilities"].Value, input.Properties["tags"].Value
	if caps.MaxItems == nil || *caps.MaxItems != 64 || tags.MaxItems == nil || *tags.MaxItems != 32 || caps.Items.Value.MaxLength == nil || *caps.Items.Value.MaxLength != 64 {
		t.Fatal("enrollment schema bounds diverged")
	}
}
