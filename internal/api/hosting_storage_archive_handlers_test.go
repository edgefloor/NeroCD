package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type archiveHTTPRepository struct {
	*store.MemoryStore
	store.HostingExecutionRepository
	resource      domain.Resource
	backup        domain.HostingStorageBackup
	restored      domain.HostingStorageRestore
	operation     domain.HostingOperation
	backupInput   domain.HostingStorageBackupInput
	restoreInput  domain.HostingStorageRestoreInput
	recoveryInput domain.HostingStorageArchiveRecoveryInput
	calls         int
	err           error
}

// This fake retains the repository-owned read authorization boundary. Controller
// transactions and physical file restoration are qualified separately.
func (r *archiveHTTPRepository) authorize(ctx context.Context, actor, role string) error {
	if r.err != nil {
		return r.err
	}
	if actor == "usr_bootstrap" {
		return nil
	}
	members, err := r.ListProjectMembers(ctx, r.resource.ProjectID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.UserID == actor && (m.Role == domain.RoleOwner || role == domain.RoleViewer && m.Role == domain.RoleViewer) {
			return nil
		}
	}
	return auth.ErrForbidden
}
func (r *archiveHTTPRepository) RequestStorageBackup(ctx context.Context, id, resource, actor string, in domain.HostingStorageBackupInput, _ domain.AuditEvent) (domain.HostingStorageBackup, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return domain.HostingStorageBackup{}, err
	}
	if r.backupInput.RequestID != "" && r.backupInput != in {
		return domain.HostingStorageBackup{}, store.ErrConflict
	}
	if r.backupInput.RequestID == "" {
		r.backupInput = in
		r.backup.OperationID = id
		r.backup.ResourceID = resource
	}
	return r.backup, nil
}
func (r *archiveHTTPRepository) GetStorageBackup(ctx context.Context, actor, _ string) (domain.HostingStorageBackup, error) {
	r.calls++
	return r.backup, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *archiveHTTPRepository) ListStorageBackups(ctx context.Context, actor, _ string, page store.Page) (store.PageResult[domain.HostingStorageBackup], error) {
	r.calls++
	return store.PageResult[domain.HostingStorageBackup]{Items: []domain.HostingStorageBackup{r.backup}, Total: 1, Limit: page.Limit, Offset: page.Offset}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *archiveHTTPRepository) RequestStorageRestore(ctx context.Context, _, _, actor, _, _ string, in domain.HostingStorageRestoreInput, _ domain.AuditEvent) (domain.HostingStorageRestore, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return domain.HostingStorageRestore{}, err
	}
	if r.restoreInput.RequestID != "" && r.restoreInput != in {
		return domain.HostingStorageRestore{}, store.ErrConflict
	}
	r.restoreInput = in
	return r.restored, nil
}
func (r *archiveHTTPRepository) GetStorageRestore(ctx context.Context, actor, _ string) (domain.HostingStorageRestore, error) {
	r.calls++
	return r.restored, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *archiveHTTPRepository) RequestStorageArchiveRecovery(ctx context.Context, _, _, actor string, in domain.HostingStorageArchiveRecoveryInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return domain.HostingOperation{}, err
	}
	if r.recoveryInput.RequestID != "" && r.recoveryInput != in {
		return domain.HostingOperation{}, store.ErrConflict
	}
	r.recoveryInput = in
	return r.operation, nil
}
func (r *archiveHTTPRepository) GetHostingOperation(_ context.Context, id string) (domain.HostingOperation, error) {
	if id == r.operation.ID {
		return r.operation, r.err
	}
	return domain.HostingOperation{}, store.ErrNotFound
}
func (r *archiveHTTPRepository) StorageArchivePlan(_ context.Context, _ string, _ domain.HostingAuthority) (domain.HostingOperation, error) {
	r.calls++
	return r.operation, r.err
}
func (r *archiveHTTPRepository) StorageArchivePrepared(_ context.Context, _ string, _ domain.HostingStorageArchivePreparedRequest) (domain.HostingOperation, error) {
	r.calls++
	return r.operation, r.err
}
func (r *archiveHTTPRepository) StorageArchiveApplyBegin(_ context.Context, _ string, _ domain.HostingStorageArchiveApplyBeginRequest) (domain.HostingOperation, error) {
	r.calls++
	return r.operation, r.err
}
func (r *archiveHTTPRepository) StorageArchiveProgress(_ context.Context, _ string, _ domain.HostingStorageArchiveProgress) (domain.HostingOperation, error) {
	r.calls++
	return r.operation, r.err
}
func (r *archiveHTTPRepository) StorageArchiveComplete(_ context.Context, _ string, _ domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error) {
	r.calls++
	return r.operation, r.err
}

