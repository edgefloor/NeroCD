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
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingCatalogPostgresAtomicReplaySecretsAndDeployment(t *testing.T) {
	pg, db, s, ctx, base := catalogDatabaseFixture(t)
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
	if len(v.Credentials) > 0 {
		entry := v.Credentials[0]
		if _, err = s.RevokeManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevokeInput{ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
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
	_, db, s, ctx, base := catalogDatabaseFixture(t)
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
	_, db, s, ctx, base := catalogDatabaseFixture(t)
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
	pg, db, s, ctx, base := catalogDatabaseFixture(t)
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

func TestHostingCatalogPostgresUptimeKumaEmptyCredentialsAndReplay(t *testing.T) {
	pg, database, _, ctx, base := catalogDatabaseFixture(t)
	// An application that generates no secrets must not need an encryption keyring.
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("9", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Monitoring", TemplateID: "uptime-kuma", ImmutableVersion: domain.HostingCatalogUptimeKumaVersion}
	var wg sync.WaitGroup
	views := make([]domain.HostingCatalogInstallation, 4)
	errs := make([]error, 4)
	for i := range views {
		wg.Go(func() { views[i], errs[i] = s.InstallHostingCatalog(ctx, in) })
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil || domain.HostingDocumentHash(views[i]) != domain.HostingDocumentHash(views[0]) {
			t.Fatal("concurrent Kuma install replay differs", e)
		}
	}
	v := views[0]
	if v.Template.ID != "uptime-kuma" || v.Resource.Source.Image.Reference != domain.HostingCatalogUptimeKumaImage || v.Credentials == nil || len(v.Credentials) != 0 || v.RuntimeConfiguration.Generation != 1 {
		t.Fatal("empty-credential installation disconnected")
	}
	var secrets, receipts int
	var runtime []byte
	if err = database.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_managed_secret_versions),(SELECT count(*) FROM hosting_catalog_installations),(SELECT body FROM hosting_runtime_configuration_versions WHERE resource_id=$1 AND generation=1)`, v.Resource.ID).Scan(&secrets, &receipts, &runtime); err != nil || secrets != 0 || receipts != 1 || string(runtime) != `{"entries": []}` {
		t.Fatal("Kuma invented secrets or lost empty runtime", secrets, receipts, string(runtime), err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$1,$2,$3,$4]::text[]`, domain.HostingOwnedStorageRuntimeCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	deploy := hostingDeploymentRequest()
	deploy.ExpectedRuntimeConfigurationGeneration = 1
	deploy.ContainerPort = 3001
	deploy.Readiness = v.Template.Definition.Readiness
	op, err := pg.RequestHostingDeployment(ctx, "hop_kuma_catalog", v.Resource.ID, "usr_bootstrap", deploy, destinationEvent("kuma_deploy"))
	if err != nil || op.Snapshot.RuntimeConfiguration != v.RuntimeConfiguration || op.Snapshot.Storage != v.StorageRef || op.Snapshot.PolicyVersion != domain.HostingOwnedStorageRuntimePolicyVersion {
		t.Fatal("Kuma ordinary deployment lost references", err)
	}
	if _, err = database.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked Kuma replay admitted", err)
	}
}

func TestHostingCatalogPostgresActualBudgetLiteralAndReplay(t *testing.T) {
	pg, database, s, ctx, base := catalogDatabaseFixture(t)
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("7", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Budget", TemplateID: "actualbudget", ImmutableVersion: domain.HostingCatalogActualBudgetVersion}
	v, err := s.InstallHostingCatalog(ctx, in)
	if err != nil || v.Credentials == nil || len(v.Credentials) != 0 || v.RuntimeConfiguration.Generation != 1 {
		t.Fatal("Actual install contract", err)
	}
	var runtime []byte
	var secrets int
	if err = database.QueryRow(ctx, `SELECT body,(SELECT count(*) FROM project_managed_secret_versions) FROM hosting_runtime_configuration_versions WHERE resource_id=$1 AND generation=1`, v.Resource.ID).Scan(&runtime, &secrets); err != nil || secrets != 0 {
		t.Fatal("Actual invented credentials", err)
	}
	var doc domain.HostingRuntimeDocument
	if json.Unmarshal(runtime, &doc) != nil || len(doc.Entries) != 1 || doc.Entries[0].Name != "ACTUAL_LOGIN_METHOD" || doc.Entries[0].Kind != "literal" || doc.Entries[0].Value == nil || *doc.Entries[0].Value != "password" || domain.HostingDocumentHash(doc) != v.RuntimeConfiguration.Hash {
		t.Fatal("pinned runtime literal lost")
	}
	replay, err := s.InstallHostingCatalog(ctx, in)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(v) {
		t.Fatal("Actual response-loss replay changed snapshot", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$1,$2,$3,$4]::text[]`, domain.HostingOwnedStorageRuntimeCapability, domain.HostingOwnedStorageCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	deploy := hostingDeploymentRequest()
	deploy.ExpectedRuntimeConfigurationGeneration = 1
	deploy.ContainerPort = 5006
	deploy.Readiness = v.Template.Definition.Readiness
	op, err := pg.RequestHostingDeployment(ctx, "hop_actual_catalog", v.Resource.ID, "usr_bootstrap", deploy, destinationEvent("actual_deploy"))
	if err != nil || op.Snapshot.RuntimeConfiguration != v.RuntimeConfiguration || op.Snapshot.Storage != v.StorageRef || op.Snapshot.PolicyVersion != domain.HostingOwnedStorageRuntimePolicyVersion {
		t.Fatal("Actual deployment lost runtime/storage pins", err)
	}
	if _, err = database.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked Actual replay admitted", err)
	}
}

func TestHostingCatalogPostgresVikunjaComposeInstallAndAdmission(t *testing.T) {
	pg, database, service, ctx, base := catalogDatabaseFixture(t)
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("e", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Tasks", TemplateID: "vikunja-with-postgresql", ImmutableVersion: domain.HostingCatalogVikunjaVersion, PublicURL: "https://tasks.example.test"}
	var views [3]domain.HostingCatalogInstallation
	var failures [3]error
	var wg sync.WaitGroup
	for i := range views {
		wg.Go(func() { views[i], failures[i] = service.InstallHostingCatalog(ctx, in) })
	}
	wg.Wait()
	for i, err := range failures {
		if err != nil || domain.HostingDocumentHash(views[i]) != domain.HostingDocumentHash(views[0]) {
			t.Fatal("concurrent Compose install changed receipt", err)
		}
	}
	installed := views[0]
	if installed.Resource.Source.Kind != "compose" || installed.Resource.Source.Compose == nil || *installed.Resource.Source.Compose != *installed.Template.Definition.Compose || installed.StorageRef != (domain.HostingStorageRef{}) || len(installed.Credentials) != 2 || installed.RuntimeConfiguration.Generation != 1 {
		t.Fatal("Compose install disconnected")
	}
	got, err := service.GetHostingCatalogInstallation(ctx, installed.Resource.ID)
	if err != nil || domain.HostingDocumentHash(got) != domain.HostingDocumentHash(installed) {
		t.Fatal("Compose receipt read", err)
	}
	runtime, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_bootstrap", installed.Resource.ID)
	if err != nil || len(runtime.Entries) != 5 || domain.HostingDocumentHash(domain.HostingRuntimeDocument{Entries: runtime.Entries}) != installed.RuntimeConfiguration.Hash {
		t.Fatal("complete runtime configuration missing", err)
	}
	values := map[string]string{}
	body, _ := json.Marshal(installed)
	for _, entry := range runtime.Entries {
		if entry.Kind == "literal" {
			values[entry.Name] = *entry.Value
			continue
		}
		revealed, err := service.RevealManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevealInput{Version: 1})
		if err != nil || len(revealed.Value) < 32 || strings.Contains(string(body), revealed.Value) {
			t.Fatal("generated credential unavailable or exposed", err)
		}
		values[entry.Name] = revealed.Value
	}
	if values["SERVICE_URL_VIKUNJA"] != in.PublicURL || values["SERVICE_USER_POSTGRESQL"] != "vikunja" || values["POSTGRESQL_DATABASE"] != "vikunja" || values["SERVICE_PASSWORD_JWTSECRET"] == values["SERVICE_PASSWORD_POSTGRESQL"] {
		t.Fatal("runtime defaults or secret identity changed")
	}
	definition, err := domain.ParseHostingStack(*installed.Resource.Source.Compose)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range definition.Services {
		for _, expression := range component.Environment {
			if _, err := domain.ExpandHostingStackValue(expression, values, true); err != nil {
				t.Fatal("missing required runtime variable", err)
			}
		}
	}
	var storageCount, receiptCount int
	if err = database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_storage WHERE resource_id=$1),(SELECT count(*) FROM hosting_storage_receipts WHERE resource_id=$1)`, installed.Resource.ID).Scan(&storageCount, &receiptCount); err != nil || storageCount != 0 || receiptCount != 0 {
		t.Fatal("Compose created image storage metadata", err)
	}
	input := hostingDeploymentRequest()
	input.ExpectedRuntimeConfigurationGeneration = 1
	input.ContainerPort = 3456
	input.Readiness = installed.Template.Definition.Readiness
	input.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	// Installation now requires a capable runner; simulate subsequent capability loss
	// to preserve the deployment admission regression independently.
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1)`, domain.HostingStackCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.RequestHostingDeployment(ctx, "hop_catalog_compose_old_runner", installed.Resource.ID, "usr_bootstrap", input, destinationEvent("catalog_compose_old_runner")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old runner accepted Compose", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=capabilities || ARRAY[$1,$2,$3]::text[]`, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	options, err := pg.HostingStorageDeploymentOptions(ctx, installed.Resource.ID, "usr_bootstrap")
	if err != nil || !options.Available || options.StackRef == nil || options.StorageRef != nil {
		t.Fatal("saved Compose deployment unavailable", err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_catalog_compose", installed.Resource.ID, "usr_bootstrap", input, destinationEvent("catalog_compose"))
	if err != nil || op.Snapshot.Stack == (domain.HostingStackRef{}) || op.Snapshot.RuntimeConfiguration != installed.RuntimeConfiguration {
		t.Fatal("catalog did not use existing Compose admission", err)
	}
	input.ExpectedRuntimeConfigurationGeneration = 0
	input.IdempotencyKey = "catalog-compose-stale-runtime"
	if _, err = pg.RequestHostingDeployment(ctx, "hop_catalog_compose_stale", installed.Resource.ID, "usr_bootstrap", input, destinationEvent("catalog_compose_stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale runtime admitted", err)
	}
	changed := in
	changed.PublicURL = "https://changed.example.test"
	if _, err = service.InstallHostingCatalog(ctx, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed URL reused receipt", err)
	}
	if _, err = database.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, in.DestinationID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked install replay admitted", err)
	}
	if _, err = service.GetHostingCatalogInstallation(ctx, installed.Resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked grant read admitted", err)
	}
}

func TestHostingCatalogPostgresComposeRollbackAndOwnerAuthority(t *testing.T) {
	_, database, service, ctx, base := catalogDatabaseFixture(t)
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("d", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Tasks rollback", TemplateID: "vikunja-with-postgresql", ImmutableVersion: domain.HostingCatalogVikunjaVersion, PublicURL: "https://tasks.example.test"}
	foreign, err := service.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_security", Name: "Foreign Compose"})
	if err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, database)
	if _, err := database.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_compose_catalog_viewer',$1,'usr_bootstrap','viewer','viewer','active')`, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "sa_compose_catalog_viewer", Provider: domain.PrincipalServiceAccount, Roles: []string{domain.RoleViewer}})
	if _, err := service.InstallHostingCatalog(viewer, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer installed Compose", err)
	}
	invalid := in
	invalid.ProjectEnvironmentID = foreign.ID
	if _, err = service.InstallHostingCatalog(ctx, invalid); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign environment installed Compose", err)
	}
	if after := hostingDatabaseCounts(t, database); after != before {
		t.Fatal("denied Compose install persisted components")
	}
	if _, err = database.Exec(ctx, `CREATE FUNCTION reject_compose_catalog_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'catalog fixture rollback'; END $$; CREATE TRIGGER reject_compose_catalog_test BEFORE INSERT ON hosting_catalog_installations FOR EACH ROW EXECUTE FUNCTION reject_compose_catalog_test()`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.InstallHostingCatalog(ctx, in); err == nil {
		t.Fatal("injected Compose install failure accepted")
	}
	if after := hostingDatabaseCounts(t, database); after != before {
		t.Fatal("Compose install retained partial components")
	}
	var count int
	if err = database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_compose_source_versions)+(SELECT count(*) FROM hosting_catalog_installations)+(SELECT count(*) FROM hosting_runtime_configuration_versions)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("Compose normalization, runtime or receipt survived rollback", err)
	}
}

func catalogDatabaseFixture(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, *app.Service, context.Context, domain.HostingDatabaseCreateInput) {
	t.Helper()
	pg, db, s, ctx, in := hostingDatabaseFixture(t, "postgresql")
	requirements := domain.CatalogRequirements(domain.HostingCatalogTemplates()[0])
	capabilities := append(requirements.RequiredCapabilities, domain.HostingStackCapability)
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || $1::text[]`, capabilities); err != nil {
		t.Fatal(err)
	}
	return pg, db, s, ctx, in
}

func TestHostingCatalogDestinationOptionsPostgres(t *testing.T) {
	_, db, s, ctx, base := hostingDatabaseFixture(t, "postgresql")
	template := domain.HostingCatalogTemplates()[0]
	beforeTemplates := domain.HostingDocumentHash(domain.HostingCatalogTemplates())
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("9", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "Options admission", TemplateID: template.ID, ImmutableVersion: template.Version}
	if _, err := db.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_options_viewer',$1,'usr_bootstrap','options viewer','viewer','active')`, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "sa_options_viewer", Provider: domain.PrincipalServiceAccount, Roles: []string{domain.RoleViewer}})
	if v, err := s.HostingCatalogDestinationOptions(viewer, in.ProjectID, template.ID, template.Version, store.Page{}); err != nil || len(v.Items) != 1 {
		t.Fatal("project viewer cannot inspect granted destination", err)
	}
	if _, err := s.HostingCatalogDestinationOptions(viewer, "proj_security", template.ID, template.Version, store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign project information leaked", err)
	}
	if v, err := s.HostingCatalogDestinationOptions(ctx, "proj_security", template.ID, template.Version, store.Page{}); err != nil || v.Total != 0 {
		t.Fatal("ungranted destination leaked", err)
	}
	if _, err := s.InstallHostingCatalog(viewer, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer installed catalog", err)
	}
	before := hostingDatabaseCounts(t, db)
	read := func() domain.HostingCatalogDestinationOptions {
		t.Helper()
		v, err := s.HostingCatalogDestinationOptions(ctx, in.ProjectID, template.ID, template.Version, store.Page{Enabled: true, LimitSpecified: true, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if v.Total != 1 || len(v.Items) != 1 {
			t.Fatalf("unexpected authorized page %+v", v)
		}
		return v
	}
	page, err := s.HostingCatalogDestinationOptions(ctx, in.ProjectID, template.ID, template.Version, store.Page{Enabled: true, LimitSpecified: true, Limit: 1, Offset: 1})
	if err != nil || page.Total != 1 || page.Offset != 1 || len(page.Items) != 0 {
		t.Fatal("destination pagination changed grant scope", err)
	}
	if _, err = db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE used_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if read().Items[0].ReasonCode != "runner_ineligible" {
		t.Fatal("revoked enrollment qualified")
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("revoked enrollment installed", err)
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("ineligible install retained side effects")
	}
	if _, err = db.Exec(ctx, `UPDATE runner_enrollments SET revoked_at=NULL`); err != nil {
		t.Fatal(err)
	}
	options := read()
	if options.Items[0].Available || options.Items[0].ReasonCode != "missing_capabilities" || len(options.Items[0].MissingCapabilities) == 0 {
		t.Fatal("underqualified runner accepted", options)
	}
	if _, err := s.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("incompatible installation accepted", err)
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("rejected install retained side effects")
	}
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities || $1::text[]`, options.Requirements.RequiredCapabilities); err != nil {
		t.Fatal(err)
	}
	if !read().Items[0].Available {
		t.Fatal("qualified runner unavailable")
	}
	if _, err := db.Exec(ctx, `UPDATE runners SET status='disabled'`); err != nil {
		t.Fatal(err)
	}
	if read().Items[0].ReasonCode != "runner_unavailable" {
		t.Fatal("inactive runner accepted")
	}
	if _, err := s.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale qualification accepted", err)
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("stale install retained side effects")
	}
	if _, err := db.Exec(ctx, `UPDATE runners SET status='active'`); err != nil {
		t.Fatal(err)
	}
	installed, err := s.InstallHostingCatalog(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	installedHash := domain.HostingDocumentHash(installed)
	if _, err = db.Exec(ctx, `UPDATE runners SET status='disabled'`); err != nil {
		t.Fatal(err)
	}
	replay, err := s.InstallHostingCatalog(ctx, in)
	if err != nil || domain.HostingDocumentHash(replay) != installedHash {
		t.Fatal("authorized saved replay changed", err)
	}
	if domain.HostingDocumentHash(domain.HostingCatalogTemplates()) != beforeTemplates {
		t.Fatal("options changed pinned template")
	}
	if _, err = s.HostingCatalogDestinationOptions(ctx, in.ProjectID, "missing", template.Version, store.Page{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unknown template accepted", err)
	}
	if _, err = s.HostingCatalogDestinationOptions(ctx, in.ProjectID, template.ID, "old", store.Page{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unknown version accepted", err)
	}
	if _, err = db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	options, err = s.HostingCatalogDestinationOptions(ctx, in.ProjectID, template.ID, template.Version, store.Page{})
	if err != nil || options.Total != 0 || len(options.Items) != 0 {
		t.Fatal("revoked destination leaked", err)
	}
	if _, err = s.InstallHostingCatalog(ctx, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked replay admitted", err)
	}
}
