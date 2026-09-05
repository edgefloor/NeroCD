package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestUserOffboardingHTTPRequiresAdminAndRevokesAccess(t *testing.T) {
	server, mem := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	now := time.Now().UTC()
	if _, err := mem.CreateAPIToken(t.Context(), domain.APIToken{ID: "tok_viewer_http", Name: "viewer automation", Kind: domain.TokenKindServiceAccount, TokenHash: "viewer-api-hash", Roles: []string{domain.RoleViewer}, Status: domain.TokenActive, CreatedBy: "usr_viewer", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/users/offboard", strings.NewReader(`{"user_id":"usr_bootstrap"}`))
	deniedRequest.Header.Set("Authorization", "Bearer "+viewerToken)
	deniedRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer offboard status=%d body=%s", denied.Code, denied.Body.String())
	}

	list := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/users?limit=1&offset=0", nil)
	listRequest.Header.Set("Authorization", "Bearer "+adminToken)
	server.ServeHTTP(list, listRequest)
	if list.Code != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Items []domain.User `json:"items"`
		Total int           `json:"total"`
		Limit int           `json:"limit"`
	}
	if err := json.NewDecoder(list.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != 2 || page.Limit != 1 || page.Items[0].PasswordHash != "" {
		t.Fatalf("user page=%#v", page)
	}

	offboard := httptest.NewRecorder()
	offboardRequest := httptest.NewRequest(http.MethodPost, "/api/v1/users/offboard", strings.NewReader(`{"user_id":"usr_viewer"}`))
	offboardRequest.Header.Set("Authorization", "Bearer "+adminToken)
	offboardRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(offboard, offboardRequest)
	if offboard.Code != http.StatusOK {
		t.Fatalf("admin offboard status=%d body=%s", offboard.Code, offboard.Body.String())
	}
	var result domain.UserOffboardingResult
	if err := json.NewDecoder(offboard.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.User.Status != domain.UserDisabled || result.RevokedSessions != 1 || result.RevokedAPITokens != 1 {
		t.Fatalf("offboarding result=%#v", result)
	}

	me := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meRequest.Header.Set("Authorization", "Bearer "+viewerToken)
	server.ServeHTTP(me, meRequest)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user's existing session status=%d body=%s", me.Code, me.Body.String())
	}
	relogin := httptest.NewRecorder()
	reloginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"email":"viewer@example.local","password":"viewer"}`))
	reloginRequest.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(relogin, reloginRequest)
	if relogin.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user's local login status=%d body=%s", relogin.Code, relogin.Body.String())
	}
	events, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !containsAuditAction(events, "identity.user.offboard.denied") || !containsAuditAction(events, "identity.user.offboard") {
		t.Fatalf("missing offboarding audits: %#v", events)
	}
}

func containsAuditAction(events []domain.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func TestUserOffboardingHTTPGuardsLastAdministratorAndRejectsUnknownFields(t *testing.T) {
	server, mem := newTestServer()
	adminToken := createTestSession(t, server)

	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "last administrator", body: `{"user_id":"usr_bootstrap"}`, want: http.StatusConflict},
		{name: "unknown field", body: `{"user_id":"usr_viewer","reactivate":true}`, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/users/offboard", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+adminToken)
			request.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
	admin, err := mem.GetUserByEmail(t.Context(), "admin@example.local")
	if err != nil || admin.Status != domain.UserActive {
		t.Fatalf("last administrator changed=%#v err=%v", admin, err)
	}
}
