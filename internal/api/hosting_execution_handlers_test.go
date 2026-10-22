package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestHostingExecutionMemoryFailsUnavailableBeforeMutation(t *testing.T) {
	server, mem := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "execution"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(ctx, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_execution_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	body := `{"expected_generation":1,"idempotency_key":"deploy-web","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5}}`
	request := func(token string, want int) string {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/resources/"+resource.ID+"/deployments", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want%d body=%s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	request("", 401)
	request(viewer, 403)
	if output := request(admin, 503); !strings.Contains(output, "hosting_unavailable") {
		t.Fatalf("unavailable not explicit: %s", output)
	}
	runs, err := mem.ListRuns(t.Context(), "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.RunSpec.Type == domain.RunTypeImageDeploy {
			t.Fatal("memory manufactured an image task")
		}
	}
	if _, err = server.app.RequestRunWithSpec(ctx, app.RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy}}); err == nil {
		t.Fatal("generic image request bypassed operation authority")
	}
}
