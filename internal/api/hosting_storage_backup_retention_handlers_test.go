package api

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type storageRetentionHTTPRepository struct {
	*archiveHTTPRepository
	store.HostingDatabaseRepository
	store.HostingStorageBackupRetentionRepository
	calls         int
	mutationError error
}

func (r *storageRetentionHTTPRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == r.resource.ID {
		return r.resource, nil
	}
	return r.MemoryStore.GetResource(ctx, id)
}
func (r *storageRetentionHTTPRepository) GetStorageBackupRetention(ctx context.Context, actor, resource string) (*domain.HostingStorageBackupRetentionPolicy, error) {
	r.calls++
	return nil, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *storageRetentionHTTPRepository) SaveStorageBackupRetention(ctx context.Context, actor, resource string, in domain.HostingStorageBackupRetentionInput) (domain.HostingStorageBackupRetentionPolicy, error) {
	r.calls++
	return domain.HostingStorageBackupRetentionPolicy{Version: domain.HostingStorageBackupRetentionVersion, ResourceID: resource}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *storageRetentionHTTPRepository) PreviewStorageBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingStorageBackupRetentionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	r.calls++
	return domain.HostingStorageBackupRetentionPreview{ID: id, Items: []domain.BackupRetentionItem{}}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *storageRetentionHTTPRepository) ExecuteStorageBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingStorageBackupRetentionExecutionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	return domain.HostingStorageBackupRetentionExecution{ID: id}, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *storageRetentionHTTPRepository) ListStorageBackupRetentionExecutions(ctx context.Context, actor, resource string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	return []domain.HostingStorageBackupRetentionExecution{}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *storageRetentionHTTPRepository) GetStorageBackupRetentionExecution(ctx context.Context, actor, resource, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	return domain.HostingStorageBackupRetentionExecution{ID: id}, r.authorize(ctx, actor, domain.RoleViewer)
}

func (r *storageRetentionHTTPRepository) StorageBackupRetentionMutation(ctx context.Context, runner string, in domain.HostingStorageBackupRetentionMutation) (domain.HostingStorageBackupRetentionExecution, error) {
	r.calls++
	if runner != "runner_archive" {
		return domain.HostingStorageBackupRetentionExecution{}, store.ErrNotFound
	}
	return domain.HostingStorageBackupRetentionExecution{ID: in.OperationID}, r.mutationError
}

func TestStorageBackupRetentionMutationHTTPAuthorityStatus(t *testing.T) {
	server, original, owner, _, runner := archiveHTTPFixture(t)
	repo := &storageRetentionHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/runners/hosting/storage-backup-retention/mutation"
	in := domain.HostingStorageBackupRetentionMutation{HostingAuthority: domain.HostingAuthority{OperationID: "execution", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}, ReceiptID: "receipt", PlanHash: "sha256:" + strings.Repeat("a", 64), CopyID: "copy", Phase: "intent"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"known_stale_authority", fmt.Errorf("private-canary: %w", store.ErrConflict), 409},
		{"unknown_or_foreign_tuple", fmt.Errorf("private-canary: %w", store.ErrNotFound), 404},
		{"database_error", fmt.Errorf("private-canary"), 500},
		{"exact_committed_replay", nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo.mutationError = tc.err
			before := repo.calls
			response := archiveHTTPCall(t, server, "POST", path, runner, string(raw), "application/json", tc.status)
			if repo.calls != before+1 || strings.Contains(response.Body.String(), "private-canary") {
				t.Fatal("mutation status bypassed repository or leaked internal detail")
			}
			if tc.err == nil {
				var out domain.HostingStorageBackupRetentionExecution
				if json.Unmarshal(response.Body.Bytes(), &out) != nil || out.ID != in.OperationID {
					t.Fatal("receipt replay response lost execution identity")
				}
			}
		})
	}
	before := repo.calls
	archiveHTTPCall(t, server, "POST", path, owner, string(raw), "application/json", 401)
	if repo.calls != before {
		t.Fatal("non-runner reached retention mutation repository")
	}
}

func TestStorageBackupRetentionHTTPPermissionsAndClosedInputs(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	original.resource.Kind = "application"
	repo := &storageRetentionHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	path := "/api/v1/resources/" + repo.resource.ID + "/storage-backup-retention"
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
		archiveHTTPCall(t, server, "POST", "/api/v1/runners/hosting/storage-backup-retention/"+suffix, owner, `{}`, "application/json", 401)
	}
}

