package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
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
	if json.Unmarshal(raw, &items) != nil || len(items) != 5 || items[0].Version != domain.HostingCatalogLinkdingVersion || items[0].Category != "RSS" || items[0].AMDOnly || items[0].ARMOnly {
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

func TestHostingCatalogHTTPCurrentLookupAndRuntimeDefinitions(t *testing.T) {
	testHostingCatalogHTTPCurrentLookupAndRuntimeDefinitions(t, "", nil)
}

func testHostingCatalogHTTPCurrentLookupAndRuntimeDefinitions(t *testing.T, only string, fault func(http.ResponseWriter, *http.Request, http.Handler) bool) {
	for _, tc := range []struct{ id, version string }{{"linkding", domain.HostingCatalogLinkdingVersion}, {"uptime-kuma", domain.HostingCatalogUptimeKumaVersion}, {"actualbudget", domain.HostingCatalogActualBudgetVersion}, {"vikunja-with-postgresql", domain.HostingCatalogVikunjaVersion}, {"wordpress-with-mysql", domain.HostingCatalogWordPressVersion}} {
		if only != "" && tc.id != only {
			continue
		}
		t.Run(tc.id, func(t *testing.T) {
			server, pg, database := newHostingBuildAPIFixture(t)
			compose := tc.id == "vikunja-with-postgresql" || tc.id == "wordpress-with-mysql"
			if compose || tc.id == "linkding" {
				ring, e := secretcrypto.New("catalog", map[string][]byte{"catalog": bytes.Repeat([]byte{8}, 32)})
				if e != nil {
					t.Fatal(e)
				}
				service, e := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
				if e != nil {
					t.Fatal(e)
				}
				server.app = service
			}
			ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
			env, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "Kuma catalog decoder"})
			if err != nil {
				t.Fatal(err)
			}
			enrollment, err := server.app.CreateRunnerEnrollment(ctx, app.RunnerEnrollmentInput{RunnerID: "runner_kuma_catalog", RunnerName: "Kuma catalog", Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.HostingOwnedStorageCapability, domain.HostingOwnedStorageRuntimeCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingStackCapability}})
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
			in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("8", 32), ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, DestinationID: detail.Destination.ID, Name: "Catalog app", TemplateID: tc.id, ImmutableVersion: tc.version}
			if compose {
				in.PublicURL = "https://tasks.example.test"
			}
			if tc.id == "wordpress-with-mysql" {
				in = wordpressInitialCatalogRequest(t, env.ID, detail.Destination.ID)
			}
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
				if fault == nil || !fault(response, request, server) {
					server.ServeHTTP(response, request)
				}
				if response.Code != status {
					t.Fatalf("catalog decoder status got=%d want=%d", response.Code, status)
				}
				return response.Body.Bytes()
			}
			if tc.id == "wordpress-with-mysql" {
				old := in
				old.RequestID = strings.Repeat("i", 32)
				call(old, 400)
			}
			badURL := in
			if compose {
				badURL.PublicURL = ""
			} else {
				badURL.PublicURL = "https://irrelevant.example.test"
			}
			call(badURL, 400)
			badURL.PublicURL = "https://tasks.example.test/private"
			call(badURL, 400)
			for _, pair := range [][2]string{{tc.id, domain.HostingCatalogLinkdingVersion}, {"linkding", tc.version}, {tc.id, tc.version + ".missing"}} {
				if pair[0] == tc.id && pair[1] == tc.version {
					continue
				}
				bad := in
				bad.TemplateID = pair[0]
				bad.ImmutableVersion = pair[1]
				call(bad, 409)
			}
			var count int
			if err = database.QueryRow(ctx, `SELECT count(*) FROM hosting_catalog_installations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("cross-pair persisted", err)
			}
			optionsPath := "/api/v1/hosting-catalog/" + tc.id + "/destination-options?project_id=" + in.ProjectID + "&immutable_version=" + tc.version
			optionsCall := func(path, credential string, status int) []byte {
				t.Helper()
				request := httptest.NewRequest("GET", path, nil)
				if credential != "" {
					request.Header.Set("Authorization", "Bearer "+credential)
				}
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				if response.Code != status {
					t.Fatalf("options HTTP status %d want %d body %s", response.Code, status, response.Body.String())
				}
				return response.Body.Bytes()
			}
			optionsCall(optionsPath, "", 401)
			for _, suffix := range []string{"&limit=-1", "&limit=101", "&offset=-1", "&limit=1&limit=2", "&immutable_version=duplicate", "&unknown=x"} {
				optionsCall(optionsPath+suffix, token, 400)
			}
			optionsCall(strings.Replace(optionsPath, tc.version, "missing", 1), token, 404)
			optionsCall(strings.Replace(optionsPath, "/"+tc.id+"/", "/missing/", 1), token, 404)
			optionsCall(strings.Replace(optionsPath, in.ProjectID, "missing", 1), token, 404)
			var options domain.HostingCatalogDestinationOptions
			if json.Unmarshal(optionsCall(optionsPath+"&limit=1", token, 200), &options) != nil || len(options.Items) != 1 || !options.Items[0].Available || options.Items[0].Destination.ID != in.DestinationID || options.Requirements.ArchitectureRestriction != "undeclared" {
				t.Fatal("HTTP options contract disconnected")
			}
			if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1) WHERE id=$2`, domain.HostingReleasePortsCapability, detail.Server.RunnerID); err != nil {
				t.Fatal(err)
			}
			call(in, 409)
			if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$1) WHERE id=$2`, domain.HostingReleasePortsCapability, detail.Server.RunnerID); err != nil {
				t.Fatal(err)
			}
			if fault != nil {
				call(in, 503)
			}
			raw := call(in, 201)
			var installed domain.HostingCatalogInstallation
			if json.Unmarshal(raw, &installed) != nil || installed.Request != in || installed.Template.ID != tc.id || installed.Credentials == nil || len(installed.Credentials) != len(installed.Template.Definition.CredentialNames) {
				t.Fatal("real decoder lost catalog identity or empty credentials")
			}
			if replay := call(in, 201); string(replay) != string(raw) {
				t.Fatal("HTTP response-loss replay changed installed copy")
			}

			if compose {
				if installed.Resource.Source.Kind != "compose" || installed.StorageRef != (domain.HostingStorageRef{}) {
					t.Fatal("HTTP install did not select managed Compose")
				}
			}
			if installed.ID != installed.Resource.ID || installed.Binding.ResourceID != installed.Resource.ID {
				t.Fatal("installation receipt resource identity mismatch")
			}
			for _, readback := range []struct {
				path   string
				status int
			}{
				{"/api/v1/hosting-catalog/installations/" + installed.ID, 404},
				{"/api/v1/resources/" + installed.Resource.ID + "/catalog-installation", 200},
			} {
				req := httptest.NewRequest("GET", readback.path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				response := httptest.NewRecorder()
				server.ServeHTTP(response, req)
				if response.Code != readback.status || readback.status == 200 && response.Body.String() != string(raw) {
					t.Fatalf("installation readback %s returned %d want %d", readback.path, response.Code, readback.status)
				}
			}
			if tc.id == "actualbudget" && installed.Template.Definition.RuntimeEnvironment["ACTUAL_LOGIN_METHOD"] != "password" {
				t.Fatal("HTTP install lost pinned literal")
			}
		})
	}
}
