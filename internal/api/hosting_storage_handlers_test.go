package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type storageAPIRepository struct {
	*store.MemoryStore
	value         domain.HostingStorage
	input         domain.HostingStorageInput
	err           error
	reads, writes int
}

func (s *storageAPIRepository) GetHostingStorage(_ context.Context, _, _ string) (domain.HostingStorage, error) {
	s.reads++
	if s.err != nil {
		return domain.HostingStorage{}, s.err
	}
	if s.value.ID == "" {
		return domain.HostingStorage{}, store.ErrNotFound
	}
	return s.value, nil
}
func (s *storageAPIRepository) CreateHostingStorage(_ context.Context, actor, resource string, in domain.HostingStorageInput, _ domain.AuditEvent) (domain.HostingStorage, error) {
	s.writes++
	if s.err != nil {
		return domain.HostingStorage{}, s.err
	}
	if s.input.RequestID != "" && s.input != in {
		return domain.HostingStorage{}, store.ErrConflict
	}
	if s.input == in {
		return s.value, nil
	}
	if in.ExpectedResourceGeneration != 1 {
		return domain.HostingStorage{}, store.ErrConflict
	}
	s.input = in
	id, volume := domain.HostingStorageIdentity("storage_binding")
	s.value = domain.HostingStorage{ID: id, ProjectID: "proj_platform", ResourceID: resource, BindingID: "storage_binding", DestinationID: "storage_destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: in.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: actor, CreatedAt: time.Unix(100, 0).UTC()}
	s.value.Hash = s.value.DocumentHash()
	return s.value, nil
}
func TestHostingStoragePublicAPI(t *testing.T) {
	server, mem := newTestServer()
	repo := &storageAPIRepository{MemoryStore: mem}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	token := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	if _, err := mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "storage_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, credential string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if method == "POST" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Request-ID", "storage-api-test")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body.String())
		}
		if strings.HasSuffix(path, "/storage") && rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("storage response is cacheable")
		}
		if rec.Header().Get("X-Request-ID") != "storage-api-test" {
			t.Fatal("missing request ID")
		}
		return rec
	}
	var env domain.ProjectEnvironment
	if err := json.Unmarshal(call("POST", "/api/v1/project-environments", `{"project_id":"proj_platform","name":"storage"}`, token, 201).Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var resource domain.Resource
	create := fmt.Sprintf(`{"project_id":"proj_platform","project_environment_id":%q,"name":"uploads","kind":"application","source":{"kind":"image","image":{"reference":"nginx:1"}}}`, env.ID)
	if err := json.Unmarshal(call("POST", "/api/v1/resources", create, token, 201).Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/resources/" + resource.ID + "/storage"
	valid := `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"mount_path":"/app/uploads"}`
	call("GET", path, "", "", 401)
	call("POST", path, valid, "", 401)
	call("GET", path, "", viewer, 404)
	call("POST", path, valid, viewer, 403)
	if repo.writes != 0 {
		t.Fatal("viewer reached storage write")
	}
	for _, body := range []string{
		`{}`, `null`, `[]`, valid + `{}`, strings.Replace(valid, `"mount_path":`, `"mount_path":"/other","mount_path":`, 1), strings.Replace(valid, `"mount_path":`, `"volume_name":"foreign","mount_path":`, 1), strings.Replace(valid, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "not-a-request-id", 1), strings.Replace(valid, "/app/uploads", "/dev/shm", 1), strings.Replace(valid, "/app/uploads", "/app/../uploads", 1), strings.Replace(valid, `"expected_resource_generation":1`, `"expected_resource_generation":0`, 1),
	} {
		call("POST", path, body, token, 400)
	}
	call("POST", path, strings.Repeat(" ", 4097), token, 413)
	if repo.writes != 0 {
		t.Fatal("invalid input reached storage")
	}
	first := call("POST", path, valid, token, 201).Body.String()
	replay := call("POST", path, valid, token, 201).Body.String()
	if first != replay || repo.input.RequestID != strings.Repeat("a", 32) || repo.input.MountPath != "/app/uploads" {
		t.Fatal("replay changed request identity or response")
	}
	if call("GET", path, "", viewer, 200).Body.String() != first {
		t.Fatal("viewer metadata differs")
	}
	call("POST", path, strings.Replace(valid, "/app/uploads", "/different", 1), token, 409)
	for _, role := range []string{domain.RoleMaintainer, domain.RoleOwner} {
		if _, err := mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "storage_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: role}); err != nil {
			t.Fatal(err)
		}
		want := 403
		if role == domain.RoleOwner {
			want = 201
		}
		call("POST", path, valid, viewer, want)
	}
	// Repository-level authority failures, including a revoked destination grant,
	// stay failures at the public boundary even for a replayed owner request.
	for _, failure := range []struct {
		err    error
		status int
	}{{auth.ErrForbidden, 403}, {store.ErrConflict, 409}, {store.ErrHostingStorageUnavailable, 503}, {errors.New("private persistence detail"), 500}} {
		repo.err = failure.err
		for _, method := range []string{"GET", "POST"} {
			body := ""
			if method == "POST" {
				body = valid
			}
			rec := call(method, path, body, token, failure.status)
			if strings.Contains(rec.Body.String(), "private persistence detail") {
				t.Fatal("persistence detail leaked")
			}
		}
	}
}
func TestHostingStorageCachePath(t *testing.T) {
	server, _ := newTestServer()
	for _, path := range []string{"/api/v1/resources/r/storage", "/api/v1/resources/r/storage/extra", "/api/v1/projects/r/storage"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if (rec.Header().Get("Cache-Control") == "no-store") != (path == "/api/v1/resources/r/storage") {
			t.Fatalf("unexpected cache path match: %s", path)
		}
	}
}
