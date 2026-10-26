package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingCatalogPostgresAtomicReplaySecretsAndDeployment(t *testing.T) {
	pg, db, s, ctx, base := hostingDatabaseFixture(t, "postgresql")
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("c", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Bookmarks", TemplateID: "linkding", ImmutableVersion: domain.HostingCatalogLinkdingVersion}
	var wg sync.WaitGroup
	views := make([]domain.HostingCatalogInstallation, 4)
	errs := make([]error, 4)
	for i := range views {
		wg.Add(1)
		go func() { defer wg.Done(); views[i], errs[i] = s.InstallHostingCatalog(ctx, in) }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent install %d: %v", i, err)
		}
		if domain.HostingDocumentHash(views[i]) != domain.HostingDocumentHash(views[0]) {
			t.Fatal("concurrent retry changed original copy")
		}
	}
	v := views[0]
	if v.Resource.Kind != "application" || v.Resource.Source.Image.Reference != domain.HostingCatalogLinkdingImage || v.StorageRef.StorageID == "" || v.RuntimeConfiguration.Generation != 1 {
		t.Fatal("installation disconnected")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM hosting_catalog_installations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("install count %d %v", count, err)
	}
	raw, _ := json.Marshal(v)
	values := map[string]string{}
	for _, entry := range v.Credentials {
		revealed, err := s.RevealManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevealInput{Version: 1})
		if err != nil {
			t.Fatal(err)
		}
		values[entry.Name] = revealed.Value
		if strings.Contains(string(raw), revealed.Value) {
			t.Fatal("plaintext leaked to installation")
		}
	}
	replay, err := s.InstallHostingCatalog(ctx, in)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(v) {
		t.Fatalf("response loss replay: %v", err)
	}
	current := domain.HostingCatalogTemplates()
	current[0].Original = "refreshed"
	got, err := s.GetHostingCatalogInstallation(ctx, v.Resource.ID)
	if err != nil || got.Template.Original != v.Template.Original {
		t.Fatal("refresh changed retained copy")
	}
	in.Name = "Changed"
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed idempotency input %v", err)
	}
	in.Name = "Bookmarks"
	for _, entry := range v.Credentials {
		again, err := s.RevealManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevealInput{Version: 1})
		if err != nil || again.Value != values[entry.Name] {
			t.Fatal("retry replaced credential")
		}
	}
	if _, err = db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked grant replay admitted", err)
	}
	if _, err = db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=NULL WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	// Ordinary image admission must select the combined family with exact runtime generation.
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=array_append(array_append(array_append(array_append(capabilities,$1),$2),$3),$4)`, domain.HostingOwnedStorageRuntimeCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	input := hostingDeploymentRequest()
	input.ExpectedRuntimeConfigurationGeneration = 1
	input.ContainerPort = 9090
	input.Readiness = v.Template.Definition.Readiness
	op, err := pg.RequestHostingDeployment(ctx, "hop_catalog", v.Resource.ID, "usr_bootstrap", input, destinationEvent("catalog_deploy"))
	if err != nil {
		t.Fatal(err)
	}
	if op.Snapshot.PolicyVersion != domain.HostingOwnedStorageRuntimePolicyVersion || op.Snapshot.RuntimeConfiguration != v.RuntimeConfiguration || op.Snapshot.Storage != v.StorageRef {
		t.Fatal("ordinary admission lost combined references")
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, op.Snapshot.RunnerID, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(ctx, op.Snapshot.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	rel := domain.HostingRelease{CanonicalImage: domain.HostingCatalogLinkdingImage, ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "catalog_daemon", OS: "linux", Architecture: "arm64"}
	rel.Hash = domain.HostingDocumentHash(rel)
	prepared, err := pg.HostingPrepared(ctx, op.Snapshot.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "catalog_prepared", Release: rel})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || prepared.Plan.RetainPrevious {
		t.Fatal("wrong combined plan")
	}

	ack, err := pg.HostingStorageCreateBegin(ctx, op.Snapshot.RunnerID, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: prepared.Plan.Hash, StorageRef: v.StorageRef, CreationNonce: prepared.Plan.Storage.CreationNonce})
	if err != nil {
		t.Fatal(err)
	}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "catalog_materialized", PlanHash: prepared.Plan.Hash, StorageRef: v.StorageRef, CreationOperationID: ack.Materialization.CreationOperationID, CreationPlanHash: ack.Materialization.CreationPlanHash, CreationNonce: ack.Materialization.CreationNonce, DaemonID: rel.DaemonID, VolumeName: ack.Storage.VolumeName, Driver: "local", Scope: "local", Labels: ack.Materialization.Labels}
	if _, err = pg.HostingStorageMaterialized(ctx, op.Snapshot.RunnerID, proof); err != nil {
		t.Fatal(err)
	}
	var credentialHash string
	if err = db.QueryRow(ctx, `SELECT token_hash FROM runners WHERE id=$1`, op.Snapshot.RunnerID).Scan(&credentialHash); err != nil {
		t.Fatal(err)
	}
	delivery := domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: prepared.Plan.Hash, RuntimeConfiguration: v.RuntimeConfiguration, DeliveryID: strings.Repeat("b", 32)}
	authorized, err := pg.AuthorizeHostingRuntimeEnvironment(ctx, op.Snapshot.RunnerID, credentialHash, delivery)
	if err != nil {
		t.Fatal("combined runtime delivery after materialization", err)
	}
	authorized.Close()
	if _, err = pg.HostingApplyBegin(ctx, op.Snapshot.RunnerID, a); err != nil {
		t.Fatal("combined apply admission", err)
	}
	for _, entry := range v.Credentials {
		if _, err = s.RevokeManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevokeInput{ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoked secret replay admitted %v", err)
	}
	if _, err = pg.AuthorizeHostingRuntimeEnvironment(ctx, op.Snapshot.RunnerID, credentialHash, delivery); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked secret admitted runtime delivery", err)
	}
	if _, err = pg.HostingApplyBegin(ctx, op.Snapshot.RunnerID, a); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked secret replay admitted writer", err)
	}

}

func TestHostingCatalogPostgresDenialAndRollback(t *testing.T) {
	_, db, s, ctx, base := hostingDatabaseFixture(t, "postgresql")
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("d", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Bookmarks", TemplateID: "linkding", ImmutableVersion: domain.HostingCatalogLinkdingVersion}
	if _, err := db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_catalog_viewer',$1,'usr_bootstrap','viewer','viewer','active')`, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(context.Background(), auth.Principal{ID: "sa_catalog_viewer", Provider: domain.PrincipalServiceAccount, Roles: []string{domain.RoleViewer}})
	if _, err := s.InstallHostingCatalog(viewer, in); err == nil {
		t.Fatal("viewer installed")
	}
	foreign, err := s.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_security", Name: "Catalog foreign environment"})
	if err != nil {
		t.Fatal(err)
	}
	foreignInput := in
	foreignInput.ProjectEnvironmentID = foreign.ID
	if _, err = s.InstallHostingCatalog(ctx, foreignInput); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("cross-project environment admitted", err)
	}
	if _, err := db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("target denial %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM hosting_catalog_installations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied install persisted")
	}
}