func archiveHTTPFixture(t *testing.T) (*Server, *archiveHTTPRepository, string, string, string) {
	t.Helper()
	server, mem := newTestServer()
	repo := &archiveHTTPRepository{MemoryStore: mem}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	owner := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := service.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "archive API"})
	if err != nil {
		t.Fatal(err)
	}
	repo.resource, err = service.CreateResource(admin, app.ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "archive_viewer", ProjectID: env.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	hash := "sha256:" + strings.Repeat("a", 64)
	repo.backup = domain.HostingStorageBackup{ID: "backup", ProjectID: env.ProjectID, ResourceID: repo.resource.ID, OperationID: "archive", State: "queued", CreatedBy: "usr_bootstrap", CreatedAt: now}
	repo.operation = domain.HostingOperation{ID: "archive", ProjectID: env.ProjectID, ResourceID: repo.resource.ID, BindingID: "binding", CreatedBy: "usr_bootstrap", State: "prepared", TaskMode: "backup", CreatedAt: now, UpdatedAt: now, MutationHold: true, StorageArchive: &domain.HostingStorageArchive{Mode: "backup", BackupID: "backup", Progress: []domain.HostingStorageArchiveProgressEvidence{{Receipt: domain.HostingReceipt{ID: "snapshot", BodyHash: hash, CreatedAt: now}, PlanHash: hash, Phase: "writer_snapshot", RestoreVerified: false}}}}
	repo.restored = domain.HostingStorageRestore{Resource: repo.resource, Operation: repo.operation}
	runner := "archive_http_runner_token"
	if _, err = mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_archive", Name: "archive HTTP", TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(runner))), Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	return server, repo, owner, viewer, runner
}

func archiveHTTPCall(t *testing.T, s *Server, method, path, token, body, media string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if media != "" {
		request.Header.Set("Content-Type", media)
	}
	request.Header.Set("X-Request-ID", "archive-http-test")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") != "archive-http-test" {
		t.Fatal("archive response lost no-store or request identity")
	}
	return response
}

