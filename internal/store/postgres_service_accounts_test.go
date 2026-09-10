package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresServiceAccountRegistryCredentialsAndActorFKs(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "service_account_registry")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	account := domain.ServiceAccount{ID: "svcacct_pg_registry", ProjectID: "proj_platform", OwnerUserID: "usr_bootstrap", Name: "registry bot", Role: domain.RoleMaintainer, CreatedAt: now, UpdatedAt: now}
	accountID := account.ID
	token := domain.APIToken{ID: "pat_pg_registry", TokenHash: "pg-registry-hash", ServiceAccountID: &accountID, ExpiresAt: &expires, CreatedAt: now}
	created, err := pg.CreateServiceAccount(ctx, account, token, domain.AuditEvent{ID: "audit_pg_registry", ActorID: "usr_bootstrap", Action: "service_account.create", TargetID: account.ID, Metadata: map[string]any{}, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != domain.ServiceAccountActive {
		t.Fatalf("created account = %#v", created)
	}
	var humanKind, accountKind string
	if err := database.QueryRow(ctx, `SELECT kind FROM principals WHERE id='usr_bootstrap'`).Scan(&humanKind); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, account.ID).Scan(&accountKind); err != nil {
		t.Fatal(err)
	}
	if humanKind != "human" || accountKind != "service_account" {
		t.Fatalf("principal kinds human=%q account=%q", humanKind, accountKind)
	}
	credential, err := pg.GetManagedServiceAccountCredentialByHash(ctx, token.TokenHash, now)
	if err != nil || credential.ServiceAccount.ID != account.ID || credential.APIToken.CreatedBy != account.OwnerUserID || len(credential.APIToken.Roles) != 0 {
		t.Fatalf("credential = %#v, %v", credential, err)
	}
	if _, err := pg.GetAPITokenByHash(ctx, token.TokenHash, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("managed credential fell through legacy lookup: %v", err)
	}
	if _, err := pg.CreateRun(ctx, domain.TaskRun{ID: "run_pg_service_account_actor", ProjectID: account.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: account.ID, StartedAt: now}); err != nil {
		t.Fatalf("stable account actor FK rejected: %v", err)
	}
	serviceID := "svc_pg_service_account_actor"
	environmentID := "env_pg_service_account_actor"
	revisionID := "rev_pg_service_account_actor"
	if _, err := pg.CreateService(ctx, domain.Service{ID: serviceID, ProjectID: account.ProjectID, Name: "service account actor", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", Profiles: []string{}, OwnerID: account.ID, CreatedAt: now}); err != nil {
		t.Fatalf("service owner actor FK rejected: %v", err)
	}
	if _, err := pg.CreateEnvironment(ctx, domain.Environment{ID: environmentID, ServiceID: serviceID, Name: "production", ComposeProject: "service-account-actor", HealthPolicy: domain.HealthPolicy{}, TimeoutSeconds: 60, SecretBindings: []domain.SecretBinding{}, RollbackSafe: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: revisionID, ServiceID: serviceID, RequestedRef: "main", CreatedBy: account.ID, CreatedAt: now}); err != nil {
		t.Fatalf("revision actor FK rejected: %v", err)
	}
	deployment, err := pg.CreateDeploymentRequest(ctx,
		domain.Deployment{ID: "dep_pg_service_account_actor", EnvironmentID: environmentID, DesiredRevisionID: revisionID, IdempotencyKey: "service-account-actor", Status: domain.DeploymentQueued, RequestedBy: account.ID, FenceRequired: true, CreatedAt: now, UpdatedAt: now},
		domain.TaskRun{ID: "run_pg_service_account_deployment", ProjectID: account.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: account.ID, StartedAt: now},
		domain.AuditEvent{ID: "audit_pg_service_account_deployment", ActorID: account.ID, Action: "deployment.create", TargetID: "dep_pg_service_account_actor", Metadata: map[string]any{}, CreatedAt: now},
	)
	if err != nil || deployment.RequestedBy != account.ID {
		t.Fatalf("deployment actor = %#v, %v", deployment, err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES ('pm_invalid_service_account','proj_platform',$1,'viewer')`, account.ID); err == nil {
		t.Fatal("service account unexpectedly accepted by users-only project membership FK")
	}
	if _, err := database.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by,service_account_id,expires_at) VALUES ('pat_invalid_roles','invalid','service_account','invalid-role-hash',ARRAY['system_admin'],'active','usr_bootstrap',$1,clock_timestamp()+interval '1 hour')`, account.ID); err == nil {
		t.Fatal("managed credential unexpectedly accepted global roles")
	}
	if _, err := database.Exec(ctx, `INSERT INTO users(id,email,name,status,global_role) VALUES ('usr_principal_trigger','principal-trigger@example.local','Trigger User','active','user')`); err != nil {
		t.Fatal(err)
	}
	var triggerKind string
	if err := database.QueryRow(ctx, `SELECT kind FROM principals WHERE id='usr_principal_trigger'`).Scan(&triggerKind); err != nil || triggerKind != "human" {
		t.Fatalf("user trigger kind=%q err=%v", triggerKind, err)
	}
	if _, err := database.Exec(ctx, `UPDATE service_accounts SET project_id='proj_security' WHERE id=$1`, account.ID); err == nil {
		t.Fatal("service account immutable scope accepted a project change")
	}
	if _, err := database.Exec(ctx, `UPDATE principals SET kind='human' WHERE id=$1`, account.ID); err == nil {
		t.Fatal("principal registry accepted a kind change")
	}
	if _, err := database.Exec(ctx, `DELETE FROM principals WHERE id=$1`, account.ID); err == nil {
		t.Fatal("principal registry accepted deletion")
	}
	var projectID, ownerUserID string
	if err := database.QueryRow(ctx, `SELECT project_id, owner_user_id FROM service_accounts WHERE id=$1`, account.ID).Scan(&projectID, &ownerUserID); err != nil {
		t.Fatal(err)
	}
	if projectID != account.ProjectID || ownerUserID != account.OwnerUserID {
		t.Fatalf("immutable scope changed project=%q owner=%q", projectID, ownerUserID)
	}
}

func TestPostgresServiceAccountRotationRollbackAndOwnerOffboardingRace(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "service_account_race")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	account := domain.ServiceAccount{ID: "svcacct_pg_race", ProjectID: "proj_security", OwnerUserID: "usr_viewer", Name: "race bot", Role: domain.RoleMaintainer, CreatedAt: now, UpdatedAt: now}
	accountID := account.ID
	original := domain.APIToken{ID: "pat_pg_race_original", TokenHash: "pg-race-original", ServiceAccountID: &accountID, ExpiresAt: &expires, CreatedAt: now}
	if _, err := pg.CreateServiceAccount(ctx, account, original, domain.AuditEvent{ID: "audit_pg_race_create", ActorID: "usr_bootstrap", Action: "service_account.create", TargetID: account.ID, Metadata: map[string]any{}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := pg.CreateAuditEvent(ctx, domain.AuditEvent{ID: "audit_pg_race_duplicate", ActorID: "usr_bootstrap", Action: "test", TargetID: "test", Metadata: map[string]any{}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	failedToken := domain.APIToken{ID: "pat_pg_race_failed", TokenHash: "pg-race-failed", ExpiresAt: &expires, CreatedAt: now}
	if _, err := pg.RotateServiceAccountCredential(ctx, account.ID, failedToken, now, domain.AuditEvent{ID: "audit_pg_race_duplicate", ActorID: "usr_bootstrap", Action: "service_account.rotate", TargetID: account.ID, Metadata: map[string]any{}, CreatedAt: now}); err == nil {
		t.Fatal("rotation with duplicate audit unexpectedly succeeded")
	}
	if _, err := pg.GetManagedServiceAccountCredentialByHash(ctx, original.TokenHash, now); err != nil {
		t.Fatalf("failed rotation revoked original: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, err := pg.RotateServiceAccountCredential(ctx, account.ID, domain.APIToken{ID: "pat_pg_race_rotated", TokenHash: "pg-race-rotated", ExpiresAt: &expires, CreatedAt: now}, now, domain.AuditEvent{ID: "audit_pg_race_rotate", ActorID: "usr_bootstrap", Action: "service_account.rotate", TargetID: account.ID, Metadata: map[string]any{}, CreatedAt: now})
		results <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := pg.OffboardUser(ctx, store.UserOffboardingRequest{UserID: "usr_viewer", RevokedAt: now, Audit: domain.AuditEvent{ID: "audit_pg_race_offboard", ActorID: "usr_bootstrap", Action: "identity.user.offboard", TargetID: "usr_viewer", Metadata: map[string]any{}, CreatedAt: now}, DeniedAudit: domain.AuditEvent{ID: "audit_pg_race_offboard_denied", ActorID: "usr_bootstrap", Action: "identity.user.offboard.denied", TargetID: "usr_viewer", Metadata: map[string]any{}, CreatedAt: now}})
		results <- err
	}()
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("race operation error: %v", err)
		}
	}
	var userStatus, accountStatus string
	var activeCredentials int
	if err := database.QueryRow(ctx, `SELECT status FROM users WHERE id='usr_viewer'`).Scan(&userStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT status FROM service_accounts WHERE id=$1`, account.ID).Scan(&accountStatus); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE service_account_id=$1 AND status='active' AND revoked_at IS NULL`, account.ID).Scan(&activeCredentials); err != nil {
		t.Fatal(err)
	}
	if userStatus != domain.UserDisabled || accountStatus != domain.ServiceAccountRevoked || activeCredentials != 0 {
		t.Fatalf("post-race user=%q account=%q active_credentials=%d", userStatus, accountStatus, activeCredentials)
	}
	for _, tokenHash := range []string{"pg-race-original", "pg-race-rotated"} {
		if _, err := pg.GetManagedServiceAccountCredentialByHash(ctx, tokenHash, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("credential %s remains valid: %v", tokenHash, err)
		}
	}
	var offboardAuditCount int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id='audit_pg_race_offboard'`).Scan(&offboardAuditCount); err != nil || offboardAuditCount != 1 {
		t.Fatalf("offboard audit count=%d err=%v", offboardAuditCount, err)
	}
}

func TestPostgresServiceAccountMigrationPreservesLegacyTokens(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "service_account_legacy")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	legacy := domain.APIToken{ID: "pat_pg_legacy", Name: "legacy", Kind: domain.TokenKindServiceAccount, TokenHash: "pg-legacy-hash", Roles: []string{domain.RoleRunnerAdmin}, Status: domain.TokenActive, CreatedBy: "usr_bootstrap", CreatedAt: now}
	if _, err := pg.CreateAPIToken(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetAPITokenByHash(ctx, legacy.TokenHash, now)
	if err != nil || got.ID != legacy.ID || got.ServiceAccountID != nil || len(got.Roles) != 1 {
		t.Fatalf("legacy token=%#v err=%v", got, err)
	}
	var migratedHumans, users int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM principals WHERE kind='human'`).Scan(&migratedHumans); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if migratedHumans != users {
		t.Fatalf("migrated human principals=%d users=%d", migratedHumans, users)
	}
	if fmt.Sprint(got.Roles) != fmt.Sprint(legacy.Roles) {
		t.Fatalf("legacy roles=%v want=%v", got.Roles, legacy.Roles)
	}
}

func TestPostgresServiceAccountMigrationUpgradesFrom0038(t *testing.T) {
	ctx := t.Context()
	database, schemaURL := openPostgresAtMigration(t, ctx, "service_account_upgrade", "0038_user_offboarding.sql")
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		t.Fatalf("apply pre-0039 development seed: %v", err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by,created_at) VALUES ('pat_pre_0039','legacy before upgrade','service_account','pre-0039-legacy-hash',ARRAY['runner_admin'],'active','usr_bootstrap',clock_timestamp())`); err != nil {
		t.Fatalf("insert pre-0039 legacy token: %v", err)
	}
	var compatibleBefore bool
	if err := database.QueryRow(ctx, `SELECT nerocd_repository_policy_schema_compatible()`).Scan(&compatibleBefore); err != nil || !compatibleBefore {
		t.Fatalf("pre-0039 repository policy compatibility=%v err=%v", compatibleBefore, err)
	}
	if err := applyEmbeddedMigrations(ctx, database, "0038_user_offboarding.sql", "0039_service_accounts.sql"); err != nil {
		t.Fatalf("upgrade from 0038 through 0039: %v", err)
	}

	pg, err := store.OpenPostgres(ctx, schemaURL)
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	defer closePostgresStore(t, pg)
	if compatible, err := pg.SchemaCompatible(ctx); err != nil || !compatible {
		t.Fatalf("upgraded schema compatibility=%v err=%v", compatible, err)
	}
	var serviceAccountCompatible, policyCompatible bool
	if err := database.QueryRow(ctx, `SELECT nerocd_service_account_schema_compatible(), nerocd_repository_policy_schema_compatible()`).Scan(&serviceAccountCompatible, &policyCompatible); err != nil {
		t.Fatal(err)
	}
	if !serviceAccountCompatible || !policyCompatible {
		t.Fatalf("upgraded compatibility service_accounts=%v repository_policy=%v", serviceAccountCompatible, policyCompatible)
	}
	var migratedHumans, users int
	if err := database.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind='human'), (SELECT count(*) FROM users) FROM principals`).Scan(&migratedHumans, &users); err != nil {
		t.Fatal(err)
	}
	if migratedHumans != users {
		t.Fatalf("upgraded human principals=%d users=%d", migratedHumans, users)
	}
	var serviceAccountID *string
	var roles []string
	if err := database.QueryRow(ctx, `SELECT service_account_id, roles FROM api_tokens WHERE id='pat_pre_0039'`).Scan(&serviceAccountID, &roles); err != nil {
		t.Fatal(err)
	}
	if serviceAccountID != nil || fmt.Sprint(roles) != fmt.Sprint([]string{domain.RoleRunnerAdmin}) {
		t.Fatalf("upgraded legacy token service_account_id=%v roles=%v", serviceAccountID, roles)
	}
	legacy, err := pg.GetAPITokenByHash(ctx, "pre-0039-legacy-hash", time.Now().UTC())
	if err != nil || legacy.ID != "pat_pre_0039" || legacy.ServiceAccountID != nil {
		t.Fatalf("upgraded legacy token=%#v err=%v", legacy, err)
	}
}

func openPostgresAtMigration(t *testing.T, ctx context.Context, label, through string) (*pgxpool.Pool, string) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	schema := fmt.Sprintf("nerocd_%s_%d", label, time.Now().UTC().UnixNano())
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		adminDB.Close()
		t.Fatalf("create schema: %v", err)
	}
	schemaURL := databaseURLWithSearchPath(t, databaseURL, schema)
	database, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminDB.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		adminDB.Close()
	})
	if err := applyEmbeddedMigrations(ctx, database, "", through); err != nil {
		t.Fatal(err)
	}
	return database, schemaURL
}