func TestHostingCatalogPostgresCommitFailureRollsBackEveryComponent(t *testing.T) {
	_, db, s, ctx, base := hostingDatabaseFixture(t, "postgresql")
	counts := func() string {
		t.Helper()
		var v string
		if err := db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM resources),(SELECT count(*) FROM resource_bindings),(SELECT count(*) FROM hosting_storage),(SELECT count(*) FROM hosting_runtime_configuration_versions),(SELECT count(*) FROM project_managed_secret_versions),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM hosting_catalog_installations))::text`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := counts()
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_catalog_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'catalog fixture rollback'; END $$; CREATE TRIGGER reject_catalog_test BEFORE INSERT ON hosting_catalog_installations FOR EACH ROW EXECUTE FUNCTION reject_catalog_test()`); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("e", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Rollback", TemplateID: "linkding", ImmutableVersion: domain.HostingCatalogLinkdingVersion}
	if _, err := s.InstallHostingCatalog(ctx, in); err == nil {
		t.Fatal("injected commit boundary failure accepted")
	}
	if after := counts(); after != before {
		t.Fatal("failed catalog transaction retained a partial component")
	}
}

// Exercise the same UTC-local pgx / JSON representation boundary as the Linux
// runtime fixture without replacing process-global time.Local in shared tests.
func TestHostingCatalogPostgresReadUTCBindingIdentity(t *testing.T) {
	if os.Getenv("NEROCD_TEST_DATABASE_URL") == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL for isolated PostgreSQL regression")
	}
	const child = "NEROCD_CATALOG_BINDING_UTC_CHILD"
	if os.Getenv(child) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHostingCatalogPostgresReadUTCBindingIdentity$", "-test.v")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TZ=") && !strings.HasPrefix(value, child+"=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TZ=UTC", child+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("UTC catalog read regression: %v\n%s", err, output)
		}
		if strings.Contains(string(output), "SKIP") {
			t.Fatal("UTC catalog regression skipped")
		}
		t.Log(string(output))
		return
	}
	pg, db, s, ctx, base := hostingDatabaseFixture(t, "postgresql")
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("f", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "UTC retained catalog", TemplateID: "linkding", ImmutableVersion: domain.HostingCatalogLinkdingVersion}
	installed, err := s.InstallHostingCatalog(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	current, err := pg.GetResourceBinding(ctx, installed.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	var saved domain.HostingCatalogInstallation
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if time.Local == time.UTC || current.CreatedAt.Location() != time.Local || saved.Binding.CreatedAt.Location() != time.UTC || current == saved.Binding || !current.CreatedAt.Equal(saved.Binding.CreatedAt) || domain.HostingDocumentHash(current) != domain.HostingDocumentHash(saved.Binding) {
		t.Fatal("fixture did not reproduce equal persisted binding with distinct UTC representations")
	}
	assertRead := func(t *testing.T) {
		t.Helper()
		got, err := s.GetHostingCatalogInstallation(ctx, installed.Resource.ID)
		if err != nil {
			t.Fatalf("unchanged UTC binding read: %v", err)
		}
		if domain.HostingDocumentHash(got) != domain.HostingDocumentHash(installed) {
			t.Fatal("read changed immutable installation")
		}
	}
	assertRead(t)
	replay, err := s.InstallHostingCatalog(ctx, in)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(installed) {
		t.Fatalf("UTC retry changed receipt: %v", err)
	}
	for _, change := range []struct {
		name, sql string
		value     any
	}{
		{"namespace", "UPDATE resource_bindings SET runtime_namespace=$1 WHERE resource_id=$2", "ncd_" + strings.Repeat("f", 32)},
		{"creator", "UPDATE resource_bindings SET created_by=$1 WHERE resource_id=$2", "usr_viewer"},
		{"timestamp", "UPDATE resource_bindings SET created_at=$1 WHERE resource_id=$2", current.CreatedAt.Add(time.Microsecond)},
	} {
		t.Run(change.name, func(t *testing.T) {
			_, err := db.Exec(ctx, change.sql, change.value, installed.Resource.ID)
			var constraint *pgconn.PgError
			if !errors.As(err, &constraint) || constraint.Code != "23514" {
				t.Fatalf("binding immutability guard did not reject change: %v", err)
			}
			assertRead(t)
		})
	}
	if _, err := db.Exec(ctx, "UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1", in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetHostingCatalogInstallation(ctx, installed.Resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("revoked destination read accepted: %v", err)
	}
}
