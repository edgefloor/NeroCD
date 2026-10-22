package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestHostingRetainedReleaseAPIMemoryFailsClosed(t *testing.T) {
	server, mem := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	environment, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "retained"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(ctx, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "retained-web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_retained_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	for _, test := range []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodGet, "/retained-releases", "", "", 401},
		{http.MethodGet, "/retained-releases", viewer, "", 503},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", viewer, `{"expected_generation":1,"idempotency_key":"cleanup"}`, 403},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", admin, `{"expected_generation":1,"idempotency_key":"cleanup"}`, 503},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", admin, `{"expected_generation":1,"idempotency_key":"cleanup","container_id":"foreign"}`, 400},
	} {
		r := httptest.NewRequest(test.method, "/api/v1/resources/"+resource.ID+test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		if test.token != "" {
			r.Header.Set("Authorization", "Bearer "+test.token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", test.method, test.path, w.Code, test.status, w.Body.String())
		}
	}
}
