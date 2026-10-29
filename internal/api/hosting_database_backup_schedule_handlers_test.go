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

type backupScheduleHTTPRepository struct {
	*databaseBackupHTTPRepository
	schedule domain.HostingDatabaseBackupSchedule
	input    domain.HostingDatabaseBackupScheduleInput
}

func (r *backupScheduleHTTPRepository) SaveDatabaseBackupSchedule(ctx context.Context, id, resource, actor, existing string, in domain.HostingDatabaseBackupScheduleInput) (domain.HostingDatabaseBackupSchedule, error) {
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
func (r *backupScheduleHTTPRepository) ListDatabaseBackupSchedules(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupSchedule, error) {
	r.calls++
	return []domain.HostingDatabaseBackupSchedule{r.schedule}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *backupScheduleHTTPRepository) ListDatabaseBackupOccurrences(ctx context.Context, actor, resource, id string) ([]domain.HostingDatabaseBackupOccurrence, error) {
	r.calls++
	return []domain.HostingDatabaseBackupOccurrence{}, r.authorize(ctx, actor, domain.RoleViewer)
}

func TestDatabaseBackupScheduleHTTPPermissionsAndClosedInputs(t *testing.T) {
	for _, version := range []string{domain.HostingDatabaseBackupScheduleVersion, domain.HostingDatabaseMySQLBackupScheduleVersion, domain.HostingDatabaseMySQLBackupComponentScheduleVersion} {
		t.Run(version, func(t *testing.T) { databaseBackupScheduleHTTPPermissionsAndClosedInputs(t, version) })
	}
}

func databaseBackupScheduleHTTPPermissionsAndClosedInputs(t *testing.T, version string) {
	t.Helper()
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "database"
	repo := &backupScheduleHTTPRepository{databaseBackupHTTPRepository: &databaseBackupHTTPRepository{archiveHTTPRepository: original}}
	now := time.Now().UTC()
	repo.schedule = domain.HostingDatabaseBackupSchedule{ID: "schedule", Version: version, ProjectID: original.resource.ProjectID, ResourceID: original.resource.ID, Revision: 1, Frequency: "daily", Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/resources/" + repo.resource.ID + "/database-backup-schedules"
	body := `{"request_id":"schedule_request","expected_resource_generation":1,"expected_version":0,"frequency":"daily","timezone":"UTC","enabled":true}`
	archiveHTTPCall(t, server, "POST", path, "", body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, body, "application/json", 403)
	archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", path+"/schedule/occurrences", viewer, "", "", 200)
	created := archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 200)
	if !strings.Contains(created.Body.String(), `"version":"`+version+`"`) {
		t.Fatal("schedule response lost engine version")
	}
	if repo.input.ExpectedVersion != 0 || !repo.input.Enabled || repo.input.Timezone != "UTC" {
		t.Fatal("create lost typed preconditions")
	}
	update := strings.Replace(strings.Replace(body, `"expected_version":0`, `"expected_version":1`, 1), `"enabled":true`, `"enabled":false`, 1)
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, update, "application/json", 200)
	if repo.input.ExpectedVersion != 1 || repo.input.Enabled || repo.schedule.ID != "schedule" {
		t.Fatal("disable lost exact schedule CAS")
	}
	archiveHTTPCall(t, server, "POST", path, owner, strings.TrimSuffix(body, "}")+`,"remote_target":{"target_id":"target","revision":1}}`, "application/json", 200)
	if repo.input.RemoteTarget == nil || repo.input.RemoteTarget.TargetID != "target" || repo.input.RemoteTarget.Revision != 1 {
		t.Fatal("remote schedule lost pinned target in HTTP decoder")
	}
	// Exercise the real decoder and application validation for additive Compose fields.
	original.resource.Source = domain.ResourceSource{Kind: "compose"}
	original.resource.Kind = "application"
	componentBody := strings.TrimSuffix(body, "}") + `,"component":"postgresql","expected_deployed_operation_id":"hop_current"}`
	archiveHTTPCall(t, server, "POST", path, owner, componentBody, "application/json", 200)
	if repo.input.Component != "postgresql" || repo.input.ExpectedDeployedOperationID != "hop_current" {
		t.Fatal("component authority lost in decoder")
	}
	disabledComponent := strings.TrimSuffix(update, "}") + `,"component":"postgresql"}`
	archiveHTTPCall(t, server, "PUT", path+"/schedule", owner, disabledComponent, "application/json", 200)
	for index, wire := range []string{componentBody, disabledComponent} {
		wire = strings.Replace(wire, `"postgresql"`, `"mysql"`, 1)
		wire = strings.TrimSuffix(wire, "}") + `,"remote_target":{"target_id":"target","revision":1}}`
		method, endpoint := "POST", path
		if index == 1 {
			method, endpoint = "PUT", path+"/schedule"
		}
		archiveHTTPCall(t, server, method, endpoint, owner, wire, "application/json", 200)
		if repo.input.Component != "mysql" || repo.input.RemoteTarget == nil || repo.input.RemoteTarget.TargetID != "target" || repo.input.RemoteTarget.Revision != 1 {
			t.Fatal("MySQL component remote schedule wire changed")
		}
	}
	beforeComponentInvalid := repo.calls
	for _, invalid := range []string{
		strings.TrimSuffix(body, "}") + `,"component":"postgresql"}`,
		strings.TrimSuffix(body, "}") + `,"expected_deployed_operation_id":"hop_current"}`,
		strings.Replace(componentBody, `"postgresql"`, `"bad.component"`, 1),
		strings.Replace(componentBody, `"hop_current"`, `"invalid operation"`, 1),
	} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
	if repo.calls != beforeComponentInvalid {
		t.Fatal("invalid component schedule reached persistence")
	}
	before := repo.calls
	for _, invalid := range []string{strings.Replace(body, `,"enabled":true`, "", 1), strings.Replace(body, `"enabled":true`, `"enabled":null`, 1), strings.Replace(body, `,"expected_version":0`, "", 1), strings.Replace(body, `"expected_version":0`, `"expected_version":null`, 1), strings.Replace(body, `"timezone":"UTC"`, `"timezone":"Local"`, 1), strings.Replace(body, `"frequency":"daily"`, `"frequency":"@every 1m"`, 1), strings.Replace(body, `"request_id":"schedule_request"`, `"request_id":"schedule_request","request_id":"other"`, 1), strings.TrimSuffix(body, "}") + `,"command":"canary_private"}`} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
	archiveHTTPCall(t, server, "GET", path+"?timezone=UTC", owner, "", "", 400)
	if repo.calls != before {
		t.Fatal("invalid schedule reached persistence")
	}
}
func TestDatabaseBackupScheduleOpenAPI(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseBackupScheduleInput{RequestID: "schedule_request", ExpectedResourceGeneration: 1, Frequency: "daily", Timezone: "UTC", Enabled: false}
	raw, _ := json.Marshal(in)
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		t.Fatal("JSON fixture")
	}
	schema := doc.Components.Schemas["HostingDatabaseBackupScheduleInput"].Value
	if err = schema.VisitJSON(value); err != nil {
		t.Fatal(err)
	}
	delete(value, "enabled")
	if schema.VisitJSON(value) == nil {
		t.Fatal("schema permits missing disable flag")
	}
	for _, name := range []string{"HostingDatabaseBackupSchedule", "HostingDatabaseBackupOccurrence"} {
		if doc.Components.Schemas[name] == nil {
			t.Fatal("missing safe schema", name)
		}
	}
	now := time.Now().UTC()
	for _, version := range []string{domain.HostingDatabaseBackupScheduleVersion, domain.HostingDatabaseBackupComponentScheduleVersion, domain.HostingDatabaseMySQLBackupScheduleVersion, domain.HostingDatabaseMySQLBackupComponentScheduleVersion} {
		schedule := domain.HostingDatabaseBackupSchedule{ID: "schedule", Version: version, ProjectID: "project", ResourceID: "resource", Revision: 1, Frequency: "daily", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
		value = nil
		raw, err := json.Marshal(schedule)
		if err != nil || json.Unmarshal(raw, &value) != nil {
			t.Fatal("schedule JSON fixture", err)
		}
		if err = doc.Components.Schemas["HostingDatabaseBackupSchedule"].Value.VisitJSON(value); err != nil {
			t.Fatal("engine schedule response violates OpenAPI", err)
		}
	}
}