func TestStorageArchivePublicHTTPBoundary(t *testing.T) {
	server, repo, owner, viewer, runner := archiveHTTPFixture(t)
	backup := `{"request_id":"` + strings.Repeat("a", 32) + `","expected_resource_generation":1}`
	restore := `{"request_id":"` + strings.Repeat("b", 32) + `","name":"restored uploads"}`
	recovery := `{"request_id":"` + strings.Repeat("c", 32) + `","expected_plan_hash":"sha256:` + strings.Repeat("d", 64) + `"}`
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	other, err := server.app.CreateProject(admin, app.ProjectInput{Name: "other archive project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "archive_other_owner", ProjectID: other.ID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	resourcePath := "/api/v1/resources/" + repo.resource.ID + "/storage-backups"
	mutations := []struct{ path, body string }{{resourcePath, backup}, {"/api/v1/storage-backups/backup/restores", restore}, {"/api/v1/hosting-operations/archive/storage-archive-recovery", recovery}}
	for _, tc := range mutations {
		archiveHTTPCall(t, server, "POST", tc.path, "", tc.body, "application/json", 401)
		archiveHTTPCall(t, server, "POST", tc.path, runner, tc.body, "application/json", 401)
		archiveHTTPCall(t, server, "POST", tc.path, viewer, tc.body, "application/json", 403)
		before := repo.calls
		for _, body := range []string{`null`, `[]`, `{}`, tc.body + `{}`, strings.Replace(tc.body, `"request_id":`, `"request_id":"`+strings.Repeat("a", 32)+`","request_id":`, 1), strings.Replace(tc.body, `"request_id"`, `"Request_ID"`, 1), strings.Replace(tc.body, `}`, `,"path":"/private"}`, 1), strings.Replace(tc.body, `}`, `,"url":"https://invalid.test"}`, 1)} {
			archiveHTTPCall(t, server, "POST", tc.path, owner, body, "application/json", 400)
		}
		archiveHTTPCall(t, server, "POST", tc.path, owner, strings.Repeat(" ", 4097), "application/json", 413)
		archiveHTTPCall(t, server, "POST", tc.path, owner, tc.body, "text/plain", 415)
		archiveHTTPCall(t, server, "POST", tc.path, owner, tc.body, "", 415)
		archiveHTTPCall(t, server, "POST", tc.path+"?path=foreign", owner, tc.body, "application/json", 400)
		if repo.calls != before {
			t.Fatal("invalid archive request reached repository")
		}
		first := archiveHTTPCall(t, server, "POST", tc.path, owner, tc.body, "application/json", 202)
		repeated := archiveHTTPCall(t, server, "POST", tc.path, owner, tc.body, "application/json", 202)
		if first.Body.String() != repeated.Body.String() {
			t.Fatal("request replay changed response")
		}
		changed := tc.body
		switch tc.path {
		case resourcePath:
			changed = strings.Replace(changed, `:1}`, `:2}`, 1)
		case "/api/v1/storage-backups/backup/restores":
			changed = strings.Replace(changed, "restored uploads", "different uploads", 1)
		default:
			changed = strings.Replace(changed, strings.Repeat("d", 64), strings.Repeat("e", 64), 1)
		}
		archiveHTTPCall(t, server, "POST", tc.path, owner, changed, "application/json", 409)
	}
	for _, path := range []string{resourcePath, "/api/v1/storage-backups/backup", "/api/v1/storage-restores/restore", "/api/v1/hosting-operations/archive"} {
		archiveHTTPCall(t, server, "GET", path, "", "", "", 401)
		response := archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
		for _, forbidden := range []string{`"lease_id"`, `"fence"`, `"attempt"`, `"authority"`, `"ready"`, runner} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatal("public archive projection exposed execution authority")
			}
		}
		if !strings.Contains(path, "hosting-operations") {
			archiveHTTPCall(t, server, "GET", path+"?url=foreign", owner, "", "", 400)
			archiveHTTPCall(t, server, "GET", path, owner, `{}`, "", 400)
		}
	}
	archiveHTTPCall(t, server, "GET", resourcePath+"?url=%zz", owner, "", "", 400)
	pageResponse := archiveHTTPCall(t, server, "GET", resourcePath+"?limit=3&offset=1", viewer, "", "", 200)
	var page map[string]any
	if json.Unmarshal(pageResponse.Body.Bytes(), &page) != nil || page["limit"] != float64(3) || page["offset"] != float64(1) {
		t.Fatal("archive pagination parameters were not preserved")
	}
	// Persisted authority failures are propagated on replay instead of cached.
	for _, failure := range []struct {
		err    error
		status int
	}{{auth.ErrForbidden, 403}, {store.ErrConflict, 409}, {store.ErrNotFound, 404}, {store.ErrStorageArchiveUnavailable, 503}, {errors.New("private archive persistence details"), 500}} {
		repo.err = failure.err
		for _, path := range []string{resourcePath, "/api/v1/storage-backups/backup", "/api/v1/storage-restores/restore"} {
			archiveHTTPCall(t, server, "GET", path, owner, "", "", failure.status)
		}
		for _, tc := range mutations {
			response := archiveHTTPCall(t, server, "POST", tc.path, owner, tc.body, "application/json", failure.status)
			if strings.Contains(response.Body.String(), "private archive persistence details") {
				t.Fatal("private error leaked")
			}
		}
	}
}

