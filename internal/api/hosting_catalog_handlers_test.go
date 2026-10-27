package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestHostingCatalogAPIAuthenticationAndClosedInput(t *testing.T) {
	s, _ := newTestServer()
	token := createTestSession(t, s)
	check := func(method, path, body, credential string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("catalog request %s %s status %d body %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	check("GET", "/api/v1/hosting-catalog?project_id=proj_platform", "", "", 401)
	raw := check("GET", "/api/v1/hosting-catalog?project_id=proj_platform", "", token, 200)
	var items []domain.HostingCatalogTemplate
	if json.Unmarshal(raw, &items) != nil || len(items) != 2 || items[0].Version != domain.HostingCatalogLinkdingVersion || items[0].Category != "RSS" || items[0].AMDOnly || items[0].ARMOnly {
		t.Fatal("catalog metadata missing")
	}
	if strings.Contains(string(raw), `"amd_only"`) || strings.Contains(string(raw), `"arm_only"`) {
		t.Fatal("catalog invented an upstream architecture restriction")
	}
	if items[1].ID != "uptime-kuma" || items[1].Definition.CredentialNames == nil || len(items[1].Definition.CredentialNames) != 0 {
		t.Fatal("Kuma catalog entry lacks empty credential contract")
	}
	for _, body := range []string{`{}`, `{"request_id":"a","compose":"services: {}"}`, `{"request_id":"a","request_id":"b"}`, `null`} {
		check("POST", "/api/v1/hosting-catalog/installations", body, token, 400)
	}
}

func TestHostingCatalogKumaHTTPCurrentLookupAndEmptyCredentials(t *testing.T) {
	server, _, database := newHostingBuildAPIFixture(t)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "Kuma catalog decoder"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := server.app.CreateRunnerEnrollment(ctx, app.RunnerEnrollmentInput{RunnerID: "runner_kuma_catalog", RunnerName: "Kuma catalog", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.app.ConsumeRunnerEnrollment(ctx, enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: fmt.Sprintf("%x", sha256.Sum256([]byte("kuma_fixture_runner")))})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := server.app.CreateServer(ctx, app.ServerInput{Name: "Kuma catalog", RunnerID: "runner_kuma_catalog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.app.GrantDestinationProject(ctx, detail.Destination.ID, app.DestinationProjectInput{ProjectID: env.ProjectID}); err != nil {
		t.Fatal(err)
	}
	token := createTestSession(t, server)
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("8", 32), ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, DestinationID: detail.Destination.ID, Name: "Kuma", TemplateID: "uptime-kuma", ImmutableVersion: domain.HostingCatalogUptimeKumaVersion}
	call := func(input domain.HostingCatalogInstallInput, status int) []byte {
		t.Helper()
		raw, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		request := httptest.NewRequest("POST", "/api/v1/hosting-catalog/installations", strings.NewReader(string(raw)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("catalog decoder status got=%d want=%d", response.Code, status)
		}
		return response.Body.Bytes()
	}
	for _, pair := range [][2]string{{"uptime-kuma", domain.HostingCatalogLinkdingVersion}, {"linkding", domain.HostingCatalogUptimeKumaVersion}} {
		bad := in
		bad.TemplateID = pair[0]
		bad.ImmutableVersion = pair[1]
		call(bad, 409)
	}
	var count int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM hosting_catalog_installations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("cross-pair persisted", err)
	}
	raw := call(in, 201)
	var installed domain.HostingCatalogInstallation
	if json.Unmarshal(raw, &installed) != nil || installed.Request != in || installed.Template.ID != "uptime-kuma" || installed.Credentials == nil || len(installed.Credentials) != 0 {
		t.Fatal("real decoder lost Kuma identity or empty credentials")
	}
	if replay := call(in, 201); string(replay) != string(raw) {
		t.Fatal("HTTP response-loss replay changed installed copy")
	}
}
