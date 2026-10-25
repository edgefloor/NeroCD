package api

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type retentionHTTPRepository struct {
	*archiveHTTPRepository
	store.HostingDatabaseRepository
	store.HostingDatabaseBackupRetentionRepository
	calls int
}

func (r *retentionHTTPRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == r.resource.ID {
		return r.resource, nil
	}
	return r.MemoryStore.GetResource(ctx, id)
}
func (r *retentionHTTPRepository) GetDatabaseBackupRetention(ctx context.Context, actor, resource string) (*domain.HostingDatabaseBackupRetentionPolicy, error) {
	r.calls++
	return nil, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *retentionHTTPRepository) SaveDatabaseBackupRetention(ctx context.Context, actor, resource string, in domain.HostingDatabaseBackupRetentionInput) (domain.HostingDatabaseBackupRetentionPolicy, error) {
	r.calls++
	return domain.HostingDatabaseBackupRetentionPolicy{Version: domain.HostingDatabaseBackupRetentionVersion, ResourceID: resource}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *retentionHTTPRepository) PreviewDatabaseBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingDatabaseBackupRetentionPreviewInput) (domain.HostingDatabaseBackupRetentionPreview, error) {
	r.calls++
	return domain.HostingDatabaseBackupRetentionPreview{ID: id, Items: []domain.BackupRetentionItem{}}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *retentionHTTPRepository) ExecuteDatabaseBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingDatabaseBackupRetentionExecutionInput) (domain.HostingDatabaseBackupRetentionExecution, error) {
	r.calls++
	return domain.HostingDatabaseBackupRetentionExecution{ID: id}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *retentionHTTPRepository) ListDatabaseBackupRetentionExecutions(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupRetentionExecution, error) {
	r.calls++
	return []domain.HostingDatabaseBackupRetentionExecution{}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *retentionHTTPRepository) GetDatabaseBackupRetentionExecution(ctx context.Context, actor, resource, id string) (domain.HostingDatabaseBackupRetentionExecution, error) {
	r.calls++
	return domain.HostingDatabaseBackupRetentionExecution{ID: id}, r.authorize(ctx, actor, domain.RoleViewer)
}

func TestBackupRetentionHTTPPermissionsAndClosedInputs(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "database"
	repo := &retentionHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/resources/" + repo.resource.ID + "/database-backup-retention"
	body := `{"request_id":"policy","expected_revision":0,"expected_resource_generation":1,"enabled":false,"automatic":false,"local":{"keep_count":0,"keep_days":0,"max_bytes":0},"remote":{"keep_count":0,"keep_days":0,"max_bytes":0}}`
	for _, token := range []string{"", runner} {
		archiveHTTPCall(t, server, "PUT", path, token, body, "application/json", 401)
	}
	archiveHTTPCall(t, server, "PUT", path, viewer, body, "application/json", 403)
	before := repo.calls
	for _, invalid := range []string{`{}`, `null`, body + `{}`, strings.Replace(body, `"enabled":false,`, ``, 1), strings.Replace(body, `"keep_days":0,`, ``, 1), strings.Replace(body, `"request_id":"policy"`, `"request_id":"policy","request_id":"other"`, 1), strings.Replace(body, `"automatic":false`, `"automatic":null`, 1), strings.Replace(body, `"max_bytes":0`, `"max_bytes":0,"path":"canary"`, 1)} {
		archiveHTTPCall(t, server, "PUT", path, owner, invalid, "application/json", 400)
	}
	if repo.calls != before {
		t.Fatal("malformed input reached repository")
	}
	archiveHTTPCall(t, server, "PUT", path, owner, body, "application/json", 200)
	archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	for _, x := range []struct{ suffix, body string }{{"/previews", `{"request_id":"preview","expected_policy_revision":1}`}, {"/executions", `{"request_id":"execute","preview_id":"preview","expected_policy_revision":1,"expected_plan_hash":"sha256:` + strings.Repeat("a", 64) + `"}`}} {
		archiveHTTPCall(t, server, "POST", path+x.suffix, viewer, x.body, "application/json", 403)
		want := 200
		if x.suffix == "/executions" {
			want = 202
		}
		archiveHTTPCall(t, server, "POST", path+x.suffix, owner, x.body, "application/json", want)
	}
	archiveHTTPCall(t, server, "GET", path+"/executions", viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", path+"/executions/execution", viewer, "", "", 200)
	for _, suffix := range []string{"plan", "mutation", "retirement", "credentials"} {
		archiveHTTPCall(t, server, "POST", "/api/v1/runners/hosting/database-backup-retention/"+suffix, owner, `{}`, "application/json", 401)
	}
}

func TestBackupRetentionSchemaMatchesTypedFields(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"BackupRetentionLimits": domain.BackupRetentionLimits{}, "HostingDatabaseBackupRetentionInput": domain.HostingDatabaseBackupRetentionInput{}, "HostingDatabaseBackupRetentionPolicy": domain.HostingDatabaseBackupRetentionPolicy{}, "HostingDatabaseBackupRetentionPreviewInput": domain.HostingDatabaseBackupRetentionPreviewInput{}, "HostingDatabaseBackupRetentionPreview": domain.HostingDatabaseBackupRetentionPreview{}, "BackupRetentionItem": domain.BackupRetentionItem{}, "HostingDatabaseBackupRetentionExecutionInput": domain.HostingDatabaseBackupRetentionExecutionInput{}, "HostingDatabaseBackupRetentionExecution": domain.HostingDatabaseBackupRetentionExecution{}, "BackupRetentionExecutionItem": domain.BackupRetentionExecutionItem{}, "BackupRetentionCopy": domain.BackupRetentionCopy{}, "HostingDatabaseBackupTransferMutation": domain.HostingDatabaseBackupTransferMutation{}, "BackupRemoteCredentialRequest": domain.BackupRemoteCredentialRequest{}, "BackupRemoteCredentialMetadata": domain.BackupRemoteCredentialMetadata{}, "HostingDatabaseBackupRetentionPlan": domain.HostingDatabaseBackupRetentionPlan{}, "HostingDatabaseBackupRetentionMutation": domain.HostingDatabaseBackupRetentionMutation{}, "HostingDatabaseBackupRetentionRetirementInput": domain.HostingDatabaseBackupRetentionRetirementInput{}, "HostingDatabaseBackupRetentionRetirement": domain.HostingDatabaseBackupRetentionRetirement{}, "BackupLocalCopyProof": domain.BackupLocalCopyProof{}, "BackupRemoteCopyProof": domain.BackupRemoteCopyProof{}} {
		ref := doc.Components.Schemas[name]
		if ref == nil {
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
		if len(fields) != len(ref.Value.Properties) {
			t.Fatalf("%s field count typed=%d schema=%d", name, len(fields), len(ref.Value.Properties))
		}
		for key := range fields {
			if ref.Value.Properties[key] == nil {
				t.Fatalf("%s missing%s", name, key)
			}
		}
	}
}
