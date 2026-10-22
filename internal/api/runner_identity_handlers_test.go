package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
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
	capabilities := make([]any, len(before.Capabilities))
	for index, capability := range before.Capabilities {
		capabilities[index] = capability
	}
	if !reflect.DeepEqual(body, map[string]any{"runner_id": before.ID, "capabilities": capabilities}) {
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

func TestRunnerIdentitySchemaMatchesPersistedCapabilities(t *testing.T) {
	server, _ := newTestServer()
	assertRunnerIdentitySchema(t, server)
}

func TestRunnerIdentitySchemaMatchesPostgresPersistedCapabilities(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL identity contract tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminDB.Close)
	schema := fmt.Sprintf("nerocd_proxy_identity_contract_%d", time.Now().UTC().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := adminDB.Exec(cleanup, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema)
	query.Set("application_name", "nerocd_proxy_identity_contract")
	parsed.RawQuery = query.Encode()
	database, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		migration, err := db.Files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service, slog.Default(), web.Static())
	assertRunnerIdentitySchema(t, server)
}

func assertRunnerIdentitySchema(t *testing.T, server *Server) {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["RunnerIdentity"].Value
	if err = schema.VisitJSON(map[string]any{"runner_id": "runner.legacy"}); err != nil {
		t.Fatalf("older identity response violates backward-compatible schema: %v", err)
	}
	for _, invalid := range []map[string]any{
		{"runner_id": ""},
		{"runner_id": "runner.legacy", "capabilities": "proxy_apply"},
		{"runner_id": "runner.legacy", "capabilities": []any{123}},
	} {
		if schema.VisitJSON(invalid) == nil {
			t.Fatal("identity schema lost its ID or capability type contract")
		}
	}
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	many := make([]string, 200)
	for index := range many {
		many[index] = fmt.Sprintf("CUSTOM-%d", index)
	}
	for _, test := range []struct {
		name         string
		capabilities []string
	}{
		{"long_label", []string{strings.Repeat("C", 200)}},
		{"many_labels", many},
		{"opaque_duplicate_labels", []string{"CUSTOM", "CUSTOM", "custom capability"}},
		{"exact_proxy_label", []string{"proxy_apply", "CUSTOM"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registered, err := server.app.RegisterRunner(admin, app.RunnerInput{ID: "runner.schema." + test.name, Name: "Identity schema fixture", Capabilities: test.capabilities})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/runners/identity", nil)
			request.Header.Set("Authorization", "Bearer "+registered.Token)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("identity status=%d", response.Code)
			}
			var body map[string]any
			if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantCapabilities := make([]any, len(test.capabilities))
			for index, capability := range test.capabilities {
				wantCapabilities[index] = capability
			}
			if !reflect.DeepEqual(body, map[string]any{"runner_id": registered.Runner.ID, "capabilities": wantCapabilities}) {
				t.Fatal("identity response changed persisted capabilities or exposed additional fields")
			}
			if err = schema.VisitJSON(body); err != nil {
				t.Fatalf("actual authenticated HTTP response violates RunnerIdentity schema: %v", err)
			}
		})
	}
}
