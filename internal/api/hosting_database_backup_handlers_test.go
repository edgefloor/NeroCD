package api

import (
	"context"
	"encoding/json"
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

type databaseBackupHTTPRepository struct {
	*archiveHTTPRepository
	store.HostingDatabaseRepository
	value domain.HostingDatabaseBackupOperation
	input domain.HostingDatabaseBackupInput
	calls int
}

func (r *databaseBackupHTTPRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == r.resource.ID {
		return r.resource, nil
	}
	return r.MemoryStore.GetResource(ctx, id)
}
func (r *databaseBackupHTTPRepository) RequestDatabaseBackup(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseBackupInput, _ domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error) {
	r.calls++
	if e := r.authorize(ctx, actor, domain.RoleOwner); e != nil {
		return r.value, e
	}
	r.input = in
	r.value.ID = id
	r.value.BackupID = id
	return r.value, nil
}
func (r *databaseBackupHTTPRepository) GetDatabaseBackup(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupOperation, error) {
	r.calls++
	return r.value, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *databaseBackupHTTPRepository) ListDatabaseBackups(ctx context.Context, actor, id string) ([]domain.HostingDatabaseBackupOperation, error) {
	r.calls++
	return []domain.HostingDatabaseBackupOperation{r.value}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *databaseBackupHTTPRepository) RequestDatabaseBackupRestore(context.Context, string, string, string, domain.HostingDatabaseBackupRestoreInput, domain.HostingDatabaseCreateInput, []store.HostingDatabaseCredentialRecord, domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error) {
	r.calls++
	return r.value, store.ErrConflict
}
func (r *databaseBackupHTTPRepository) DatabaseBackupPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error) {
	r.calls++
	return domain.HostingDatabaseBackupPlan{}, store.ErrConflict
}
func (r *databaseBackupHTTPRepository) DatabaseBackupMutation(context.Context, string, domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error) {
	r.calls++
	return r.value, store.ErrConflict
}
func TestDatabaseBackupHTTPPermissionsAndClosedInputs(t *testing.T) {
	testDatabaseBackupHTTPPermissionsAndClosedInputs(t, false)
}
func TestMySQLComponentDatabaseBackupHTTPPermissionsAndClosedInputs(t *testing.T) {
	testDatabaseBackupHTTPPermissionsAndClosedInputs(t, true)
}
func TestMySQLComponentRemoteBackupHTTPContract(t *testing.T) {
	testDatabaseBackupHTTPPermissionsAndClosedInputs(t, true, true)
}
func testDatabaseBackupHTTPPermissionsAndClosedInputs(t *testing.T, component bool, remote ...bool) {
	t.Helper()
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "database"
	repo := &databaseBackupHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	now := time.Now().UTC()
	repo.value = domain.HostingDatabaseBackupOperation{MaxBytes: 1 << 20, TimeoutSeconds: 30, ID: "backup", Version: domain.HostingDatabaseBackupVersion, ProjectID: repo.resource.ProjectID, SourceResourceID: repo.resource.ID, SourceGeneration: 1, BackupID: "backup", Mode: "backup", State: "queued", Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
	path := "/api/v1/resources/" + repo.resource.ID + "/database-backups"
	body := `{"request_id":"backup","expected_resource_generation":1}`
	if component {
		repo.resource.Kind = "application"
		repo.resource.Source = domain.ResourceSource{Kind: "compose", Compose: &domain.HostingStackSource{}}
		repo.value.Version = domain.HostingDatabaseMySQLBackupComponentVersion
		repo.value.SourceComponent = "mysql"
		repo.value.SourceDeployedOperationID = "hop_source"
		body = `{"request_id":"backup","expected_resource_generation":1,"component":"mysql","expected_deployed_operation_id":"hop_source"}`
	}
	if len(remote) > 0 && remote[0] {
		repo.value.RemoteTarget = &domain.BackupTargetSelection{TargetID: "target", Revision: 3}
		body = strings.TrimSuffix(body, "}") + `,"remote_target":{"target_id":"target","revision":3}}`
	}
	archiveHTTPCall(t, server, "POST", path, "", body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, body, "application/json", 403)
	before := repo.calls
	for _, invalid := range []string{`{}`, `null`, body + `{}`, strings.Replace(body, `"request_id":"backup"`, `"request_id":"backup","request_id":"other"`, 1), strings.Replace(body, `}`, `,"path":"/private"}`, 1), strings.Replace(body, `}`, `,"password":"canary"}`, 1), strings.Replace(body, `"request_id"`, `"Request_ID"`, 1)} {
		archiveHTTPCall(t, server, "POST", path, owner, invalid, "application/json", 400)
	}
	if repo.calls != before {
		t.Fatal("invalid JSON reached repository")
	}
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 202)
	if len(remote) > 0 && remote[0] {
		if repo.input.Component != "mysql" || repo.input.ExpectedDeployedOperationID != "hop_source" || !reflect.DeepEqual(repo.input.RemoteTarget, repo.value.RemoteTarget) {
			t.Fatal("component remote request authority changed")
		}
	}

	response := archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	for _, unsafe := range []string{"credentials", "source_plan", "fence", "/private", "canary"} {
		if strings.Contains(response.Body.String(), unsafe) {
			t.Fatal("public metadata leaked", unsafe)
		}
	}
	archiveHTTPCall(t, server, "POST", "/api/v1/database-backups/backup/restores", viewer, `{"request_id":"restore","name":"Restored"}`, "application/json", 403)
	for _, endpoint := range []string{"plan", "mutation"} {
		archiveHTTPCall(t, server, "POST", "/api/v1/runners/hosting/database-backup/"+endpoint, owner, `{}`, "application/json", 401)
	}
}
func TestDatabaseBackupSchemaMatchesTypedFields(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"HostingDatabaseBackupInput": domain.HostingDatabaseBackupInput{}, "HostingDatabaseBackupRestoreInput": domain.HostingDatabaseBackupRestoreInput{}, "HostingDatabaseBackupBlob": domain.HostingDatabaseBackupBlob{}, "HostingDatabaseBackupOperation": domain.HostingDatabaseBackupOperation{}, "HostingDatabaseBackupPlan": domain.HostingDatabaseBackupPlan{}, "HostingDatabaseBackupMutation": domain.HostingDatabaseBackupMutation{}} {
		schema := doc.Components.Schemas[name]
		if schema == nil {
			t.Fatal("missing schema", name)
		}
		fields := map[string]bool{}
		var walk func(reflect.Type)
		walk = func(v reflect.Type) {
			for i := 0; i < v.NumField(); i++ {
				f := v.Field(i)
				if f.Anonymous {
					walk(f.Type)
					continue
				}
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if tag != "" && tag != "-" {
					fields[tag] = true
				}
			}
		}
		walk(reflect.TypeOf(value))
		if len(fields) != len(schema.Value.Properties) {
			t.Fatalf("%s field count mismatch", name)
		}
		for key := range fields {
			if schema.Value.Properties[key] == nil {
				t.Fatalf("%s missing %s", name, key)
			}
		}
	}
	for _, tc := range []struct {
		name, schema string
		input        any
	}{
		{"standalone backup", "HostingDatabaseBackupInput", domain.HostingDatabaseBackupInput{RequestID: "backup", ExpectedResourceGeneration: 1}},
		{"component backup", "HostingDatabaseBackupInput", domain.HostingDatabaseBackupInput{RequestID: "backup", ExpectedResourceGeneration: 1, Component: "postgresql", ExpectedDeployedOperationID: "hop_deployed"}},
		{"standalone restore", "HostingDatabaseBackupRestoreInput", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", Name: "New database"}},
		{"native remote restore", "HostingDatabaseBackupRestoreInput", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", Name: "Remote MySQL target", RemoteSource: &domain.BackupRemoteSource{TransferID: "upload", ExpectedTargetRevision: 1}}},
		{"component remote restore", "HostingDatabaseBackupRestoreInput", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: "res_target", Component: "mysql", ExpectedDeployedOperationID: "hop_target", ExpectedResourceGeneration: 1, AcknowledgeReplacement: true}, RemoteSource: &domain.BackupRemoteSource{TransferID: "upload", ExpectedTargetRevision: 1}}},
		{"component restore", "HostingDatabaseBackupRestoreInput", domain.HostingDatabaseBackupRestoreInput{RequestID: "restore", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: "res_target", Component: "postgresql", ExpectedDeployedOperationID: "hop_target", ExpectedResourceGeneration: 1, AcknowledgeReplacement: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err = json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if err = doc.Components.Schemas[tc.schema].Value.VisitJSON(value); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMySQLComponentDatabaseEligibilitySchemaPairs(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		engine string
		major  int
		valid  bool
	}{{"postgresql", 16, true}, {"mysql", 8, true}, {"mysql", 16, false}, {"postgresql", 8, false}, {"mariadb", 8, false}} {
		in := map[string]any{"component": "db", "engine": tc.engine, "major_version": tc.major, "running": true, "eligible": true, "reason": "requires_runtime_profile_verification"}
		raw, e := json.Marshal(in)
		if e != nil {
			t.Fatal(e)
		}
		var wire any
		if json.Unmarshal(raw, &wire) != nil {
			t.Fatal("invalid fixture JSON")
		}
		e = doc.Components.Schemas["HostingDatabaseBackupComponent"].Value.VisitJSON(wire)
		if (e == nil) != tc.valid {
			t.Fatal("invalid engine-major schema pair", tc.engine, tc.major, e)
		}
	}
}