func TestStorageArchiveRunnerHTTPBoundary(t *testing.T) {
	server, repo, owner, _, runner := archiveHTTPFixture(t)
	a := domain.HostingAuthority{OperationID: "archive", RunID: "backup_archive", LeaseID: "private_lease", Fence: "private_fence", Attempt: 1}
	hash := "sha256:" + strings.Repeat("a", 64)
	helper := domain.HostingStorageArchiveHelper{Mode: "local_image", RecipeVersion: domain.HostingStorageArchiveVersion, ImageID: hash, ExecutableSHA256: hash, SourceCommit: strings.Repeat("b", 40), OS: "linux", Architecture: "arm64", DaemonID: "daemon"}
	helper.Hash = domain.HostingDocumentHash(helper)
	cases := []struct {
		suffix string
		input  any
	}{{"plan", a}, {"prepared", domain.HostingStorageArchivePreparedRequest{HostingAuthority: a, ReceiptID: "prepared", PlanHash: hash, Helper: helper}}, {"apply-begin", domain.HostingStorageArchiveApplyBeginRequest{HostingAuthority: a, ReceiptID: "begin", PlanHash: hash}}, {"progress", domain.HostingStorageArchiveProgress{HostingAuthority: a, ReceiptID: "progress", PlanHash: hash, Phase: "helper_intent"}}, {"complete", domain.HostingStorageArchiveCompleteRequest{HostingAuthority: a, ReceiptID: "complete", PlanHash: hash, Result: domain.HostingStorageArchiveResult{ArchiveOutcome: "not_attempted", ResumeOutcome: "uncertain", Uncertain: true, FailureCode: "interrupted"}}}}
	for _, tc := range cases {
		path := "/api/v1/runners/hosting/storage-archive/" + tc.suffix
		raw, err := json.Marshal(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		archiveHTTPCall(t, server, "POST", path, "", string(raw), "application/json", 401)
		archiveHTTPCall(t, server, "POST", path, owner, string(raw), "application/json", 401)
		before := repo.calls
		for _, body := range []string{`null`, `[]`, `{}`, string(raw) + `{}`, strings.Replace(string(raw), `"operation_id":`, `"operation_id":"archive","operation_id":`, 1), strings.Replace(string(raw), `"operation_id"`, `"Operation_ID"`, 1), strings.Replace(string(raw), `}`, `,"path":"/private"}`, 1)} {
			archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 400)
		}
		for _, key := range []string{"operation_id", "run_id", "lease_id", "fence", "attempt"} {
			var missing map[string]json.RawMessage
			if json.Unmarshal(raw, &missing) != nil {
				t.Fatal("runner fixture decode")
			}
			delete(missing, key)
			body, err := json.Marshal(missing)
			if err != nil {
				t.Fatal(err)
			}
			archiveHTTPCall(t, server, "POST", path, runner, string(body), "application/json", 400)
		}
		archiveHTTPCall(t, server, "POST", path, runner, strings.Repeat(" ", domain.HostingStorageProtocolJSONLimit+1), "application/json", 413)
		archiveHTTPCall(t, server, "POST", path, runner, string(raw), "text/plain", 415)
		if repo.calls != before {
			t.Fatal("invalid runner archive request reached repository")
		}
		archiveHTTPCall(t, server, "POST", path, runner, string(raw), "application/json", 200)
		repo.err = store.ErrStorageArchiveUnavailable
		archiveHTTPCall(t, server, "POST", path, runner, string(raw), "application/json", 503)
		repo.err = nil
	}
}

