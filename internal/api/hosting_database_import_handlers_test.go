package api

import (
	"context"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"strings"
	"testing"
	"time"
)

type databaseImportHTTPRepository struct {
	*archiveHTTPRepository
	store.HostingDatabaseImportRepository
	stage       domain.HostingDatabaseImportStage
	operation   domain.HostingDatabaseImportOperation
	stageInput  domain.HostingDatabaseImportStageInput
	importInput domain.HostingDatabaseImportInput
}

func (r *databaseImportHTTPRepository) RequestDatabaseImportStage(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseImportStageInput) (domain.HostingDatabaseImportStage, error) {
	r.calls++
	if e := r.authorize(ctx, actor, domain.RoleOwner); e != nil {
		return r.stage, e
	}
	r.stageInput = in
	return r.stage, nil
}
func (r *databaseImportHTTPRepository) GetDatabaseImportStage(ctx context.Context, actor, id string) (domain.HostingDatabaseImportStage, error) {
	r.calls++
	return r.stage, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *databaseImportHTTPRepository) RequestDatabaseImport(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseImportInput) (domain.HostingDatabaseImportOperation, error) {
	r.calls++
	if e := r.authorize(ctx, actor, domain.RoleOwner); e != nil {
		return r.operation, e
	}
	r.importInput = in
	return r.operation, nil
}
func (r *databaseImportHTTPRepository) GetDatabaseImport(ctx context.Context, actor, id string) (domain.HostingDatabaseImportOperation, error) {
	r.calls++
	return r.operation, r.authorize(ctx, actor, domain.RoleViewer)
}

func TestExternalDatabaseImportHTTPRealDecodersAndAuthority(t *testing.T) {
	server, base, owner, viewer, runner := archiveHTTPFixture(t)
	repo := &databaseImportHTTPRepository{archiveHTTPRepository: base}
	now := time.Now().UTC()
	repo.stage = domain.HostingDatabaseImportStage{ID: "stage", Version: domain.HostingDatabaseImportStageVersion, TargetResourceID: repo.resource.ID, Component: "mysql", ExpectedDeployedOperationID: "deployed", ExpectedResourceGeneration: 1, S3Source: domain.HostingDatabaseImportS3Source{TargetID: "s3", Revision: 1, ObjectKey: "nerocd/operator-import/gzip.data"}, State: "queued", CreatedAt: now, UpdatedAt: now}
	repo.operation = domain.HostingDatabaseImportOperation{ID: "import", Version: domain.HostingDatabaseImportVersion, StageID: "stage", TargetResourceID: repo.resource.ID, TargetComponent: "mysql", TargetDeployedOperationID: "deployed", State: "queued", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
	mem := repo.MemoryStore
	service, e := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if e != nil {
		t.Fatal(e)
	}
	server.app = service
	stageBody := `{"request_id":"stage_request","component":"mysql","expected_deployed_operation_id":"deployed","expected_resource_generation":1,"s3_source":{"target_id":"s3","revision":1,"object_key":"nerocd/operator-import/gzip.data"}}`
	target := `{"resource_id":"` + repo.resource.ID + `","component":"mysql","expected_deployed_operation_id":"deployed","expected_resource_generation":1,"acknowledge_replacement":true}`
	importBody := `{"request_id":"import_request","stage_id":"stage","expected_stage_hash":"sha256:` + strings.Repeat("a", 64) + `","component_target":` + target + `}`
	for _, tc := range []struct{ suffix, body string }{{"database-import-stages", stageBody}, {"database-imports", importBody}} {
		path := "/api/v1/resources/" + repo.resource.ID + "/" + tc.suffix
		archiveHTTPCall(t, server, "POST", path, "", tc.body, "application/json", 401)
		archiveHTTPCall(t, server, "POST", path, runner, tc.body, "application/json", 401)
		archiveHTTPCall(t, server, "POST", path, viewer, tc.body, "application/json", 403)
		archiveHTTPCall(t, server, "POST", path, owner, tc.body, "application/json", 202)
		calls := repo.calls
		for _, body := range []string{strings.TrimSuffix(tc.body, "}") + `,"command":"private"}`, strings.Replace(tc.body, `"request_id":`, `"request_id":"duplicate","request_id":`, 1), strings.Replace(tc.body, `"mysql"`, `"postgresql"`, 1)} {
			archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 400)
		}
		if repo.calls != calls {
			t.Fatal("invalid external input reached persistence")
		}
	}

	stagePath := "/api/v1/resources/" + repo.resource.ID + "/database-import-stages"
	originalKey := "nerocd/operator-import/gzip.data"
	beforeUnicode := repo.calls
	for _, key := range []string{"invalid" + string([]byte{0xff}) + ".sql", `\ud800.sql`, `\udc00.sql`, `\ud800x.sql`} {
		archiveHTTPCall(t, server, "POST", stagePath, owner, strings.Replace(stageBody, originalKey, key, 1), "application/json", 400)
	}
	if repo.calls != beforeUnicode {
		t.Fatal("malformed Unicode reached stage persistence")
	}
	for _, tc := range []struct{ wire, want string }{{"literal�.sql", "literal�.sql"}, {`\ud83d\ude00.sql`, "😀.sql"}} {
		archiveHTTPCall(t, server, "POST", stagePath, owner, strings.Replace(stageBody, originalKey, tc.wire, 1), "application/json", 202)
		if repo.stageInput.S3Source.ObjectKey != tc.want {
			t.Fatal("valid Unicode key was normalized or repaired")
		}
	}
	// Restore the original valid sample for the exact request identity assertion.
	archiveHTTPCall(t, server, "POST", stagePath, owner, stageBody, "application/json", 202)
	if repo.stageInput.S3Source.ObjectKey != "nerocd/operator-import/gzip.data" || repo.importInput.StageID != "stage" || !repo.importInput.ComponentTarget.AcknowledgeReplacement {
		t.Fatal("exact external authority lost")
	}
	archiveHTTPCall(t, server, "GET", "/api/v1/database-import-stages/stage", viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", "/api/v1/database-imports/import", viewer, "", "", 200)
	// No extra backup provenance, arbitrary endpoint, or root credential input exists.
	for _, extra := range []string{`"endpoint":"http://private"`, `"backup_id":"fake"`, `"credentials":{"password":"private"}`} {
		archiveHTTPCall(t, server, "POST", "/api/v1/resources/"+repo.resource.ID+"/database-import-stages", owner, strings.TrimSuffix(stageBody, "}")+`,`+extra+`}`, "application/json", 400)
	}
}
