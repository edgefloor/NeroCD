package api

import (
	"encoding/json"
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
