package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/web"
)

func TestHostingDatabaseServerAdmissionPublicBootstrap(t *testing.T) {
	_, pg, _ := newHostingBuildAPIFixture(t)
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("test key generation failed")
	}
	ring, err := secretcrypto.New("database-server-admission", map[string][]byte{"database-server-admission": key[:]})
	clear(key[:])
	if err != nil {
		t.Fatal("test keyring failed")
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, SecretKeyring: ring, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal("test service failed")
	}
	server := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), web.Static())
	request := func(method, path, bearer string, input, output any, status int) {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal("public bootstrap input encoding failed")
		}
		defer clear(raw)
		var body io.Reader
		if input != nil {
			body = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		defer clear(rec.Body.Bytes())
		if rec.Code != status {
			t.Fatalf("public bootstrap method=%s path=%s status=%d want=%d", method, path, rec.Code, status)
		}
		if output != nil && json.Unmarshal(rec.Body.Bytes(), output) != nil {
			t.Fatal("public bootstrap response decoding failed")
		}
	}
	token := createTestSession(t, server)
	caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
	var enrollment app.CreatedRunnerEnrollment
	request(http.MethodPost, "/api/v1/runner-enrollments", token, app.RunnerEnrollmentInput{RunnerID: "runner_database_acceptance", RunnerName: "private database acceptance", Capabilities: caps}, &enrollment, http.StatusCreated)
	var enrolled app.ConsumedRunnerEnrollment
	credentialHash := sha256.Sum256([]byte("ncr_database_runner_fixture_only"))
	request(http.MethodPost, "/api/v1/runner-enrollments/consume", enrollment.Token, app.RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("a", 32), CredentialHash: hex.EncodeToString(credentialHash[:])}, &enrolled, http.StatusOK)
	if !reflect.DeepEqual(enrolled.Runner.Capabilities, caps) {
		t.Fatal("enrollment changed the default database capabilities")
	}
	var target app.ServerDetail
	request(http.MethodPost, "/api/v1/servers", token, app.ServerInput{Name: "database runner acceptance", RunnerID: enrolled.Runner.ID}, &target, http.StatusCreated)
	request(http.MethodPost, "/api/v1/destinations/"+target.Destination.ID+"/project-grants", token, app.DestinationProjectInput{ProjectID: "proj_platform"}, nil, http.StatusCreated)
	var environment domain.ProjectEnvironment
	request(http.MethodPost, "/api/v1/project-environments", token, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "database runner acceptance"}, &environment, http.StatusCreated)
	for i, engine := range []string{"postgresql", "redis"} {
		recipe := domain.HostingDatabasePostgresRecipe
		if engine == "redis" {
			recipe = domain.HostingDatabaseRedisRecipe
		}
		var created domain.HostingDatabase
		request(http.MethodPost, "/api/v1/databases", token, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat(string(rune('a'+i)), 32), ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "runner fixture " + engine, DestinationID: target.Destination.ID, Engine: engine, RecipeVersion: recipe}, &created, http.StatusCreated)
		if created.Resource.Kind != "database" || created.Binding.DestinationID != target.Destination.ID || created.Engine != engine || created.RuntimeState != domain.HostingDatabaseUnqualified || len(created.Credentials) != 2 {
			t.Fatal("database metadata does not preserve the public bootstrap scope")
		}
		var loaded domain.HostingDatabase
		request(http.MethodGet, "/api/v1/resources/"+created.Resource.ID+"/database", token, nil, &loaded, http.StatusOK)
		if !reflect.DeepEqual(created, loaded) {
			t.Fatal("created database differs from public metadata")
		}
	}
	persisted, err := pg.GetRunnerByID(t.Context(), enrolled.Runner.ID)
	if err != nil || !reflect.DeepEqual(persisted.Capabilities, caps) {
		t.Fatal("server admission mutated runner capabilities")
	}
}
