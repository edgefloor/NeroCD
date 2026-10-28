package api

import (
	"encoding/json"
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

func TestPublicRunAndTemplateAPIsRejectNestedHostingTasks(t *testing.T) {
	server, mem := newTestServer()
	token := createTestSession(t, server)
	generic := domain.RunSpec{Type: "ansible"}
	internal := domain.RunSpec{Type: domain.RunTypeImageDeploy}
	nested := &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: internal}}}
	for _, shape := range []struct {
		name     string
		spec     domain.RunSpec
		workflow domain.Workflow
	}{
		{"top", internal, domain.Workflow{}},
		{"compose_process", domain.RunSpec{Type: domain.RunTypeComposeDeploy, Process: &domain.ProcessSpec{Command: []string{"not-executed"}}}, domain.Workflow{}},
		{"nested_compose", generic, domain.Workflow{Steps: []domain.WorkflowStep{{ID: "compose", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}}}}},
		{"spec_nested_compose", domain.RunSpec{Type: "ansible", Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "compose", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}}}}}, domain.Workflow{}},
		{"step_approval", generic, domain.Workflow{Steps: []domain.WorkflowStep{{ID: "approval", RequiresAck: true, RunSpec: generic}}}},
		{"nested_step_approval", domain.RunSpec{Type: "ansible", Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "approval", RequiresAck: true, RunSpec: generic}}}}, domain.Workflow{}},
		{"workflow", generic, *nested},
		{"run_spec_workflow", domain.RunSpec{Type: "ansible", Workflow: nested}, domain.Workflow{}},
		{"deep_step", generic, domain.Workflow{Steps: []domain.WorkflowStep{{ID: "outer", RunSpec: domain.RunSpec{Type: "ansible", Workflow: nested}}}}},
		{"unexpected_operation", domain.RunSpec{Type: "ansible", HostingOperationID: "hop_forbidden"}, domain.Workflow{}},
		{"unexpected_step_mode", generic, domain.Workflow{Steps: []domain.WorkflowStep{{ID: "mode", RunSpec: domain.RunSpec{Type: "ansible", HostingMode: "observe"}}}}},
	} {
		for _, endpoint := range []struct{ method, path string }{{"POST", "/api/v1/runs"}, {"POST", "/api/v1/templates"}, {"PATCH", "/api/v1/templates"}} {
			t.Run(shape.name+endpoint.method+endpoint.path, func(t *testing.T) {
				body, _ := json.Marshal(map[string]any{"project_id": "proj_platform", "name": "internal-denial", "kind": "ansible", "run_spec": shape.spec, "workflow": shape.workflow})
				if endpoint.method == "PATCH" {
					body, _ = json.Marshal(map[string]any{"id": "tpl_plan", "project_id": "proj_platform", "name": "internal-denial", "kind": "ansible", "run_spec": shape.spec, "workflow": shape.workflow})
				}
				if endpoint.path == "/api/v1/runs" {
					body, _ = json.Marshal(map[string]any{"project_id": "proj_platform", "run_spec": shape.spec, "workflow": shape.workflow})
				}
				r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(string(body)))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"bad_request"`) {
					t.Fatalf("internal task accepted: status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
	}
	runs, err := mem.ListRuns(t.Context(), "proj_platform")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if domain.ContainsHostingExecution(run.RunSpec, run.Workflow) {
			t.Fatal("public validation persisted internal authority")
		}
	}
}
