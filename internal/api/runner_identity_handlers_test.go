package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestRunnerIdentityAuthenticatesOnlyDedicatedCredentialAndDoesNotHeartbeat(t *testing.T) {
	server, mem := newTestServer()
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	registered, err := server.app.RegisterRunner(admin, app.RunnerInput{ID: "runner_identity_api", Name: "Identity API", Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	session := createTestSession(t, server)
	account, err := server.app.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "identity bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.app.CreateRunnerEnrollment(admin, app.RunnerEnrollmentInput{RunnerID: "runner_identity_enroll", RunnerName: "Not yet enrolled", Capabilities: []string{"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := mem.GetRunnerByID(t.Context(), registered.Runner.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string, cookie bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runners/identity", nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "nerocd_session", Value: token})
		} else if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("identity status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
		}
		return rec
	}
	rec := request(registered.Token, false, 200)
	var body map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["runner_id"] != before.ID {
		t.Fatalf("identity response=%v", body)
	}
	after, err := mem.GetRunnerByID(t.Context(), before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("identity mutated runner: %+v %+v %v", before, after, err)
	}
	request("", false, 401)
	request(session, false, 401)
	request(session, true, 401)
	request(account.Token, false, 401)
	request(enrollment.Token, false, 401)
	if _, err = server.app.RevokeRunnerToken(admin, app.RunnerTokenInput{RunnerID: before.ID}); err != nil {
		t.Fatal(err)
	}
	request(registered.Token, false, 401)
}
