package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func TestReadRunnerCredentialFileSecurity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("supported Unix credential checks")
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string) string
		want    string
	}{
		{name: "valid", want: "runner-secret", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "valid")
			if err := os.WriteFile(filename, []byte("runner-secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "wrong_permissions", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "permissive")
			if err := os.WriteFile(filename, []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "empty", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "empty")
			if err := os.WriteFile(filename, []byte(" \n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "symlink", prepare: func(t *testing.T, root string) string {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "not_regular", prepare: func(t *testing.T, root string) string { return root }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			credential, err := readRunnerCredentialFile(tc.prepare(t, t.TempDir()))
			if tc.want != "" {
				if err != nil || credential != tc.want {
					t.Fatalf("credential=%q err=%v, want %q", credential, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("credential=%q unexpectedly accepted", credential)
			}
		})
	}
}

func TestRunnerCredentialFileSkipsRegistration(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	credential := filepath.Join(t.TempDir(), "runner-token")
	const token = "dedicated-runner-token"
	if err := os.WriteFile(credential, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	registerRequests := 0
	heartbeatRequests := 0
	telemetry := app.RunnerOperationalTelemetry{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/runners/register":
			mu.Lock()
			registerRequests++
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(app.RunnerIdentity{RunnerID: "runner-dedicated"})
		case "/api/v1/runners/heartbeat":
			mu.Lock()
			heartbeatRequests++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner-dedicated", Status: domain.RunnerActive})
		case "/api/v1/runners/telemetry":
			if err := json.NewDecoder(r.Body).Decode(&telemetry); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/runners/claim":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := runRunner([]string{"--server", server.URL, "--credential-file", credential, "--id", "runner-dedicated", "--work-dir", t.TempDir(), "--once"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if registerRequests != 0 || heartbeatRequests != 1 || telemetry.JournalDepth != 0 || telemetry.RetryCount != 0 || telemetry.RenewFailures != 0 {
		t.Fatalf("register=%d heartbeat=%d telemetry=%#v, want 0/1/zero", registerRequests, heartbeatRequests, telemetry)
	}
}

func TestRunnerCredentialAndRegistrationModesAreExclusive(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	credential := filepath.Join(t.TempDir(), "runner-token")
	if err := os.WriteFile(credential, []byte("dedicated"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runRunner([]string{"--token", "privileged", "--credential-file", credential})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateRunnerSecretRootRejectsRelativePath(t *testing.T) {
	if err := validateRunnerSecretRoot("relative/secrets"); err == nil {
		t.Fatal("validateRunnerSecretRoot accepted a relative path")
	}
	if err := validateRunnerSecretRoot(t.TempDir()); err != nil {
		t.Fatalf("validateRunnerSecretRoot absolute path error=%v", err)
	}
}

func TestOpenAPIContractLoadsAndMatchesImplementedRoutes(t *testing.T) {
	document, err := loadOpenAPIContract("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := readOpenAPIOperations(document, "../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range api.PublicRoutes() {
		key := route.Method + " " + route.Path
		if _, ok := documented[key]; !ok {
			t.Fatalf("%s is implemented but missing from OpenAPI contract", key)
		}
	}
}

func TestRuntimeConfigValidation(t *testing.T) {
	t.Setenv("NEROCD_DATABASE_URL", "mysql://example.local/db")
	if _, err := loadRuntimeConfig(":8080"); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("loadRuntimeConfig accepted invalid database URL: %v", err)
	}

	t.Setenv("NEROCD_DATABASE_URL", "")
	t.Setenv("NEROCD_REQUIRE_DATABASE", "true")
	if _, err := loadRuntimeConfig(":8080"); err == nil || !strings.Contains(err.Error(), "requires NEROCD_DATABASE_URL") {
		t.Fatalf("loadRuntimeConfig accepted missing required database: %v", err)
	}

	t.Setenv("NEROCD_REQUIRE_DATABASE", "")
	t.Setenv("NEROCD_DEV_MEMORY", "true")
	bootstrap := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(bootstrap, []byte("test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_DEV_BOOTSTRAP_EMAIL", "test@example.invalid")
	t.Setenv("NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE", bootstrap)
	cfg, err := loadRuntimeConfig("127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:18080" || cfg.databaseURL != "" || !cfg.cookieSecure {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	t.Setenv("NEROCD_COOKIE_SECURE", "false")
	cfg, err = loadRuntimeConfig("127.0.0.1:18080")
	if err != nil || cfg.cookieSecure {
		t.Fatalf("local cookie override config=%#v err=%v", cfg, err)
	}
	t.Setenv("NEROCD_COOKIE_SECURE", "invalid")
	if _, err := loadRuntimeConfig("127.0.0.1:18080"); err == nil || !strings.Contains(err.Error(), "NEROCD_COOKIE_SECURE") {
		t.Fatalf("loadRuntimeConfig accepted invalid cookie security override: %v", err)
	}
}

func TestMigrationFilesAreOrderedAndChecksummed(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || files[0] != "migrations/0001_backend_primitives.sql" {
		t.Fatalf("unexpected migration files: %#v", files)
	}
	if checksum := sqlChecksum([]byte("select 1;")); !strings.HasPrefix(checksum, "sha256:") {
		t.Fatalf("unexpected checksum: %s", checksum)
	}
}

func TestDocumentedOperationMetadataComesFromOpenAPIModel(t *testing.T) {
	document, err := loadOpenAPIContract("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := readOpenAPIOperations(document, "../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	session := documented[http.MethodPost+" /api/v1/sessions"]
	if !session.OperationID {
		t.Fatal("POST /api/v1/sessions missing operationId metadata")
	}
	if !session.SecurityEmpty {
		t.Fatal("POST /api/v1/sessions should document security: []")
	}
	if !session.RequestBody || !session.JSONRequestBody {
		t.Fatal("POST /api/v1/sessions should document a JSON request body")
	}
	if !session.JSONResponseCodes["201"] {
		t.Fatal("POST /api/v1/sessions should document a JSON 201 response")
	}

	projects := documented[http.MethodGet+" /api/v1/projects"]
	if projects.SecurityEmpty {
		t.Fatal("GET /api/v1/projects should inherit bearer security")
	}
	if !projects.Responses["401"] {
		t.Fatal("GET /api/v1/projects should document 401")
	}
	for _, key := range []string{
		http.MethodDelete + " /api/v1/sessions",
		http.MethodPost + " /api/v1/runner-enrollments/consume",
	} {
		op := documented[key]
		if !op.SecuritySchemes["bearerAuth"] || op.SecuritySchemes["browserSession"] {
			t.Fatalf("%s must explicitly document bearer-only security: %#v", key, op.SecuritySchemes)
		}
	}
}
