package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func serviceAccountRequest(t *testing.T, server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestServiceAccountHTTPLifecycleAndProjectIsolation(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	createdResponse := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", adminToken, `{"project_id":"proj_platform","name":"HTTP bot","role":"maintainer"}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created app.CreatedServiceAccount
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.ServiceAccount.ID == "" || strings.Contains(createdResponse.Body.String(), "token_hash") {
		t.Fatalf("unsafe or incomplete create response: %s", createdResponse.Body.String())
	}

	list := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/service-accounts?project_id=proj_platform&limit=1&offset=0", adminToken, "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), created.Token) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Items []domain.ServiceAccount `json:"items"`
		Total int                     `json:"total"`
	}
	if err := json.NewDecoder(list.Body).Decode(&page); err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}

	run := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/runs", created.Token, `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"true"}},"workflow":{"steps":[]},"runner_tags":[]}`)
	if run.Code != http.StatusCreated {
		t.Fatalf("service-account run status=%d body=%s", run.Code, run.Body.String())
	}
	var taskRun domain.TaskRun
	if err := json.NewDecoder(run.Body).Decode(&taskRun); err != nil || taskRun.RequestedBy != created.ServiceAccount.ID {
		t.Fatalf("run=%#v err=%v", taskRun, err)
	}
	crossProject := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/runs", created.Token, `{"project_id":"proj_security","run_spec":{"type":"shell","inputs":{"command":"true"}},"workflow":{"steps":[]},"runner_tags":[]}`)
	if crossProject.Code != http.StatusForbidden {
		t.Fatalf("cross-project status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}
	selfManage := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/service-accounts?project_id=proj_platform", created.Token, "")
	if selfManage.Code != http.StatusForbidden {
		t.Fatalf("self-management status=%d body=%s", selfManage.Code, selfManage.Body.String())
	}

	rotate := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts/rotate", adminToken, `{"service_account_id":"`+created.ServiceAccount.ID+`"}`)
	if rotate.Code != http.StatusCreated {
		t.Fatalf("rotate status=%d body=%s", rotate.Code, rotate.Body.String())
	}
	var rotated app.RotatedServiceAccountCredential
	if err := json.NewDecoder(rotate.Body).Decode(&rotated); err != nil || rotated.Token == "" {
		t.Fatalf("rotate=%#v err=%v", rotated, err)
	}
	if old := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/me", created.Token, ""); old.Code != http.StatusUnauthorized {
		t.Fatalf("old credential status=%d body=%s", old.Code, old.Body.String())
	}
	if current := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/me", rotated.Token, ""); current.Code != http.StatusOK {
		t.Fatalf("rotated credential status=%d body=%s", current.Code, current.Body.String())
	}

	update := serviceAccountRequest(t, server, http.MethodPatch, "/api/v1/service-accounts", adminToken, `{"service_account_id":"`+created.ServiceAccount.ID+`","name":"HTTP viewer bot","role":"viewer"}`)
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	revoke := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts/revoke", adminToken, `{"service_account_id":"`+created.ServiceAccount.ID+`"}`)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	if current := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/me", rotated.Token, ""); current.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential status=%d body=%s", current.Code, current.Body.String())
	}
}

