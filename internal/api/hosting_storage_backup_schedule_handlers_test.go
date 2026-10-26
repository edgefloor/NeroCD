package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

type storageScheduleHTTPRepository struct {
	*archiveHTTPRepository
	schedule domain.HostingStorageBackupSchedule
	input    domain.HostingStorageBackupScheduleInput
}

func (r *storageScheduleHTTPRepository) SaveStorageBackupSchedule(ctx context.Context, id, resource, actor, existing string, in domain.HostingStorageBackupScheduleInput) (domain.HostingStorageBackupSchedule, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return r.schedule, err
	}
	r.input = in
	if existing != "" {
		id = existing
	}
	r.schedule.ID = id
	r.schedule.Revision = in.ExpectedVersion + 1
	r.schedule.Frequency = in.Frequency
	r.schedule.Timezone = in.Timezone
	r.schedule.Enabled = in.Enabled
	return r.schedule, nil
}
func (r *storageScheduleHTTPRepository) ListStorageBackupSchedules(ctx context.Context, actor, resource string) ([]domain.HostingStorageBackupSchedule, error) {
	r.calls++
	return []domain.HostingStorageBackupSchedule{r.schedule}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *storageScheduleHTTPRepository) ListStorageBackupOccurrences(ctx context.Context, actor, resource, id string) ([]domain.HostingStorageBackupOccurrence, error) {
	r.calls++
	return []domain.HostingStorageBackupOccurrence{}, r.authorize(ctx, actor, domain.RoleViewer)
}

func TestStorageBackupScheduleHTTPPermissionsAndClosedInputs(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "application"
	repo := &storageScheduleHTTPRepository{archiveHTTPRepository: original}
	now := time.Now().UTC()
	repo.schedule = domain.HostingStorageBackupSchedule{ID: "schedule", Version: domain.HostingStorageBackupScheduleVersion, ProjectID: original.resource.ProjectID, ResourceID: original.resource.ID, Revision: 1, Frequency: "daily", Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/resources/" + repo.resource.ID + "/storage-backup-schedules"
	body := `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"expected_version":0,"frequency":"daily","timezone":"UTC","enabled":true}`
	archiveHTTPCall(t, server, "POST", path, "", body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, body, "application/json", 403)
	archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", path+"/schedule/occurrences", viewer, "", "", 200)
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 200)
	if repo.input.ExpectedVersion != 0 || !repo.input.Enabled || repo.input.Timezone != "UTC" {
		t.Fatal("create lost typed preconditions")
	}
	update := strings.Replace(strings.Replace(body, `"expected_version":0`, `"expected_version":1`, 1), `"enabled":true`, `"enabled":false`, 1)
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, update, "application/json", 200)
	if repo.input.ExpectedVersion != 1 || repo.input.Enabled || repo.schedule.ID != "schedule" {
		t.Fatal("disable lost exact schedule CAS")
	}
	before := repo.calls
	for _, invalid := range []string{strings.Replace(body, `,"enabled":true`, "", 1), strings.Replace(body, `"enabled":true`, `"enabled":null`, 1), strings.Replace(body, `,"expected_version":0`, "", 1), strings.Replace(body, `"expected_version":0`, `"expected_version":null`, 1), strings.Replace(body, `"timezone":"UTC"`, `"timezone":"Local"`, 1), strings.Replace(body, `"frequency":"daily"`, `"frequency":"@every 1m"`, 1), strings.Replace(body, `"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","request_id":"other"`, 1), strings.TrimSuffix(body, "}") + `,"command":"canary_private"}`} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
	archiveHTTPCall(t, server, "GET", path+"?timezone=UTC", owner, "", "", 400)
	if repo.calls != before {
		t.Fatal("invalid schedule reached persistence")
	}
}
func TestStorageBackupScheduleOpenAPI(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingStorageBackupScheduleInput{RequestID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpectedResourceGeneration: 1, Frequency: "daily", Timezone: "UTC", Enabled: false}
	raw, _ := json.Marshal(in)
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		t.Fatal("JSON fixture")
	}
	schema := doc.Components.Schemas["HostingStorageBackupScheduleInput"].Value
	if err = schema.VisitJSON(value); err != nil {
		t.Fatal(err)
	}
	delete(value, "enabled")
	if schema.VisitJSON(value) == nil {
		t.Fatal("schema permits missing disable flag")
	}
	for _, name := range []string{"HostingStorageBackupSchedule", "HostingStorageBackupOccurrence"} {
		if doc.Components.Schemas[name] == nil {
			t.Fatal("missing safe schema", name)
		}
	}
}

// storageScheduleResponseCode limits runtime failure evidence to known API codes.
// Response messages, unknown codes and submitted documents never enter the log.
func storageScheduleResponseCode(raw []byte) string {
	var envelope struct {
		Code string `json:"code"`
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &envelope) != nil {
		return "unknown"
	}
	switch envelope.Code {
	case "invalid_request", "bad_request", "conflict", "not_found", "unauthenticated", "forbidden", "rate_limited", "internal_error", "hosting_storage_archive_unavailable", "hosting_storage_unavailable", "hosting_unavailable", "unavailable", "request_too_large":
		return envelope.Code
	default:
		return "unknown"
	}
}

func TestStorageBackupScheduleResponseCodeEvidence(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"code":"conflict","error":"canary_private"}`, "conflict"},
		{`{"code":"bad_request","error":"canary_private"}`, "bad_request"},
		{`{"code":"canary_private","error":"canary_private"}`, "unknown"},
		{`{"code":42}`, "unknown"},
		{`canary_private`, "unknown"},
		{strings.Repeat("canary_private", 4096), "unknown"},
	} {
		if got := storageScheduleResponseCode([]byte(tc.raw)); got != tc.want {
			t.Errorf("finite response code got=%s want=%s", got, tc.want)
		}
	}
}