func TestStorageArchiveRoutesAndClosedContract(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range PublicRoutes() {
		if !hostingStorageArchivePath(route.Path) && !hostingStorageArchiveRunnerPath(route.Path) {
			continue
		}
		item := document.Paths.Find(route.Path)
		if item == nil || item.GetOperation(route.Method) == nil {
			t.Fatalf("archive route missing contract: %s %s", route.Method, route.Path)
		}
	}
	for _, name := range []string{"HostingStorageBackupInput", "HostingStorageRestoreInput", "HostingStorageArchiveRecoveryInput", "HostingStorageArchivePreparedRequest", "HostingStorageArchiveProgress", "HostingStorageArchiveCompleteRequest", "HostingStorageArchiveProgressEvidence", "HostingStorageArchiveResult"} {
		schema := document.Components.Schemas[name]
		if schema == nil || schema.Value.AdditionalProperties.Has == nil || *schema.Value.AdditionalProperties.Has {
			t.Fatalf("archive schema is not closed: %s", name)
		}
	}
}

func TestStorageArchiveContractMatchesTypedFieldsAndZeroSnapshot(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	types := []any{domain.HostingStorageBackupInput{}, domain.HostingStorageRestoreInput{}, domain.HostingStorageArchiveRecoveryInput{}, domain.HostingStorageArchiveHelper{}, domain.HostingStorageArchiveLimits{}, domain.HostingStorageArchiveBlob{}, domain.HostingStorageArchivePlan{}, domain.HostingStorageArchiveTarget{}, domain.HostingStorageArchiveWriterState{}, domain.HostingStorageArchiveRecoveryEvidence{}, domain.HostingStorageArchivePreparedRequest{}, domain.HostingStorageArchiveApplyBeginRequest{}, domain.HostingStorageArchiveProgress{}, domain.HostingStorageArchiveProgressEvidence{}, domain.HostingStorageArchiveResult{}, domain.HostingStorageArchiveCompleteRequest{}, domain.HostingStorageArchive{}, domain.HostingStorageBackup{}, domain.HostingStorageRestore{}}
	var fields func(reflect.Type) []string
	fields = func(typ reflect.Type) []string {
		var names []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous {
				names = append(names, fields(f.Type)...)
				continue
			}
			names = append(names, strings.Split(f.Tag.Get("json"), ",")[0])
		}
		return names
	}
	for _, sample := range types {
		typ := reflect.TypeOf(sample)
		schema := doc.Components.Schemas[typ.Name()]
		if schema == nil {
			t.Fatalf("missing typed archive schema: %s", typ.Name())
		}
		names := fields(typ)
		if len(names) != len(schema.Value.Properties) {
			t.Fatalf("contract field count differs: %s", typ.Name())
		}
		for _, name := range names {
			if schema.Value.Properties[name] == nil {
				t.Fatalf("missing field %s.%s", typ.Name(), name)
			}
		}
	}
	raw, err := json.Marshal(domain.HostingSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var zero any
	if json.Unmarshal(raw, &zero) != nil {
		t.Fatal("zero snapshot decode")
	}
	if err = doc.Components.Schemas["HostingStorageArchiveZeroSnapshot"].Value.VisitJSON(zero); err != nil {
		t.Fatalf("Go zero snapshot differs from archive contract: %v", err)
	}
	if doc.Components.Schemas["HostingSnapshot"].Value.VisitJSON(zero) == nil {
		t.Fatal("archive zero snapshot also accepted as application snapshot")
	}
	evidence := map[string]any{"receipt": map[string]any{"id": "receipt", "body_hash": "sha256:" + strings.Repeat("a", 64), "created_at": time.Unix(1700000000, 0).UTC().Format(time.RFC3339)}, "plan_hash": "sha256:" + strings.Repeat("a", 64), "phase": "writer_snapshot", "restore_verified": false}
	schema := doc.Components.Schemas["HostingStorageArchiveProgressEvidence"].Value
	if err = schema.VisitJSON(evidence); err != nil {
		t.Fatal("safe progress evidence rejected")
	}
	for _, key := range []string{"authority", "lease_id", "fence", "attempt", "body"} {
		evidence[key] = "private"
		if schema.VisitJSON(evidence) == nil {
			t.Fatal("contract accepted private progress fields")
		}
		delete(evidence, key)
	}
	result := map[string]any{"archive_outcome": "not_attempted", "resume_outcome": "uncertain", "restore_verified": false, "uncertain": true}
	resultSchema := doc.Components.Schemas["HostingStorageArchiveResult"].Value
	if err = resultSchema.VisitJSON(result); err != nil {
		t.Fatal("independent result rejected")
	}
	result["ready"] = true
	if resultSchema.VisitJSON(result) == nil {
		t.Fatal("archive result accepted application readiness")
	}
}

