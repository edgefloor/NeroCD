package main

import (
	"net/http"
	"strings"
	"testing"

	"nerocd/internal/api"
)

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
	cfg, err := loadRuntimeConfig("127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:18080" || cfg.databaseURL != "" {
		t.Fatalf("unexpected config: %#v", cfg)
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
}
