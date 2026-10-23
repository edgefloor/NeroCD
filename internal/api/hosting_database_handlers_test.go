package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
	"nerocd/web"
)

type databaseHTTPRepository struct {
	*store.MemoryStore
	view  domain.HostingDatabase
	calls int
	err   error
}

func (r *databaseHTTPRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == r.view.Resource.ID {
		return r.view.Resource, nil
	}
	return r.MemoryStore.GetResource(ctx, id)
}
func (r *databaseHTTPRepository) GetHostingDatabase(context.Context, string, string) (domain.HostingDatabase, error) {
	r.calls++
	return r.view, r.err
}
func (r *databaseHTTPRepository) CreateHostingDatabase(_ context.Context, actor string, in domain.HostingDatabaseCreateInput, records []store.HostingDatabaseCredentialRecord, _ domain.AuditEvent) (domain.HostingDatabase, error) {
	r.calls++
	recipe, err := domain.HostingDatabaseRecipeFor(in.Engine, in.RecipeVersion)
	if err != nil {
		return domain.HostingDatabase{}, err
	}
	resource := domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID)
	now := time.Now().UTC()
	r.view = domain.HostingDatabase{Resource: domain.Resource{ID: resource, ProjectID: in.ProjectID, ProjectEnvironmentID: in.ProjectEnvironmentID, Name: in.Name, Kind: "database", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: recipe.ImageReference}}, Generation: 1, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}, Binding: domain.ResourceBinding{ID: "binding_database", ResourceID: resource, DestinationID: in.DestinationID, RuntimeNamespace: "ncd_database", CreatedBy: actor, CreatedAt: now}, StorageRef: domain.HostingStorageRef{StorageID: "hsv_" + strings.Repeat("a", 32), Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}, Engine: in.Engine, RecipeVersion: in.RecipeVersion, RuntimeState: domain.HostingDatabaseUnqualified}
	for _, record := range records {
		r.view.Credentials = append(r.view.Credentials, record.Reference)
	}
	return r.view, r.err
}

func TestHostingDatabaseHTTPAuthorizationClosedInputAndMetadata(t *testing.T) {
	mem := newSeededTestStore()
	repo := &databaseHTTPRepository{MemoryStore: mem}
	ring, err := secretcrypto.New("database-http", map[string][]byte{"database-http": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, SecretKeyring: ring, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(s, slog.Default(), web.Static())
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "database HTTP"})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("a", 32), ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "HTTP database", DestinationID: "dest_database", Engine: "redis", RecipeVersion: domain.HostingDatabaseRedisRecipe}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/databases"
	request := func(method, url, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("database response cacheable: %d %+v", rec.Code, rec.Header())
		}
		return rec
	}
	if rec := request(http.MethodPost, path, "", string(raw)); rec.Code != 401 {
		t.Fatalf("anonymous create: %d", rec.Code)
	}
	if rec := request(http.MethodGet, "/api/v1/resources/res_database/database", "", ""); rec.Code != 401 {
		t.Fatalf("anonymous metadata: %d", rec.Code)
	}
	token := createTestSession(t, server)
	for _, bad := range []string{`{}`, `null`, string(raw) + `{}`, strings.TrimSuffix(string(raw), "}") + `,"password":"injected"}`, strings.TrimSuffix(string(raw), "}") + `,"engine":"postgresql"}`, strings.Replace(string(raw), `"redis"`, `"mysql"`, 1)} {
		if rec := request(http.MethodPost, path, token, bad); rec.Code != 400 {
			t.Fatalf("closed input: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := request(http.MethodPost, path+"?image=other", token, string(raw)); rec.Code != 400 {
		t.Fatalf("query override: %d", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatal("rejected input reached database repository")
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: in.ProjectID, Name: "HTTP database " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		want := 403
		if role == domain.RoleOwner {
			want = 201
		}
		rec := request(http.MethodPost, path, account.Token, string(raw))
		if rec.Code != want {
			t.Fatalf("%s creation: %d %s", role, rec.Code, rec.Body.String())
		}
	}
	readPath := "/api/v1/resources/" + repo.view.Resource.ID + "/database"
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		url, body := path, string(raw)
		want := 201
		if method == http.MethodGet {
			url, body, want = readPath, "", 200
		}
		rec := request(method, url, token, body)
		if rec.Code != want {
			t.Fatalf("valid database request: %d %s", rec.Code, rec.Body.String())
		}
		var view domain.HostingDatabase
		if err = json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.RuntimeState != domain.HostingDatabaseUnqualified || view.Resource.Kind != "database" || len(view.Credentials) != 2 {
			t.Fatalf("metadata contract: %s %v", rec.Body.String(), err)
		}
		for _, private := range []string{`"value"`, `"password"`, `"ciphertext"`, `"endpoint"`} {
			if strings.Contains(rec.Body.String(), private) {
				t.Fatal("database response contains private or unqualified fields")
			}
		}
	}
	for _, suffix := range []string{"?unexpected=1", "?id=private"} {
		if rec := request(http.MethodGet, readPath+suffix, token, ""); rec.Code != 400 {
			t.Fatalf("read query: %d", rec.Code)
		}
	}
	if rec := request(http.MethodGet, readPath, token, "{}"); rec.Code != 400 {
		t.Fatalf("read body: %d", rec.Code)
	}
	foreign, e := s.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_security", Name: "foreign database owner", Role: domain.RoleOwner})
	if e != nil {
		t.Fatal(e)
	}
	if rec := request(http.MethodGet, readPath, foreign.Token, ""); rec.Code != 403 {
		t.Fatalf("foreign metadata: %d", rec.Code)
	}
	for _, mapping := range []struct {
		err    error
		status int
		code   string
	}{{store.ErrConflict, 409, "conflict"}, {store.ErrNotFound, 404, "not_found"}, {auth.ErrForbidden, 403, "forbidden"}, {store.ErrHostingDatabaseUnavailable, 503, "hosting_database_unavailable"}} {
		repo.err = mapping.err
		rec := request(http.MethodGet, readPath, token, "")
		var envelope struct {
			Code string `json:"code"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if rec.Code != mapping.status || envelope.Code != mapping.code {
			t.Fatalf("%v mapped to %d %s", mapping.err, rec.Code, rec.Body.String())
		}
	}
}