func TestStorageArchiveHTTPProjectOwnerAndDisabledConfiguration(t *testing.T) {
	server, repo, _, viewer, _ := archiveHTTPFixture(t)
	if _, err := repo.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "archive_viewer", ProjectID: repo.resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1}`
	archiveHTTPCall(t, server, "POST", "/api/v1/resources/"+repo.resource.ID+"/storage-backups", viewer, body, "application/json", 202)
	disabled, _ := newTestServer()
	user := createTestSession(t, disabled)
	archiveHTTPCall(t, disabled, "GET", "/api/v1/storage-backups/backup", user, "", "", 503)
	archiveHTTPCall(t, disabled, "GET", "/api/v1/storage-restores/restore", user, "", "", 503)
	archiveHTTPCall(t, disabled, "POST", "/api/v1/hosting-operations/archive/storage-archive-recovery", user, `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_plan_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "application/json", 503)
}

func (r *archiveHTTPRepository) StorageBackupVolumes(ctx context.Context, actor, resource string) (domain.HostingStorageBackupVolumes, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleViewer); err != nil {
		return domain.HostingStorageBackupVolumes{}, err
	}
	return domain.HostingStorageBackupVolumes{ResourceID: resource, ResourceGeneration: r.resource.Generation, Available: true, OperationID: "deployed", Volumes: []domain.HostingStorageBackupVolume{{Volume: "uploads", Eligible: true, Mounts: []domain.HostingStorageBackupVolumeMount{{Component: "app", MountPath: "/uploads", ReadOnly: false}}}}}, nil
}

func TestStorageArchiveComposeSelectionHTTPDecoder(t *testing.T) {
	server, repo, owner, viewer, _ := archiveHTTPFixture(t)
	path := "/api/v1/resources/" + repo.resource.ID + "/storage-backups"
	body := `{"request_id":"` + strings.Repeat("6", 32) + `","expected_resource_generation":1,"volume":"uploads","expected_deployed_operation_id":"hop_deployed","acknowledge_interruption":true}`
	for _, invalid := range []string{strings.Replace(body, `,"acknowledge_interruption":true`, "", 1), strings.Replace(body, `true`, `false`, 1), strings.Replace(body, `"uploads"`, `null`, 1)} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 202)
	if repo.backupInput.Volume != "uploads" || repo.backupInput.ExpectedDeployedOperationID != "hop_deployed" || !repo.backupInput.AcknowledgeInterruption {
		t.Fatal("real HTTP decoder dropped selection")
	}
	discoveryPath := "/api/v1/resources/" + repo.resource.ID + "/storage-backup-volumes"
	response := archiveHTTPCall(t, server, "GET", discoveryPath, viewer, "", "", 200)
	var discovery domain.HostingStorageBackupVolumes
	if json.Unmarshal(response.Body.Bytes(), &discovery) != nil || !discovery.Available || len(discovery.Volumes) != 1 {
		t.Fatal("viewer discovery failed")
	}
	archiveHTTPCall(t, server, "GET", discoveryPath+"?volume=foreign", owner, "", "", 400)
}