func TestServiceAccountHTTPValidationUsesGenericBadRequest(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	createdResponse := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", adminToken, `{"project_id":"proj_platform","name":"validation bot","role":"viewer"}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created app.CreatedServiceAccount
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		method   string
		path     string
		body     string
		wantBody string
	}{
		{name: "list missing project", method: http.MethodGet, path: "/api/v1/service-accounts"},
		{name: "create missing project", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"name":"bot","role":"viewer"}`},
		{name: "create invalid name", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"project_id":"proj_platform","name":" ","role":"viewer"}`},
		{name: "create invalid role", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"project_id":"proj_platform","name":"bot","role":"system_admin"}`},
		{name: "create expired credential", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"project_id":"proj_platform","name":"expired","role":"viewer","expires_at":"2020-01-01T00:00:00Z"}`},
		{name: "create unbounded expiry", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"project_id":"proj_platform","name":"late","role":"viewer","expires_at":"2030-01-01T00:00:00Z"}`},
		{name: "create unknown field", method: http.MethodPost, path: "/api/v1/service-accounts", body: `{"project_id":"proj_platform","name":"bad","role":"viewer","roles":["system_admin"]}`, wantBody: "{\"code\":\"invalid_request\",\"error\":\"invalid JSON body\"}\n"},
		{name: "update missing account", method: http.MethodPatch, path: "/api/v1/service-accounts", body: `{"name":"bot"}`},
		{name: "update missing fields", method: http.MethodPatch, path: "/api/v1/service-accounts", body: `{"service_account_id":"` + created.ServiceAccount.ID + `"}`},
		{name: "update invalid name", method: http.MethodPatch, path: "/api/v1/service-accounts", body: `{"service_account_id":"` + created.ServiceAccount.ID + `","name":" "}`},
		{name: "update invalid role", method: http.MethodPatch, path: "/api/v1/service-accounts", body: `{"service_account_id":"` + created.ServiceAccount.ID + `","role":"system_admin"}`},
		{name: "rotate missing account", method: http.MethodPost, path: "/api/v1/service-accounts/rotate", body: `{}`},
		{name: "rotate expired credential", method: http.MethodPost, path: "/api/v1/service-accounts/rotate", body: `{"service_account_id":"` + created.ServiceAccount.ID + `","expires_at":"2020-01-01T00:00:00Z"}`},
		{name: "revoke missing account", method: http.MethodPost, path: "/api/v1/service-accounts/revoke", body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serviceAccountRequest(t, server, test.method, test.path, adminToken, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			want := test.wantBody
			if want == "" {
				want = "{\"code\":\"bad_request\",\"error\":\"invalid request\"}\n"
			}
			if got := response.Body.String(); got != want {
				t.Fatalf("body=%q want=%q", got, want)
			}
		})
	}
}

func TestServiceAccountHTTPValidationPreservesAuthenticationPrecedence(t *testing.T) {
	server, _ := newTestServer()
	response := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", "", `{}`)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got, want := response.Body.String(), "{\"code\":\"unauthenticated\",\"error\":\"authentication is required\"}\n"; got != want {
		t.Fatalf("body=%q want=%q", got, want)
	}
}

func TestServiceAccountHTTPDeniesIneligiblePrincipalsBeforeIDValidation(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	createdResponse := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", adminToken, `{"project_id":"proj_platform","name":"authorization target","role":"maintainer"}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create managed account status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created app.CreatedServiceAccount
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	legacyResponse := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/api-tokens", adminToken, `{"name":"legacy service-account token","roles":["runner_admin"]}`)
	if legacyResponse.Code != http.StatusCreated {
		t.Fatalf("create legacy token status=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}
	var legacy app.CreatedAPIToken
	if err := json.NewDecoder(legacyResponse.Body).Decode(&legacy); err != nil {
		t.Fatal(err)
	}

	accountsBefore := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/service-accounts?project_id=proj_platform", adminToken, "")
	if accountsBefore.Code != http.StatusOK {
		t.Fatalf("list before status=%d body=%s", accountsBefore.Code, accountsBefore.Body.String())
	}
	wantForbidden := "{\"code\":\"forbidden\",\"error\":\"access is denied\"}\n"
	projectIDs := []string{"", "malformed id", "proj_platform", "proj_missing"}
	accountIDs := []string{"", "malformed id", created.ServiceAccount.ID, "svcacct_missing"}
	for _, principal := range []struct {
		name  string
		token string
	}{
		{name: "managed service account", token: created.Token},
		{name: "legacy API token", token: legacy.Token},
	} {
		principal := principal
		t.Run(principal.name, func(t *testing.T) {
			assertForbidden := func(method, path, body string) {
				t.Helper()
				response := serviceAccountRequest(t, server, method, path, principal.token, body)
				if response.Code != http.StatusForbidden || response.Body.String() != wantForbidden {
					t.Fatalf("%s %s status=%d body=%q, want generic forbidden", method, path, response.Code, response.Body.String())
				}
			}
			for _, projectID := range projectIDs {
				queryID := strings.ReplaceAll(projectID, " ", "%20")
				assertForbidden(http.MethodGet, "/api/v1/service-accounts?project_id="+queryID, "")
				assertForbidden(http.MethodPost, "/api/v1/service-accounts", `{"project_id":"`+projectID+`","name":"forbidden","role":"viewer"}`)
			}
			for _, accountID := range accountIDs {
				assertForbidden(http.MethodPatch, "/api/v1/service-accounts", `{"service_account_id":"`+accountID+`","name":"forbidden update"}`)
				assertForbidden(http.MethodPost, "/api/v1/service-accounts/rotate", `{"service_account_id":"`+accountID+`"}`)
				assertForbidden(http.MethodPost, "/api/v1/service-accounts/revoke", `{"service_account_id":"`+accountID+`"}`)
			}
		})
	}

	t.Run("human viewer known unauthorized scope", func(t *testing.T) {
		assertForbidden := func(method, path, body string) {
			t.Helper()
			response := serviceAccountRequest(t, server, method, path, viewerToken, body)
			if response.Code != http.StatusForbidden || response.Body.String() != wantForbidden {
				t.Fatalf("%s %s status=%d body=%q, want generic forbidden", method, path, response.Code, response.Body.String())
			}
		}
		assertForbidden(http.MethodGet, "/api/v1/service-accounts?project_id=proj_platform", "")
		assertForbidden(http.MethodPost, "/api/v1/service-accounts", `{"project_id":"proj_platform","name":"forbidden","role":"viewer"}`)
		assertForbidden(http.MethodPatch, "/api/v1/service-accounts", `{"service_account_id":"`+created.ServiceAccount.ID+`","name":"forbidden update"}`)
		assertForbidden(http.MethodPost, "/api/v1/service-accounts/rotate", `{"service_account_id":"`+created.ServiceAccount.ID+`"}`)
		assertForbidden(http.MethodPost, "/api/v1/service-accounts/revoke", `{"service_account_id":"`+created.ServiceAccount.ID+`"}`)
	})

	accountsAfter := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/service-accounts?project_id=proj_platform", adminToken, "")
	if accountsAfter.Code != http.StatusOK || accountsAfter.Body.String() != accountsBefore.Body.String() {
		t.Fatalf("denied HTTP requests changed accounts: before=%s after_status=%d after=%s", accountsBefore.Body.String(), accountsAfter.Code, accountsAfter.Body.String())
	}
}

func TestServiceAccountHTTPCannotAdministerProjectsOrHumanMemberships(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	createAccount := func(projectID, name string) app.CreatedServiceAccount {
		t.Helper()
		response := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", adminToken, `{"project_id":"`+projectID+`","name":"`+name+`","role":"owner"}`)
		if response.Code != http.StatusCreated {
			t.Fatalf("create account status=%d body=%s", response.Code, response.Body.String())
		}
		var created app.CreatedServiceAccount
		if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
			t.Fatal(err)
		}
		return created
	}
	platformAccount := createAccount("proj_platform", "HTTP platform owner")
	securityAccount := createAccount("proj_security", "HTTP security owner")
	projectsBefore := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/projects?limit=100", adminToken, "").Body.String()
	membersBefore := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/project-members?limit=100", adminToken, "").Body.String()

	for _, attempt := range []struct {
		name  string
		token string
		body  string
	}{
		{name: "add human", token: platformAccount.Token, body: `{"project_id":"proj_platform","email":"viewer@example.local","role":"viewer"}`},
		{name: "promote human", token: securityAccount.Token, body: `{"project_id":"proj_security","email":"viewer@example.local","role":"owner"}`},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			response := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/project-members", attempt.token, attempt.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	createProject := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/projects", securityAccount.Token, `{"name":"forbidden HTTP project"}`)
	if createProject.Code != http.StatusForbidden {
		t.Fatalf("project create status=%d body=%s", createProject.Code, createProject.Body.String())
	}
	projectsAfter := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/projects?limit=100", adminToken, "").Body.String()
	membersAfter := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/project-members?limit=100", adminToken, "").Body.String()
	if projectsAfter != projectsBefore || membersAfter != membersBefore {
		t.Fatalf("administration state changed: projects=%s members=%s", projectsAfter, membersAfter)
	}
	role := serviceAccountRequest(t, server, http.MethodGet, "/api/v1/project-role?project_id=proj_security", securityAccount.Token, "")
	if role.Code != http.StatusOK {
		t.Fatalf("allowed role read status=%d body=%s", role.Code, role.Body.String())
	}
}
