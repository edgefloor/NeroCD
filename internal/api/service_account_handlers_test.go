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

func TestServiceAccountHTTPRejectsUnboundedExpiryAndUnknownFields(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unbounded expiry", body: `{"project_id":"proj_platform","name":"late","role":"viewer","expires_at":"2030-01-01T00:00:00Z"}`},
		{name: "unknown field", body: `{"project_id":"proj_platform","name":"bad","role":"viewer","roles":["system_admin"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serviceAccountRequest(t, server, http.MethodPost, "/api/v1/service-accounts", adminToken, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