func TestStorageBackupRetentionSchemaMatchesTypedFields(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"BackupRetentionLimits": domain.BackupRetentionLimits{}, "HostingStorageBackupRetentionInput": domain.HostingStorageBackupRetentionInput{}, "HostingStorageBackupRetentionPolicy": domain.HostingStorageBackupRetentionPolicy{}, "HostingStorageBackupRetentionPreviewInput": domain.HostingStorageBackupRetentionPreviewInput{}, "HostingStorageBackupRetentionPreview": domain.HostingStorageBackupRetentionPreview{}, "BackupRetentionItem": domain.BackupRetentionItem{}, "HostingStorageBackupRetentionExecutionInput": domain.HostingStorageBackupRetentionExecutionInput{}, "HostingStorageBackupRetentionExecution": domain.HostingStorageBackupRetentionExecution{}, "BackupRetentionExecutionItem": domain.BackupRetentionExecutionItem{}, "BackupRetentionCopy": domain.BackupRetentionCopy{}, "HostingDatabaseBackupTransferMutation": domain.HostingDatabaseBackupTransferMutation{}, "BackupRemoteCredentialRequest": domain.BackupRemoteCredentialRequest{}, "BackupRemoteCredentialMetadata": domain.BackupRemoteCredentialMetadata{}, "HostingStorageBackupRetentionPlan": domain.HostingStorageBackupRetentionPlan{}, "HostingStorageBackupRetentionMutation": domain.HostingStorageBackupRetentionMutation{}, "HostingStorageBackupRetentionRetirementInput": domain.HostingStorageBackupRetentionRetirementInput{}, "HostingStorageBackupRetentionRetirement": domain.HostingStorageBackupRetentionRetirement{}, "BackupLocalCopyProof": domain.BackupLocalCopyProof{}, "BackupRemoteCopyProof": domain.BackupRemoteCopyProof{}} {
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

func TestStorageBackupRetentionProofSchemasAcceptDomainValues(t *testing.T) {
	printableASCII := ""
	for c := rune(32); c <= 126; c++ {
		printableASCII += string(c)
	}
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, schema string
		value        any
	}{
		{"local_created", "BackupLocalCopyProof", domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 2, Bytes: 65537, Created: true}},
		{"remote_nonversioned", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque-etag"`, VersionID: "", Created: true}},
		{"remote_all_printable_ASCII", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque-etag"`, VersionID: printableASCII, Created: true}},
		{"remote_opaque_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque-etag"`, VersionID: "version+/=:?._-%()[]", Created: true}},
		{"remote_utf8_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque-etag"`, VersionID: strings.Repeat("é", 512), Created: true}},
		{"remote_max_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque-etag"`, VersionID: strings.Repeat("v", 1024), Created: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch v := tc.value.(type) {
			case domain.BackupLocalCopyProof:
				if v.Validate() != nil {
					t.Fatal("invalid local fixture")
				}
			case domain.BackupRemoteCopyProof:
				if v.Validate() != nil {
					t.Fatal("invalid remote fixture")
				}
			}
			raw, e := json.Marshal(tc.value)
			if e != nil {
				t.Fatal(e)
			}
			var value any
			if json.Unmarshal(raw, &value) != nil {
				t.Fatal("decode fixture")
			}
			if e = doc.Components.Schemas[tc.schema].Value.VisitJSON(value); e != nil {
				t.Fatalf("valid domain proof rejected: %v", e)
			}
		})
	}
}

func TestStorageBackupRetentionProofSchemasRejectInvalidIdentity(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, schema string
		value        any
	}{
		{"local_identifier_not_digest", "BackupLocalCopyProof", domain.BackupLocalCopyProof{PoolID: "pool", Device: 1, Inode: 2, Bytes: 65537, Created: true}},
		{"local_zero_inode", "BackupLocalCopyProof", domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 0, Bytes: 65537, Created: true}},
		{"remote_oversized_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque"`, VersionID: strings.Repeat("v", 1025), Created: true}},
		{"remote_control_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque"`, VersionID: "opaque\nvalue", Created: true}},
		{"remote_delete_control_version", "BackupRemoteCopyProof", domain.BackupRemoteCopyProof{ETag: `"opaque"`, VersionID: "opaque\x7fvalue", Created: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch v := tc.value.(type) {
			case domain.BackupLocalCopyProof:
				if v.Validate() == nil {
					t.Fatal("valid local negative fixture")
				}
			case domain.BackupRemoteCopyProof:
				if v.Validate() == nil {
					t.Fatal("valid remote negative fixture")
				}
			}
			raw, e := json.Marshal(tc.value)
			if e != nil {
				t.Fatal(e)
			}
			var value any
			if json.Unmarshal(raw, &value) != nil {
				t.Fatal("decode fixture")
			}
			if doc.Components.Schemas[tc.schema].Value.VisitJSON(value) == nil {
				t.Fatal("invalid proof accepted by schema")
			}
		})
	}
}
